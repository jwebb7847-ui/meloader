package taxonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monbooru/monloader/internal/shared/fsx"
)

const (
	derpibooruBaseURL       = "https://derpibooru.org"
	derpibooruMaxConcurrent = 1
	derpibooruMaxAttempts   = 5
)

type Enricher interface {
	Enrich(context.Context, map[string]any) error
}

type tagCall struct {
	done chan struct{}
	rec  tagRecord
	err  error
}

type Derpibooru struct {
	mu              sync.Mutex
	saveMu          sync.Mutex
	cache           map[string]tagRecord
	inflight        map[string]*tagCall
	slots           chan struct{}
	generation      uint64
	savedGeneration uint64
	cachePath       string
	baseURL         string
	client          *http.Client
}

type tagRecord struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug,omitempty"`
	Category        string `json:"category,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
	NameInNamespace string `json:"name_in_namespace,omitempty"`
}

type tagSearchResponse struct {
	Tags []tagRecord `json:"tags"`
}

func NewDerpibooru(cachePath string) (*Derpibooru, error) {
	return newDerpibooru(
		cachePath,
		derpibooruBaseURL,
		&http.Client{Timeout: 15 * time.Second},
	)
}

func newDerpibooru(
	cachePath string,
	baseURL string,
	client *http.Client,
) (*Derpibooru, error) {
	d := &Derpibooru{
		cache:     map[string]tagRecord{},
		inflight:  map[string]*tagCall{},
		slots:     make(chan struct{}, derpibooruMaxConcurrent),
		cachePath: cachePath,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    client,
	}

	if cachePath == "" {
		return d, nil
	}

	raw, err := os.ReadFile(cachePath)
	switch {
	case os.IsNotExist(err):
		return d, nil
	case err != nil:
		return nil, fmt.Errorf("reading taxonomy cache: %w", err)
	case len(raw) == 0:
		return d, nil
	}

	if err := json.Unmarshal(raw, &d.cache); err != nil {
		return nil, fmt.Errorf("decoding taxonomy cache: %w", err)
	}

	return d, nil
}

func (d *Derpibooru) Enrich(
	ctx context.Context,
	meta map[string]any,
) error {
	if strings.ToLower(strings.TrimSpace(stringValue(meta["category"]))) != "derpibooru" {
		return nil
	}

	sourceTags := tagStrings(meta["tags"])
	if len(sourceTags) == 0 {
		return nil
	}

	/*
		Philomena supplies one flat tag list. Keep that shape because
		Monloader's mapper already understands namespace-prefixed entries
		inside a flat list.

		Examples:
		  wings                     -> general:wings
		  rainbow dash              -> character:rainbow dash
		  artist:test               -> artist:test
		  equestria at war mod      -> meta:equestria at war mod

		Rating tags remain unchanged so the existing Philomena rating
		path continues to lift them into Monbooru's rating category.
	*/
	records := make([]tagRecord, len(sourceTags))
	errs := make([]error, len(sourceTags))

	var wg sync.WaitGroup

	for i, sourceName := range sourceTags {
		i := i
		sourceName := sourceName

		wg.Add(1)

		go func() {
			defer wg.Done()

			records[i], errs[i] = d.resolve(
				ctx,
				sourceName,
			)
		}()
	}

	wg.Wait()

	/*
		Persist successful cache misses even if another tag in the same
		post failed. The metadata itself is still mutated only after every
		tag resolved successfully, preserving all-or-nothing enrichment.
	*/
	saveErr := d.save()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf(
				"resolving Derpibooru tag %q: %w",
				sourceTags[i],
				err,
			)
		}
	}

	if saveErr != nil {
		return saveErr
	}

	rewritten := make([]string, 0, len(sourceTags))

	for i, sourceName := range sourceTags {
		rec := records[i]

		target, name, isRating := derpibooruTarget(
			rec,
			sourceName,
		)

		switch {
		case isRating:
			rewritten = appendUnique(
				rewritten,
				name,
			)

		case target == "" || target == "general":
			rewritten = appendUnique(
				rewritten,
				name,
			)

		default:
			rewritten = appendUnique(
				rewritten,
				target+":"+name,
			)
		}
	}

	meta["tags"] = rewritten

	return nil
}

func derpibooruTarget(
	rec tagRecord,
	sourceName string,
) (category string, name string, rating bool) {
	sourceName = strings.TrimSpace(sourceName)

	if strings.EqualFold(rec.Category, "rating") {
		return "", strings.ToLower(strings.TrimSpace(rec.Name)), true
	}

	namespace := strings.ToLower(strings.TrimSpace(rec.Namespace))
	nameInNamespace := strings.TrimSpace(rec.NameInNamespace)

	switch namespace {
	case "artist":
		if nameInNamespace == "" {
			nameInNamespace = afterNamespace(sourceName)
		}
		return "artist", nameInNamespace, false

	case "oc":
		if nameInNamespace == "" {
			nameInNamespace = afterNamespace(sourceName)
		}
		return "character", nameInNamespace, false
	}

	switch strings.ToLower(strings.TrimSpace(rec.Category)) {
	case "species":
		return "species", sourceName, false

	case "character":
		return "character", sourceName, false

	case "oc",
		"origin",
		"content-official",
		"content-fanmade",
		"error",
		"spoiler":
		return "meta", sourceName, false

	default:
		/*
			Derpibooru deliberately leaves many ordinary descriptive tags
			uncategorized. Preserve those honestly as general tags.
		*/
		return "general", sourceName, false
	}
}

func afterNamespace(name string) string {
	_, rest, ok := strings.Cut(name, ":")
	if !ok {
		return name
	}
	return strings.TrimSpace(rest)
}

func (d *Derpibooru) resolve(
	ctx context.Context,
	name string,
) (tagRecord, error) {
	key := normalizeKey(name)

	d.mu.Lock()

	if rec, ok := d.cache[key]; ok {
		d.mu.Unlock()
		return rec, nil
	}

	/*
		Multiple jobs frequently share common tags. If another goroutine
		is already resolving this exact tag, wait for its result instead
		of issuing another HTTP request.
	*/
	if call, ok := d.inflight[key]; ok {
		done := call.done
		d.mu.Unlock()

		select {
		case <-ctx.Done():
			return tagRecord{}, ctx.Err()

		case <-done:
			return call.rec, call.err
		}
	}

	call := &tagCall{
		done: make(chan struct{}),
	}

	d.inflight[key] = call
	d.mu.Unlock()

	rec, err := d.fetch(ctx, name)

	d.mu.Lock()

	if err == nil {
		d.cache[key] = rec
		d.generation++
	}

	call.rec = rec
	call.err = err

	delete(d.inflight, key)
	close(call.done)

	d.mu.Unlock()

	return rec, err
}

func (d *Derpibooru) fetch(
	ctx context.Context,
	name string,
) (tagRecord, error) {
	/*
		Derpibooru rate-limits bursts aggressively. Keep actual source
		requests serialized; the cache and in-flight map still prevent
		repeated work across jobs.
	*/
	select {
	case d.slots <- struct{}{}:
		defer func() {
			<-d.slots
		}()

	case <-ctx.Done():
		return tagRecord{}, ctx.Err()
	}

	u, err := url.Parse(
		d.baseURL + "/api/v1/json/search/tags",
	)
	if err != nil {
		return tagRecord{}, err
	}

	q := u.Query()
	q.Set("q", name)
	u.RawQuery = q.Encode()

	for attempt := 0; attempt < derpibooruMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			u.String(),
			nil,
		)
		if err != nil {
			return tagRecord{}, err
		}

		req.Header.Set(
			"User-Agent",
			"monloader Derpibooru taxonomy resolver",
		)

		resp, err := d.client.Do(req)
		if err != nil {
			return tagRecord{}, err
		}

		if resp.StatusCode >= 200 &&
			resp.StatusCode < 300 {
			var body tagSearchResponse

			err := json.NewDecoder(
				resp.Body,
			).Decode(&body)

			resp.Body.Close()

			if err != nil {
				return tagRecord{}, fmt.Errorf(
					"decoding Derpibooru response: %w",
					err,
				)
			}

			for i := range body.Tags {
				if strings.EqualFold(
					strings.TrimSpace(body.Tags[i].Name),
					strings.TrimSpace(name),
				) {
					return body.Tags[i], nil
				}
			}

			return tagRecord{}, fmt.Errorf(
				"no exact tag match returned",
			)
		}

		status := resp.StatusCode

		retryable := status == http.StatusTooManyRequests ||
			status >= 500

		delay, hasRetryAfter := retryAfter(
			resp.Header.Get("Retry-After"),
		)

		// Drain so the HTTP connection can be reused efficiently.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if !retryable ||
			attempt == derpibooruMaxAttempts-1 {
			return tagRecord{}, fmt.Errorf(
				"Derpibooru returned HTTP %d",
				status,
			)
		}

		if !hasRetryAfter {
			delay = time.Second * time.Duration(
				1<<attempt,
			)
		}

		if err := sleepContext(
			ctx,
			delay,
		); err != nil {
			return tagRecord{}, err
		}
	}

	return tagRecord{}, fmt.Errorf(
		"Derpibooru request attempts exhausted",
	)
}

func retryAfter(
	value string,
) (time.Duration, bool) {
	value = strings.TrimSpace(value)

	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil &&
		seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}

	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}

	delay := time.Until(when)

	if delay < 0 {
		delay = 0
	}

	return delay, true
}

func sleepContext(
	ctx context.Context,
	delay time.Duration,
) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-timer.C:
		return nil
	}
}

func (d *Derpibooru) save() error {
	if d.cachePath == "" {
		return nil
	}

	/*
		Serialize writers. Taking the cache snapshot only after acquiring
		saveMu prevents an older snapshot from finishing after and
		overwriting a newer one.
	*/
	d.saveMu.Lock()
	defer d.saveMu.Unlock()

	d.mu.Lock()

	if d.generation == d.savedGeneration {
		d.mu.Unlock()
		return nil
	}

	generation := d.generation

	snapshot := make(
		map[string]tagRecord,
		len(d.cache),
	)

	for key, rec := range d.cache {
		snapshot[key] = rec
	}

	d.mu.Unlock()

	raw, err := json.MarshalIndent(
		snapshot,
		"",
		"  ",
	)
	if err != nil {
		return fmt.Errorf(
			"encoding taxonomy cache: %w",
			err,
		)
	}

	if err := fsx.WriteFile(
		d.cachePath,
		raw,
	); err != nil {
		return fmt.Errorf(
			"writing taxonomy cache: %w",
			err,
		)
	}

	d.mu.Lock()

	if generation > d.savedGeneration {
		d.savedGeneration = generation
	}

	d.mu.Unlock()

	return nil
}

func normalizeKey(s string) string {
	return strings.ToLower(
		strings.TrimSpace(s),
	)
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func tagStrings(v any) []string {
	var out []string

	switch v := v.(type) {
	case []string:
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}

	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}

			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}

	case string:
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}

	return out
}

func appendUnique(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst)+len(values))

	for _, value := range dst {
		seen[normalizeKey(value)] = true
	}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		key := normalizeKey(value)
		if seen[key] {
			continue
		}

		seen[key] = true
		dst = append(dst, value)
	}

	return dst
}

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
	"github.com/monbooru/monloader/internal/shared/logx"
)

const (
	derpibooruBaseURL       = "https://derpibooru.org"
	derpibooruMaxConcurrent = 1
	derpibooruMaxAttempts   = 5
)

type Enricher interface {
	Enrich(context.Context, map[string]any) error
}

// Warmer is optional. It lets the pipeline populate source-taxonomy caches
// while media is downloading without changing metadata or committing output.
type Warmer interface {
	Warm(context.Context, map[string]any) error
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

	cache, err := loadDerpibooruCache(cachePath)
	if err != nil {
		return nil, err
	}
	d.cache = cache

	return d, nil
}

func loadDerpibooruCache(
	cachePath string,
) (map[string]tagRecord, error) {
	cache := map[string]tagRecord{}

	if strings.TrimSpace(cachePath) == "" {
		return cache, nil
	}

	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cache, nil
		}

		return nil, fmt.Errorf(
			"reading Derpibooru taxonomy cache: %w",
			err,
		)
	}

	if err := json.Unmarshal(raw, &cache); err == nil {
		if cache == nil {
			cache = map[string]tagRecord{}
		}

		return cache, nil
	} else {
		quarantined, qerr := quarantineCorruptCache(
			cachePath,
		)
		if qerr != nil {
			return nil, fmt.Errorf(
				"decoding Derpibooru taxonomy cache: %v; preserving corrupt cache: %w",
				err,
				qerr,
			)
		}

		logx.Warnf(
			"taxonomy: corrupt Derpibooru cache moved to %q; rebuilding it: %v",
			quarantined,
			err,
		)

		return map[string]tagRecord{}, nil
	}
}

func quarantineCorruptCache(
	cachePath string,
) (string, error) {
	stamp := time.Now().
		UTC().
		Format("20060102T150405.000000000Z")

	target := cachePath + ".corrupt-" + stamp

	if err := os.Rename(
		cachePath,
		target,
	); err != nil {
		return "", err
	}

	return target, nil
}

func (d *Derpibooru) Warm(
	ctx context.Context,
	meta map[string]any,
) error {
	if strings.ToLower(strings.TrimSpace(
		stringValue(meta["category"]),
	)) != "derpibooru" {
		return nil
	}

	sourceTags := tagStrings(meta["tags"])
	if len(sourceTags) == 0 {
		return nil
	}

	/*
		Philomena includes tag_ids alongside tags. Resolve all uncached
		taxonomy records in batches first; fall back to exact-name lookup
		only for records the batch response did not provide.
	*/
	if err := d.prefillByIDs(
		ctx,
		sourceTags,
		meta["tag_ids"],
	); err != nil {
		return err
	}

	for _, sourceName := range sourceTags {
		if _, ok := d.cached(sourceName); ok {
			continue
		}

		if _, err := d.resolve(
			ctx,
			sourceName,
		); err != nil {
			return fmt.Errorf(
				"warming Derpibooru tag %q: %w",
				sourceName,
				err,
			)
		}
	}

	return nil
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

	if err := d.Warm(ctx, meta); err != nil {
		return err
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

func (d *Derpibooru) prefillByIDs(
	ctx context.Context,
	sourceTags []string,
	rawIDs any,
) error {
	ids := tagIDStrings(rawIDs)

	/*
		The tag and tag_id arrays are positional siblings in Philomena.
		If their shapes do not agree, do not guess; the caller will fall
		back to the exact-name resolver.
	*/
	if len(ids) != len(sourceTags) {
		return nil
	}

	const batchSize = 50

	type wanted struct {
		id   string
		name string
	}

	missing := make([]wanted, 0, len(sourceTags))

	for i, sourceName := range sourceTags {
		if _, ok := d.cached(sourceName); ok {
			continue
		}

		if ids[i] == "" {
			continue
		}

		missing = append(
			missing,
			wanted{
				id:   ids[i],
				name: sourceName,
			},
		)
	}

	for start := 0; start < len(missing); start += batchSize {
		end := start + batchSize
		if end > len(missing) {
			end = len(missing)
		}

		chunk := missing[start:end]
		chunkIDs := make([]string, 0, len(chunk))

		for _, item := range chunk {
			chunkIDs = append(
				chunkIDs,
				item.id,
			)
		}

		records, err := d.fetchMany(
			ctx,
			chunkIDs,
		)
		if err != nil {
			return fmt.Errorf(
				"batch resolving Derpibooru taxonomy: %w",
				err,
			)
		}

		d.storeRecords(records)
	}

	return nil
}

func (d *Derpibooru) cached(
	name string,
) (tagRecord, bool) {
	key := normalizeKey(name)

	d.mu.Lock()
	defer d.mu.Unlock()

	rec, ok := d.cache[key]
	return rec, ok
}

func (d *Derpibooru) storeRecords(
	records []tagRecord,
) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, rec := range records {
		key := normalizeKey(rec.Name)
		if key == "" {
			continue
		}

		old, exists := d.cache[key]

		if exists && old == rec {
			continue
		}

		d.cache[key] = rec
		d.generation++
	}
}

func tagIDStrings(v any) []string {
	switch values := v.(type) {
	case []any:
		out := make([]string, len(values))

		for i, value := range values {
			out[i] = tagIDString(value)
		}

		return out

	case []string:
		out := make([]string, len(values))

		for i, value := range values {
			out[i] = tagIDString(value)
		}

		return out

	case []int:
		out := make([]string, len(values))

		for i, value := range values {
			out[i] = tagIDString(value)
		}

		return out

	case []int64:
		out := make([]string, len(values))

		for i, value := range values {
			out[i] = tagIDString(value)
		}

		return out
	}

	return nil
}

func tagIDString(v any) string {
	switch value := v.(type) {
	case string:
		value = strings.TrimSpace(value)

		if n, err := strconv.ParseInt(
			value,
			10,
			64,
		); err == nil && n > 0 {
			return strconv.FormatInt(n, 10)
		}

	case json.Number:
		if n, err := value.Int64(); err == nil && n > 0 {
			return strconv.FormatInt(n, 10)
		}

	case int:
		if value > 0 {
			return strconv.Itoa(value)
		}

	case int32:
		if value > 0 {
			return strconv.FormatInt(int64(value), 10)
		}

	case int64:
		if value > 0 {
			return strconv.FormatInt(value, 10)
		}

	case uint:
		if value > 0 {
			return strconv.FormatUint(uint64(value), 10)
		}

	case uint32:
		if value > 0 {
			return strconv.FormatUint(uint64(value), 10)
		}

	case uint64:
		if value > 0 {
			return strconv.FormatUint(value, 10)
		}

	case float64:
		n := int64(value)

		if n > 0 && value == float64(n) {
			return strconv.FormatInt(n, 10)
		}

	case float32:
		n := int64(value)

		if n > 0 && value == float32(n) {
			return strconv.FormatInt(n, 10)
		}
	}

	return ""
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
	records, err := d.search(
		ctx,
		name,
		50,
	)
	if err != nil {
		return tagRecord{}, err
	}

	for i := range records {
		if strings.EqualFold(
			strings.TrimSpace(records[i].Name),
			strings.TrimSpace(name),
		) {
			return records[i], nil
		}
	}

	return tagRecord{}, fmt.Errorf(
		"no exact tag match returned",
	)
}

func (d *Derpibooru) fetchMany(
	ctx context.Context,
	ids []string,
) ([]tagRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	terms := make([]string, 0, len(ids))

	for _, id := range ids {
		if id == "" {
			continue
		}

		terms = append(
			terms,
			"id:"+id,
		)
	}

	if len(terms) == 0 {
		return nil, nil
	}

	return d.search(
		ctx,
		strings.Join(terms, " || "),
		len(terms),
	)
}

func (d *Derpibooru) search(
	ctx context.Context,
	query string,
	perPage int,
) ([]tagRecord, error) {
	/*
		All source requests share the same slot. A normal post now uses
		one batch request instead of one request per tag, while fallback
		requests retain the same rate-limit protection.
	*/
	select {
	case d.slots <- struct{}{}:
		defer func() {
			<-d.slots
		}()

	case <-ctx.Done():
		return nil, ctx.Err()
	}

	u, err := url.Parse(
		d.baseURL + "/api/v1/json/search/tags",
	)
	if err != nil {
		return nil, err
	}

	if perPage < 1 {
		perPage = 1
	}

	if perPage > 50 {
		perPage = 50
	}

	q := u.Query()
	q.Set("q", query)
	q.Set("page", "1")
	q.Set(
		"per_page",
		strconv.Itoa(perPage),
	)
	u.RawQuery = q.Encode()

	for attempt := 0; attempt < derpibooruMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			u.String(),
			nil,
		)
		if err != nil {
			return nil, err
		}

		req.Header.Set(
			"User-Agent",
			"monloader Derpibooru taxonomy resolver",
		)

		resp, err := d.client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode >= 200 &&
			resp.StatusCode < 300 {
			var body tagSearchResponse

			err := json.NewDecoder(
				resp.Body,
			).Decode(&body)

			resp.Body.Close()

			if err != nil {
				return nil, fmt.Errorf(
					"decoding Derpibooru response: %w",
					err,
				)
			}

			return body.Tags, nil
		}

		status := resp.StatusCode

		retryable := status == http.StatusTooManyRequests ||
			status >= 500

		delay, hasRetryAfter := retryAfter(
			resp.Header.Get("Retry-After"),
		)

		_, _ = io.Copy(
			io.Discard,
			resp.Body,
		)
		resp.Body.Close()

		if !retryable ||
			attempt == derpibooruMaxAttempts-1 {
			return nil, fmt.Errorf(
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
			return nil, err
		}
	}

	return nil, fmt.Errorf(
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

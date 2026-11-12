package taxonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const derpibooruBaseURL = "https://derpibooru.org"

type Enricher interface {
	Enrich(context.Context, map[string]any) error
}

type Derpibooru struct {
	mu        sync.Mutex
	cache     map[string]tagRecord
	cachePath string
	baseURL   string
	client    *http.Client
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
	rewritten := make([]string, 0, len(sourceTags))

	for _, sourceName := range sourceTags {
		rec, err := d.resolve(ctx, sourceName)
		if err != nil {
			return fmt.Errorf(
				"resolving Derpibooru tag %q: %w",
				sourceName,
				err,
			)
		}

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

	case "character", "oc":
		return "character", sourceName, false

	case "origin",
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
	d.mu.Unlock()

	u, err := url.Parse(
		d.baseURL + "/api/v1/json/search/tags",
	)
	if err != nil {
		return tagRecord{}, err
	}

	q := u.Query()
	q.Set("q", name)
	u.RawQuery = q.Encode()

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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tagRecord{}, fmt.Errorf(
			"Derpibooru returned HTTP %d",
			resp.StatusCode,
		)
	}

	var body tagSearchResponse

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return tagRecord{}, fmt.Errorf(
			"decoding Derpibooru response: %w",
			err,
		)
	}

	var match *tagRecord

	for i := range body.Tags {
		if strings.EqualFold(
			strings.TrimSpace(body.Tags[i].Name),
			strings.TrimSpace(name),
		) {
			match = &body.Tags[i]
			break
		}
	}

	if match == nil {
		return tagRecord{}, fmt.Errorf(
			"no exact tag match returned",
		)
	}

	d.mu.Lock()
	d.cache[key] = *match
	err = d.saveLocked()
	d.mu.Unlock()

	if err != nil {
		return tagRecord{}, err
	}

	return *match, nil
}

func (d *Derpibooru) saveLocked() error {
	if d.cachePath == "" {
		return nil
	}

	if err := os.MkdirAll(
		filepath.Dir(d.cachePath),
		0o755,
	); err != nil {
		return fmt.Errorf(
			"creating taxonomy cache directory: %w",
			err,
		)
	}

	raw, err := json.MarshalIndent(
		d.cache,
		"",
		"  ",
	)
	if err != nil {
		return fmt.Errorf(
			"encoding taxonomy cache: %w",
			err,
		)
	}

	if err := os.WriteFile(
		d.cachePath,
		raw,
		0o600,
	); err != nil {
		return fmt.Errorf(
			"writing taxonomy cache: %w",
			err,
		)
	}

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

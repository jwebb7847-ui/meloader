package taxonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/shared/fsx"
	"github.com/monbooru/monloader/internal/shared/logx"
)

const (
	derpibooruBaseURL       = "https://derpibooru.org"
	derpibooruMaxConcurrent = 1
	derpibooruMaxAttempts   = 5
	derpibooruBatchSize     = 50
)

type Enricher interface {
	Enrich(context.Context, map[string]any) error
}

// BatchEnricher enriches posts from one source and returns one error per post.
type BatchEnricher interface {
	EnrichBatch(context.Context, string, []map[string]any) []error
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
	inflight        map[tagLookup]*tagCall
	slots           chan struct{}
	generation      uint64
	savedGeneration uint64
	cachePath       string
	baseURL         string
	client          *http.Client
}

type tagRecord struct {
	ID              int64       `json:"id"`
	Name            string      `json:"name"`
	Slug            string      `json:"slug,omitempty"`
	Category        tagCategory `json:"category,omitzero"`
	Namespace       string      `json:"namespace,omitempty"`
	NameInNamespace string      `json:"name_in_namespace,omitempty"`
	AliasedTag      string      `json:"aliased_tag"`
	Aliases         []string    `json:"aliases"`
	// AliasFieldsKnown distinguishes old cache entries from an empty family.
	AliasFieldsKnown bool `json:"-"`
}

func (r *tagRecord) UnmarshalJSON(raw []byte) error {
	type record tagRecord
	var decoded struct {
		record
		Aliases    json.RawMessage `json:"aliases"`
		AliasedTag json.RawMessage `json:"aliased_tag"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*r = tagRecord(decoded.record)
	if decoded.Aliases != nil {
		if err := json.Unmarshal(decoded.Aliases, &r.Aliases); err != nil {
			return err
		}
	}
	if decoded.AliasedTag != nil {
		if err := json.Unmarshal(decoded.AliasedTag, &r.AliasedTag); err != nil {
			return err
		}
	}
	r.AliasFieldsKnown = r.Aliases != nil && decoded.AliasedTag != nil
	slices.Sort(r.Aliases)
	return nil
}

func (r tagRecord) equal(other tagRecord) bool {
	return r.ID == other.ID && r.Name == other.Name && r.Slug == other.Slug &&
		r.Category == other.Category && r.Namespace == other.Namespace &&
		r.NameInNamespace == other.NameInNamespace && r.AliasedTag == other.AliasedTag &&
		r.AliasFieldsKnown == other.AliasFieldsKnown && slices.Equal(r.Aliases, other.Aliases)
}

// Philomena sends null for a tag with no category. Older caches omitted
// that field, so an omitted category needs fresh source evidence.
type tagCategory struct {
	value         string
	uncategorized bool
}

func (c *tagCategory) UnmarshalJSON(raw []byte) error {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == nil {
		*c = tagCategory{uncategorized: true}
	} else {
		*c = tagCategory{value: *value}
	}
	return nil
}

func (c tagCategory) MarshalJSON() ([]byte, error) {
	if c.uncategorized {
		return []byte("null"), nil
	}
	return json.Marshal(c.value)
}

func (r tagRecord) validate() error {
	if r.ID <= 0 || normalizeKey(r.Name) == "" {
		return fmt.Errorf("invalid Derpibooru tag identity (id %d, name %q)", r.ID, r.Name)
	}
	if !r.Category.uncategorized {
		switch r.Category.value {
		case "character", "species", "body-type", "oc", "origin",
			"content-official", "content-fanmade", "error", "spoiler", "rating":
		case "":
			return fmt.Errorf("Derpibooru tag %q has a missing or empty category", r.Name)
		default:
			return fmt.Errorf("Derpibooru tag %q has an unknown category %q", r.Name, r.Category.value)
		}
	}
	if !r.AliasFieldsKnown {
		return fmt.Errorf("Derpibooru tag %q has no verified alias fields", r.Name)
	}
	return nil
}

// id is empty when gallery-dl did not supply a usable tag ID.
type tagLookup struct {
	name string
	id   string
	slug string
}

func (t tagLookup) checkID(rec tagRecord) error {
	if t.id != "" && t.id != strconv.FormatInt(rec.ID, 10) {
		return fmt.Errorf("Derpibooru tag %q: expected id %s, got %d", t.name, t.id, rec.ID)
	}
	return nil
}

type tagSearchResponse struct {
	Tags []tagRecord `json:"tags"`
	Tag  tagRecord   `json:"tag"`
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
		inflight:  map[tagLookup]*tagCall{},
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
			return map[string]tagRecord{}, nil
		}
		for key, rec := range cache {
			if rec.validate() != nil || key != normalizeKey(rec.Name) {
				delete(cache, key)
			}
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

func (d *Derpibooru) Warm(ctx context.Context, meta map[string]any) error {
	if normalizeAdapterSource(stringValue(meta["category"])) != d.Source() {
		return nil
	}
	_, _, err := d.resolvePostTags(ctx, meta)
	return err
}

func (d *Derpibooru) Enrich(ctx context.Context, meta map[string]any) error {
	if normalizeAdapterSource(stringValue(meta["category"])) != d.Source() {
		return nil
	}
	if _, verified := meta[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag); verified {
		return nil
	}
	sourceTags, records, err := d.resolvePostTags(ctx, meta)
	if err != nil {
		return err
	}
	return d.enrichVerified(ctx, meta, sourceTags, records)
}

func (d *Derpibooru) enrichVerified(ctx context.Context, meta map[string]any, sourceTags []string, records []tagRecord) error {
	if len(sourceTags) == 0 {
		return nil
	}
	/*
		Keep the enriched flat view for rating and metadata consumers. The
		mapper reads the verified category, name and original spelling from
		the typed view so it does not infer categories from literal colons.

		Examples:
		  wings                     -> general:wings
		  rainbow dash              -> character:rainbow dash
		  artist:test               -> artist:test
		  equestria at war mod      -> meta:equestria at war mod

		Rating tags remain unchanged so the existing Philomena rating
		path continues to lift them into Monbooru's rating category.
	*/
	rewritten := make([]string, 0, len(sourceTags))
	interpreted := make([]kwdict.InterpretedTag, 0, len(sourceTags))

	for i, sourceName := range sourceTags {
		rec, aliases, err := d.aliasFamily(ctx, records[i])
		if err != nil {
			return err
		}

		nameForFiling := sourceName
		if records[i].AliasedTag != "" {
			nameForFiling = rec.Name
		}
		target, name, isRating := derpibooruTarget(rec, nameForFiling)
		if !isRating {
			tag := kwdict.InterpretedTag{Listed: sourceName, Category: target, Name: name, Aliases: aliases}
			if strings.EqualFold(strings.TrimSpace(rec.Namespace), "oc") {
				tag.LegacyBlacklistName = strings.TrimSpace(rec.NameInNamespace)
				if tag.LegacyBlacklistName == "" {
					tag.LegacyBlacklistName = afterNamespace(nameForFiling)
				}
			}
			interpreted = append(interpreted, tag)
		}

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

	if err := d.save(); err != nil {
		return err
	}
	meta["tags"] = rewritten
	meta[kwdict.InterpretedTagsKey] = interpreted

	return nil
}

func derpibooruTarget(
	rec tagRecord,
	sourceName string,
) (category string, name string, rating bool) {
	sourceName = strings.TrimSpace(sourceName)

	if rec.Category.value == "rating" {
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
		return "character", sourceName, false
	}

	switch rec.Category.value {
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

func derpibooruPostLookups(meta map[string]any) ([]string, []tagLookup, error) {
	sourceTags, err := tagStrings(meta["tags"])
	if err != nil {
		return nil, nil, err
	}
	ids := tagIDStrings(meta["tag_ids"])
	if len(sourceTags) == 0 {
		for _, id := range ids {
			if id != "" {
				return nil, nil, fmt.Errorf("Derpibooru returned tag_ids with an empty tags array")
			}
		}
	}
	// The arrays are positional siblings. An unusable ID or mismatched array
	// length leaves the corresponding names to the existing exact lookup.
	lookups := make([]tagLookup, len(sourceTags))
	idsByName := map[string]string{}
	namesByID := map[string]string{}
	for i, name := range sourceTags {
		lookup := tagLookup{name: normalizeKey(name)}
		if len(ids) == len(sourceTags) {
			lookup.id = ids[i]
		}
		lookups[i] = lookup
		if lookup.id == "" {
			continue
		}
		if prev := idsByName[lookup.name]; prev != "" && prev != lookup.id {
			return nil, nil, fmt.Errorf("Derpibooru tag %q has conflicting ids %s and %s", name, prev, lookup.id)
		}
		if prev := namesByID[lookup.id]; prev != "" && prev != lookup.name {
			return nil, nil, fmt.Errorf("Derpibooru tag id %s has conflicting names %q and %q", lookup.id, prev, name)
		}
		idsByName[lookup.name] = lookup.id
		namesByID[lookup.id] = lookup.name
	}
	for i := range lookups {
		lookups[i].id = idsByName[lookups[i].name]
	}
	return sourceTags, lookups, nil
}

func (d *Derpibooru) resolvePostTags(ctx context.Context, meta map[string]any) ([]string, []tagRecord, error) {
	sourceTags, lookups, err := derpibooruPostLookups(meta)
	if err != nil {
		return nil, nil, err
	}
	if err := d.prefillByIDs(ctx, lookups); err != nil {
		return nil, nil, fmt.Errorf("batch resolving Derpibooru taxonomy: %w", err)
	}
	records, err := d.resolveLookups(ctx, sourceTags, lookups, nil)
	return sourceTags, records, err
}

func (d *Derpibooru) uncachedIDs(lookups []tagLookup) []tagLookup {
	missing := make([]tagLookup, 0, len(lookups))
	seen := map[tagLookup]bool{}
	for _, lookup := range lookups {
		if lookup.id == "" || seen[lookup] {
			continue
		}
		seen[lookup] = true
		if _, ok := d.cached(lookup); !ok {
			missing = append(missing, lookup)
		}
	}
	return missing
}

func (d *Derpibooru) prefillByIDs(ctx context.Context, lookups []tagLookup) error {
	for chunk := range slices.Chunk(d.uncachedIDs(lookups), derpibooruBatchSize) {
		results, err := d.fetchTagBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for _, lookup := range chunk {
			if result := results[lookup]; result.err != nil {
				return result.err
			}
		}
		d.storeTagResults(chunk, results)
	}
	return nil
}

func (d *Derpibooru) cached(lookup tagLookup) (tagRecord, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cachedLocked(lookup)
}

func (d *Derpibooru) cachedLocked(lookup tagLookup) (tagRecord, bool) {
	rec, ok := d.cache[lookup.name]
	return rec, ok && (lookup.id == "" || lookup.id == strconv.FormatInt(rec.ID, 10)) &&
		(lookup.slug == "" || rec.Slug == lookup.slug)
}

func (d *Derpibooru) storeRecords(
	records map[string]tagRecord,
) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for key, rec := range records {
		old, exists := d.cache[key]

		if exists && old.equal(rec) {
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

func (d *Derpibooru) resolve(ctx context.Context, lookup tagLookup, sourceName string) (tagRecord, error) {
	d.mu.Lock()
	if rec, ok := d.cachedLocked(lookup); ok {
		d.mu.Unlock()
		return rec, nil
	}

	// Only callers asking for the same identity share the in-flight result.
	if call, ok := d.inflight[lookup]; ok {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return tagRecord{}, ctx.Err()
		case <-call.done:
			return call.rec, call.err
		}
	}
	call := &tagCall{done: make(chan struct{})}
	d.inflight[lookup] = call
	d.mu.Unlock()

	rec, err := d.fetch(ctx, lookup, sourceName)

	d.mu.Lock()
	if err == nil {
		d.cache[lookup.name] = rec
		d.generation++
	}
	call.rec = rec
	call.err = err
	delete(d.inflight, lookup)
	close(call.done)
	d.mu.Unlock()
	return rec, err
}

func (d *Derpibooru) fetch(ctx context.Context, lookup tagLookup, sourceName string) (tagRecord, error) {
	if lookup.slug != "" {
		pathSlug := strings.ReplaceAll(url.PathEscape(lookup.slug), "+", "%2B")
		u, err := url.Parse(d.baseURL + "/api/v1/json/tags/" + pathSlug)
		if err != nil {
			return tagRecord{}, err
		}
		body, err := d.requestTags(ctx, u, 1)
		if err != nil {
			return tagRecord{}, err
		}
		rec := body.Tag
		if rec.Slug != lookup.slug || normalizeKey(rec.Name) != lookup.name {
			return tagRecord{}, fmt.Errorf("Derpibooru alias target does not match slug %q", lookup.slug)
		}
		return rec, rec.validate()
	}
	records, err := d.search(ctx, sourceName, 50)
	if err != nil {
		return tagRecord{}, err
	}
	var matched tagRecord
	for _, rec := range records {
		if normalizeKey(rec.Name) != lookup.name {
			if lookup.id != "" && lookup.id == strconv.FormatInt(rec.ID, 10) {
				return tagRecord{}, fmt.Errorf("Derpibooru tag id %s: expected name %q, got %q", lookup.id, lookup.name, rec.Name)
			}
			continue
		}
		if err := lookup.checkID(rec); err != nil {
			return tagRecord{}, err
		}
		if err := rec.validate(); err != nil {
			return tagRecord{}, err
		}
		if matched.ID != 0 && !matched.equal(rec) {
			return tagRecord{}, fmt.Errorf("Derpibooru returned conflicting exact matches for tag %q", sourceName)
		}
		matched = rec
	}
	if matched.ID == 0 {
		return tagRecord{}, fmt.Errorf("no exact tag match returned")
	}
	return matched, nil
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

	body, err := d.requestTags(ctx, u, len(strings.Split(query, " || ")))
	return body.Tags, err
}

func (d *Derpibooru) requestTags(ctx context.Context, u *url.URL, terms int) (tagSearchResponse, error) {
	stats, _ := ctx.Value(requestStatsKey{}).(*RequestStats)
	waitStarted := time.Now()
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-ctx.Done():
		if stats != nil {
			stats.slotWait.Add(int64(time.Since(waitStarted)))
		}
		return tagSearchResponse{}, ctx.Err()
	}
	if stats != nil {
		stats.slotWait.Add(int64(time.Since(waitStarted)))
	}
	for attempt := 0; attempt < derpibooruMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			u.String(),
			nil,
		)
		if err != nil {
			return tagSearchResponse{}, err
		}

		req.Header.Set(
			"User-Agent",
			"monloader Derpibooru taxonomy resolver",
		)

		requestStarted := time.Now()

		resp, err := d.client.Do(req)
		requestTime := time.Since(requestStarted)
		if stats != nil {
			stats.attempts.Add(1)
			stats.httpTime.Add(int64(requestTime))
			if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
				stats.rateLimited.Add(1)
			}
		}
		if err != nil {
			logx.Infof(
				"perf: taxonomy source=derpibooru, stage=http, attempt=%d, terms=%d, duration=%s, err=%v",
				attempt+1,
				terms,
				requestTime.Round(time.Millisecond),
				err,
			)
			return tagSearchResponse{}, err
		}

		logx.Infof(
			"perf: taxonomy source=derpibooru, stage=http, attempt=%d, terms=%d, status=%d, duration=%s",
			attempt+1,
			terms,
			resp.StatusCode,
			requestTime.Round(time.Millisecond),
		)

		if resp.StatusCode >= 200 &&
			resp.StatusCode < 300 {
			var body tagSearchResponse

			err := json.NewDecoder(
				resp.Body,
			).Decode(&body)

			resp.Body.Close()

			if err != nil {
				return tagSearchResponse{}, fmt.Errorf(
					"decoding Derpibooru response: %w",
					err,
				)
			}

			return body, nil
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

		if !retryable {
			return tagSearchResponse{}, fmt.Errorf(
				"Derpibooru returned HTTP %d",
				status,
			)
		}
		if attempt == derpibooruMaxAttempts-1 {
			return tagSearchResponse{}, &RetryExhaustedError{Status: status, Attempts: attempt + 1}
		}

		if !hasRetryAfter {
			delay = time.Second * time.Duration(
				1<<attempt,
			)
		}

		if stats != nil {
			stats.retries.Add(1)
			logx.Infof("perf: job=%d stage=taxonomy-retry source=derpibooru attempt=%d status=%d terms=%d delay=%s retry_after=%t",
				stats.job, attempt+1, status, terms, delay, hasRetryAfter)
		}
		retryStarted := time.Now()
		err = sleepContext(ctx, delay)
		if stats != nil {
			stats.retryWait.Add(int64(time.Since(retryStarted)))
		}
		if err != nil {
			return tagSearchResponse{}, err
		}
	}

	return tagSearchResponse{}, fmt.Errorf(
		"Derpibooru request attempts exhausted",
	)
}

// RetryExhaustedError describes a taxonomy request that used all its HTTP
// attempts without obtaining source evidence.
type RetryExhaustedError struct {
	Status   int
	Attempts int
}

func (e *RetryExhaustedError) Error() string {
	return fmt.Sprintf("Derpibooru returned HTTP %d after %d attempts", e.Status, e.Attempts)
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

func tagStrings(v any) ([]string, error) {
	var tags []string
	switch values := v.(type) {
	case []string:
		tags = append([]string(nil), values...)
	case []any:
		tags = make([]string, len(values))
		for i, value := range values {
			name, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("Derpibooru tags entry %d is not a string", i+1)
			}
			tags[i] = name
		}
	default:
		return nil, fmt.Errorf("Derpibooru tags must be an array of strings")
	}
	for i, name := range tags {
		tags[i] = strings.TrimSpace(name)
		if tags[i] == "" {
			return nil, fmt.Errorf("Derpibooru tags entry %d is blank", i+1)
		}
	}
	return tags, nil
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

// Source identifies the gallery-dl category owned by this adapter.
func (*Derpibooru) Source() string {
	return "derpibooru"
}

var _ Adapter = (*Derpibooru)(nil)

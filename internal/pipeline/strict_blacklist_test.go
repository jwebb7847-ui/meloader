package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/monbooru"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
	"github.com/monbooru/monloader/internal/shared/logx"
	"github.com/monbooru/monloader/internal/sitestate"
	"github.com/monbooru/monloader/internal/taxonomy"
)

func TestStrictBlacklistVerdict(t *testing.T) {
	cfg := config.Default()
	cfg.Tags.Blacklist = []string{"character:rainbow_dash violence"}
	mapper, err := mapping.New(config.NewProvider(cfg), "")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := mapper.StrictBlacklist("derpibooru")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		tags           []string
		fails, blocked bool
	}{
		{name: "violent match", tags: []string{"safe", "rainbow dash", "violence"}, blocked: true},
		{name: "nonviolent explicit", tags: []string{"explicit", "rainbow dash"}},
		{name: "unverifiable", tags: []string{"safe", "rainbow dash"}, fails: true},
		{name: "missing tags", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := gdl.Item{Category: "derpibooru", Subcategory: "post", ID: "42", MediaURL: "https://example.invalid/a.jpg",
				Meta: map[string]any{"category": "derpibooru", "id": "42", "tags": tc.tags}}
			resolver := taxonomyEnrichFunc(func(_ context.Context, meta map[string]any) error {
				if tc.fails {
					return errors.New("taxonomy temporarily unavailable")
				}
				tags := slices.Clone(tc.tags)
				tags[1] = "character:rainbow dash"
				meta["tags"] = tags
				return nil
			})
			note, _, err := StrictBlacklistVerdict(context.Background(), mapper, resolver, item, rules)
			if (note != "") != tc.blocked || (err != nil) != tc.fails {
				t.Fatalf("note=%q err=%v", note, err)
			}
			if !slices.Equal(item.Meta["tags"].([]string), tc.tags) {
				t.Fatal("preflight changed the raw evidence")
			}
		})
	}
}

func TestStrictBlacklistGalleryDLMediaBoundary(t *testing.T) {
	galleryDL := os.Getenv("MELOADER_STRICT_TEST_GDL")
	if galleryDL == "" {
		t.Skip("set MELOADER_STRICT_TEST_GDL to a gallery-dl 1.32.19 virtualenv executable")
	}
	var timingLogs bytes.Buffer
	originalOutput, originalLevel := log.Writer(), logx.Get()
	log.SetOutput(&timingLogs)
	logx.Set("info")
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		logx.Set(originalLevel)
	})
	// The wrapper substitutes only source metadata. The real gallery-dl
	// CLI, booru extractors, file predicate, archive and HTTP downloader run.
	binDir := filepath.Dir(galleryDL)
	python := filepath.Join(binDir, "python3")
	if _, err := os.Stat(python); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var lock sync.Mutex
	var paths []string
	var firstMedia time.Time
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		if firstMedia.IsZero() {
			firstMedia = time.Now()
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		lock.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, "synthetic media")
	}))
	defer media.Close()
	var uploads atomic.Int32
	gallery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/api/v1/images" {
			t.Errorf("unexpected gallery request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		uploads.Add(1)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":101}`)
	}))
	defer gallery.Close()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gallery-dl")
	body := fmt.Sprintf(`#!/usr/bin/env python3
import sys
from gallery_dl import main
from gallery_dl.extractor import danbooru, e621, philomena

def posts(self):
    if self.params.get("q") == "batch_fixture":
        for ident in range(1001, 1201):
            tags, ids = ["safe", "fixture " + str(ident)], [1, ident]
            if ident == 1002:
                tags.append("unverified")
                ids.append(4)
            if ident == 1003 and "-j" not in sys.argv:
                tags.append("violence")
                ids.append(3)
            yield {"id": ident, "tags": tags, "tag_ids": ids,
                   "created_at": "2020-01-01T00:00:00Z", "format": "jpg",
                   "representations": {"full": "%[1]s/" + str(ident) + ".jpg"}}
        return
    tag_ids = {"safe": 1, "explicit": 2, "rainbow dash": 39229,
               "dashie": 24568, "rd": 63932, "violence": 3, "unverified": 4}
    listing = (
        (41, ["safe", "rainbow dash", "violence"], 41),
        (42, ["explicit", "dashie"], 42),
        (43, ["safe", "unverified"], 43),
        (44, ["safe", "rainbow dash"], 44),
        (45, ["safe", "rainbow dash"], 45),
        (46, ["safe", "dashie", "violence"], 47),
        (47, ["safe", "rd", "violence"], 48),
    )
    if "-j" not in sys.argv:
        listing = (
            (99, ["safe", "rainbow dash", "violence"], 99),
            (44, ["safe", "rainbow dash", "violence"], 44),
            (45, ["safe", "rainbow dash"], 46),
            (42, ["explicit", "dashie"], 42),
            (43, ["safe", "unverified"], 43),
            (46, ["safe", "dashie", "violence"], 47),
            (47, ["safe", "rd", "violence"], 48),
        )
    for ident, tags, media_id in listing:
        yield {"id": ident, "tags": tags, "tag_ids": [tag_ids[tag] for tag in tags], "created_at": "2020-01-01T00:00:00Z",
               "format": "jpg", "representations": {"full": "%[1]s/" + str(media_id) + ".jpg"}}

philomena.PhilomenaSearchExtractor.posts = posts

def danbooru_post(self):
    ident = int(self.groups[-1])
    content = "violence" if ident == 51 else "portrait"
    return ({"id": ident, "file_url": "%[1]s/" + str(ident) + ".jpg", "created_at": "2020-01-01T00:00:00Z",
             "rating": "e", "tag_string": "rainbow_dash " + content, "tag_string_artist": "",
             "tag_string_character": "rainbow_dash", "tag_string_copyright": "",
             "tag_string_general": content, "tag_string_meta": ""},)

def e621_post(self):
    ident = int(self.groups[-2])
    content = "violence" if ident == 61 else "portrait"
    return ({"id": ident, "file": {"url": "%[1]s/" + str(ident) + ".jpg", "ext": "jpg", "md5": "synthetic"},
             "created_at": "2020-01-01T00:00:00Z", "rating": "e",
             "tags": {"artist": [], "character": ["rainbow_dash"], "copyright": [],
                      "general": [content], "meta": []}},)

danbooru.DanbooruPostExtractor.posts = danbooru_post
e621.E621PostExtractor.posts = e621_post
main()
`, media.URL)
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Tags.StrictBlacklist = true
	cfg.Tags.Blacklist = []string{"character:rainbow_dash violence"}
	cfg.Monbooru.APIURL = gallery.URL
	cfg.GalleryDL.BinaryPath = bin
	cfg.GalleryDL.ConfigPath = filepath.Join(dir, "gallery-dl.json")
	cfg.GalleryDL.ArchivePath = filepath.Join(dir, "archive.sqlite")
	cfg.GalleryDL.SleepRequest = 0
	// User-supplied gallery-dl keywords must not hide native content tags
	// or overwrite the URL used by the strict download predicate.
	cfg.GalleryDL.RawConfig = `{
		"keywords-global":{"meloader_verified_media_url":"https://example.invalid/override.jpg"},
		"extractor":{
			"sleep":0.05,
			"danbooru":{"keywords":{"tags":["portrait"],"tags_general":["portrait"]}},
			"derpibooru":{"sleep":0,"search":{"sleep-request":[0,0.001]},"api-key":"timing-fixture-secret"}
		}
	}`
	provider := config.NewProvider(cfg)
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	lists := gdl.ListsFrom(mapper)
	if err := gdl.WriteManagedConfig(cfg, lists); err != nil {
		t.Fatal(err)
	}
	tagRecords := map[string]json.RawMessage{
		"safe":         json.RawMessage(`{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]}`),
		"explicit":     json.RawMessage(`{"id":2,"name":"explicit","category":"rating","aliased_tag":null,"aliases":[]}`),
		"rainbow dash": json.RawMessage(`{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["rd","rainbowdash","dashie"]}`),
		"dashie":       json.RawMessage(`{"id":24568,"name":"dashie","slug":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}`),
		"rd":           json.RawMessage(`{"id":63932,"name":"rd","slug":"rd","category":null,"aliased_tag":"rainbow+dash","aliases":[]}`),
		"violence":     json.RawMessage(`{"id":3,"name":"violence","category":null,"aliased_tag":null,"aliases":[]}`),
		"unverified":   json.RawMessage(`{"id":4,"name":"unverified","category":null}`),
	}
	var taxonomyCalls atomic.Int32
	var taxonomyTime atomic.Int64
	var simulateLatency atomic.Bool
	var retryOnce atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		defer func() { taxonomyTime.Add(int64(time.Since(started))) }()
		taxonomyCalls.Add(1)
		if simulateLatency.Load() {
			time.Sleep(10 * time.Millisecond)
		}
		if retryOnce.Swap(false) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "fixture rate limit", http.StatusTooManyRequests)
			return
		}
		if r.URL.Path == "/api/v1/json/tags/rainbow+dash" {
			_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"tag": tagRecords["rainbow dash"]})
			return
		}
		name := r.URL.Query().Get("q")
		if strings.HasPrefix(name, "id:") {
			var records []json.RawMessage
			for _, term := range strings.Split(name, " || ") {
				for _, record := range tagRecords {
					if strings.HasPrefix(string(record), `{"id":`+strings.TrimPrefix(term, "id:")+",") {
						records = append(records, record)
						break
					}
				}
				var id int
				if _, err := fmt.Sscanf(term, "id:%d", &id); err == nil && id >= 1001 && id <= 1200 {
					aliases := `[]`
					if id == 1001 {
						aliases = `["batch-dash-block"]`
					}
					records = append(records, json.RawMessage(fmt.Sprintf(
						`{"id":%d,"name":"fixture %d","category":null,"aliased_tag":null,"aliases":%s}`,
						id, id, aliases)))
				}
			}
			_ = json.NewEncoder(w).Encode(map[string][]json.RawMessage{"tags": records})
			return
		}
		if rec, ok := tagRecords[name]; ok {
			_ = json.NewEncoder(w).Encode(map[string][]json.RawMessage{"tags": {rec}})
			return
		}
		t.Errorf("unexpected taxonomy request: %s", r.URL)
		http.NotFound(w, r)
	}))
	defer source.Close()
	galleryClient := monbooru.New(provider)
	originalTransport := http.DefaultTransport
	http.DefaultTransport = derpibooruFixtureTransport{server: source, fallback: originalTransport}
	defer func() { http.DefaultTransport = originalTransport }()
	resolver, err := taxonomy.NewDerpibooru("")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := taxonomy.NewRegistry(resolver)
	if err != nil {
		t.Fatal(err)
	}
	p := New(gdl.New(provider, lists), mapper, galleryClient, provider, dir, sitestate.New(), nil, nil, registry)
	for round, rule := range []string{"character:rainbow_dash violence", "character:dashie violence", "character:rd violence"} {
		if err := provider.Update(func(cfg *config.Config) error {
			cfg.Tags.Blacklist = []string{rule}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		job := &queue.Job{ID: int64(round + 1), URL: "https://derpibooru.org/search?q=rainbow+dash", Gallery: "Audit_A", MaxItems: 7}
		if err := p.processDownload(context.Background(), job, job.Snapshot()); err != nil {
			t.Fatal(err)
		}
		items := job.Snapshot().Items
		if len(items) != 7 {
			t.Fatalf("items=%+v", items)
		}
		good := outcome.Created
		if round > 0 {
			good = outcome.SkippedArchive
		}
		want := []outcome.Outcome{outcome.SkippedBlacklist, good, outcome.Failed, outcome.Failed, outcome.Failed,
			outcome.SkippedBlacklist, outcome.SkippedBlacklist}
		for i, item := range items {
			if item.Outcome != want[i] {
				t.Errorf("round %d post %s = %+v, want %s", round, item.PostID, item, want[i])
			}
		}
		if !strings.Contains(items[2].Error, "no verified alias fields") {
			t.Errorf("missing taxonomy diagnostic: %+v", items[2])
		}
	}
	if err := provider.Update(func(cfg *config.Config) error {
		cfg.Tags.Blacklist = []string{"character:rainbow_dash violence"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"/untagged.jpg", "/untagged"} {
		job := &queue.Job{ID: int64(i + 4), URL: media.URL + path, Gallery: "Audit_A"}
		if err := p.processDownload(context.Background(), job, job.Snapshot()); err == nil {
			t.Errorf("direct link %s was accepted", path)
		}
	}
	for i, tc := range []struct {
		url  string
		want outcome.Outcome
	}{
		{"https://danbooru.donmai.us/posts/51", outcome.SkippedBlacklist},
		{"https://danbooru.donmai.us/posts/52", outcome.Created},
		{"https://e621.net/posts/61", outcome.SkippedBlacklist},
		{"https://e621.net/posts/62", outcome.Created},
	} {
		job := &queue.Job{ID: int64(i + 6), URL: tc.url, Gallery: "Audit_A"}
		if err := p.processDownload(context.Background(), job, job.Snapshot()); err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		items := job.Snapshot().Items
		if len(items) != 1 || items[0].Outcome != tc.want {
			t.Errorf("%s: items=%+v, want %s", tc.url, items, tc.want)
		}
	}
	if got := uploads.Load(); got != 3 {
		t.Errorf("gallery uploads=%d, want 3", got)
	}
	lock.Lock()
	if !slices.Equal(paths, []string{"GET /42.jpg", "GET /52.jpg", "GET /62.jpg"}) {
		t.Errorf("media requests=%v, want only the explicit nonviolent controls", paths)
	}
	lock.Unlock()
	for job := int64(2); job <= 3; job++ {
		transfer := strictTimingFields(t, timingLogs.String(), job, "strict-transfer")
		if transfer["first_file"] != "none" || transfer["files"] != "0" || transfer["archive_skips"] != "1" || transfer["file_bytes"] != "0" {
			t.Errorf("archive-only timing for job %d = %v", job, transfer)
		}
	}
	// An explicit replay must recover an allowed file even when the
	// download archive remembers it. The same replay still rejects posts
	// blocked by the current blacklist or missing valid source evidence.
	forced := &queue.Job{ID: 11, URL: "https://derpibooru.org/search?q=rainbow+dash", Options: queue.Options{Gallery: "Audit_A", MaxItems: 7, Force: true}}
	if err := p.processDownload(t.Context(), forced, forced.Snapshot()); err != nil {
		t.Fatal(err)
	}
	var forcedOutcomes []outcome.Outcome
	for _, item := range forced.Snapshot().Items {
		forcedOutcomes = append(forcedOutcomes, item.Outcome)
	}
	wantForced := []outcome.Outcome{outcome.SkippedBlacklist, outcome.Created, outcome.Failed, outcome.Failed, outcome.Failed, outcome.SkippedBlacklist, outcome.SkippedBlacklist}
	if !slices.Equal(forcedOutcomes, wantForced) || uploads.Load() != 4 {
		t.Errorf("forced replay outcomes = %v, uploads = %d", forcedOutcomes, uploads.Load())
	}
	lock.Lock()
	if !slices.Equal(paths, []string{"GET /42.jpg", "GET /52.jpg", "GET /62.jpg", "GET /42.jpg"}) {
		t.Errorf("forced replay requested unexpected media: %v", paths)
	}
	lock.Unlock()

	if err := provider.Update(func(cfg *config.Config) error {
		cfg.Tags.Blacklist = []string{"batch-block"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var baseline []outcome.Outcome
	simulateLatency.Store(true)
	for _, mode := range []string{"per-post", "cross-post"} {
		resolver, err := taxonomy.NewDerpibooru("")
		if err != nil {
			t.Fatal(err)
		}
		registry, err := taxonomy.NewRegistry(resolver)
		if err != nil {
			t.Fatal(err)
		}
		var enricher taxonomy.Enricher = registry
		wantCalls := int32(5)
		wantRetries := "0"
		if mode == "per-post" {
			// Preserve the old pipeline path with the same resolver and fixture.
			enricher = taxonomyEnrichFunc(registry.Enrich)
			wantCalls = 200
		} else {
			retryOnce.Store(true)
			wantCalls++
			wantRetries = "1"
		}
		p := New(gdl.New(provider, lists), mapper, galleryClient, provider, dir, sitestate.New(), nil, nil, enricher)
		if err := os.Remove(cfg.GalleryDL.ArchivePath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		taxonomyCalls.Store(0)
		taxonomyTime.Store(0)
		lock.Lock()
		paths, firstMedia = nil, time.Time{}
		lock.Unlock()
		timingLogs.Reset()
		start := time.Now()
		job := &queue.Job{ID: 10, URL: "https://derpibooru.org/search?q=batch_fixture", Gallery: "Audit_A", MaxItems: 200}
		if err := p.processDownload(t.Context(), job, job.Snapshot()); err != nil {
			t.Fatal(err)
		}
		if got := taxonomyCalls.Load(); got != wantCalls {
			t.Errorf("%s: %d taxonomy requests, want %d", mode, got, wantCalls)
		}
		var outcomes []outcome.Outcome
		for _, item := range job.Snapshot().Items {
			outcomes = append(outcomes, item.Outcome)
		}
		if len(outcomes) != 200 || !slices.Equal(outcomes[:3], []outcome.Outcome{outcome.SkippedBlacklist, outcome.Failed, outcome.Failed}) {
			t.Fatalf("%s: unexpected outcomes %v", mode, outcomes)
		}
		for _, got := range outcomes[3:] {
			if got != outcome.Created {
				t.Fatalf("%s: allowed control outcome %q", mode, got)
			}
		}
		if baseline == nil {
			baseline = outcomes
		} else if !slices.Equal(baseline, outcomes) {
			t.Fatal("cross-post outcomes differ from per-post outcomes")
		}
		lock.Lock()
		gotPaths, first := slices.Clone(paths), firstMedia
		lock.Unlock()
		if len(gotPaths) != 197 {
			t.Fatalf("%s: %d media requests, want 197", mode, len(gotPaths))
		}
		for i, path := range gotPaths {
			if path != fmt.Sprintf("GET /%d.jpg", i+1004) {
				t.Fatalf("%s: unexpected media request %q", mode, path)
			}
		}
		logs := timingLogs.String()
		if strings.Contains(logs, "timing-fixture-secret") {
			t.Fatal("timing exposed raw gallery-dl configuration")
		}
		preflight := strictTimingFields(t, logs, job.ID, "strict-preflight")
		transfer := strictTimingFields(t, logs, job.ID, "strict-transfer")
		ingest := strictTimingFields(t, logs, job.ID, "strict-ingest")
		for _, check := range []struct {
			got, want map[string]string
		}{
			{preflight, map[string]string{"source": "derpibooru", "posts": "200", "approved": "198", "blacklisted": "1", "refused": "1",
				"complete": "true", "canceled": "false", "taxonomy_attempts": strconv.Itoa(int(wantCalls)), "taxonomy_429": wantRetries, "taxonomy_retries": wantRetries}},
			{transfer, map[string]string{"files": "197", "archive_skips": "0", "file_bytes": "2955", "size_errors": "0", "sleep": "0", "sleep_request": "0-0.001", "failed": "false", "canceled": "false"}},
			{ingest, map[string]string{"transfer_rows": "197", "canceled": "false"}},
		} {
			for key, want := range check.want {
				if got := check.got[key]; got != want {
					t.Errorf("%s: %s=%q, want %q", mode, key, got, want)
				}
			}
		}
		for _, stage := range []map[string]string{preflight, transfer, ingest} {
			strictTimingDuration(t, stage, "duration")
		}
		for _, key := range []string{"taxonomy_http", "taxonomy_slot_wait", "taxonomy_retry_wait"} {
			strictTimingDuration(t, preflight, key)
		}
		if wantRetries == "1" {
			retry := strictTimingFields(t, logs, job.ID, "taxonomy-retry")
			if retry["status"] != "429" || retry["delay"] != "1s" || retry["retry_after"] != "true" {
				t.Errorf("unexpected retry diagnostic: %v", retry)
			}
			if got := strictTimingDuration(t, preflight, "taxonomy_retry_wait"); got < time.Second {
				t.Errorf("retry wait = %s, want at least 1s", got)
			}
		}
		for _, key := range []string{"manifest", "version_check", "filter_probe", "result_verification"} {
			strictTimingDuration(t, transfer, key)
		}
		if strictTimingDuration(t, transfer, "first_file") > strictTimingDuration(t, transfer, "download_process") {
			t.Error("first file appeared after the download process ended")
		}
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, " stage=strict-") {
				t.Log(line)
			}
		}
		t.Logf("%s: 200 posts, %d taxonomy requests, taxonomy handler time %s, first media %s, total %s",
			mode, taxonomyCalls.Load(), time.Duration(taxonomyTime.Load()), first.Sub(start), time.Since(start))
	}
}

func strictTimingFields(t *testing.T, logs string, job int64, stage string) map[string]string {
	t.Helper()
	prefix := "perf: job=" + strconv.FormatInt(job, 10) + " stage=" + stage + " "
	for _, line := range strings.Split(logs, "\n") {
		if _, data, found := strings.Cut(line, prefix); found {
			fields := make(map[string]string)
			for _, field := range strings.Fields(data) {
				key, value, _ := strings.Cut(field, "=")
				fields[key] = value
			}
			return fields
		}
	}
	t.Fatalf("missing timing for job %d stage %s", job, stage)
	return nil
}

func strictTimingDuration(t *testing.T, fields map[string]string, key string) time.Duration {
	t.Helper()
	duration, err := time.ParseDuration(fields[key])
	if err != nil || duration < 0 {
		t.Fatalf("invalid timing %s=%q", key, fields[key])
	}
	return duration
}

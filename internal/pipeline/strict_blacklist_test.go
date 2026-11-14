package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/monbooru"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
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
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
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
        yield {"id": ident, "tags": tags, "created_at": "2020-01-01T00:00:00Z",
               "format": "jpg", "representations": {"full": "%s/" + str(media_id) + ".jpg"}}

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
	// User-supplied gallery-dl keywords must not hide native content tags
	// or overwrite the URL used by the strict download predicate.
	cfg.GalleryDL.RawConfig = `{
		"keywords-global":{"meloader_verified_media_url":"https://example.invalid/override.jpg"},
		"extractor":{"danbooru":{"keywords":{"tags":["portrait"],"tags_general":["portrait"]}}}
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
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/json/tags/rainbow+dash" {
			_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"tag": tagRecords["rainbow dash"]})
			return
		}
		name := r.URL.Query().Get("q")
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
	p := New(gdl.New(provider, lists), mapper, galleryClient, provider, dir, sitestate.New(), nil, nil, resolver)
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
	defer lock.Unlock()
	if !slices.Equal(paths, []string{"GET /42.jpg", "GET /52.jpg", "GET /62.jpg"}) {
		t.Errorf("media requests=%v, want only the explicit nonviolent controls", paths)
	}
}

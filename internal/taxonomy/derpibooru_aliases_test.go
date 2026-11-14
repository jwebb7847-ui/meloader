package taxonomy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
)

func TestDerpibooruAliasesAcrossBlacklistPaths(t *testing.T) {
	var batches, details atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v1/json/search/tags":
			batches.Add(1)
			if got := r.URL.Query().Get("q"); got != "id:1 || id:39229 || id:2" {
				t.Errorf("unexpected taxonomy query %q", got)
			}
			fmt.Fprint(w, `{"tags":[
				{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},
				{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["rd","rainbowdash","dashie"]},
				{"id":2,"name":"violence","category":null,"aliased_tag":null,"aliases":[]}
			]}`)
		default:
			details.Add(1)
			t.Errorf("unexpected taxonomy request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "tags.json")
	resolver, err := newDerpibooru(cache, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{"category": "derpibooru", "tags": []string{"safe", "rainbow dash", "violence"}, "tag_ids": []int{1, 39229, 2}}
	meta := map[string]any{"category": raw["category"], "tags": raw["tags"], "tag_ids": raw["tag_ids"]}
	if err := resolver.Enrich(t.Context(), meta); err != nil {
		t.Fatal(err)
	}
	warm, err := newDerpibooru(cache, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	meta = map[string]any{"category": raw["category"], "tags": raw["tags"], "tag_ids": raw["tag_ids"]}
	if err := warm.Enrich(t.Context(), meta); err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 1 || details.Load() != 0 {
		t.Fatalf("taxonomy requests after restart: %d batch, %d detail", batches.Load(), details.Load())
	}

	for _, tc := range []struct {
		rule string
		hit  bool
	}{
		{"character:dashie violence", true},
		{"character:rd violence", true},
		{"rainbowdash violence", true},
		{"character:unverified_dash violence", false},
		{"artist:dashie violence", false},
		{"-character:rd violence", false},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			cfg := config.Default()
			cfg.Tags.Blacklist = []string{tc.rule}
			mapper, err := mapping.New(config.NewProvider(cfg), "")
			if err != nil {
				t.Fatal(err)
			}
			rules, err := mapper.StrictBlacklist("derpibooru")
			if err != nil {
				t.Fatal(err)
			}
			_, normal := mapper.Blacklisted(meta)
			strict := rules.Match(raw, meta) != ""
			trace := mapper.Trace(meta)
			if normal != tc.hit || strict != tc.hit || (trace.Skipped != "") != tc.hit {
				t.Fatalf("normal=%v strict=%v trace=%q, want match=%v", normal, strict, trace.Skipped, tc.hit)
			}
			if !slices.ContainsFunc(trace.Rows, func(row mapping.TraceRow) bool {
				return row.Listed == "rainbow dash" && slices.ContainsFunc(row.Reads, func(read mapping.TraceRead) bool {
					return read.Tag == "character:dashie"
				})
			}) {
				t.Fatal("Trace did not show the alias under the original spelling")
			}
			if !tc.hit {
				want := []string{"character:rainbow_dash", "rating:general", "violence"}
				if got := mapper.Map(meta).Tags; !slices.Equal(got, want) {
					t.Fatalf("filed tags %v, want %v", got, want)
				}
			}
		})
	}
}

func TestDerpibooruListedAliasRequiresConfirmedTarget(t *testing.T) {
	for _, tc := range []struct {
		name, target, errorText string
	}{
		{"verified", `{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["dashie"]}`, ""},
		{"unconfirmed", `{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":[]}`, "does not confirm"},
		{"wrong slug", `{"id":39229,"name":"rainbow dash","slug":"other","category":"character","aliased_tag":null,"aliases":["dashie"]}`, "does not match slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var details atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case "/api/v1/json/search/tags":
					fmt.Fprint(w, `{"tags":[{"id":24568,"name":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}]}`)
				case "/api/v1/json/tags/rainbow%2Bdash":
					details.Add(1)
					fmt.Fprintf(w, `{"tag":%s}`, tc.target)
				default:
					t.Errorf("unexpected taxonomy request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			resolver, err := newDerpibooru("", server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			meta := map[string]any{"category": "derpibooru", "tags": []string{"dashie"}, "tag_ids": []int{24568}}
			err = resolver.Enrich(t.Context(), meta)
			if tc.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorText) || !slices.Equal(meta["tags"].([]string), []string{"dashie"}) {
					t.Fatalf("enrich error=%v tags=%v", err, meta["tags"])
				}
			} else if err != nil || !slices.Equal(meta["tags"].([]string), []string{"character:rainbow dash"}) {
				t.Fatalf("enrich error=%v tags=%v", err, meta["tags"])
			}
			if details.Load() != 1 {
				t.Fatalf("detail requests = %d, want 1", details.Load())
			}
		})
	}
}

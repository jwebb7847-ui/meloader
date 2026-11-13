package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/monbooru"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
)

type taxonomyEnrichFunc func(context.Context, map[string]any) error

func (f taxonomyEnrichFunc) Enrich(ctx context.Context, meta map[string]any) error {
	return f(ctx, meta)
}

func TestOnlineLookupResolvesRequiredTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		resolve error
		failure string
	}{
		{name: "resolved", id: "42"},
		{name: "temporarily unavailable", id: "42", resolve: errors.New("taxonomy service temporarily unavailable"), failure: "taxonomy service temporarily unavailable"},
		{name: "missing post identity", failure: "source identity:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct {
				path    string
				payload monbooru.EnrichPayload
				status  map[string]string
			}
			requests := make(chan request, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := request{path: r.URL.Path}
				var err error
				switch r.URL.Path {
				case "/api/v1/images/7/enrich":
					err = json.NewDecoder(r.Body).Decode(&got.payload)
				case "/api/v1/images/7/fetch-status":
					err = json.NewDecoder(r.Body).Decode(&got.status)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if err != nil {
					t.Errorf("decode %s: %v", r.URL.Path, err)
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				requests <- got
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			cfg := config.Default()
			cfg.Monbooru.APIURL = server.URL
			provider := config.NewProvider(cfg)
			mapper, err := mapping.New(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			p := &Processor{mapper: mapper, cfg: provider, client: monbooru.New(provider),
				taxonomy: taxonomyEnrichFunc(func(_ context.Context, meta map[string]any) error {
					if tc.id == "" {
						t.Error("taxonomy called before validating post ID")
					}
					if tc.resolve != nil {
						return tc.resolve
					}
					meta["tags"] = []string{"safe", "character:rainbow dash"}
					return nil
				}),
			}
			job := &queue.Job{}
			job.SetItems([]queue.Item{{Status: queue.ItemPending}})
			snap := &queue.Job{}
			snap.ImageID = 7
			snap.Kind = queue.KindLookup
			meta := map[string]any{"category": "derpibooru", "id": tc.id, "tags": []string{"safe", "rainbow dash"}}

			_, _, _, err = p.enrichAllOnline(context.Background(), job, snap, meta, nil)
			if tc.failure != "" {
				if err == nil || errorCode(err) != outcome.ErrCodeMappingFailed {
					t.Fatalf("taxonomy refusal = %v, want mapping_failed", err)
				}
				p.failOnline(context.Background(), job, snap, err)
				item := job.Snapshot().Items[0]
				if item.Status != queue.ItemFailed || !strings.Contains(item.Error, tc.failure) {
					t.Fatalf("taxonomy failure was not visible: %+v", item)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("got %d requests, want exactly one", len(requests))
			}
			got := <-requests
			if tc.failure != "" {
				if got.path != "/api/v1/images/7/fetch-status" ||
					got.status["state"] != outcome.ErrCodeMappingFailed ||
					!strings.Contains(got.status["message"], tc.failure) {
					t.Fatalf("unexpected failure report: %+v", got)
				}
			} else if got.path != "/api/v1/images/7/enrich" ||
				got.payload.Source != "derpibooru" || got.payload.PostID != "42" ||
				got.payload.URL != "https://derpibooru.org/images/42" ||
				!slices.Equal(got.payload.Tags, []string{"character:rainbow_dash", "rating:general"}) {
				t.Fatalf("unexpected enrichment: %+v", got)
			}
		})
	}
}

func TestBundleRejectsFlatOnlyPageBeforeBuilding(t *testing.T) {
	cfg := config.NewProvider(config.Default())
	mapper, err := mapping.New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{mapper: mapper, cfg: cfg}
	job := &queue.Job{}
	job.SetItems([]queue.Item{{Status: queue.ItemPending}})
	dir := t.TempDir()
	p.processCBZ(context.Background(), job, 0, []gdl.Downloaded{
		{Path: filepath.Join(dir, "1.jpg"), Meta: map[string]any{
			"category": "e621", "rating": "s", "tags_general": []string{"sky"},
		}},
		{Path: filepath.Join(dir, "2.jpg"), Meta: map[string]any{
			"category": "e621", "rating": "s", "tags": []string{"rainbow_dash"},
		}},
	}, 2, "", dir, nil, "https://example.invalid/pool")

	item := job.Snapshot().Items[0]
	if item.Status != queue.ItemFailed || item.ErrorCode != outcome.ErrCodeMappingFailed ||
		!strings.Contains(item.Error, mapping.FlattenedTagsMsg) {
		t.Fatalf("flat-only bundle page was not refused: %+v", item)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundle.cbz")); !os.IsNotExist(err) {
		t.Fatalf("bundle built before taxonomy validation: stat error=%v", err)
	}
}

func TestBundleSkipsWhenAnEnrichedPageMatchesBlacklist(t *testing.T) {
	cfg := config.Default()
	cfg.Tags.Blacklist = []string{"character:twilight_sparkle"}
	provider := config.NewProvider(cfg)
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{mapper: mapper, cfg: provider,
		taxonomy: taxonomyEnrichFunc(func(_ context.Context, meta map[string]any) error {
			if meta["id"] == "2" {
				meta["tags"] = []string{"safe", "character:twilight sparkle"}
			}
			return nil
		}),
	}
	job := &queue.Job{}
	job.SetItems([]queue.Item{{Status: queue.ItemPending}})
	dir := t.TempDir()
	p.processCBZ(context.Background(), job, 0, []gdl.Downloaded{
		{Path: filepath.Join(dir, "1.jpg"), Meta: map[string]any{
			"category": "derpibooru", "id": "1", "tags": []string{"safe", "sky"},
		}},
		{Path: filepath.Join(dir, "2.jpg"), Meta: map[string]any{
			"category": "derpibooru", "id": "2", "tags": []string{"safe", "twilight sparkle"},
		}},
	}, 2, "", dir, nil, "https://example.invalid/pool")

	item := job.Snapshot().Items[0]
	if item.Status != queue.ItemSkipped || item.Outcome != outcome.SkippedBlacklist {
		t.Fatalf("bundle with a blacklisted page was not skipped: %+v", item)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundle.cbz")); !os.IsNotExist(err) {
		t.Fatalf("bundle built before the enriched blacklist check: stat error=%v", err)
	}
}

type derpibooruRefreshRunner struct {
	gdl.Runner
	items []gdl.Item
}

func (r derpibooruRefreshRunner) Resolve(context.Context, string, string, bool) (gdl.ResolveResult, error) {
	return gdl.ResolveResult{Items: r.items}, nil
}

func TestDerpibooruRefreshChecksPostIdentity(t *testing.T) {
	provider := config.NewProvider(config.Default())
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	first := map[string]any{"category": "derpibooru", "id": "42"}
	other := map[string]any{"category": "derpibooru", "id": "43"}
	const listing = "https://derpibooru.org/search?q=safe&id=42"
	for _, tc := range []struct {
		name  string
		url   string
		items []gdl.Item
		want  string
	}{
		{name: "matching short post URL", url: "https://derpibooru.org/42"},
		{name: "post URL disagrees", url: "https://derpibooru.org/images/43", want: outcome.ErrCodeMappingFailed},
		{name: "listing resolves to same single post", url: listing, items: []gdl.Item{{ID: "42", Meta: first}}},
		{name: "listing changed", url: listing, items: []gdl.Item{{ID: "43", Meta: other}}, want: outcome.ErrCodeMappingFailed},
		{name: "listing has more than one post", url: listing, items: []gdl.Item{{ID: "42", Meta: first}, {ID: "43", Meta: other}}, want: outcome.ErrCodeUnsupportedURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Processor{mapper: mapper, runner: derpibooruRefreshRunner{items: tc.items}}
			err := p.manyPostsErr(context.Background(), tc.url, first)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || errorCode(err) != tc.want) {
				t.Fatalf("refresh validation = %v, want code %q", err, tc.want)
			}
		})
	}
}

package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/monbooru"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
	"github.com/monbooru/monloader/internal/shared/contract"
)

// Exercises the real pipeline -> mapping -> HTTP-upload boundary against an
// in-process fake Mebooru. No real site, configured gallery, or internet is used.
func TestRatingIntegrityAtImageUploadBoundary(t *testing.T) {
	cases := []struct {
		name       string
		meta       map[string]any
		wantUpload bool
		wantTags   []string
		wantReason string
	}{
		{
			name: "categorized e621 valid source rating",
			meta: map[string]any{
				"category":       "e621",
				"id":             535,
				"rating":         "s",
				"tags_character": []any{"rainbow_dash"},
				"tags_general":   []any{"sky"},
			},
			wantUpload: true,
			wantTags:   []string{"character:rainbow_dash", "rating:general", "sky"},
		},
		{
			name: "generic source genuinely unrated",
			meta: map[string]any{
				"category": "example-photography",
				"id":       12,
				"tags":     []any{"blue_sky"},
			},
			wantUpload: true,
			wantTags:   []string{"blue_sky"},
		},
		{
			name: "known rated source malformed rating",
			meta: map[string]any{
				"category":       "e621",
				"id":             536,
				"rating":         "not-a-rating",
				"tags_character": []any{"rainbow_dash"},
			},
			wantReason: "source rating:",
		},
		{
			name: "known rated source missing rating",
			meta: map[string]any{
				"category":       "e621",
				"id":             537,
				"tags_character": []any{"rainbow_dash"},
			},
			wantReason: "source rating:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			uploaded := make(chan []string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" {
					t.Errorf("unexpected upload request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("reading upload form: %v", err)
					http.Error(w, "invalid multipart", http.StatusBadRequest)
					return
				}
				var tags []string
				if err := json.Unmarshal([]byte(r.FormValue(contract.FieldTags)), &tags); err != nil {
					t.Errorf("parsing uploaded tags: %v", err)
					http.Error(w, "invalid tags", http.StatusBadRequest)
					return
				}
				uploaded <- tags
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":101}`))
			}))
			defer srv.Close()

			cfg := config.Default()
			cfg.Monbooru.APIURL = srv.URL
			cfg.Monbooru.APIToken = "integration-test-token"
			cfg.Tags.Blacklist = nil
			provider := config.NewProvider(cfg)
			mapper, err := mapping.New(provider, "")
			if err != nil {
				t.Fatal(err)
			}

			p := &Processor{
				mapper: mapper,
				cfg:    provider,
				client: monbooru.New(provider),
			}
			job := &queue.Job{}
			job.SetItems([]queue.Item{{Status: queue.ItemPending}})
			path := filepath.Join(t.TempDir(), "candidate.jpg")
			if err := os.WriteFile(path, []byte("local test bytes, not actual image"), 0o600); err != nil {
				t.Fatal(err)
			}
			p.processItems(context.Background(), job,
				[]*gdl.Downloaded{{Path: path, Meta: tc.meta}},
				nil, "", "https://example.invalid/submitted", "", nil, 1)

			item := job.Snapshot().Items[0]
			expectedCount := int32(0)
			if tc.wantUpload {
				expectedCount = 1
			}
			if got := requests.Load(); got != expectedCount {
				t.Fatalf("requests = %d, expected %d, item = %+v", got, expectedCount, item)
			}
			if tc.wantUpload {
				if item.Status != queue.ItemDone || item.Outcome != outcome.Created {
					t.Fatalf("expected successful upload, got %+v", item)
				}
				select {
				case tags := <-uploaded:
					if !reflect.DeepEqual(tags, tc.wantTags) {
						t.Fatalf("sent tags = %v, want %v", tags, tc.wantTags)
					}
				default:
					t.Fatal("upload succeeded without sending tags")
				}
			} else {
				if item.Status != queue.ItemFailed || item.ErrorCode != outcome.ErrCodeMappingFailed ||
					!strings.Contains(item.Error, tc.wantReason) {
					t.Fatalf("expected mapping_failed without HTTP upload, got %+v", item)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("pipeline left scratch media after processing: stat error=%v", err)
			}
		})
	}
}

// A bundled gallery is only as safe as its least trustworthy page.
func TestRatingIntegrityRejectsBundledGalleryBeforeUpload(t *testing.T) {
	cfg := config.Default()
	provider := config.NewProvider(cfg)
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{mapper: mapper, cfg: provider} // nil client must never be called
	job := &queue.Job{}
	job.SetItems([]queue.Item{{Status: queue.ItemPending}})

	dir := t.TempDir()
	p.processCBZ(context.Background(), job, 0, []gdl.Downloaded{
		{Path: filepath.Join(dir, "1.jpg"), Meta: map[string]any{"category": "e621", "rating": "s"}},
		{Path: filepath.Join(dir, "2.jpg"), Meta: map[string]any{"category": "e621", "rating": "bad"}},
	}, 2, "", dir, nil, "https://example.invalid/pool")

	it := job.Snapshot().Items[0]
	if it.Status != queue.ItemFailed || it.ErrorCode != outcome.ErrCodeMappingFailed ||
		!strings.Contains(it.Error, "source rating:") {
		t.Fatalf("bundle incorrectly accepted invalid member: %+v", it)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundle.cbz")); !os.IsNotExist(err) {
		t.Fatalf("bundle built before rating validation; stat error=%v", err)
	}
}

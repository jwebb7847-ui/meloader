package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// Benchmarks the local pipeline -> mapper/rating guard -> HTTP multipart upload
// boundary, not an external extractor or real Mebooru's SQLite/image decoding.
//
// Scratch media are synthetic 16 KiB files created outside the timed section.
// The timed section still reads them and sends their bytes over loopback to a
// local httptest server that parses the multipart request and its rated tags.
// No internet access, real gallery, or configured client credentials are used.
func BenchmarkRatingIntegrityHTTPUploadBatch(b *testing.B) {
	for _, posts := range []int{20, 200, 1_000} {
		b.Run(fmt.Sprintf("%d-posts", posts), func(b *testing.B) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" {
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					http.Error(w, "invalid multipart", http.StatusBadRequest)
					return
				}
				var tags []string
				if err := json.Unmarshal([]byte(r.FormValue(contract.FieldTags)), &tags); err != nil {
					http.Error(w, "invalid tags", http.StatusBadRequest)
					return
				}
				hasRating := false
				for _, tag := range tags {
					if tag == "rating:general" {
						hasRating = true
						break
					}
				}
				if !hasRating {
					http.Error(w, "rating missing", http.StatusUnprocessableEntity)
					return
				}
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":101}`))
			}))
			defer srv.Close()

			cfg := config.Default()
			cfg.Monbooru.APIURL = srv.URL
			cfg.Monbooru.APIToken = "local-benchmark-only"
			cfg.Tags.Blacklist = nil
			provider := config.NewProvider(cfg)
			mapper, err := mapping.New(provider, "")
			if err != nil {
				b.Fatal(err)
			}
			p := &Processor{
				mapper: mapper,
				cfg:    provider,
				client: monbooru.New(provider),
			}
			workDir := b.TempDir()
			fakeMedia := bytes.Repeat([]byte{0xa5}, 16<<10)

			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				job := &queue.Job{}
				items := make([]queue.Item, posts)
				entries := make([]*gdl.Downloaded, posts)
				for i := range items {
					items[i].Status = queue.ItemPending
					path := filepath.Join(workDir, fmt.Sprintf("post-%05d.jpg", i))
					if err := os.WriteFile(path, fakeMedia, 0o600); err != nil {
						b.Fatal(err)
					}
					entries[i] = &gdl.Downloaded{
						Path: path,
						Meta: map[string]any{
							"category":       "e621",
							"id":             i + 1,
							"rating":         "s",
							"tags_character": []any{"rainbow_dash"},
							"tags_general":   []any{"sky"},
						},
					}
				}
				job.SetItems(items)
				b.StartTimer()
				p.processItems(context.Background(), job, entries, nil, "SyntheticAudit", "https://example.invalid/posts", "", nil, posts)
				b.StopTimer()

				for i, item := range job.Snapshot().Items {
					if item.Status != queue.ItemDone || item.Outcome != outcome.Created {
						b.Fatalf("post %d not uploaded: status=%v, outcome=%v, error=%q", i, item.Status, item.Outcome, item.Error)
					}
				}
			}
			if got, want := requests.Load(), int64(posts*b.N); got != want {
				b.Fatalf("uploaded %d posts, expected %d", got, want)
			}
			if elapsed := b.Elapsed().Seconds(); elapsed > 0 {
				b.ReportMetric(float64(posts*b.N)/elapsed, "posts/s")
			}
			// Using a different source/rating or actual image bytes requires a
			// new benchmark label; these timings must not be called download
			// throughput or Mebooru ingestion/storage throughput.
		})
	}
}

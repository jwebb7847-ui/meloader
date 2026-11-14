package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/monbooru/monloader/internal/taxonomy"
)

// Route only the taxonomy transport to a loopback fixture. Mebooru requests
// also stay on loopback, independently of the taxonomy resolver.
type derpibooruFixtureTransport struct {
	server   *httptest.Server
	fallback http.RoundTripper
}

func (tr derpibooruFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "derpibooru.org" {
		return tr.fallback.RoundTrip(r)
	}
	if r.URL.Path != "/api/v1/json/search/tags" && !strings.HasPrefix(r.URL.Path, "/api/v1/json/tags/") {
		return nil, fmt.Errorf("unexpected taxonomy request: %s", r.URL)
	}
	target := r.Clone(r.Context())
	target.URL.Scheme = "http"
	target.URL.Host = tr.server.Listener.Addr().String()
	return tr.server.Client().Transport.RoundTrip(target)
}

func TestDerpibooruSourceIntegrityAtUploadBoundary(t *testing.T) {
	var uploads atomic.Int32
	gallery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" {
			t.Errorf("unexpected gallery request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("reading upload: %v", err)
			http.Error(w, "invalid multipart", http.StatusBadRequest)
			return
		}
		for key, want := range map[string]string{
			contract.FieldSource:   "derpibooru",
			contract.FieldPostID:   "42",
			contract.FieldURL:      "https://derpibooru.org/images/42",
			contract.FieldOriginal: "https://example.invalid/one\nhttps://example.invalid/two",
		} {
			if got := r.FormValue(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		var tags []string
		if err := json.Unmarshal([]byte(r.FormValue(contract.FieldTags)), &tags); err != nil ||
			!slices.Equal(tags, []string{"rating:general", "sky"}) {
			t.Errorf("uploaded tags = %v, decode error = %v", tags, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":101}`)
	}))
	defer gallery.Close()

	var lookups atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		switch q := r.URL.Query().Get("q"); q {
		case "id:10":
			fmt.Fprint(w, `{"tags":[{"id":10,"name":"sky","category":null,"aliased_tag":null,"aliases":[]}]}`)
		case "id:11":
			fmt.Fprint(w, `{"tags":[{"id":11,"name":"cloud"}]}`)
		case "id:12":
			fmt.Fprint(w, `{"tags":[{"id":99,"name":"bird","category":"species"}]}`)
		default:
			t.Errorf("unexpected taxonomy query: %q", q)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	defer source.Close()
	cfg := config.Default()
	cfg.Monbooru.APIURL = gallery.URL
	provider := config.NewProvider(cfg)
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	galleryClient := monbooru.New(provider)
	// The resolver uses the default transport when none is set on its client.
	// Pipeline tests run serially. Restore the transport before closing fixtures.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = derpibooruFixtureTransport{server: source, fallback: originalTransport}
	defer func() { http.DefaultTransport = originalTransport }()
	adapter, err := taxonomy.NewDerpibooru("")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{cfg: provider, mapper: mapper, client: galleryClient, taxonomy: adapter}

	cases := []struct {
		id      any
		tags    any
		tagID   int
		failure string
	}{
		{nil, []any{"sky"}, 10, "source identity:"},
		{"wrong", []any{"sky"}, 10, "source identity:"},
		{json.Number("43"), []any{"sky", false}, 10, "source taxonomy:"},
		{json.Number("44"), []any{"cloud"}, 11, "missing or empty category"},
		{json.Number("45"), []any{"bird"}, 12, "expected id 12, got 99"},
		{json.Number("42"), []any{"sky"}, 10, ""},
	}
	var entries []*gdl.Downloaded
	var rows []queue.Item
	for i, tc := range cases {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("post-%d.jpg", i))
		if err := os.WriteFile(path, []byte("test media"), 0o600); err != nil {
			t.Fatal(err)
		}
		meta := map[string]any{
			"category": "derpibooru", "id": tc.id, "gallery_id": json.Number("100"),
			"rating": "safe", "tags": tc.tags, "tag_ids": []int{tc.tagID},
			"source_urls": []any{"https://example.invalid/one", "https://example.invalid/two"},
		}
		entries = append(entries, &gdl.Downloaded{Path: path, Meta: meta})
		rows = append(rows, queue.Item{Status: queue.ItemPending})
	}
	job := &queue.Job{}
	job.SetItems(rows)
	p.processItems(context.Background(), job, entries, nil, "Audit_A", "https://derpibooru.org/search?q=safe", "", nil, len(entries))
	for i, row := range job.Snapshot().Items {
		if want := cases[i].failure; want != "" {
			if row.Status != queue.ItemFailed || row.ErrorCode != outcome.ErrCodeMappingFailed || !strings.Contains(row.Error, want) {
				t.Errorf("item %d = %+v, want mapping_failed with %q", i, row, want)
			}
		} else if row.Status != queue.ItemDone || row.Outcome != outcome.Created {
			t.Errorf("valid control was not uploaded: %+v", row)
		}
		if _, err := os.Stat(entries[i].Path); !os.IsNotExist(err) {
			t.Errorf("item %d left scratch media: %v", i, err)
		}
	}
	if got := uploads.Load(); got != 1 {
		t.Errorf("uploads = %d, want one valid control", got)
	}
	if got := lookups.Load(); got != 3 {
		t.Errorf("taxonomy lookups = %d, want three", got)
	}
}

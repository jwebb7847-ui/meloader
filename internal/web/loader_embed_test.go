package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
)

func TestLoaderFramePolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Monbooru.WebURL = "http://127.0.0.1:8455"
	s := &Server{cfg: config.NewProvider(cfg)}
	next := s.loaderFramePolicy(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct{ path, want string }{
		{"/loader/queue?gallery=Audit_A", "frame-ancestors http://127.0.0.1:8455"},
		{"/login", "frame-ancestors http://127.0.0.1:8455"},
		{"/settings", "frame-ancestors http://127.0.0.1:8455"},
		{"/api/v1/queue", ""},
	} {
		w := httptest.NewRecorder()
		next.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if got := w.Header().Get("Content-Security-Policy"); got != tc.want {
			t.Errorf("%s frame policy = %q", tc.path, got)
		}
	}
	for _, raw := range []string{"", "https://*", "https://user:pass@library.test", "https://library.test;"} {
		if got := loaderFrameOrigin(raw); got != "" {
			t.Errorf("unsafe origin %q accepted as %q", raw, got)
		}
	}
}

func TestQueueArchiveSkipDescribesDownloadHistory(t *testing.T) {
	item := itemView(queue.Item{Outcome: outcome.SkippedArchive}, nil)
	if item.Outcome != "skipped by download archive" || !strings.Contains(item.OutcomeTitle, "Force download") {
		t.Errorf("archive skip display = %+v", item)
	}
}

func TestQueueStrictTaxonomyFailureLabels(t *testing.T) {
	for _, tc := range []struct {
		code, detail, label string
	}{
		{outcome.ErrCodeRateLimited, outcome.StrictTaxonomyPrefix + "HTTP 429 after 5 attempts", "taxonomy rate limited"},
		{outcome.ErrCodeDownloadFailed, outcome.StrictTaxonomyPrefix + "HTTP 503 after 5 attempts", "taxonomy source unavailable"},
		{outcome.ErrCodeMappingFailed, outcome.StrictTaxonomyPrefix + "no verified alias fields", "taxonomy unverified"},
		{outcome.ErrCodeRateLimited, "HTTP 429 from gallery-dl", "rate limited"},
		{outcome.ErrCodeDownloadFailed, "HTTP 503 from gallery-dl", "download failed"},
		{outcome.ErrCodeMappingFailed, "unreadable sidecar", "no usable file"},
	} {
		message := tc.code + ": " + tc.detail
		view := itemView(queue.Item{ErrorCode: tc.code, Error: message}, nil)
		if view.Outcome != tc.label || view.OutcomeCls != "o-failed" || view.OutcomeTitle != message {
			t.Errorf("%s: unexpected queue item %+v", tc.label, view)
		}
	}
}

func TestLoaderEnqueuePinsGalleryAndBulkLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Monbooru.APIToken = "paired"
	cfg.Monbooru.DefaultGallery = "home"
	q := queue.New(nil, 1, 0)
	tmpl := template.Must(template.New("queue_rows").Parse("queue rows"))
	s := &Server{cfg: config.NewProvider(cfg), queue: q, tmpl: tmpl}
	for _, tc := range []struct {
		path, gallery, items string
		count, target        int
		auto                 bool
	}{
		{"/loader/queue", "", "200", 0, 0, false},
		{"/loader/queue", "../home", "200", 0, 0, false},
		{"/loader/queue", "Audit_A", "200", 1, 200, false},
		{"/loader/queue", "Audit_B", "all", 2, 0, true},
		{"/", "", "200", 3, 200, false},
	} {
		body := url.Values{"url": {"https://derpibooru.org/search?q=safe"}, "gallery": {tc.gallery}, "items": {tc.items}}
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// The integrated path uses the same queue fragment as the standalone page.
		w := httptest.NewRecorder()
		flashForm(s.enqueueForm)(w, r)
		jobs, count := q.List(queue.ListOptions{})
		if count != tc.count {
			t.Fatalf("%s (%q) queued %d jobs, want %d", tc.path, tc.gallery, count, tc.count)
		}
		if count > 0 {
			j := jobs[0]
			if j.Gallery != tc.gallery || j.Target != tc.target || j.Auto != tc.auto {
				t.Errorf("queued options %+v", j.Options)
			}
		}
	}
}

func TestQueueResultLinkUsesJobGallery(t *testing.T) {
	ctx := map[string]any{"MonbooruURL": "http://127.0.0.1:8455", "Gallery": "Audit_B"}
	item := queue.Item{MonbooruID: 7, SHA256: strings.Repeat("a", 64)}
	if got := itemView(item, ctx).ViewURL; got != "http://127.0.0.1:8455/i/"+item.SHA256+"?gallery=Audit_B" {
		t.Errorf("unexpected result link %q", got)
	}
	item.SHA256 = ""
	if got := itemView(item, ctx).ViewURL; got != "" {
		t.Errorf("gallery-bound result linked an unscoped ID: %q", got)
	}
}

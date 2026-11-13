package pipeline

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/monbooru"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
)

// Opt-in local acceptance. See docs/validation/negative-path-throughput.md.
func TestRatingIntegrityAgainstLocalAuditGallery(t *testing.T) {
	configPath := os.Getenv("MELOADER_LIVE_RATING_AUDIT_CONFIG")
	if configPath == "" {
		t.Skip("set MELOADER_LIVE_RATING_AUDIT_CONFIG to run against the local Audit_A gallery")
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	}
	const gallery = "Audit_A"
	cfg, err := config.LoadFromFileIn(configPath, filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(cfg.Monbooru.APIURL)
	if err != nil || target.Scheme != "http" || target.Host != "127.0.0.1:8455" ||
		target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" ||
		(target.Path != "" && target.Path != "/") || cfg.Monbooru.APIToken == "" {
		t.Fatal("expected an audit config with a token and Mebooru at http://127.0.0.1:8455")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider := config.NewProvider(cfg)
	live := monbooru.New(provider)
	galleries, err := live.ListGalleries(ctx)
	if err != nil {
		t.Fatalf("read local gallery list: %v", err)
	}
	found := false
	for _, g := range galleries {
		if g.Name == gallery {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("local server has no %s gallery", gallery)
	}

	validPath, validSHA := writeRatingAuditPNG(t, "valid-rating")
	badPath, _ := writeRatingAuditPNG(t, "malformed-rating")
	submittedURL := "https://example.invalid/meloader-rating-audit/" + validSHA

	var attempts atomic.Int32
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/images" ||
			r.URL.Query().Get("gallery") != gallery {
			http.Error(w, "unexpected audit request", http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	testConfig := config.Default()
	testConfig.Monbooru.APIURL = srv.URL
	testConfig.Monbooru.APIToken = cfg.Monbooru.APIToken
	testConfig.Tags.Blacklist = nil
	testProvider := config.NewProvider(testConfig)
	mapper, err := mapping.New(testProvider, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{mapper: mapper, cfg: testProvider, client: monbooru.New(testProvider)}
	job := &queue.Job{}
	job.SetItems([]queue.Item{{Status: queue.ItemPending}, {Status: queue.ItemPending}})
	p.processItems(ctx, job, []*gdl.Downloaded{
		{Path: badPath, Meta: map[string]any{"category": "safebooru", "rating": "not-a-rating"}},
		{Path: validPath, Meta: map[string]any{"category": "safebooru", "rating": "s"}},
	}, nil, gallery, submittedURL, "", nil, 2)

	items := job.Snapshot().Items
	bad, valid := items[0], items[1]
	if bad.Status != queue.ItemFailed || bad.ErrorCode != outcome.ErrCodeMappingFailed ||
		!strings.Contains(bad.Error, "source rating:") {
		t.Errorf("expected malformed rating to fail with mapping_failed, got %+v", bad)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("upload attempts = %d, want 1 for the valid control only", got)
	}
	for _, path := range []string{badPath, validPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("scratch media remains at %s: stat error=%v", path, err)
		}
	}
	if valid.Status != queue.ItemDone || valid.Outcome != outcome.Created || valid.MonbooruID <= 0 {
		t.Fatalf("expected Mebooru to create the valid control, got %+v", valid)
	}
	storedSHA, err := live.ImageSHA256(ctx, valid.MonbooruID, gallery)
	if err != nil || storedSHA != validSHA {
		t.Fatalf("verify stored control in %s: sha256 = %q, want %q, error = %v",
			gallery, storedSHA, validSHA, err)
	}
	t.Logf("malformed rating: %s", bad.Error)
	t.Logf("upload attempts: %d; valid control verified in %s as image ID %d",
		attempts.Load(), gallery, valid.MonbooruID)
}

func writeRatingAuditPNG(t *testing.T, name string) (string, string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
}

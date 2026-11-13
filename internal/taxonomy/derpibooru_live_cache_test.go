package taxonomy

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
)

// Opt-in live metadata and taxonomy audit. No media or Mebooru is involved.
func TestDerpibooruLiveTaxonomyColdWarm(t *testing.T) {
	binary := os.Getenv("MELOADER_DERPIBOORU_CACHE_AUDIT_GDL")
	if binary == "" {
		t.Skip("set MELOADER_DERPIBOORU_CACHE_AUDIT_GDL to the gallery-dl executable")
	}
	cfg := config.Default()
	cfg.GalleryDL.BinaryPath = binary
	cfg.GalleryDL.ConfigPath = ""
	provider := config.NewProvider(cfg)
	mapper, err := mapping.New(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	runner := gdl.New(provider, gdl.ListsFrom(mapper))

	const (
		workload  = "https://derpibooru.org/search?q=safe%2Crainbow+dash&sf=id&sd=desc"
		postCount = 20
	)
	metadataCtx, cancelMetadata := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelMetadata()
	version := runner.Version(metadataCtx)
	if version == "" {
		t.Fatal("could not run gallery-dl --version")
	}
	started := time.Now()
	res, err := runner.Resolve(metadataCtx, workload, "1-20", false)
	metadataElapsed := time.Since(started)
	if err != nil || res.Err != nil || res.Category != "derpibooru" || len(res.Items) != postCount {
		t.Fatalf("expected %d Derpibooru records, got %d (%s): resolve=%v, listing=%v",
			postCount, len(res.Items), res.Category, err, res.Err)
	}
	ids := make([]string, 0, postCount)
	sourceTags := make(map[string]struct{})
	for _, item := range res.Items {
		tags := tagStrings(item.Meta["tags"])
		if item.Category != "derpibooru" || item.ID == "" || slices.Contains(ids, item.ID) ||
			!slices.Contains(tags, "safe") || !slices.Contains(tags, "rainbow dash") {
			t.Fatalf("unexpected listing record at position %d (post %q)", len(ids)+1, item.ID)
		}
		ids = append(ids, item.ID)
		for _, tag := range tags {
			sourceTags[normalizeKey(tag)] = struct{}{}
		}
	}
	t.Logf("gallery-dl %s; metadata: %s; posts: %v; distinct source tags: %d",
		version, metadataElapsed.Round(time.Millisecond), ids, len(sourceTags))

	cachePath := filepath.Join(t.TempDir(), "derpibooru.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var coldTags [][]string
	for _, phase := range []string{"cold", "warm 1", "warm 2", "warm 3"} {
		cold := phase == "cold"
		transport := &taxonomyAuditTransport{allowNetwork: cold}
		client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
		started := time.Now()
		resolver, err := newDerpibooru(cachePath, derpibooruBaseURL, client)
		if err != nil {
			t.Fatal(err)
		}
		loadElapsed := time.Since(started)
		var tagsByPost [][]string
		totalTags := 0
		started = time.Now()
		for _, item := range res.Items {
			meta := maps.Clone(item.Meta)
			if err := resolver.Enrich(ctx, meta); err != nil {
				t.Fatalf("%s post %s after %d taxonomy HTTP attempts: %v",
					phase, item.ID, transport.requests, err)
			}
			if err := mapper.ValidateSourceRating(meta); err != nil {
				t.Fatalf("%s post %s: %v", phase, item.ID, err)
			}
			mapped := mapper.Map(meta)
			if mapped.Rating != mapping.RatingGeneral ||
				!slices.Contains(mapped.Tags, "character:rainbow_dash") {
				t.Fatalf("%s post %s lacks the expected rating or character", phase, item.ID)
			}
			tagsByPost = append(tagsByPost, mapped.Tags)
			totalTags += len(mapped.Tags)
		}
		elapsed := time.Since(started)
		if cold {
			if transport.requests == 0 {
				t.Fatal("cold resolver made no taxonomy HTTP requests")
			}
			coldTags = tagsByPost
		} else {
			for i, tags := range tagsByPost {
				if !slices.Equal(tags, coldTags[i]) {
					t.Fatalf("%s post %s produced different mapped tags", phase, ids[i])
				}
			}
		}
		info, err := os.Stat(cachePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: cache load %s; enrich + map %s; taxonomy HTTP attempts %d; mapped tags %d; cache %d bytes",
			phase, loadElapsed.Round(time.Microsecond), elapsed.Round(time.Microsecond),
			transport.requests, totalTags, info.Size())
	}
}

type taxonomyAuditTransport struct {
	allowNetwork bool
	requests     int
}

func (t *taxonomyAuditTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests++
	if !t.allowNetwork {
		return nil, errors.New("warm taxonomy cache attempted an HTTP request")
	}
	return http.DefaultTransport.RoundTrip(req)
}

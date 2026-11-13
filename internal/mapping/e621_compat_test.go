package mapping

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/taxonomy"
)

// e621 gallery-dl >= 1.32 emits categorized tags_* arrays as well as a
// flattened tags list. This synthetic fixture follows the extractor's public
// output shape; it is deliberately not a live-site/network test.
func e621MetadataFixture() map[string]any {
	return map[string]any{
		"category":         "e621",
		"id":               float64(535),
		"rating":           "s",
		"tags":             []any{"anry", "rainbow_dash", "canine", "sky", "english_text"},
		"tags_artist":      []any{"anry"},
		"tags_character":   []any{"rainbow_dash"},
		"tags_copyright":   []any{"example_series"},
		"tags_general":     []any{"sky"},
		"tags_species":     []any{"canine"},
		"tags_meta":        []any{"english_text"},
		"tags_lore":        []any{"in_universe_lore"},
		"tags_contributor": []any{"editor_one"},
		"tags_invalid":     []any{"retired_tag"},
		"file": map[string]any{
			"md5":    "630b624cc581023ef9d26fd89d37a410",
			"width":  float64(900),
			"height": float64(900),
			"size":   float64(155108),
		},
		"extension": "jpg",
		"sources": []any{
			"https://artist.example/original",
			"https://artist.example/mirror",
		},
		"relationships": map[string]any{"parent_id": float64(534)},
	}
}

func newE621Mapper(t *testing.T, cfg *config.Config) *Mapper {
	t.Helper()
	m, err := New(config.NewProvider(cfg), "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestE621CategorizedMetadataFastPath(t *testing.T) {
	m := newE621Mapper(t, config.Default())
	meta := e621MetadataFixture()

	if m.SuspectFlattenedTags(meta) {
		t.Fatal("categorized e621 metadata was incorrectly flagged as flattened")
	}
	pf := m.Map(meta)

	want := []string{
		"artist:anry",
		"artist:editor_one",
		"character:rainbow_dash",
		"copyright:example_series",
		"meta:english_text",
		"meta:in_universe_lore",
		"rating:general",
		"sky",
		"species:canine",
	}
	slices.Sort(want)
	if !reflect.DeepEqual(pf.Tags, want) {
		t.Fatalf("e621 tags = %#v, want %#v", pf.Tags, want)
	}
	if pf.Source != "e621" {
		t.Fatalf("source = %q, want e621", pf.Source)
	}
	if pf.Rating != RatingGeneral {
		t.Fatalf("rating = %q, want %q", pf.Rating, RatingGeneral)
	}
	if got := m.PostURL(meta); got != "https://e621.net/posts/535" {
		t.Fatalf("canonical post URL = %q", got)
	}
	if pf.ParentURL != "https://e621.net/posts/534" {
		t.Fatalf("parent URL = %q", pf.ParentURL)
	}
	if pf.Original != "https://artist.example/original\nhttps://artist.example/mirror" {
		t.Fatalf("original source URLs = %q", pf.Original)
	}
	if pf.PostWidth != 900 || pf.PostHeight != 900 || pf.PostSize != 155108 || pf.PostExt != "jpg" {
		t.Fatalf("file fields = %dx%d, size %d, ext %q", pf.PostWidth, pf.PostHeight, pf.PostSize, pf.PostExt)
	}
}

func TestE621NoAdapterDoesNotMutateMetadata(t *testing.T) {
	derpi, err := taxonomy.NewDerpibooru("")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := taxonomy.NewRegistry(derpi)
	if err != nil {
		t.Fatal(err)
	}

	meta := e621MetadataFixture()
	original := maps.Clone(meta)
	// The registry contains only Derpibooru; e621 must be a no-op and must
	// not attempt any HTTP calls merely because it is processed by the registry.
	if err := registry.Warm(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enrich(context.Background(), meta); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, original) {
		t.Fatalf("unadapted e621 metadata changed: got %#v, want %#v", meta, original)
	}
}

func TestE621FlattenedMetadataIsNotTrusted(t *testing.T) {
	m := newE621Mapper(t, config.Default())
	meta := map[string]any{
		"category": "e621",
		"rating":   "s",
		"tags":     []any{"rainbow_dash", "canine"},
	}
	if !m.SuspectFlattenedTags(meta) {
		t.Fatal("e621 post with only flat tags must be flagged for the existing pipeline guard")
	}
}

func TestE621RatingFamilyIsNotDanbooru(t *testing.T) {
	m := newE621Mapper(t, config.Default())
	for _, tc := range []struct {
		rating string
		want   string
	}{
		{"s", RatingGeneral},
		{"q", RatingQuestionable},
		{"e", RatingExplicit},
	} {
		t.Run(tc.rating, func(t *testing.T) {
			meta := e621MetadataFixture()
			meta["rating"] = tc.rating
			pf := m.Map(meta)
			if pf.Rating != tc.want || !slices.Contains(pf.Tags, "rating:"+tc.want) {
				t.Fatalf("e621 raw rating %q mapped to %q, tags %v; want %q", tc.rating, pf.Rating, pf.Tags, tc.want)
			}
		})
	}
	// The same "s" is sensitive on Danbooru: source-family interpretation
	// must not be conflated when the adapter framework expands.
	if got := mapRating(FamilyDanbooru, "s"); got != RatingSensitive {
		t.Fatalf("danbooru rating s = %q, want sensitive", got)
	}
}

func TestE621UserOverridesAndPreDownloadBlacklist(t *testing.T) {
	cfg := config.Default()
	cfg.TagOverrides = []config.TagOverride{
		{Site: "e621", From: "species", To: "general"},
	}
	cfg.RatingOverrides = []config.RatingOverride{
		{Site: "e621", From: "s", To: RatingSensitive},
	}
	cfg.Tags.Blacklist = []string{"character:rainbow_dash"}
	m := newE621Mapper(t, cfg)
	meta := e621MetadataFixture()

	pf := m.Map(meta)
	if !slices.Contains(pf.Tags, "canine") || slices.Contains(pf.Tags, "species:canine") {
		t.Fatalf("category override did not win over e621 source default: %v", pf.Tags)
	}
	if pf.Rating != RatingSensitive {
		t.Fatalf("rating override = %q, want sensitive", pf.Rating)
	}
	if !slices.Contains(pf.Tags, "rating:sensitive") {
		t.Fatalf("user rating override not present in mapped tags: %v", pf.Tags)
	}
	note, skipped := m.Blacklisted(meta)
	if !skipped || note == "" {
		t.Fatalf("categorized character blacklist not applied before download: skipped=%v note=%q", skipped, note)
	}
}

func BenchmarkE621CategorizedLocalProcessing(b *testing.B) {
	mapper, err := New(config.NewProvider(config.Default()), "")
	if err != nil {
		b.Fatal(err)
	}
	// Deliberately no e621 adapter: this benchmarks the expected zero-I/O
	// path through the registry and generic mapping, not an image download.
	registry, err := taxonomy.NewRegistry()
	if err != nil {
		b.Fatal(err)
	}
	meta := e621MetadataFixture()

	for _, posts := range []int{200, 1_000, 10_000} {
		b.Run(fmt.Sprintf("%d-posts", posts), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for i := 0; i < posts; i++ {
					if err := registry.Enrich(context.Background(), meta); err != nil {
						b.Fatal(err)
					}
					_ = mapper.Map(meta)
				}
			}
		})
	}
}

package mapping

import (
	"reflect"
	"slices"
	"testing"

	"github.com/monbooru/monloader/internal/config"
)

func TestTagTracePreservesSourceFieldAndSpelling(t *testing.T) {
	cfg := config.Default()
	cfg.TagOverrides = []config.TagOverride{
		{Site: "e621", From: "character", To: "meta"},
		{Site: "e621", From: "species", To: ""},
		{Site: "derpibooru", From: "character", To: "meta"},
	}
	mapper := newE621Mapper(t, cfg)
	meta := map[string]any{
		"category":       "e621",
		"rating":         "s",
		"tags_character": []any{"Rainbow Dash"},
		"tags_species":   []any{"Canine"},
	}
	trace := mapper.Trace(meta)
	if trace.Refused != "" || len(trace.Rows) != 2 {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	if row := trace.Rows[0]; row.Field != "character" || row.Listed != "Rainbow Dash" ||
		row.Category != "meta" || row.Sent != "meta:rainbow_dash" {
		t.Errorf("source character and mapped destination lost: %+v", row)
	}
	if row := trace.Rows[1]; row.Field != "species" || row.Listed != "Canine" ||
		!row.MapDropped || row.Sent != "" {
		t.Errorf("source evidence lost for suppressed tag: %+v", row)
	}
	if !reflect.DeepEqual(meta["tags_character"], []any{"Rainbow Dash"}) ||
		!reflect.DeepEqual(meta["tags_species"], []any{"Canine"}) {
		t.Fatalf("mapping modified source metadata: %+v", meta)
	}

	// This is the working shape produced by the Derpibooru taxonomy adapter.
	trace = mapper.Trace(map[string]any{
		"category": "derpibooru",
		"tags":     []string{"safe", "character:rainbow dash"},
	})
	if trace.Refused != "" || len(trace.Rows) != 1 || trace.Rows[0].Listed != "character:rainbow dash" ||
		!slices.Contains(trace.Tags, "meta:rainbow_dash") {
		t.Fatalf("resolved category and mapped destination lost: %+v", trace)
	}
}

func TestCategorizedSourcesRequireUsableCategoryEvidenceForFlatTags(t *testing.T) {
	mapper := newE621Mapper(t, config.Default())
	for _, tc := range []struct {
		name string
		meta map[string]any
		want bool
	}{
		{"missing category fields", map[string]any{"category": "e621", "tags": "sky"}, true},
		{"empty category field", map[string]any{"category": "e621", "tags": "sky", "tags_general": []any{}}, true},
		{"whitespace category field", map[string]any{"category": "e621", "tags": "sky", "tags_general": []any{" "}}, true},
		{"optional category field", map[string]any{"category": "e621", "tags": "sky", "tags_bookmark": "cloud"}, true},
		{"valid category field", map[string]any{"category": "safebooru", "tags": "sky", "tags_general": "sky"}, false},
		{"genuinely tagless post", map[string]any{"category": "e621", "tags_general": []any{}}, false},
		{"generic flat source", map[string]any{"category": "unknown", "tags": "sky"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapper.SuspectFlattenedTags(tc.meta); got != tc.want {
				t.Errorf("flat tag refusal = %t, want %t", got, tc.want)
			}
		})
	}
}

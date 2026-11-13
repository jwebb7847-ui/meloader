package mapping

import (
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
)

func TestValidateSourceRatingSeparatesUnratedAndInvalid(t *testing.T) {
	m, err := New(config.NewProvider(config.Default()), "")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		meta map[string]any
		want string
	}{
		{"e621 safe", map[string]any{"category": "e621", "rating": "s"}, ""},
		{"e621 questionable", map[string]any{"category": "e621", "rating": "q"}, ""},
		{"e621 explicit", map[string]any{"category": "e621", "rating": "e"}, ""},
		{"danbooru sensitive", map[string]any{"category": "danbooru", "rating": "s"}, ""},
		{"safebooru site override", map[string]any{"category": "safebooru", "rating": "q"}, ""},
		{"e621 unknown code", map[string]any{"category": "e621", "rating": "banana"}, "unrecognized rating"},
		{"e621 invalid numeric code", map[string]any{"category": "e621", "rating": float64(9)}, "unrecognized rating"},
		{"e621 malformed field", map[string]any{"category": "e621", "rating": map[string]any{"code": "s"}}, "malformed rating"},
		{"e621 absent field", map[string]any{"category": "e621"}, "none was found"},
		{"e621 nil field", map[string]any{"category": "e621", "rating": nil}, "none was found"},
		{"e621 whitespace field", map[string]any{"category": "e621", "rating": "  "}, "none was found"},
		{"unprofiled unrated", map[string]any{"category": "custom-photography"}, ""},
		{"unprofiled with arbitrary claim", map[string]any{"category": "custom-photography", "rating": "unknown"}, ""},
		{"philomena safe", map[string]any{"category": "derpibooru", "tags": []any{"safe", "rainbow dash"}}, ""},
		{"philomena suggestive", map[string]any{"category": "derpibooru", "tags": []any{"suggestive", "rainbow dash"}}, ""},
		{"philomena repeated identical rating", map[string]any{"category": "derpibooru", "tags": []any{"safe", "safe"}}, ""},
		{"philomena conflicting tags", map[string]any{"category": "derpibooru", "tags": []any{"safe", "explicit"}}, "conflicting rating tags"},
		{"philomena field conflicts with tag", map[string]any{"category": "derpibooru", "rating": "e", "tags": []any{"safe"}}, "conflicting with rating tag"},
		{"philomena missing tag", map[string]any{"category": "derpibooru", "tags": []any{"rainbow dash"}}, "none was found"},
		{"philomena malformed field", map[string]any{"category": "derpibooru", "rating": []any{"safe"}, "tags": []any{"safe"}}, "malformed rating"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.ValidateSourceRating(tc.meta)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected rating validation error: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("expected error containing %q, got success", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateSourceRatingDoesNotLetUserOverrideLegitimizeBadSourceClaim(t *testing.T) {
	cfg := config.Default()
	cfg.RatingOverrides = []config.RatingOverride{
		{Site: "e621", From: "garbage", To: RatingGeneral},
		{Site: "e621", From: "s", To: RatingSensitive},
	}
	m, err := New(config.NewProvider(cfg), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ValidateSourceRating(map[string]any{"category": "e621", "rating": "garbage"}); err == nil {
		t.Fatal("arbitrary unknown rating was accepted merely because a user mapping override exists")
	}
	meta := map[string]any{"category": "e621", "rating": "s"}
	if err := m.ValidateSourceRating(meta); err != nil {
		t.Fatalf("valid source rating unexpectedly rejected: %v", err)
	}
	if got := m.Map(meta).Rating; got != RatingSensitive {
		t.Fatalf("user override of valid source rating = %q, want sensitive", got)
	}
}

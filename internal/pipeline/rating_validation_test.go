package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/outcome"
)

func TestRatingErrReturnsMappingFailureOnlyForBadRatedSource(t *testing.T) {
	mapper, err := mapping.New(config.NewProvider(config.Default()), "")
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{mapper: mapper}

	for _, tc := range []struct {
		name string
		meta map[string]any
		bad  bool
	}{
		{"known source with valid rating", map[string]any{"category": "e621", "rating": "s"}, false},
		{"known source missing rating", map[string]any{"category": "e621"}, true},
		{"known source malformed rating", map[string]any{"category": "danbooru", "rating": "bogus"}, true},
		{"conflicting philomena tags", map[string]any{"category": "derpibooru", "tags": []any{"safe", "explicit"}}, true},
		{"genuinely unrated source", map[string]any{"category": "example-photos"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.ratingErr(tc.meta)
			if !tc.bad {
				if err != nil {
					t.Fatalf("unrated or valid source rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("malformed source was allowed to import")
			}
			if got := errorCode(err); got != outcome.ErrCodeMappingFailed {
				t.Fatalf("error code %q, want mapping_failed", got)
			}
			var specific *sourceMetadataError
			if !errors.As(err, &specific) {
				t.Fatalf("error type %T does not preserve source-rating identity", err)
			}
			var coded *outcome.CodedError
			if !errors.As(err, &coded) || !strings.HasPrefix(bareMessage(err), "source rating: ") {
				t.Fatalf("source-rating reason not preserved: %v", err)
			}
		})
	}
}

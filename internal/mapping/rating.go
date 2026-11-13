package mapping

import (
	"fmt"
	"strings"

	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/shared/contract"
)

const (
	RatingGeneral      = contract.RatingGeneral
	RatingSensitive    = contract.RatingSensitive
	RatingQuestionable = contract.RatingQuestionable
	RatingExplicit     = contract.RatingExplicit
)

func Stricter(a, b string) string {
	if contract.RatingRank(b) > contract.RatingRank(a) {
		return b
	}
	return a
}

// `s` is family dependent, so it stays out of this table.
var ratingWords = map[string]string{
	"general":      RatingGeneral,
	"sensitive":    RatingSensitive,
	"questionable": RatingQuestionable,
	"explicit":     RatingExplicit,
	"safe":         RatingGeneral,
	"suggestive":   RatingSensitive, // philomena and mangadex term for the sensitive tier
	"g":            RatingGeneral,
	"q":            RatingQuestionable,
	"e":            RatingExplicit,
}

// An unrecognized value returns "": an unset rating is visible under
// every monbooru ceiling.
func mapRating(family, raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "s" {
		if family == FamilyDanbooru {
			return RatingSensitive
		}
		return RatingGeneral // e621, moebooru, gelbooru_v02, generic: s = safe
	}
	return ratingWords[v]
}

func philomenaRatingTag(tag string) bool {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "safe", "suggestive", "questionable", "explicit":
		return true
	}
	return false
}

// Returns the raw tag, so user and profile overrides still apply.
func philomenaRating(meta map[string]any) string {
	for _, t := range parseTagField(meta["tags"]) {
		if philomenaRatingTag(t) {
			return strings.ToLower(strings.TrimSpace(t))
		}
	}
	return ""
}

func requiresSourceRating(family string) bool {
	switch family {
	case FamilyDanbooru, FamilyE621, FamilyMoebooru, FamilyGelbooruV02, FamilyPhilomena:
		return true
	default:
		return false
	}
}

// Known sources keep their shipped rating semantics even when a user
// edits the profile. A custom-only source can still declare its own family.
func (m *Mapper) sourceRatingProfile(site string, profile Profile) Profile {
	if builtin, ok := m.BuiltinProfile(site); ok && requiresSourceRating(builtin.Family) {
		return builtin
	}
	return profile
}

func sourceRatingField(meta map[string]any) string {
	return strings.TrimSpace(kwdict.String(meta, "rating"))
}

func ratingOverride(overrides map[string]string, raw string) string {
	for from, to := range overrides {
		if strings.EqualFold(from, raw) && validRatings[to] {
			return to
		}
	}
	return ""
}

func sourceRating(profile Profile, raw string) string {
	if rating := ratingOverride(profile.RatingOverrides, raw); rating != "" {
		return rating
	}
	return mapRating(profile.Family, raw)
}

// ValidateSourceRating checks the facts claimed by booru families that are
// expected to report ratings. It runs before any image or source metadata is
// pushed to Mebooru. Generic sources may legitimately have no rating, and
// user overrides still control the final *mapped* rating, not the validity of
// the source's original claim.
//
// A built-in site profile can explicitly interpret an unusual source code
// through rating_overrides (e.g. Safebooru's q). The user-facing mapping
// override is intentionally not evidence that an arbitrary source code was
// valid in the first place.
func (m *Mapper) ValidateSourceRating(meta map[string]any) error {
	site := kwdict.String(meta, "category")
	profile := m.sourceRatingProfile(site, m.profileFor(site))
	if !requiresSourceRating(profile.Family) {
		return nil
	}

	raw := sourceRatingField(meta)
	// Distinguish malformed metadata from a truly missing value. A value
	// that exists but cannot be interpreted as text is never a valid claim.
	if value, exists := meta["rating"]; exists && value != nil && raw == "" {
		if _, isString := value.(string); !isString {
			return fmt.Errorf("%s returned a malformed rating field", site)
		}
	}

	if profile.Family == FamilyPhilomena {
		// Philomena ratings normally arrive as tags, not a field. Detect
		// contradictory tags rather than trusting whichever was listed first.
		var fromTags string
		for _, tag := range parseTagField(meta["tags"]) {
			if !philomenaRatingTag(tag) {
				continue
			}
			candidate := strings.ToLower(strings.TrimSpace(tag))
			if fromTags != "" && fromTags != candidate {
				return fmt.Errorf("%s returned conflicting rating tags %q and %q", site, fromTags, candidate)
			}
			fromTags = candidate
		}
		if raw != "" && fromTags != "" && sourceRating(profile, raw) != sourceRating(profile, fromTags) {
			return fmt.Errorf("%s returned rating %q conflicting with rating tag %q", site, raw, fromTags)
		}
		if raw == "" {
			raw = fromTags
		}
	}

	if raw == "" {
		if profile.DefaultRating != "" {
			return nil
		}
		return fmt.Errorf("%s is expected to provide a rating, but none was found", site)
	}
	if sourceRating(profile, raw) == "" {
		return fmt.Errorf("%s returned an unrecognized rating %q", site, raw)
	}
	return nil
}

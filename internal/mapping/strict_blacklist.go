package mapping

import (
	"fmt"
	"slices"
	"strings"

	"github.com/monbooru/monloader/internal/kwdict"
)

// StrictBlacklist captures the applicable rules before evaluating a post.
type StrictBlacklist struct {
	mapper  *Mapper
	profile Profile
	rules   []blacklistRule
}

func (m *Mapper) StrictBlacklist(site string) (StrictBlacklist, error) {
	cfg := m.cfg.Current()
	check := StrictBlacklist{mapper: m, profile: m.profileFor(site)}
	add := func(list string, lines []string) error {
		for _, line := range lines {
			rule, bad := parseBlacklistRule(line)
			if bad != "" {
				return fmt.Errorf("invalid %s blacklist term %q", list, bad)
			}
			if rule.text != "" {
				rule.list = list
				check.rules = append(check.rules, rule)
			}
		}
		return nil
	}
	if err := add("global", cfg.Tags.Blacklist); err != nil {
		return check, err
	}
	if siteConfig := cfg.FindSite(site); siteConfig != nil {
		if err := add(site, siteConfig.Blacklist); err != nil {
			return check, err
		}
	}
	return check, nil
}

// EarlyMatch can prove positive bare-tag matches with raw source spellings.
// Category-qualified and negated terms need the authoritative post metadata.
func (b StrictBlacklist) EarlyMatch(meta map[string]any) string {
	tags := bareSourceTags(meta)
	for _, rule := range b.rules {
		if slices.ContainsFunc(rule.tokens, func(t blacklistToken) bool {
			return t.negate || t.category != "" || t.rating != ""
		}) {
			continue
		}
		if rule.matches(tags, "") {
			return rule.note()
		}
	}
	return ""
}

func bareSourceTags(meta map[string]any) []postTag {
	var tags []postTag
	for _, name := range parseTagField(meta["tags"]) {
		tags = append(tags, postTag{name: normalizeName(name)})
	}
	return tags
}

func (b StrictBlacklist) Match(raw, interpreted map[string]any) string {
	note, _ := b.mapper.matchBlacklist(interpreted, b.profile, b.rules, false, bareSourceTags(raw))
	return note
}

// ValidateStrictTags verifies the supported extractors' native tag shape and
// catches categorized payloads that omit a field or lose tags from the flat list.
func ValidateStrictTags(meta map[string]any) error {
	site := kwdict.String(meta, "category")
	names, ok := strictTagArray(meta["tags"])
	if !ok || len(names) == 0 {
		return fmt.Errorf("%s did not supply a usable native tag list", site)
	}
	if site == CategoryDerpibooru {
		return nil
	}

	for _, field := range []string{"tags_artist", "tags_character", "tags_copyright", "tags_general", "tags_meta"} {
		if _, ok := strictTagArray(meta[field]); !ok {
			return fmt.Errorf("%s did not supply usable %s", site, field)
		}
	}
	categorized := map[string]bool{}
	for _, suffix := range tagSuffixes(meta) {
		if extraTagSuffixes[suffix] {
			continue
		}
		field := "tags_" + suffix
		tags, ok := strictTagArray(meta[field])
		if !ok {
			return fmt.Errorf("%s returned malformed %s", site, field)
		}
		for _, name := range tags {
			categorized[name] = true
		}
	}
	if len(categorized) == 0 {
		return fmt.Errorf("%s returned no categorized tags", site)
	}
	for _, name := range names {
		if !categorized[name] {
			return fmt.Errorf("%s has a flat tag missing from the categorized fields", site)
		}
	}
	return nil
}

func strictTagArray(value any) ([]string, bool) {
	switch tags := value.(type) {
	case []string:
		return tags, !slices.ContainsFunc(tags, func(s string) bool { return strings.TrimSpace(s) == "" })
	case []any:
		names := make([]string, len(tags))
		for i, tag := range tags {
			name, ok := tag.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return nil, false
			}
			names[i] = name
		}
		return names, true
	default:
		return nil, false
	}
}

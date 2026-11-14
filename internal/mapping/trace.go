package mapping

import (
	"fmt"
	"slices"
	"strings"

	"github.com/monbooru/monloader/internal/kwdict"
)

type TraceRead struct {
	Tag string
	Hit bool
}

// MapDropped and RuleDropped end a row early; Sent is empty when the tag never
// reaches monbooru.
type TraceRow struct {
	Field       string
	Listed      string
	Category    string
	Mapped      string
	MapDropped  bool
	Ruled       string
	RuleDropped bool
	Reads       []TraceRead
	Unchecked   string
	Sent        string
}

// Tags is Map's own answer, so the verdict cannot drift from a real push.
type PostTrace struct {
	Site         string
	PostURL      string
	Rows         []TraceRow
	RatingListed string
	Rating       string
	RatingHit    bool
	Skipped      string
	Refused      string
	Tags         []string
	Warnings     []string
}

func (m *Mapper) Trace(meta map[string]any) PostTrace {
	category := kwdict.String(meta, "category")
	profile := m.profileFor(category)
	drop := m.cfg.Current().Tags.DropCategories
	mapped := m.Map(meta)
	t := PostTrace{Site: category, PostURL: m.PostURL(meta), RatingListed: m.listedRating(category, profile, meta), Tags: mapped.Tags, Warnings: mapped.UnknownTargets}

	var tags []postTag
	row := func(r TraceRow, spellings []string) {
		r.Field = strings.TrimPrefix(r.Field, "tags_")
		for _, s := range slices.Compact(spellings) {
			r.Reads = append(r.Reads, TraceRead{Tag: s})
			tags = append(tags, splitPostTag(s))
		}
		t.Rows = append(t.Rows, r)
	}
	m.eachTag(category, profile, meta, func(field, listed, s, legacy string, aliases []string) {
		source := normalizeFiledTag(s)
		after := applyFiledTagRule(profile.TagRules, source, legacy)
		mapped := splitPostTag(source)
		r := TraceRow{Field: field, Listed: listed, Category: mapped.category, Mapped: source, Ruled: after, RuleDropped: after == "" && source != ""}
		if after != "" {
			r.Unchecked = droppedCategory(drop, after)
		}
		if sentTag(drop, after) {
			r.Sent = after
		}
		if r.Unchecked != "" && r.Unchecked != mapped.category {
			t.Warnings = append(t.Warnings, fmt.Sprintf("tag rule %s -> %s: %s is unchecked in Rules, so this tag never reaches monbooru", source, after, r.Unchecked))
		}
		row(r, postBlacklistReads(profile, s, legacy, aliases, false))
	}, func(field, listed, s, legacy string, aliases []string) {
		row(TraceRow{Field: field, Listed: listed, MapDropped: true}, postBlacklistReads(profile, s, legacy, aliases, true))
	})

	t.Rating = m.postRating(category, profile, meta)
	rules := m.blacklistRules(category)
	if rule, hit := firstBlacklistRule(rules, tags, t.Rating, m.flatOnly(profile, meta)); hit {
		t.Skipped = rule.note()
		t.RatingHit = rule.namesRating(t.Rating)
		for _, r := range t.Rows {
			for i, read := range r.Reads {
				r.Reads[i].Hit = rule.names(splitPostTag(read.Tag))
			}
		}
	}
	if m.SuspectFlattenedTags(meta) {
		t.Refused = FlattenedTagsMsg
	}
	if err := m.ValidateSourceRating(meta); err != nil {
		if t.Refused != "" {
			t.Refused += "; "
		}
		t.Refused += "source rating: " + err.Error()
	}
	for _, rule := range rules {
		if name, filed := rule.misfiled(tags); filed != "" {
			t.Warnings = append(t.Warnings, fmt.Sprintf("blacklist rule %q misses: this site files %s under %s", rule.text, name, filed))
		}
	}
	return t
}

func (r blacklistRule) names(tag postTag) bool {
	return slices.ContainsFunc(r.tokens, func(t blacklistToken) bool {
		return !t.negate && t.rating == "" && (t.category == "" || t.category == tag.category) && globMatch(t.pattern, tag.name)
	})
}

func (r blacklistRule) namesRating(rating string) bool {
	return slices.ContainsFunc(r.tokens, func(t blacklistToken) bool {
		return !t.negate && t.rating != "" && t.rating == rating
	})
}

func (r blacklistRule) misfiled(tags []postTag) (name, filed string) {
	for _, t := range r.tokens {
		if t.negate || t.category == "" {
			continue
		}
		for _, tag := range tags {
			if !globMatch(t.pattern, tag.name) {
				continue
			}
			if tag.category == t.category {
				filed = ""
				break
			}
			name, filed = tag.name, tag.category
		}
		if filed != "" {
			return name, filed
		}
	}
	return "", ""
}

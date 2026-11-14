package taxonomy

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Philomena's reversible tag slug encoding is separate from URL escaping.
var philomenaSlugEncoder = strings.NewReplacer(
	"-", "-dash-", "/", "-fwslash-", "\\", "-bwslash-",
	":", "-colon-", ".", "-dot-", "+", "-plus-", " ", "+",
)

var philomenaSlugDecoder = strings.NewReplacer(
	"-dash-", "-", "-fwslash-", "/", "-bwslash-", "\\",
	"-colon-", ":", "-dot-", ".", "-plus-", "+", "+", " ",
)

func philomenaSlugName(slug string) (string, bool) {
	name := philomenaSlugDecoder.Replace(slug)
	return name, normalizeKey(name) != "" && !strings.ContainsAny(slug, "/\\") &&
		philomenaSlugEncoder.Replace(name) == slug
}

// Canonical records already carry their incoming alias slugs. Only a post
// listing an alias needs a separate target lookup, and that lookup is cached.
func (d *Derpibooru) aliasFamily(ctx context.Context, listed tagRecord) (tagRecord, []string, error) {
	canonical := listed
	if listed.AliasedTag != "" {
		name, ok := philomenaSlugName(listed.AliasedTag)
		if !ok || normalizeKey(name) == normalizeKey(listed.Name) {
			return tagRecord{}, nil, fmt.Errorf("invalid Derpibooru alias target for %q", listed.Name)
		}
		var err error
		canonical, err = d.resolve(ctx, tagLookup{name: normalizeKey(name), slug: listed.AliasedTag}, name)
		if err != nil {
			return tagRecord{}, nil, fmt.Errorf("verifying Derpibooru alias %q: %w", listed.Name, err)
		}
		if canonical.AliasedTag != "" || canonical.ID == listed.ID ||
			!slices.Contains(canonical.Aliases, philomenaSlugEncoder.Replace(listed.Name)) {
			return tagRecord{}, nil, fmt.Errorf("Derpibooru alias target %q does not confirm %q", canonical.Name, listed.Name)
		}
	}
	var names []string
	for _, slug := range canonical.Aliases {
		name, ok := philomenaSlugName(slug)
		if !ok || normalizeKey(name) == normalizeKey(canonical.Name) {
			return tagRecord{}, nil, fmt.Errorf("invalid incoming Derpibooru alias for %q", canonical.Name)
		}
		names = append(names, name)
	}
	return canonical, names, nil
}

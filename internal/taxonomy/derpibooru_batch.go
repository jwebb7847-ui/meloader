package taxonomy

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/monbooru/monloader/internal/kwdict"
)

type tagBatchResult struct {
	record tagRecord
	err    error
}

func (d *Derpibooru) EnrichBatch(ctx context.Context, source string, posts []map[string]any) []error {
	errs := make([]error, len(posts))
	if normalizeAdapterSource(source) != d.Source() {
		return errs
	}
	type postLookups struct {
		names   []string
		lookups []tagLookup
	}
	prepared := make([]postLookups, len(posts))
	var all []tagLookup
	idsByName := make(map[string]string)
	conflictingNames := make(map[string]bool)
	for i, meta := range posts {
		if normalizeAdapterSource(stringValue(meta["category"])) != d.Source() {
			errs[i] = fmt.Errorf("expected Derpibooru metadata")
			continue
		}
		if _, verified := meta[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag); verified {
			continue
		}
		names, lookups, err := derpibooruPostLookups(meta)
		errs[i] = err
		if err == nil {
			prepared[i] = postLookups{names, lookups}
			all = append(all, lookups...)
			for _, lookup := range lookups {
				if lookup.id == "" {
					continue
				}
				if id := idsByName[lookup.name]; id != "" && id != lookup.id {
					conflictingNames[lookup.name] = true
				}
				idsByName[lookup.name] = lookup.id
			}
		}
	}

	results := make(map[tagLookup]tagBatchResult)
	// Do not let one record fill the name-keyed cache when the listing
	// assigns that name conflicting identities across different posts.
	all = slices.DeleteFunc(all, func(lookup tagLookup) bool {
		if !conflictingNames[lookup.name] {
			return false
		}
		results[lookup] = tagBatchResult{err: fmt.Errorf("conflicting Derpibooru tag IDs for %q across the listing", lookup.name)}
		return true
	})
	for chunk := range slices.Chunk(d.uncachedIDs(all), derpibooruBatchSize) {
		if ctx.Err() != nil {
			break
		}
		fetched, err := d.fetchTagBatch(ctx, chunk)
		if err != nil {
			for _, lookup := range chunk {
				results[lookup] = tagBatchResult{err: err}
			}
			continue
		}
		d.storeTagResults(chunk, fetched)
		for lookup, result := range fetched {
			results[lookup] = result
		}
	}

	// Each post still resolves exact fallbacks and verifies aliases.
	for i, post := range prepared {
		if errs[i] != nil || len(post.lookups) == 0 {
			continue
		}
		records, err := d.resolveLookups(ctx, post.names, post.lookups, results)
		if err == nil {
			err = d.enrichVerified(ctx, posts[i], post.names, records)
		}
		errs[i] = err
	}
	return errs
}

func (d *Derpibooru) resolveLookups(ctx context.Context, names []string, lookups []tagLookup, results map[tagLookup]tagBatchResult) ([]tagRecord, error) {
	records := make([]tagRecord, len(lookups))
	for i, lookup := range lookups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, found := results[lookup]
		if !found {
			// A missing row still uses the exact lookup, including its ID check.
			result.record, result.err = d.resolve(ctx, lookup, names[i])
		}
		if result.err != nil {
			return nil, fmt.Errorf("resolving Derpibooru tag %q: %w", names[i], result.err)
		}
		records[i] = result.record
	}
	return records, nil
}

func (d *Derpibooru) fetchTagBatch(ctx context.Context, lookups []tagLookup) (map[tagLookup]tagBatchResult, error) {
	ids := make([]string, 0, len(lookups))
	seen := make(map[string]bool, len(lookups))
	for _, lookup := range lookups {
		if !seen[lookup.id] {
			seen[lookup.id] = true
			ids = append(ids, lookup.id)
		}
	}
	records, err := d.fetchMany(ctx, ids)
	if err != nil {
		return nil, err
	}

	results := make(map[tagLookup]tagBatchResult, len(lookups))
	for _, lookup := range lookups {
		// Validate every row claiming this ID or name, including duplicates.
		// A contradiction is evidence against this lookup alone.
		var result tagBatchResult
		for _, record := range records {
			name := normalizeKey(record.Name)
			id := strconv.FormatInt(record.ID, 10)
			switch {
			case id == lookup.id && name != lookup.name:
				result.err = fmt.Errorf("Derpibooru tag id %s: expected name %q, got %q", id, lookup.name, record.Name)
			case name == lookup.name:
				result.err = lookup.checkID(record)
				if result.err == nil {
					result.err = record.validate()
				}
				if result.err == nil && result.record.ID != 0 && !result.record.equal(record) {
					result.err = fmt.Errorf("Derpibooru returned conflicting records for tag %q (id %s)", name, id)
				}
				result.record = record
			}
			if result.err != nil {
				break
			}
		}
		if result.err != nil || result.record.ID != 0 {
			results[lookup] = result
		}
	}
	return results, nil
}

func (d *Derpibooru) storeTagResults(lookups []tagLookup, results map[tagLookup]tagBatchResult) {
	verified := make(map[string]tagRecord, len(results))
	for _, lookup := range lookups {
		if result, ok := results[lookup]; ok && result.err == nil {
			verified[lookup.name] = result.record
		}
	}
	d.storeRecords(verified)
}

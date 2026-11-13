package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
	"github.com/monbooru/monloader/internal/taxonomy"
)

// StrictBlacklistVerdict is shared by acquisition and Trace. The raw post
// remains intact because the transfer gate compares it to the download pass.
func StrictBlacklistVerdict(ctx context.Context, mapper *mapping.Mapper, resolver taxonomy.Enricher, item gdl.Item, rules mapping.StrictBlacklist) (note string, interpreted map[string]any, err error) {
	if !item.HasStrictIdentity() {
		return "", nil, strictBlacklistError("source identity or media URL is unavailable for verification")
	}
	if note := rules.EarlyMatch(item.Meta); note != "" {
		return note, nil, nil
	}
	if err := mapping.ValidateStrictTags(item.Meta); err != nil {
		return "", nil, strictBlacklistError(err.Error())
	}
	interpreted = maps.Clone(item.Meta)
	if item.Category == mapping.CategoryDerpibooru {
		if resolver == nil {
			return "", nil, strictBlacklistError("Derpibooru taxonomy resolver is unavailable")
		}
		if err := resolver.Enrich(ctx, interpreted); err != nil {
			if ctx.Err() != nil {
				return "", nil, ctx.Err()
			}
			return "", nil, strictBlacklistError("source taxonomy: " + err.Error())
		}
	}
	if err := mapper.ValidateSourceRating(interpreted); err != nil {
		return "", nil, strictBlacklistError("source rating: " + err.Error())
	}
	return rules.Match(item.Meta, interpreted), interpreted, nil
}

func strictBlacklistError(message string) error {
	return &outcome.CodedError{Code: outcome.ErrCodeMappingFailed, Msg: "strict blacklist: " + message}
}

func (p *Processor) fetchStrict(ctx context.Context, job, snap *queue.Job, resolved []gdl.Item, site, rng string, listErr error) error {
	gallery := p.beginJob(job, snap, site)
	job.SetItems(p.initialItems(resolved, false, snap.URL, snap.PageURL))

	rules, err := p.mapper.StrictBlacklist(site)
	if err != nil {
		return abortJob(ctx, job, strictBlacklistError(err.Error()))
	}
	approved := make([]gdl.Item, 0, len(resolved))
	allowed := make(map[string]int, len(resolved))
	// Preflight outcomes must survive processItems, which treats a nil
	// result as an archive skip on the normal acquisition path.
	withheld := make(map[int]string)
	for i, item := range resolved {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if item.Category != site {
			failItem(job, i, outcome.ErrCodeMappingFailed, "strict blacklist: source changed within the listing")
			withheld[i] = ""
			continue
		}
		note, _, err := StrictBlacklistVerdict(ctx, p.mapper, p.taxonomy, item, rules)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failItem(job, i, errorCode(err), err.Error())
			withheld[i] = ""
		case note != "":
			skipItem(job, i, outcome.SkippedBlacklist, "before download: "+note)
			withheld[i] = note
		default:
			if _, exists := allowed[item.ID]; exists {
				return abortJob(ctx, job, strictBlacklistError("duplicate post ID in the listing"))
			}
			allowed[item.ID] = i
			approved = append(approved, item)
		}
	}
	if len(approved) == 0 {
		if listErr != nil {
			p.markCutShort(job, snap, listErr, snap.Offset+len(resolved))
		}
		return nil
	}

	workDir := filepath.Join(p.workRoot, fmt.Sprintf("job-%d", snap.ID))
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return abortJob(ctx, job, &outcome.CodedError{Code: outcome.ErrCodeDownloadFailed, Msg: err.Error()})
	}
	defer os.RemoveAll(workDir)

	mode := p.archiveMode(snap, resolved, site, false, false)
	onFile := func(d gdl.Downloaded) {
		if i, ok := allowed[mapping.PostID(d.Meta)]; ok {
			job.UpdateItem(i, func(it *queue.Item) { it.Status = queue.ItemDownloaded })
		}
	}
	downloaded, dlErr := p.runner.DownloadStrict(ctx, snap.URL, rng, workDir, mode, onFile, approved)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	entries := make([]*gdl.Downloaded, len(resolved))
	seen := make(map[int]bool, len(downloaded))
	last := -1
	for i := range downloaded {
		d := &downloaded[i]
		row, ok := allowed[mapping.PostID(d.Meta)]
		if !ok || seen[row] {
			return abortJob(ctx, job, strictBlacklistError("download result could not be paired with its verified post"))
		}
		entries[row] = d
		seen[row] = true
		last = max(last, row)
	}
	stopped := mode == gdl.ArchiveCatchUp && gdl.CaughtUp(downloaded)
	reached := len(entries)
	if stopped {
		reached = last + 1
	}
	for _, row := range allowed {
		if row < reached && entries[row] == nil {
			err := cmp.Or(dlErr, strictBlacklistError("the verified post was not fetched; its listing or metadata may have changed"))
			failItem(job, row, errorCode(err), err.Error())
			withheld[row] = ""
		}
	}
	p.processItems(ctx, job, entries, withheld, gallery, snap.URL, snap.PageURL, dlErr, reached)
	if cut := cmp.Or(listErr, dlErr); cut != nil && ctx.Err() == nil {
		p.markCutShort(job, snap, cut, snap.Offset+len(resolved))
	}
	return nil
}

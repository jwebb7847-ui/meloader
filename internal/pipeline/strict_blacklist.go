package pipeline

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
	"github.com/monbooru/monloader/internal/shared/logx"
	"github.com/monbooru/monloader/internal/taxonomy"
)

// StrictBlacklistVerdict is shared by acquisition and Trace. The raw post
// remains intact because the transfer gate compares it to the download pass.
func StrictBlacklistVerdict(ctx context.Context, mapper *mapping.Mapper, resolver taxonomy.Enricher, item gdl.Item, rules mapping.StrictBlacklist) (note string, interpreted map[string]any, err error) {
	note, err = strictListingVerdict(item, rules)
	if note != "" || err != nil {
		return note, nil, err
	}
	interpreted = maps.Clone(item.Meta)
	if item.Category == mapping.CategoryDerpibooru {
		if resolver == nil {
			return "", nil, strictBlacklistError("Derpibooru taxonomy resolver is unavailable")
		}
		if err := resolver.Enrich(ctx, interpreted); err != nil {
			return "", nil, strictTaxonomyError(ctx, err)
		}
	}
	note, err = strictInterpretedVerdict(mapper, item, interpreted, rules)
	if err != nil {
		return "", nil, err
	}
	return note, interpreted, nil
}

func strictListingVerdict(item gdl.Item, rules mapping.StrictBlacklist) (string, error) {
	if !item.HasStrictIdentity() {
		return "", strictBlacklistError("source identity or media URL is unavailable for verification")
	}
	if note := rules.EarlyMatch(item.Meta); note != "" {
		return note, nil
	}
	if err := mapping.ValidateStrictTags(item.Meta); err != nil {
		return "", strictBlacklistError(err.Error())
	}
	return "", nil
}

func strictTaxonomyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return strictBlacklistError("source taxonomy: " + err.Error())
}

func strictInterpretedVerdict(mapper *mapping.Mapper, item gdl.Item, interpreted map[string]any, rules mapping.StrictBlacklist) (string, error) {
	if err := mapper.ValidateSourceRating(interpreted); err != nil {
		return "", strictBlacklistError("source rating: " + err.Error())
	}
	return rules.Match(item.Meta, interpreted), nil
}

type strictVerdict struct {
	note string
	err  error
}

func (p *Processor) strictWindowVerdicts(ctx context.Context, items []gdl.Item, site string, rules mapping.StrictBlacklist) []strictVerdict {
	verdicts := make([]strictVerdict, len(items))
	batch, batchable := p.taxonomy.(taxonomy.BatchEnricher)
	var metas []map[string]any
	var rows []int
	for i, item := range items {
		if ctx.Err() != nil {
			return verdicts
		}
		switch {
		case item.Category != site:
			verdicts[i].err = strictBlacklistError("source changed within the listing")
		case site != mapping.CategoryDerpibooru || !batchable:
			verdicts[i].note, _, verdicts[i].err = StrictBlacklistVerdict(ctx, p.mapper, p.taxonomy, item, rules)
		default:
			note, err := strictListingVerdict(item, rules)
			verdicts[i] = strictVerdict{note, err}
			if note == "" && err == nil {
				rows = append(rows, i)
				metas = append(metas, maps.Clone(item.Meta))
			}
		}
	}
	if len(metas) == 0 {
		return verdicts
	}
	errs := batch.EnrichBatch(ctx, site, metas)
	if len(errs) != len(rows) {
		for _, row := range rows {
			verdicts[row].err = strictBlacklistError("source taxonomy returned incomplete batch verdicts")
		}
		return verdicts
	}
	for i, row := range rows {
		if errs[i] != nil {
			verdicts[row].err = strictTaxonomyError(ctx, errs[i])
		} else if _, ok := metas[i][kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag); !ok {
			verdicts[row].err = strictBlacklistError("source taxonomy did not verify the post")
		} else {
			verdicts[row].note, verdicts[row].err = strictInterpretedVerdict(p.mapper, items[row], metas[i], rules)
		}
	}
	return verdicts
}

func strictBlacklistError(message string) error {
	return &outcome.CodedError{Code: outcome.ErrCodeMappingFailed, Msg: "strict blacklist: " + message}
}

func (p *Processor) fetchStrict(ctx context.Context, job, snap *queue.Job, resolved []gdl.Item, site, rng string, listErr error) error {
	gallery := p.beginJob(job, snap, site)
	job.SetItems(p.initialItems(resolved, false, snap.URL, snap.PageURL))

	preflightStarted := time.Now()
	rules, err := p.mapper.StrictBlacklist(site)
	if err != nil {
		return abortJob(ctx, job, strictBlacklistError(err.Error()))
	}
	approved := make([]gdl.Item, 0, len(resolved))
	allowed := make(map[string]int, len(resolved))
	// Preflight outcomes must survive processItems, which treats a nil
	// result as an archive skip on the normal acquisition path.
	withheld := make(map[int]string)
	blacklisted := 0
	verdicts := p.strictWindowVerdicts(ctx, resolved, site, rules)
	for i, item := range resolved {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		note, err := verdicts[i].note, verdicts[i].err
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
			blacklisted++
		default:
			if _, exists := allowed[item.ID]; exists {
				return abortJob(ctx, job, strictBlacklistError("duplicate post ID in the listing"))
			}
			allowed[item.ID] = i
			approved = append(approved, item)
		}
	}
	logx.Infof("perf: job=%d stage=strict-preflight source=%s duration=%s posts=%d approved=%d blacklisted=%d refused=%d",
		snap.ID, site, time.Since(preflightStarted).Round(time.Millisecond), len(resolved), len(approved), blacklisted, len(withheld)-blacklisted)
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
	downloaded, dlErr := p.runner.DownloadStrict(gdl.WithStrictDownloadJob(ctx, snap.ID), snap.URL, rng, workDir, mode, onFile, approved)
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
	ingestStarted := time.Now()
	p.processItems(ctx, job, entries, withheld, gallery, snap.URL, snap.PageURL, dlErr, reached)
	logx.Infof("perf: job=%d stage=strict-ingest duration=%s transfer_rows=%d canceled=%t",
		snap.ID, time.Since(ingestStarted).Round(time.Millisecond), len(downloaded), ctx.Err() != nil)
	if cut := cmp.Or(listErr, dlErr); cut != nil && ctx.Err() == nil {
		p.markCutShort(job, snap, cut, snap.Offset+len(resolved))
	}
	return nil
}

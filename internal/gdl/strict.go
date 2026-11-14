package gdl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/shared/logx"
)

const strictMediaURLKey = "meloader_verified_media_url"

// StrictSource limits the transfer guarantee to extractor paths covered by
// the pinned gallery-dl predicate behavior. Other routes stop before resolve.
func StrictSource(site, subcategory string) bool {
	switch site {
	case "derpibooru":
		return subcategory == "post" || subcategory == "search"
	case "danbooru", "e621":
		return subcategory == "post" || subcategory == "tag"
	default:
		return false
	}
}

func (t *Tool) ResolveStrict(ctx context.Context, url, rng string) (ResolveResult, error) {
	site, subcategory := t.ExtractorInfo(ctx, url)
	if ctx.Err() != nil {
		return ResolveResult{}, ctx.Err()
	}
	if !StrictSource(site, subcategory) {
		return ResolveResult{}, strictTransferError("pre-download verification is unavailable for this source or link type")
	}
	res, err := t.resolveWithOptions(ctx, url, rng, false, true)
	res.Err = t.summarized(res.Err)
	return res, t.summarized(err)
}

func strictEvidenceField(key string) bool {
	return key == "tags" || key == "tag_ids" || key == "rating" || strings.HasPrefix(key, "tags_")
}

func (item Item) HasStrictIdentity() bool {
	id, err := strconv.ParseUint(item.ID, 10, 64)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == item.ID &&
		item.ID == kwdict.String(item.Meta, "id") && config.IsHTTPURL(item.MediaURL) &&
		item.Category == kwdict.String(item.Meta, "category") && StrictSource(item.Category, item.Subcategory)
}

func strictEvidence(item Item) map[string]any {
	record := map[string]any{"category": item.Category, "subcategory": item.Subcategory, strictMediaURLKey: item.MediaURL}
	for key, value := range item.Meta {
		if strictEvidenceField(key) {
			record[key] = value
		}
	}
	return record
}

// strictGate binds a verdict to the source, post, media URL and metadata used
// by the rule evaluator. A changed or new post never inherits a verdict.
func strictGate(workDir string, approved []Item) (string, error) {
	records := make(map[string]map[string]any, len(approved))
	for _, item := range approved {
		if !item.HasStrictIdentity() {
			return "", fmt.Errorf("missing strict blacklist transfer identity")
		}
		if _, exists := records[item.ID]; exists {
			return "", fmt.Errorf("duplicate strict blacklist post ID")
		}
		records[item.ID] = strictEvidence(item)
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	path := filepath.Join(workDir, "strict-blacklist.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return strictGateExpression(path), nil
}

// Only a local path is interpolated. Source values stay in JSON, and the
// transfer process caches the manifest after reading it for the first file.
func strictGateExpression(path string) string {
	return "(lambda d, j, p: (lambda r: r is not None and " +
		"all(k in d and (list(d[k]) if isinstance(d[k], tuple) else d[k]) == v for k,v in r.items()) and " +
		"all(k in r for k in d if k in ('tags','tag_ids','rating') or k.startswith('tags_')))(" +
		"(j.__dict__.get(p) or j.__dict__.setdefault(p, j.loads(std.pathlib.Path(p).read_text(encoding='utf-8')))).get(str(d.get('id'))))" +
		")(locals(), std.json, " + strconv.QuoteToASCII(path) + ")"
}

func strictSourceArgs() []string {
	// gallery-dl applies custom keywords and generated metadata after
	// extraction. Keep those options from replacing the source evidence.
	return []string{"--child-filter", "False", "-o", "follow=false",
		"-o", "keywords={}", "-o", "keywords-global={}",
		"-o", "metadata-url=" + strictMediaURLKey, "-o", "metadata-path=false",
		"-o", "metadata-http=false", "-o", "metadata-version=false", "-o", "metadata-extractor=false"}
}

func strictDownloadArgs(workDir, rng, url string, archive Archive, gate string) []string {
	args := append(strictSourceArgs(), "-D", workDir, "--filter", gate, "-o", "fallback=false")
	if rng != "" {
		// File ranges are evaluated after the file predicate. Count source
		// posts before the predicate so a fully rejected window stays bounded.
		args = append(args, "--post-range", rng)
	}
	if archive == ArchiveBypass {
		args = append(args, "-o", "archive=")
	}
	return append(args, "--", url)
}

func (t *Tool) DownloadStrict(ctx context.Context, url, rng, workDir string, archive Archive, onFile func(Downloaded), approved []Item) (_ []Downloaded, transferErr error) {
	if len(approved) == 0 {
		return nil, nil
	}
	job, logTiming := ctx.Value(strictDownloadJobKey{}).(int64)
	logTiming = logTiming && logx.Enabled(logx.LevelInfo)
	timing := strictDownloadTiming{
		manifest: "not_run", version: "not_run", probe: "not_run",
		process: "not_run", firstFile: "none", verification: "not_run",
		fileSleep: "unknown", requestSleep: "unknown",
	}
	started := time.Now()
	if logTiming {
		defer func() { timing.log(job, started, transferErr != nil, ctx.Err() != nil) }()
	}
	gate, err := strictGate(workDir, approved)
	timing.manifest = elapsed(started)
	if err != nil {
		return nil, strictTransferError(err.Error())
	}
	defer os.Remove(filepath.Join(workDir, "strict-blacklist.json"))
	// gallery-dl can warn and continue after an invalid filter expression.
	// Exercise the exact predicate first, against a metadata-only fixture
	// that would produce a file message without the gate.
	bin := t.binary()
	stage := time.Now()
	version, err := runBinary(ctx, bin, "--version")
	timing.version = elapsed(stage)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || version.exitCode != 0 || strings.TrimSpace(string(version.stdout)) != "1.32.19" {
		return nil, strictTransferError("strict blacklist requires gallery-dl 1.32.19")
	}
	stage = time.Now()
	err = verifyStrictGate(ctx, bin, workDir, gate)
	timing.probe = elapsed(stage)
	if err != nil {
		return nil, err
	}
	args := t.configArgs()
	if logTiming {
		timing.fileSleep, timing.requestSleep = strictConfiguredPacing(args, approved[0])
	}
	stage = time.Now()
	if logTiming {
		report := onFile
		onFile = func(d Downloaded) {
			if timing.firstFile == "none" {
				timing.firstFile = elapsed(stage)
			}
			if report != nil {
				report(d)
			}
		}
	}
	results, err := t.download(ctx, url, rng, workDir, archive, onFile, false, gate, bin, args)
	timing.process = elapsed(stage)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stage = time.Now()
	byID := make(map[string]map[string]any, len(approved))
	for _, item := range approved {
		byID[item.ID] = strictEvidence(item)
	}
	for _, result := range results {
		item := itemFromMeta(result.Meta)
		item.MediaURL = kwdict.String(result.Meta, strictMediaURLKey)
		delete(result.Meta, strictMediaURLKey)
		if !item.HasStrictIdentity() || !reflect.DeepEqual(byID[item.ID], strictEvidence(item)) {
			timing.verification = elapsed(stage)
			return nil, strictTransferError("download metadata did not match the verified post")
		}
	}
	timing.verification = elapsed(stage)
	if logTiming {
		timing.countFiles(results)
	}
	return results, t.redact(err)
}

func verifyStrictGate(ctx context.Context, bin, workDir, gate string) error {
	const probeURL = "https://example.invalid/meloader-filter-probe.jpg"
	// A negative probe catches a missing predicate. A positive probe also
	// catches a predicate whose evaluation errors are treated as false.
	path := filepath.Join(workDir, "strict-blacklist-probe.json")
	fixture := []byte(`{"1":{"category":"directlink","tags":["probe"]}}`)
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		return strictTransferError(err.Error())
	}
	defer os.Remove(path)
	values := `keywords-global={"id":1,"tags":["probe"]}`
	for _, probe := range []struct {
		filter string
		files  int
	}{{gate, 0}, {strictGateExpression(path), 1}} {
		res, err := runBinary(ctx, bin, "--config-ignore", "-j", "-o", values, "--filter", probe.filter, "--", probeURL)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || res.exitCode != 0 || res.stderr != "" {
			return strictTransferError("gallery-dl could not validate the pre-download filter")
		}
		var messages [][]json.RawMessage
		if json.Unmarshal(res.stdout, &messages) != nil || len(messages) != 1+probe.files ||
			len(messages[0]) < 2 || string(messages[0][0]) != "2" ||
			(probe.files == 1 && (len(messages[1]) < 3 || string(messages[1][0]) != "3")) {
			return strictTransferError("gallery-dl did not enforce the pre-download filter")
		}
	}
	return nil
}

func strictTransferError(message string) error {
	return &outcome.CodedError{Code: outcome.ErrCodeMappingFailed, Msg: "strict blacklist: " + message}
}

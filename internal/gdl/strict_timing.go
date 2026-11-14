package gdl

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monbooru/monloader/internal/shared/logx"
)

type strictDownloadJobKey struct{}

// WithStrictDownloadJob correlates gallery-dl timing with the queue job.
func WithStrictDownloadJob(ctx context.Context, job int64) context.Context {
	return context.WithValue(ctx, strictDownloadJobKey{}, job)
}

type strictDownloadTiming struct {
	manifest, version, probe, process, firstFile, verification string
	fileSleep, requestSleep                                    string
	files, archiveSkips, sizeErrors                            int
	bytes                                                      int64
}

func (t *strictDownloadTiming) countFiles(results []Downloaded) {
	for _, result := range results {
		if result.Skipped {
			t.archiveSkips++
			continue
		}
		t.files++
		if info, err := os.Stat(result.Path); err == nil && info.Mode().IsRegular() {
			t.bytes += info.Size()
		} else {
			t.sizeErrors++
		}
	}
}

func (t *strictDownloadTiming) log(job int64, started time.Time, failed, canceled bool) {
	logx.Infof("perf: job=%d stage=strict-transfer duration=%s manifest=%s version_check=%s filter_probe=%s download_process=%s first_file=%s result_verification=%s files=%d archive_skips=%d file_bytes=%d size_errors=%d sleep=%s sleep_request=%s failed=%t canceled=%t",
		job, elapsed(started), t.manifest, t.version, t.probe, t.process, t.firstFile, t.verification,
		t.files, t.archiveSkips, t.bytes, t.sizeErrors, t.fileSleep, t.requestSleep, failed, canceled)
}

func elapsed(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}

// Read only the pacing settings from the config passed to this invocation.
// The values describe configuration, since retries and extractor minimums
// can change the actual waits. Parsing failures affect telemetry only.
func strictConfiguredPacing(args []string, item Item) (fileSleep, requestSleep string) {
	if len(args) == 1 {
		return "extractor_default", "extractor_default"
	}
	raw, err := os.ReadFile(args[2]) // --config-ignore -c <managed config>
	if err != nil {
		return "unknown", "unknown"
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "unknown", "unknown"
	}
	extractor, _ := root["extractor"].(map[string]any)
	site, _ := extractor[item.Category].(map[string]any)
	// gallery-dl 1.32.19 resolves a root-level option first, then the
	// site and its extractor family, then the shared extractor option.
	baseName := map[string]string{"derpibooru": "philomena", "danbooru": "Danbooru", "e621": "E621"}[item.Category]
	base, _ := extractor[baseName].(map[string]any)
	subcategory, _ := site[item.Subcategory].(map[string]any)
	baseSubcategory, _ := base[item.Subcategory].(map[string]any)
	configured := func(key string) string {
		for _, scope := range []map[string]any{root, subcategory, site, baseSubcategory, base, extractor} {
			if value, ok := scope[key]; ok {
				return pacingValue(value)
			}
		}
		return "extractor_default"
	}
	return configured("sleep"), configured("sleep-request")
}

// Render only numbers and ranges. Arbitrary strings from raw config must
// never enter an info log.
func pacingValue(value any) string {
	number := func(value any) string {
		var seconds float64
		switch v := value.(type) {
		case float64:
			seconds = v
		case string:
			var err error
			seconds, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return ""
			}
		default:
			return ""
		}
		if math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds < 0 {
			return ""
		}
		return strconv.FormatFloat(seconds, 'g', -1, 64)
	}
	if value == nil || value == false {
		return "0"
	}
	if scalar := number(value); scalar != "" {
		return scalar
	}
	var lower, upper string
	switch v := value.(type) {
	case string:
		if lo, hi, ok := strings.Cut(v, "-"); ok {
			lower, upper = number(lo), number(hi)
		}
	case []any:
		if len(v) == 2 {
			lower, upper = number(v[0]), number(v[1])
		}
	}
	if lower != "" && upper != "" {
		return lower + "-" + upper
	}
	return "unreported"
}

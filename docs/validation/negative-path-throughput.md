# Negative-path integrity and bounded throughput acceptance

Status (2026-11-13): **expanded negative-path/mixed-batch tests passed Go race+vet CI** ([run 43356175480](https://github.com/jwebb7847-ui/meloader/actions/runs/43356175480), test code at `85c2d2e`, validation doc at `aa25f70`). **positive Derpibooru -> running Mebooru Audit_A import fully passed**; image ID 7 retained its file, source, `rating:general`, and all 19 image-specific tags. The reverse Mebooru -> Meloader authenticated link was repaired by correcting the local API port. Live e621 extraction remains blocked by a peer connection reset, not a metadata regression.

This protocol adds **test-only** coverage and measurements. It changes no behavior, configuration, routing policy, gallery contents, or source authority.

## 1. Hermetic negative-path tests

Meloader's `internal/pipeline/rating_upload_integration_test.go` uses its **actual** mapper, source-rating validator, `Processor.processItems`, and `monbooru.Client.PushImageFile`, but points the HTTP client to an in-process `httptest` server, never a running gallery.

The cases now include:
- Positive e621 (`s` -> general), Danbooru (`g` -> general, `s` -> sensitive), Philomena/Derpibooru safe -> general, Safebooru's **built-in** `q` interpretation -> general, and genuinely unrated generic sources.
- Negative e621 missing/unrecognized rating; malformed Danbooru rating; conflicting Philomena tags; a user rating override that cannot authorize an invented source code.
- A mixed 3-item batch (valid e621, corrupt e621, valid Danbooru): **only the two valid items** may reach HTTP; the corrupt item must fail `mapping_failed` without discarding its neighbors. Local scratch media must be removed.
- Invalid rating in a bundled gallery must reject the bundle before upload or CBZ construction.

This is a true pipeline-to-HTTP boundary test, **not** a Mebooru SQLite storage test. The independent live positive test covers real storage.

### Local rerun without disturbing running services

Because the Meloader `meloader-e621-validation` checkout may still be running the live audit service, create a **separate detached QA worktree** rather than switching its files out from underneath the process. These commands never start or stop either server:

```sh
SOURCE="$HOME/Documents/VSC/meloader-e621-validation"
QA="$HOME/Documents/VSC/meloader-rating-qa"

git -C "$SOURCE" fetch origin --prune
if [[ -e "$QA" ]]; then
  echo "STOP: QA path already exists; inspect it before reusing or replacing."
else
  git -C "$SOURCE" worktree add --detach "$QA" origin/meloader-item3-rating-integrity
fi
```

After successfully creating the QA worktree:

```sh
cd "$HOME/Documents/VSC/meloader-rating-qa"
git rev-parse --short HEAD
git status --short

go test -race ./internal/mapping ./internal/pipeline \
  -run 'TestRatingIntegrity|TestRatingErr|TestValidateSourceRating|TestTraceReportsRatingIntegrityRefusal' \
  -count=1 -v
```

**Do not use `git reset --hard` or `git clean`**, and don't update the live audit checkout to obtain test-only changes. CI runs `go test -race ./...` and `go vet ./...` on the feature branch.

## 2. Repeatable local HTTP-boundary benchmark

`BenchmarkRatingIntegrityHTTPUploadBatch` measures the **local, sequential processing + mapping + rating validation + multipart upload** path for 20, 200, and 1,000 metadata records. It uses unique synthetic e621-shaped metadata, precreates 16 KiB placeholder `.jpg` files *outside* the timed section, and reads/posts the files over local loopback to an `httptest` server. That server parses the multipart tags and checks `rating:general` before accepting the record. The same benchmark checks all images were marked `Created`, every expected HTTP request arrived, and no network or gallery configuration is used.

Run from the QA worktree:

```sh
go test ./internal/pipeline -run '^$' \
  -bench '^BenchmarkRatingIntegrityHTTPUploadBatch$' \
  -benchtime=1x -count=3 -benchmem
```

Record the commit, CPU, `ns/op`, custom `posts/s`, `B/op`, and `allocs/op` per batch size. Run three times because local loopback HTTP and filesystem cache introduce variation. Since this test uses **fake 16 KiB files** and a fake Mebooru server, it **does not** measure site requests, image transfer, real decoding, storage, database indexing, or end-to-end library throughput. Compare only like-for-like code revisions and test conditions.

The earlier e621 `BenchmarkE621CategorizedLocalProcessing` measured mapper CPU only, and its timings are **not directly comparable** to this HTTP-inclusive benchmark.

### Recorded Apple M5 benchmark — 2026-11-13

Operator used the disposable QA worktree at Meloader commit `41c954b` (macOS `darwin/arm64`, Apple M5). The focused `go test -race` pattern passed for both `./internal/mapping` and `./internal/pipeline` (1.533 s, 1.564 s). The three one-iteration benchmark runs per batch size passed as well; the test-only implementation passed full Go race/vet CI.

Command:

```sh
go test ./internal/pipeline -run '^$' \
  -bench '^BenchmarkRatingIntegrityHTTPUploadBatch$' \
  -benchtime=1x -count=3 -benchmem
```

| Batch size | Raw elapsed times (ms) | Median elapsed (ms) | Median throughput (posts/s) | Median B/op | Median allocs/op |
| ---: | --- | ---: | ---: | ---: | ---: |
| 20 | 9.936, 9.441, 9.395 | **9.441** | **2,118** | 3,032,640 | 7,746 |
| 200 | 40.707, 32.625, 31.911 | **32.625** | **6,130** | 30,313,136 | 76,226 |
| 1,000 | 172.142, 170.287, 171.237 | **171.237** | **5,840** | 162,436,224 | 386,154 |

B/op means **total allocated bytes during the synthetic batch**, not resident or peak memory. The 200->1,000 scale-up increased wall time ~5.25x for 5x records, consistent with approximately linear scaling in this controlled workload. At 1,000 posts, ~162 KB and ~386 allocations occurred per synthetic post. These measurements exclude real source networking, actual media file sizes, Mebooru storage and indexing, and production image processing.

**Assessment:** No convincing scaling bottleneck at this local HTTP boundary. Do not optimize from these synthetic numbers alone. Next meaningful performance evidence is controlled real source-to-library acquisition.

## 3. Alternate live booru: Danbooru (if accessible)

Rather than repeatedly retrying e621 after its connection reset, use the upstream gallery-dl v1.32.19 fixture `https://danbooru.donmai.us/posts/294929`. Its pinned fixture declares `rating: "s"`, which means **sensitive** in the Danbooru family, not e621's **safe**. Its source tags include artist, character, copyright, meta and general fields. This makes it a stronger cross-family rating/tag test than another Derpibooru post.

**Metadata-only preflight** (one post, no media download):

```sh
GDL="$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl"
OUT="${TMPDIR:-/tmp}/danbooru-rating-check.json"

"$GDL" --config-ignore --dump-json \
  'https://danbooru.donmai.us/posts/294929' > "$OUT"

python3 - "$OUT" <<'PY'
import json
import sys
from pathlib import Path

messages = json.loads(Path(sys.argv[1]).read_text())
if not isinstance(messages, list):
    raise SystemExit("Unexpected extractor message format.")
errors = [m[-1] for m in messages
          if isinstance(m, list) and m and m[0] == -1 and isinstance(m[-1], dict)]
if errors:
    print("Extractor error class:", errors[0].get("error", "unknown"))
    raise SystemExit("Source metadata unavailable; stop without importing.")
posts = [m[-1] for m in messages
         if isinstance(m, list) and m and isinstance(m[-1], dict)
         and m[-1].get("category") == "danbooru"]
if not posts:
    raise SystemExit("No Danbooru post metadata; stop without importing.")
post = posts[0]
print("Source:", post.get("category"))
print("Post ID:", post.get("id"))
print("Native rating:", post.get("rating"))
print("Categorized fields:", sorted(k for k in post if k.startswith("tag_string_")))
if post.get("id") != 294929 or post.get("rating") != "s":
    raise SystemExit("Source changed since pinned fixture; inspect before importing.")
print("Metadata preflight: PASS (expected mapped rating: sensitive)")
PY
```

The post and rating may change over time. **Never** import it merely because the extractor process exited with code zero: `--dump-json` can embed error messages. Do not share raw metadata URLs or API credentials. If access fails, stop after one attempt. A fallback candidate is the Gelbooru-family `safebooru.org` source; start with one metadata-only post and verify rating/category fields before attempting an import.

**Routing guard:** before any Danbooru import, check that Meloader's `[monbooru].default_gallery` plus any `[[sites]]` override for `danbooru` resolves to **Audit_A**, not `home` or `Audit_B`. The existing successful Derpibooru override check does *not* prove Danbooru's routing.

Once metadata and routing both pass, import **only this one post** through Meloader using `up to 1`. Confirm `1 created` or a clearly distinguished duplicate; inspect the individual Mebooru image detail for `rating:sensitive`, categorized tags and source provenance. Do not turn a failed source into a generic/unrated import.

## 4. Staged live workload, not a bulk leap

After the one-post cross-family import works:
- Run a source-filtered **5-post** audit job (never `all`) and inspect created/skipped/failed counts, the correct gallery, source attribution and per-item rating.
- If it completes cleanly, run a **20-post** audit job to measure end-to-end wall time, bytes downloaded, rate/requests where available, and Mebooru item persistence.
- Stop at unexpected authorization errors, sustained rate limiting, repeated 5xx, bad/missing source ratings or an unexpected gallery destination. Never defeat access controls or increase concurrency just to complete the test.

For every run, record the source URL/query, gallery, two running server commit SHAs, gallery-dl version, job window, start/end time, successes/skips/failures and first failure reason. **Do not conflate** these real-run results with the mock HTTP benchmark.

## Remaining acceptance gates

- [x] Positive Derpibooru source -> Mebooru Audit_A -> per-image metadata, tags and original source persisted
- [x] Reverse Mebooru -> Meloader authenticated health restored with existing bearer token
- [x] Expanded mixed-batch rejection and normal source-rating cases passed Go race+vet CI (run 43356175480)
- [x] Local Apple M5 negative-path/rating tests passed in mapping and pipeline at commit `41c954b` (`go test -race`, 1.533s/1.564s)
- [x] Execute local Apple M5 loopback HTTP benchmark: median 20=2,118 posts/s, 200=6,130 posts/s, 1,000=5,840 posts/s; synthetic 16-KiB files and mock Mebooru only
- [ ] One live Danbooru (or another distinct-rated-family) post accepted with correct native/mapped rating
- [ ] Bounded 5-post and 20-post live workloads accepted and measured
- [ ] Live e621 categorized extractor confirmation (deferred for transport reset; do not block all other testing)
- [ ] Source taxonomy semantic decisions beyond the approved rating-integrity distinction (discuss before changes)

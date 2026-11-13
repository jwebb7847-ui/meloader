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

**Observed local Danbooru preflight (2026-11-13):** routing validation reported `Danbooru destination: Audit_A` and `Routing preflight: PASS`. The gallery-dl 1.32.19 `--dump-json` metadata request for post `294929` returned one `HttpError` record and the script stopped before download/import. The redacted Danbooru detail was subsequently inspected and matched the e621 transport error: `ConnectionError: ('Connection aborted.', ConnectionResetError(54, 'Connection reset by peer'))`. **No usable source metadata or HTTP status was returned** on either site. These failures do not identify which peer or intermediary reset the connection, and they are not evidence of rating/taxonomy incompatibility. The earlier Derpibooru positive test remains valid for its date; a fresh metadata-only retest can establish current reachability.

Read the saved metadata result **without another network request**:

```sh
python3 - <<'PY'
import json
import os
import re
from pathlib import Path

out = Path(os.environ.get("TMPDIR", "/tmp")) / "danbooru-rating-check.json"
messages = json.loads(out.read_text())
for msg in messages:
    if isinstance(msg, list) and msg and msg[0] == -1 and isinstance(msg[-1], dict):
        error = msg[-1]
        detail = str(error.get("message", ""))
        detail = re.sub(r"https?://\S+", "[URL REDACTED]", detail)
        print("Extractor error class:", error.get("error", "unknown"))
        print("Redacted detail:", detail[:350] or "(none)")
PY
```

If the detail also shows a connection reset, classify both unavailable source tests as **network-transport blocked** without claiming an origin. The third candidate is gallery-dl's pinned Safebooru/Gelbooru-v02 fixture `https://safebooru.org/index.php?page=post&s=view&id=1169132`. A single metadata-only probe was run using isolated gallery-dl 1.32.19 and `-o extractor.safebooru.tags=true`; its parser said **`STOP: Expected exactly one post.`** That result is **inconclusive**, not a confirmed HTTP failure. The pinned gallery-dl implementation emits both `Message.Directory` and `Message.Url` for each booru post, with post metadata in **both** messages. The previous script incorrectly enforced `len(posts) == 1`, likely rejecting the two expected messages. Inspect the **already saved** Safebooru JSON and group messages by unique `(category, id)` rather than counting post messages. For a fresh Derpibooru comparison, request its same known single-post URL `https://derpibooru.org/images/3796956` with `--config-ignore --dump-json` **once** and compare message type counts, unique post IDs, rating and category field names; no image download or import. If Safebooru has one unique post and no error, its metadata-only extraction likely passed. Before any import, check its native rating, taxonomy completeness, file size and per-site override to `Audit_A`. Do not retry blocked hosts repeatedly.

### Controlled comparison outcome: Derpibooru and Safebooru both reachable

The operator reran **one** metadata-only Derpibooru request for previously validated post `3796956` using gallery-dl 1.32.19. Both the original and fresh Derpibooru payloads were `Message.Directory=1, Message.Url=1`, one distinct post, rating field absent, rating tag `safe`, **19** tags and no `tags_*` fields (correct for the Philomena-family extractor; taxonomy enrichment comes later).

The previously saved **Safebooru** metadata-only payload was also `Message.Directory=1, Message.Url=1`, **one** distinct post `1169132`, native rating field `s`, **9** flat tags, and categorized fields `tags_artist`, `tags_character`, `tags_copyright`, `tags_general`, `tags_metadata`. Therefore its earlier `Expected exactly one post` was purely a faulty test parser counting two messages for one post. It was **a successful live extractor test**, with no image imported yet. Its `gelbooru_v02` family maps native `s` to `rating:general`, and the mapper maps `tags_metadata` to Mebooru's `meta`.

**e621** and **Danbooru** each had exactly one embedded `HttpError`, zero post metadata and the same connection reset: `ConnectionError: ('Connection aborted.', ConnectionResetError(54, 'Connection reset by peer'))`. Neither error establishes which peer or intermediary caused the reset. The working boorus make a general network outage or universally broken gallery-dl install unlikely. Leave those two sources deferred rather than repeatedly retrying.

A follow-up **test-only regression** covers Safebooru's string-valued categorized fields and native `s` rating at the pipeline-to-mock-HTTP upload boundary. See `internal/pipeline/rating_upload_integration_test.go`, case `safebooru real extractor shape with categorized string fields`. This does **not** change runtime behavior.

**Safebooru follow-up preflight (2026-11-13):** The operator's read-only check confirmed `safebooru -> Audit_A`, exactly one file URL message for post `1169132`, source rating `s`, and all expected category fields. The post dictionary did **not** expose `file_size` or `size`, so the script stopped with `File size: Unknown` and **no media download/import**. This is a size-guard stop, not an extractor/metadata failure; gallery-dl's Gelbooru-v02 extractor reads the API response without synthesizing the missing byte count. Next perform a **single HTTP HEAD** against the original image URL in the saved `Message.Url` payload, allow only `https://safebooru.org` (or subdomain) without following redirects, and require a valid `Content-Length` in (0, 10 MiB]. If HEAD is refused, redirected, or omits Content-Length, stop and report it; never fall back to a blind full download or treat unknown as a pass. Headers are advisory; a future bounded downloader limit would be a separate behavior decision.

**Safebooru header-only size probe (2026-11-13) — PASSED via verified curl:** Standalone Python 3.12 (`/Library/Frameworks/Python.framework/Versions/3.12/bin/python3`, OpenSSL 3.0.16) reported `ssl.get_default_verify_paths().cafile` unresolved; its three `urllib.request` HEAD attempts failed with `SSLCertVerificationError` before an HTTP response. A separate **default-verification macOS curl HEAD** against the saved `Message.Url` for post `1169132` returned **HTTP/2 200**, **`Content-Type: image/jpeg`**, **`Content-Length: 247882` bytes (0.24 MiB)**, under the 10 MiB audit threshold, without downloading media. Safebooru's preflight is now complete: `Audit_A` routing, source ID, rating, categorized tags, and advertised media size verified. The Python trust failure is environment-specific evidence, not proof of any remote certificate defect; investigate only if Python-based direct access is required. Do not use insecure TLS overrides. HEAD size is an advisory report, **not** an enforced byte cap during a future download. **Next step:** one Meloader UI import to `Audit_A`, up to 1 item, inspect per-image categories and `rating:general` in Mebooru; do not claim successful upload until observed.

**Next acceptance gate:** confirm the configured `safebooru` site gallery override (if any) resolves to `Audit_A`; verify saved post `1169132` has the exact expected identity, `s` rating, nonempty categorized fields, and acceptable media size before importing **one** post in Meloader. Check the resulting image's own detail sidebar and source afterward. Meloader's managed configuration already enables `tags: true` for gelbooru_v02-family sites via `internal/gdl/config.go`.

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
- [x] Metadata-only source contrast: Derpibooru original and retest consistent, Safebooru post 1169132 confirmed as a single post with native `s` and five categorized tag fields; e621/Danbooru remain reset-blocked
- [x] Safebooru image HEAD preflight passed with verified curl TLS, `image/jpeg`, `247882` bytes (0.24 MiB); Python 3.12's own default CA-file lookup was unresolved and its urllib HEAD failed certificate verification
- [ ] One live Danbooru (or another distinct-rated-family) post accepted with correct native/mapped rating
- [ ] Bounded 5-post and 20-post live workloads accepted and measured
- [ ] Live e621 categorized extractor confirmation (deferred for transport reset; do not block all other testing)
- [ ] Source taxonomy semantic decisions beyond the approved rating-integrity distinction (discuss before changes)

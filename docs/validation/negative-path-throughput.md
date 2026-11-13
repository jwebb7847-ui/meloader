# Negative-path integrity and bounded throughput acceptance

Status (2026-11-13): **expanded negative-path/mixed-batch tests passed Go race+vet CI** ([run 43356175480](https://github.com/jwebb7847-ui/meloader/actions/runs/43356175480), test code at `85c2d2e`, validation doc at `aa25f70`). **positive Derpibooru -> running Mebooru Audit_A import fully passed**; image ID 7 retained its file, source, `rating:general`, and all 19 image-specific tags. The reverse Mebooru -> Meloader authenticated link was repaired by correcting the local API port. The initial e621 attempt encountered a peer connection reset. A later [live e621 result](e621.md#live-e621-import--2026-11-13) passed after a network-path change.

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

### Safebooru live end-to-end acceptance — PASSED, 2026-11-13

The operator submitted one post through the **running Meloader audit UI**:

- Source: `https://safebooru.org/index.php?page=post&s=view&id=1169132`
- Extractor/category: `safebooru` / gallery-dl Gelbooru-v02
- Destination: **`Audit_A`**, not `home` or `Audit_B`
- Meloader queue: **`succeeded`**, **`1 created`**, per-item **`created 10 tags sent`**
- Mebooru single-image detail: **image ID 8**, `safebooru_1169132_9b0101aa8a66b09c2ac564a1bad8269d.jpg`, JPEG **700 × 506**, **242.1 KiB**. Source `safebooru`, `Added via monloader`, source-original link to `i1.pixiv.net`; hashes present. Footer displays connected to monloader.
- **Image-specific tag sidebar has all ten tagged values**: rating `general` (1); meta `tagme` (1); artist `kawanakajima` (1); copyright `dc_comics`, `mcdonald's`, `the_dark_knight` (3); character `heath_ledger`, `ronald_mcdonald`, `the_joker` (3); general `parody` (1). The counts next to tags in the UI are overall usage counts, not per-image multiplicities; the list itself is image-specific.
- User estimated approximately **4 seconds** for this single job. This is an anecdotal wall-time observation, **not** measured sustained throughput.
- Preceding saved metadata validation showed `s` and string-valued `tags_artist`, `tags_character`, `tags_copyright`, `tags_general`, `tags_metadata`; their correct category persistence and `s -> rating:general` are now demonstrated against the **real Mebooru HTTP/API + on-disk library**, not just the mock.
- Preliminary media HEAD `Content-Length: 247882` bytes matches the image-size displayed (~242.1 KiB); HEAD was advisory, not enforced by the downloader.

**Result:** The second independent acquisition family, Safebooru/Gelbooru-v02, has **passed full positive end-to-end acceptance**, following the earlier Derpibooru/Philomena pass. **Not** yet proved: repeated/multi-post real workload throughput, all source-rating variants, live invalid-rating rejection against a running Mebooru (the mock-HTTP negative tests do pass), or e621/Danbooru network transport access.

### Bounded five-post Safebooru audit — download/upload PASS, one tag warning pending

User ran Meloader's **`up to 5`** queue submission for `https://safebooru.org/index.php?page=post&s=list&tags=all` to **`Audit_A`** on 2026-11-13. Screenshot of expanded queue confirms **`succeeded`**, **`5 created`**, **`more available`**, five item rows, zero apparent failures/skips, and no change to gallery routing.

| Source post | Outcome | Tags reported sent | Warning |
| --- | --- | ---: | --- |
| 7303986 | created | 52 | **1 tag warning**; truncated as `tag ...: invalid tag name: name must c...` |
| 7303985 | created | 31 | none shown |
| 7303984 | created | 30 | none shown |
| 7303983 | created | 30 | none shown |
| 7303982 | created | 64 | none shown |

Total **207 tags reported sent**. Important: `TagsSent` is assigned from **`len(sent.Tags)`** in Meloader `internal/pipeline/processor.go`, **before** Mebooru's validator decides acceptance. Thus these are tags **submitted**, not a confirmed count of persisted tags. The one warning for `7303986` likely indicates rejection of a punctuation-only tag, visually resembling `...`, but the exact name/warning requires the operator to hover the UI warning and copy the full title text. Mebooru's `tags.ValidateTagName` in `internal/tags/tags_crud.go` intentionally rejects names consisting solely of decoration such as `...` (requires at least one meaningful rune). **Do not silently normalize or change that validation policy** without discussion. Check one or two newly imported Mebooru image-specific detail sidebars, including post `7303986`, to confirm category/rating persistence before marking the full five-image metadata acceptance green.

Operator stopwatch estimate: **~12 seconds click-to-finish** (about **10–11 seconds from first downloading status**). This corresponds to ~0.42 posts/sec or **25 successfully created images/minute** for this *one five-post job*, with wall-time measurement uncertainty. Do **not** extrapolate to sustained rate, assume no server-side throttling, or compare directly to the synthetic localhost HTTP benchmark. Additional source-dependent metadata requests, image sizes and tag counts were not measured.

**Next gate:** hover `1 tag warning` or the adjacent truncated text in Meloader UI to obtain the exact warning (the `[debug]` button **re-runs an extractor** and is not needed); inspect image-specific persisted tags/rating in Mebooru for `7303986` and at least one clean companion item. If the warning is the deliberate rejection of `...` and other metadata checks pass, advance to a **bounded 20-post** real workflow as a separate test.

### Five-post metadata acceptance follow-up — PASSED

Operator provided the **full tooltip** from Safebooru post `7303986`:

> `tag ...: invalid tag name: name must contain at least one letter, digit, or emoticon character`

This exactly matches Mebooru's existing `tags.ValidateTagName` rule: a decoration-only string `...` cannot become a valid tag, while meaningful emoticons can. The warning is **visible, actionable and specific**; do not silently coerce or accept the rejected value. The `52 tags sent` value is a submitted count and does **not** mean all 52 persisted; that single rejected tag is a known exception.

Two **image-specific** Mebooru Audit_A detail screenshots confirm the rest of the pipeline's core integrity:

- **Image ID 9**, from the new Safebooru batch: PNG **2048×2048**, **3.1 MiB**, `Added via monloader`; Safebooru source and independent original URL preserved. Rating `general`. Character `nikki_(nikki)`, copyright `nikki_(series)`, meta entries including `highres`, and general descriptive tags shown. Notably general emoticon `:3` persisted, but rejected `...` is absent. Screenshot is vertically scrolled, so not every tag can be counted from this view.
- **Image ID 11**, source post `7303984`: PNG **1920×1080**, **3.5 MiB**, `Added via monloader`; Safebooru and original source preserved. Rating `general`; character `neuvillette_(genshin_impact)`, copyright `genshin_impact`, meta `official_art` and `highres`, with correctly grouped general tags such as `fountain`, `night` and `rain`.

**Acceptance decision:** the five-image *acquisition and spot-checked metadata persistence* gate is **PASSED with one intentional tag rejection**. The source faithfully exposed the questionable raw tag, Meloader did not suppress its rejection, and Mebooru maintained its existing validation policy. This is **not** a zero-warning run, nor exhaustive independent comparison of all 207 submitted tags.

The next scale test should keep the `Audit_A` routing, avoid an unbounded `Get all`, and distinguish skipped archive positions from newly downloaded/created images. A new listing job with `up to 20` on the **same URL** includes previously processed first-five positions, so expect the archive to skip those positions; the job then tests **up to 20 listing positions**, not 20 guaranteed new downloads. Alternatively the existing series' `Get next 5` advances disjoint windows but is not a single 20-item job. Do not conflate the two throughput measurements.

## 4. Staged live workload, not a bulk leap

After the one-post cross-family import works:
- Run a source-filtered **5-post** audit job (never `all`) and inspect created/skipped/failed counts, the correct gallery, source attribution and per-item rating.
- If it completes cleanly, run a **20-post** audit job to measure end-to-end wall time, bytes downloaded, rate/requests where available, and Mebooru item persistence.
- Stop at unexpected authorization errors, sustained rate limiting, repeated 5xx, bad/missing source ratings or an unexpected gallery destination. Never defeat access controls or increase concurrency just to complete the test.

For every run, record the source URL/query, gallery, two running server commit SHAs, gallery-dl version, job window, start/end time, successes/skips/failures and first failure reason. **Do not conflate** these real-run results with the mock HTTP benchmark.

### Bounded 20-post Safebooru run and continuation — PASSED

The operator used the same Safebooru listing and the running Meloader/Mebooru audit services, with `Audit_A` as the destination. Times are approximate operator stopwatch readings. Counts below apply to each window.

| Window | Source posts (descending) | Created | Archive skips | Failed | Tags submitted | Elapsed |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Up to 20 | `7303987` through `7303968` | 15 | 5 | 0 | 552 | ~31 s |
| Get next 20 | `7303967` through `7303948` | 20 | 0 | 0 | 880 | ~35 s |

The five archive skips in the first window were the previously fetched posts `7303986` through `7303982`. The continuation began at the next listing position and imported all 20 rows. The full new-image batch averaged ~1.75 seconds per created image end to end. The stopwatch includes the source, transfer and storage steps, so it cannot isolate taxonomy or database time. The earlier 20-position run also included five skips and is a mixed workload.

Mebooru image ID `24` (source post `7303972`, first 20-position window) showed its general rating, meta `highres`, copyright `golden_kamuy`, characters `asirpa` and `ogata_hyakunosuke`, general tags, Safebooru attribution, and an original `i.pximg.net` link. The continuation screenshot verifies the acquisition outcomes and submitted counts. Runtime SHAs and downloaded bytes were not independently captured in the batch screenshots.

## 5. Malformed source rating against the running audit gallery

Use the detached Meloader QA worktree, with the existing Mebooru audit server already listening at `127.0.0.1:8455`. The opt-in test reads the existing Meloader audit config for its Mebooru endpoint and token, confirms `Audit_A` exists, and uses a loopback proxy to count upload attempts. A synthetic two-item Safebooru batch contains a malformed native rating and a valid `s` rating. The fixtures are generated PNGs, so the test requires no external source requests. Expect `mapping_failed` on the malformed item, exactly one upload attempt for the control, and a verified SHA-256 for the resulting image in `Audit_A`.

From the QA checkout at the revision containing the opt-in test:

```sh
MELOADER_LIVE_RATING_AUDIT_CONFIG="$HOME/Library/Application Support/Monloader Audit/monloader.toml" \
  go test -race ./internal/pipeline -run '^TestRatingIntegrityAgainstLocalAuditGallery$' -count=1 -v
```

A passing run leaves one synthetic control image in `Audit_A` and reports its image ID. The test removes its local scratch PNGs and reads the audit config without saving changes.

**Local audit result (2026-11-13) — PASSED.** The operator ran `TestRatingIntegrityAgainstLocalAuditGallery` with `-race` from detached Meloader revision `ed6477fe9d2c45635a6515a7458b98568434d440` on macOS, against the running Mebooru audit server. The malformed Safebooru item failed with `mapping_failed: source rating: safebooru returned an unrecognized rating "not-a-rating"`. The proxy counted exactly **one** upload attempt, the valid control, and a read-back from `Audit_A` matched the generated control's SHA-256 (image ID `7` in this run). The test also passed its scratch-cleanup assertions. The reported `0.02 s` test duration is for the local synthetic check.

## 6. Live Derpibooru taxonomy cache audit

Run the opt-in test from the detached Meloader QA worktree using the isolated gallery-dl 1.32.19 executable. The workload resolves the newest 20 Derpibooru posts tagged `safe` and `rainbow dash` once. The exact post IDs are printed and the same metadata is reused across all four passes.

```sh
MELOADER_DERPIBOORU_CACHE_AUDIT_GDL="$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl" \
  go test ./internal/taxonomy -run '^TestDerpibooruLiveTaxonomyColdWarm$' -count=1 -v
```

The cold pass uses a new empty cache path in the test's temporary directory and calls the real Derpibooru taxonomy API. Three warm passes each construct a fresh resolver from the same persisted file and fail if the resolver attempts HTTP. Every pass checks the general rating and Rainbow Dash character category. Warm passes must reproduce every post's full mapped tag list from the cold pass.

Record the gallery-dl version, post IDs, distinct source tags, metadata fetch time, cache load time, enrich-and-map time, taxonomy HTTP attempts, mapped tags and cache bytes. Compare the cold enrich-and-map time to the median of the three warm passes, with cache load reported separately. The cold timing includes taxonomy HTTP and persistence. The warm passes run in the same process and the OS can cache the file. This measures the resolver and mapper sequentially. The downloader's concurrent cache warming, media transfers, time to first import and gallery storage are outside this measurement. The test uses its own cache and needs neither running audit service.

### MacBook Air live result — PASSED, 2026-11-13

The operator ran revision `94641eb` with gallery-dl `1.32.19`. The listing took **1.042 s** to resolve 20 posts with **193 distinct source tags**. All four passes mapped **442 tag assignments** and the saved taxonomy cache was **26,985 bytes**. All three fresh warm resolvers reproduced every post's full mapped tag list. The test checked `rating:general` and `character:rainbow_dash` on every post.

| Pass | Cache load | Enrich and map | Taxonomy HTTP attempts |
| --- | ---: | ---: | ---: |
| Cold | 17 µs | 22.99015 s | 17 |
| Warm 1 | 382 µs | 1.661 ms | 0 |
| Warm 2 | 727 µs | 1.320 ms | 0 |
| Warm 3 | 282 µs | 1.380 ms | 0 |
| Warm median | 382 µs | 1.380 ms | 0 |

The median of the three warm load-plus-processing totals was **2.043 ms**. The cold run logged **15 HTTP 200 responses and two HTTP 429 responses**. Each 429 recovered on the second attempt after roughly 10 seconds. Summed logged HTTP durations were about **2.665 s** and the retry waits accounted for about **20 s** of the cold pass. These are timings for sequential taxonomy enrichment and mapping of a fixed metadata set. The listing time is separate and full acquisition throughput remains open.

Posts in listing order: `3931277 3931184 3931173 3931162 3931149 3931126 3931098 3931062 3931023 3931022 3931021 3931020 3931019 3931018 3931017 3930996 3930986 3930944 3930943 3930940`.

## 7. Post-merge Derpibooru upgrade smoke test

On 2026-11-13, the operator ran the merged Meloader build (`3aa5aca`, PR #5) with the copied audit setup against Mebooru's `Audit_A` gallery. The first run of a fixed five-post Derpibooru search succeeded with **5 created** and **142 tag assignments submitted**. The completed job was visible immediately after Meloader restarted. Submitting the same search again succeeded with **0 created, 5 skipped, and 0 failed**. Every replay row was labeled `already fetched`.

| Derpibooru post | Initial run | Replay after restart |
| --- | --- | --- |
| `3931277` | Created, 11 tags sent | Already fetched |
| `3931184` | Created, 26 tags sent | Already fetched |
| `3931173` | Created, 19 tags sent | Already fetched |
| `3931162` | Created, 40 tags sent | Already fetched |
| `3931149` | Created, 46 tags sent | Already fetched |

**PASSED.** Five live imports, queue history reload, and archive skips after restart passed. In Mebooru, the operator spot-checked Derpibooru posts `3931184` and `3931173` (image IDs `8` and `9` in `Audit_A`). Both detail pages show `rating:general`, artist, character, species, meta and general tags, and Derpibooru attribution. The original source links point to X and DeviantArt, respectively. The operator confirmed the source-link checks.

After restart, tracing Derpibooru post `3796956` previewed **19 tags**, showed `safe` mapping to `rating:general`, and populated the `Audit_A` resolution column. The operator confirmed all 19 rows. Meloader logged successful taxonomy enrichment and no taxonomy HTTP request for that trace, confirming cache reuse after restart.

## Remaining acceptance gates

- [x] Positive Derpibooru source -> Mebooru Audit_A -> per-image metadata, tags and original source persisted
- [x] Reverse Mebooru -> Meloader authenticated health restored with existing bearer token
- [x] Expanded mixed-batch rejection and normal source-rating cases passed Go race+vet CI (run 43356175480)
- [x] Local Apple M5 negative-path/rating tests passed in mapping and pipeline at commit `41c954b` (`go test -race`, 1.533s/1.564s)
- [x] Execute local Apple M5 loopback HTTP benchmark: median 20=2,118 posts/s, 200=6,130 posts/s, 1,000=5,840 posts/s; synthetic 16-KiB files and mock Mebooru only
- [x] Metadata-only source contrast: Derpibooru original and retest consistent, Safebooru post 1169132 confirmed as a single post with native `s` and five categorized tag fields; the initial e621 and Danbooru probes encountered connection resets
- [x] Safebooru image HEAD preflight passed with verified curl TLS, `image/jpeg`, `247882` bytes (0.24 MiB); Python 3.12's own default CA-file lookup was unresolved and its urllib HEAD failed certificate verification
- [x] One live alternate-family Safebooru/Gelbooru-v02 post imported to Audit_A with `rating:general`, all 10 categorized per-image tags and source provenance (Mebooru ID 8); Danbooru-specific live rating `s -> sensitive` remains unverified due to transport reset
- [x] Five-post Safebooru acquisition and spot-checked metadata persistence accepted: all 5 created, ~12 s wall-time, 207 tags submitted, exact `...` validator warning intentionally rejected; Audit_A image IDs 9 and 11 confirm rating/category/source preservation
- [x] Bounded 20-position workload and full 20-new-image continuation accepted and timed; source metadata spot-checked in the first window
- [x] Malformed-rating test against the running local Audit_A server passed: one rejected item, one upload attempt and one SHA-256-verified control
- [x] Bounded live Derpibooru cold/warm taxonomy measurement passed: 20 posts, identical tags across all passes and zero warm taxonomy HTTP attempts
- [x] Post-merge Derpibooru smoke test passed for five live imports, queue history reload and archive skips after restart
- [x] Post-merge image metadata spot checks and taxonomy-cache reuse passed on the merged build
- [x] Live e621 trace and import passed for post `535` in `Audit_A`. See the [live e621 result](e621.md#live-e621-import--2026-11-13).
- [ ] Danbooru live validation remains deferred after the transport reset
- [ ] Source taxonomy semantic decisions beyond the approved rating-integrity distinction (discuss before changes)

# Source rating integrity — acceptance plan and results

**Status (2026-11-13):** The real pipeline-to-HTTP boundary tests and full Go race tests + vet passed at commit `16d490887d17d3a4de4966182bdec2b33c28852f` on [GitHub CI run 43346314936](https://github.com/jwebb7847-ui/meloader/actions/runs/43346314936). Positive imports from Derpibooru and Safebooru and the local malformed Safebooru rating check against running Mebooru have passed. Live e621 and Danbooru validation remains transport-blocked.

## Approved policy

- A known rating-bearing booru must provide an interpretable, nonconflicting source rating. Malformed, missing, or conflicting required ratings fail visibly as `mapping_failed` before Meloader automatically pushes file or metadata.
- A genuinely unrated generic source remains allowed; the receiving library can later filter its unrated images or separately run local AI classification.
- The *raw source's rating* and any later *inferred classification* must remain distinguishable; no Mebooru AI provenance feature is implemented as part of this change.
- User rating overrides continue to change how valid source ratings are represented; they do not validate arbitrary corrupt original claims.

## Automated acceptance already implemented

1. `internal/mapping/rating_validation_test.go`: standard rating codes, different family semantics, missing/malformed values, conflicting Philomena rating tags, user overrides, genuinely unrated sources, diagnostic traces.
2. `internal/pipeline/rating_validation_test.go`: error code and reason propagation.
3. `internal/pipeline/rating_upload_integration_test.go`: the **real Meloader pipeline and HTTP client** talking to a local `httptest` server (not an external or running Mebooru). It verifies valid e621 uploads with `rating:general`, generic unrated uploads without a rating tag, no upload for missing/invalid e621 ratings, and a bundled gallery refusal before a CBZ file is constructed.
4. Full `go test -race ./...` and `go vet ./...` passed in GitHub CI.

These tests do not prove real gallery-dl extractor compatibility, real Mebooru persistence/filtering, or WAN acquisition throughput. Do not mark the release fully validated until those are checked.

## Optional local repeat in VS Code (safe; no download or running server)

From the **separate Meloader validation worktree**, not the Mebooru repository, and with no local changes to preserve:

```sh
git fetch origin --prune
git status --short
```

If `git status --short` prints anything, **stop** and inspect the changes before switching.

With a clean checkout:

```sh
git switch --detach origin/meloader-item3-rating-integrity
git rev-parse HEAD
go test -race ./internal/mapping ./internal/pipeline -run 'TestRatingIntegrity|TestRatingErr|TestValidateSourceRating|TestTraceReportsRatingIntegrityRefusal' -count=1 -v
```

No service, network access, personal data, or Mebooru instance is required for these tests.

## Controlled live-source confirmation (metadata only)

### macOS: locate Meloader's gallery-dl first

On 2026-11-13, local Go rating/integration tests all passed on the Apple M5 worktree at `844b6b2`. The subsequent extraction attempt did **not** contact e621: `gallery-dl: command not found` means the command is absent from that shell's PATH. The JSON decoder then saw the empty redirected file. Neither outcome indicates a rating or extractor regression.

Meloader can use its own managed gallery-dl, invisible to the shell's PATH. For the previously used non-desktop Audit configuration, the managed executable is generally in `~/Library/Application Support/Monloader Audit/gallery-dl/venv/bin/gallery-dl` (macOS venv); check the standalone and PATH alternatives too.

Run this whole block from the Meloader validation terminal; it leaves source and audit data untouched, checks executable availability **before** accessing the network, and doesn't parse JSON after a failed extraction:

```sh
AUDIT="$HOME/Library/Application Support/Monloader Audit"
GDL=""
for candidate in \
  "$AUDIT/gallery-dl/venv/bin/gallery-dl" \
  "$AUDIT/gallery-dl/gallery-dl" \
  "$AUDIT/gallery-dl"; do
  if [ -f "$candidate" ] && [ -x "$candidate" ]; then
    GDL="$candidate"
    break
  fi
done
if [ -z "$GDL" ]; then
  GDL="$(command -v gallery-dl 2>/dev/null || true)"
fi

if [ -z "$GDL" ]; then
  echo "No gallery-dl executable found in the audit folder or on PATH."
  echo "Stop here and report this message; do not parse an empty JSON file."
else
  echo "Using: $GDL"
  "$GDL" --version
  OUT="${TMPDIR:-/tmp}/e621-rating-check.json"
  if "$GDL" --dump-json 'https://e621.net/posts/535' > "$OUT"; then
    python3 - "$OUT" <<'PY'
import collections
import json
import re
import sys
from pathlib import Path

messages = json.loads(Path(sys.argv[1]).read_text())
if not isinstance(messages, list):
    raise SystemExit("Unexpected JSON shape: expected a list of gallery-dl messages.")

codes = collections.Counter(str(m[0]) for m in messages if isinstance(m, list) and m)
print("message types:", dict(codes))
for msg in messages:
    if not isinstance(msg, list) or not msg or not isinstance(msg[-1], dict):
        continue
    if msg[0] == -1:  # gallery-dl DataJob embeds extractor failures in JSON
        error = msg[-1]
        detail = str(error.get("message", ""))
        http_status = re.search(r"(?<![0-9])([45][0-9]{2})(?![0-9])", detail)
        print("extractor error class:", error.get("error", "unknown"))
        print("HTTP status:", http_status.group(1) if http_status else "not available")
        # Intentionally do not print the full error message or embedded URLs.

posts = [m[-1] for m in messages
         if isinstance(m, list) and m
         and isinstance(m[-1], dict)
         and m[-1].get("category") == "e621"]
if not posts:
    raise SystemExit("No e621 post metadata; inspect the message types and error class above.")
post = posts[0]
print("source:", post.get("category"))
print("post id:", post.get("id"))
print("rating:", post.get("rating"))
print("categorized fields:", sorted(k for k in post if k.startswith("tags_")))
PY
  else
    echo "gallery-dl could not retrieve e621 metadata; no JSON parsing attempted."
  fi
fi
```

**Important:** gallery-dl `--dump-json` can return a JSON list containing an error message (`-1`) and an exit code of zero, because the `DataJob` captures the extractor exception. A successful shell exit alone is **not** proof of successful metadata extraction. The parser above prints diagnostic **types/statuses only**, not raw error strings or URLs. On 2026-11-13, a pinned 1.32.19 macOS validation command returned `No e621 metadata returned`; extraction remains **unverified** until the embedded message is inspected.

This requests post **metadata over the internet**, not the image media, and does not import into Mebooru. A site refusal or removed example post is a separate access/extractor issue. If no executable is found, verify the audit configuration's `gallerydl.binary_path` *locally* (do not share the whole TOML file, as it may hold secrets) or install the pinned gallery-dl into a separate temporary Python virtual environment for the test. **Do not change the application's configuration or add a global installer just to satisfy this check.**

### Follow-up: JSON contains one `HttpError`, no reported HTTP status

The 2026-11-13 macOS attempt with isolated gallery-dl 1.32.19 returned **one** `[-1, {"error": "HttpError", ...}]`-style message and no e621 post fields. A diagnostic that searched the error text for a three-digit HTTP status found none. This is **inconclusive** as to the root cause. In upstream 1.32.19, an `HttpError` can be raised after a network/transport exception without an HTTP response; do not assume 403, 429 or a removed post.

Inspect the pre-existing file without making another HTTP request:

```sh
OUT="${TMPDIR:-/tmp}/e621-rating-check.json"
python3 - "$OUT" <<'PY'
import json
import re
import sys
from pathlib import Path

messages = json.loads(Path(sys.argv[1]).read_text())
errors = [msg[-1] for msg in messages
          if isinstance(msg, list) and msg and msg[0] == -1
          and isinstance(msg[-1], dict)]
if not errors:
    raise SystemExit("No embedded gallery-dl extractor error found")
for error in errors:
    message = str(error.get("message", ""))
    message = re.sub(r"https?://\S+", "[URL REDACTED]", message)
    print("Error class:", error.get("error", "unknown"))
    print("Redacted detail:", message[:350] or "(empty)")
PY
```

Do not share unredacted raw JSON or a configuration file. If the detail names a connection/proxy/SSL/DNS/timeout problem, classify the live check as **environment blocked** while keeping the already-passed local mapping and upload-boundary tests intact. If the detail includes an HTTP status, investigate site access or post availability separately. Do not repeatedly probe a blocked host.

### Observed root-cause category — network transport reset

The redacted exception from the existing 1.32.19 metadata attempt was:

```text
ConnectionError: ('Connection aborted.', ConnectionResetError(54, 'Connection reset by peer'))
```

It indicates the HTTP client did **not** receive a usable e621 HTTP response. We cannot attribute the reset to the site, intermediary or client path from this exception alone. The live e621 extraction gate is **environment-blocked / incomplete**, not a failed source-category or rating-compatibility test. **Do not keep retrying** or report an invented HTTP 403/429. Pinned upstream 1.32.19 extractor source and fixtures show the expected categorized `tags_<category>` output when a post can be fetched; our local mapper tests and mock HTTP push tests remain green.

For a separate, controlled future test, either retry once from an independently working network or choose another reachable rated/unrated source for the end-to-end pipeline. The first priority is to establish the existing audit configuration and the active Mebooru instance without changing library state. The isolated validation venv's binary can be selected at runtime with `MONLOADER_GALLERYDL_BINARY_PATH` for a `go run` test without editing `monloader.toml`; verify the config and target data/gallery first.

## Running the application from the checked-out source

Only when the dedicated audit configuration is present and the usual port is free:

```sh
CFG="$HOME/Library/Application Support/Monloader Audit/monloader.toml"
if [[ ! -f "$CFG" ]]; then
  echo "Missing audit config — do not start with defaults"
else
  echo "Audit config exists"
fi
lsof -nP -iTCP:8456 -sTCP:LISTEN
```

If the port is in use, **identify the old process and stop only the known audit instance** before launching another. Then, in a terminal in the Meloader worktree:

```sh
if [[ -f "$CFG" ]]; then
  go run ./cmd/monloader -config "$CFG"
else
  echo "Aborting: missing audit configuration"
fi
```

Stop it with Ctrl+C. Do not use an older compiled binary or kill an unknown process just because it owns port 8456. Running the application is not necessary for the Go unit/integration test above; and to test a real push into Mebooru we must also verify Mebooru's commit, launch command, config, gallery, and local data root.

## Live positive acceptance — Derpibooru to Mebooru (2026-11-13)

**Reported environment:** two source-based `go run` instances, Mebooru on `127.0.0.1:8455` at `mebooru-foundation` (`fbb5232`) and Meloader on `127.0.0.1:8456` at `meloader-item3-rating-integrity` (`11a894c` at last verified source synchronization). Both used explicit `~/Library/Application Support/* Audit/*.toml` configurations. Meloader targeted local Mebooru and `Audit_A`. Isolated `gallery-dl 1.32.19` was selected with `MONLOADER_GALLERYDL_BINARY_PATH`. The exact running process revision was not independently recorded after relaunch; the commit IDs are the previously verified target branch heads.

**Metadata-only preflight:** `https://derpibooru.org/images/3796956` returned `Source: derpibooru`, `Post ID: 3796956`, `Source rating tags: ['safe']`, `Tag count: 19`, `Metadata preflight: PASS`.

**Real import operator evidence:** Meloader queue row for the same URL showed `succeeded`, `derpibooru`, gallery `Audit_A`, `1 created`; expanded item `post 3796956` showed `created`, `19 tags sent`. Mebooru's sidebar showed `general` under rating and recognizable artist, character, species, meta and general taxonomy. **Important:** sidebar counts are gallery-wide, so we still need to open the imported image's own detail view to verify its exact persisted rating, source provenance and tag categories.

**Image-level persisted metadata CONFIRMED (2026-11-13):** The operator supplied the individual detail page for Mebooru `Audit_A` image ID `7`, `3796956.jpg`, including a follow-up with `id:7` in the search box. The page showed the image rendered from `~/Desktop/Audit Gallery A`, 774 × 1024 JPEG, hashes, `Added via monloader`, and the `derpibooru` source with original x.com link. **Correction of prior misinterpretation:** code inspection in Mebooru's `internal/web/handlers_detail.go` and `internal/web/tag_sidebar.go` confirms the detail page's tag sidebar is built from `GetImageTags(id)` for **this image only**; the numbers beside the tags indicate their global usage counts, not that the sidebar is showing tags from all search results. Therefore its displayed tags definitively belong to image 7: `rating:general` (translated from source `safe`), `meta:equestria_girls`, `meta:g4`, `artist:liaaqila`, `character:twilight_sparkle`, `species:human`, `species:pony`, and twelve general tags (`commission`, `duality`, `eyes_closed`, `human_ponidox`, `open_mouth`, `open_smile`, `outstretched_hoof`, `promo`, `self_paradox`, `self_ponidox`, `smiling`, `traditional_art`). This totals **19 persisted categorized tags**, matching the Meloader job's `19 tags sent`.

**Global connection warning (separate issue):** The operator reports `monloader auth rejected` appears on pre-existing images too, confirming it is the shared reverse link status, **not an image-specific error**. In Mebooru's `internal/monloader/link.go`, the status probe is a bearer-token read of `GET /api/v1/queue?limit=1` against Meloader; `ConnRejected` is set for 401, 403, 503 or `setup_required`. The token comes from Mebooru's `[monloader].api_token` per `internal/web/router.go`. **Root cause isolated with a credential-safe read-only localhost probe:** Mebooru `[monloader].api_url` was `http://127.0.0.1:8455` (self) rather than Meloader `http://127.0.0.1:8456`. The token in `[monloader].api_token` received **HTTP 200** from `http://127.0.0.1:8456/api/v1/queue?limit=1`. Mebooru `internal/monloader/link.go` chooses configured `APIURL` over paired `PeerURL`; the erroneous 8455 overrides the correct pairing target. **Repair operator-confirmed (2026-11-13):** Set `[monloader].api_url` to `http://127.0.0.1:8456`, preserved token/pairing/gallery configuration and restarted Mebooru. Screenshot of Mebooru footer shows a green status indicator and `connected to monloader dev`. No re-pair, token rotation, gallery modification or code change was required.

**Result:** **positive live acquisition, rating integrity translation, all 19 image-specific categorized tags, source provenance, physical image persistence and real Mebooru HTTP import all passed.** The reverse Mebooru -> Meloader link was repaired during this audit. Subsequent Safebooru checks covered bounded throughput and a malformed-rating refusal against the running audit server. Live e621 extraction remains pending.

## Local malformed-rating acceptance (2026-11-13)

**Local audit result (2026-11-13) — PASSED.** The operator ran `TestRatingIntegrityAgainstLocalAuditGallery` with `-race` from detached Meloader revision `ed6477fe9d2c45635a6515a7458b98568434d440` on macOS, against the running Mebooru audit server. The malformed Safebooru item failed with `mapping_failed: source rating: safebooru returned an unrecognized rating "not-a-rating"`. The proxy counted exactly **one** upload attempt, the valid control, and a read-back from `Audit_A` matched the generated control's SHA-256 (image ID `7` in this run). The test also passed its scratch-cleanup assertions. The reported `0.02 s` test duration is for the local synthetic check. [Procedure and workload results](negative-path-throughput.md#5-malformed-source-rating-against-the-running-audit-gallery).

## Remaining gates

- [x] Full Go race detector and vet on code change `16d4908`
- [x] Real pipeline -> mock HTTP upload boundary across valid, invalid, missing, genuinely unrated and bundled cases
- [x] Live gallery-dl **Derpibooru** metadata-only extraction: post 3796956 produced `category=derpibooru`, `id=3796956`, a `safe` rating tag and 19 source tags
- [x] Controlled **positive** real Meloader-to-Mebooru import: single Derpibooru post 3796956 routed to disposable `Audit_A` and returned `succeeded`, `1 created`, `19 tags sent`
- [x] Confirm the imported item is individually persisted: image ID `7`, filename `3796956.jpg`, real JPEG in `Audit_A`, `Added via monloader`, source `derpibooru`, and visible image preview
- [x] Verify the image-specific `rating:general` and all 19 tags: the image detail sidebar is built from `GetImageTags(7)`, with numbers reflecting gallery-wide usage counts, not an aggregate display
- [x] Diagnose global Mebooru footer `monloader auth rejected`: reverse-link `api_url` incorrectly targeted Mebooru on 8455; the existing bearer token returned HTTP 200 against real Meloader on 8456
- [x] Operator confirmed Mebooru `connected to monloader dev` after correcting `[monloader].api_url` to 8456 and restarting; existing bearer token accepted and no re-pair was necessary
- [ ] Live **e621** categorized metadata extraction; pinned gallery-dl 1.32.19 encountered a peer connection reset, not a source-mapping failure
- [x] Opt-in malformed Safebooru rating test passed against local `Audit_A`: `mapping_failed`, one upload attempt for the valid control, and SHA-256 verified by read-back
- [ ] Review e621 `lore` and `contributor` mapping **separately**; do not conflate with rating integrity
- [x] Bounded Safebooru end-to-end acquisition: five new images in ~12 s and 20 new images in ~35 s; see the [staged workload results](negative-path-throughput.md#bounded-20-post-safebooru-run-and-continuation--passed)
- [x] [Bounded Derpibooru taxonomy cache audit](negative-path-throughput.md#macbook-air-live-result--passed-2026-11-13): all three warm cache reloads reproduced the cold pass's mapped tags with zero taxonomy HTTP attempts
- [ ] Compare larger end-to-end workloads that exercise source taxonomy cache behavior

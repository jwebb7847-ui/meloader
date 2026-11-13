# Source rating integrity — acceptance plan and results

**Status (2026-11-13):** The real pipeline-to-HTTP boundary tests and full Go race tests + vet passed at commit `16d490887d17d3a4de4966182bdec2b33c28852f` on [GitHub CI run 43346314936](https://github.com/jwebb7847-ui/meloader/actions/runs/43346314936). Source-to-running-Mebooru acceptance is **not yet completed**.

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

With an existing gallery-dl executable available, check one public post's metadata without downloading its image:

```sh
gallery-dl --version
OUT="${TMPDIR:-/tmp}/e621-rating-check.json"
gallery-dl --dump-json 'https://e621.net/posts/535' > "$OUT"
python3 - "$OUT" <<'PY'
import json
import sys
from pathlib import Path
records = json.loads(Path(sys.argv[1]).read_text())
posts = [x[-1] for x in records if isinstance(x, list) and x
         and isinstance(x[-1], dict) and x[-1].get("category") == "e621"]
if not posts:
    raise SystemExit("No e621 metadata returned: check site access, authorization and extractor output")
post = posts[0]
print("source:", post.get("category"))
print("post id:", post.get("id"))
print("rating:", post.get("rating"))
print("categorized fields:", sorted(k for k in post if k.startswith("tags_")))
PY
```

This requests post metadata over the internet and may be refused by e621; it does **not** fetch image bytes or import them into Mebooru. Share only the printed summary; the raw JSON file is not needed. The public example may become unavailable or be blocked.

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

## Remaining gates

- [x] Full Go race detector and vet on code change `16d4908`
- [x] Real pipeline -> mock HTTP upload boundary across valid, invalid, missing, genuinely unrated and bundled cases
- [ ] Live gallery-dl rating/category extraction without media download
- [ ] Controlled actual Meloader-to-Mebooru import into an explicitly selected disposable audit gallery using a pinned commit pair
- [ ] Review e621 `lore` and `contributor` mapping **separately**; do not conflate with rating integrity
- [ ] End-to-end acquisition throughput and cache behavior for larger workloads

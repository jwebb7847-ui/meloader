# Local audit runbook

Last verified by the operator on the Apple Silicon MacBook Air on 2026-11-13. This is the development audit setup. Verify current listeners, checkout SHAs, and config files before use.

## Last validated setup

| Component | Checkout and code checkpoint | Audit configuration |
| --- | --- | --- |
| Mebooru | `$HOME/Documents/VSC/mebooru` · `mebooru-foundation` · `fbb5232` code checkpoint | `$HOME/Library/Application Support/Mebooru Audit/monbooru.toml` · `127.0.0.1:8455` |
| Meloader | `$HOME/Documents/VSC/meloader-rating-qa` · detached at merged `main` commit `3aa5aca` during the upgrade audit | `$HOME/Library/Application Support/Monloader Audit Main Upgrade/monloader.toml` · `127.0.0.1:8456` |
| gallery-dl | Isolated `1.32.19` executable | `$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl` |

The repository Go modules declare Go 1.27.3 at these code checkpoints. The older Meloader state is in `$HOME/Library/Application Support/Monloader Audit`. Reuse the existing **Main Upgrade** copy for this audit. The two sites' upstream internal command names are still `monbooru` and `monloader`.

The configured Mebooru galleries include `Audit_A`, `Audit_B`, and `home`. All queue tests here target `Audit_A`. The Mebooru config is authoritative for its media and database paths. The copied audit setup isolates Meloader state and still targets the dedicated Mebooru audit gallery.

## Inspect before launch

**VS Code — control terminal.** Check which app currently owns each port.

```sh
lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN
```

One row on 8455 means Mebooru is listening and no listener was reported on 8456. If an intended service already owns its port, establish which checkout and config started it before reusing it. When replacing a known foreground audit process, press Ctrl+C in its own terminal, wait for the prompt, and rerun `lsof`. Use `ps -p <PID> -o pid=,comm=` with the observed PID if identification is needed.

**VS Code — each app's terminal.** In the corresponding checkout, inspect `git status --short --branch`, `git rev-parse HEAD`, and `go version`. Fetch `origin` when the test needs a current ref, then choose a clean worktree and a specific revision deliberately. Fetching or changing a checkout does not replace a running executable. Preserve local edits and use a separate worktree for a candidate build.

## Launch the paired audit services

**VS Code — Mebooru terminal.** Start Mebooru first. Leave this terminal running.

```sh
cd "$HOME/Documents/VSC/mebooru" &&
  git status --short --branch &&
  git rev-parse HEAD &&
  test -f "$HOME/Library/Application Support/Mebooru Audit/monbooru.toml" &&
  go run ./cmd/monbooru -desktop=false \
    -config "$HOME/Library/Application Support/Mebooru Audit/monbooru.toml"
```

**Browser — Mebooru.** Open [127.0.0.1:8455](http://127.0.0.1:8455/) and select `Audit_A` in the gallery switcher. The companion connection indicator may update after Meloader starts.

**VS Code — Meloader terminal.** Define and run this function in a separate terminal. Use the same function after a restart. It is scoped to this shell session.

```sh
run_meloader_upgrade() (
  set -e
  audit_copy="$HOME/Library/Application Support/Monloader Audit Main Upgrade"
  audit_gdl="$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl"

  cd "$HOME/Documents/VSC/meloader-rating-qa"
  git status --short --branch
  git rev-parse HEAD
  for audit_file in monloader.toml gallery-dl.json upgrade-archive.sqlite; do
    if [ ! -f "$audit_copy/$audit_file" ]; then
      printf 'Missing audit file: %s\n' "$audit_copy/$audit_file" >&2
      exit 1
    fi
  done
  if [ ! -x "$audit_gdl" ]; then
    printf 'Missing gallery-dl executable: %s\n' "$audit_gdl" >&2
    exit 1
  fi
  "$audit_gdl" --version

  MONLOADER_SERVER_BIND_ADDRESS=127.0.0.1:8456 \
  MONLOADER_SERVER_BASE_URL=http://127.0.0.1:8456 \
  MONLOADER_MONBOORU_API_URL=http://127.0.0.1:8455 \
  MONLOADER_MONBOORU_WEB_URL=http://127.0.0.1:8455 \
  MONLOADER_MONBOORU_DEFAULT_GALLERY=Audit_A \
  MONLOADER_GALLERYDL_BINARY_PATH="$audit_gdl" \
  MONLOADER_GALLERYDL_CONFIG_PATH="$audit_copy/gallery-dl.json" \
  MONLOADER_GALLERYDL_ARCHIVE_PATH="$audit_copy/upgrade-archive.sqlite" \
  MONLOADER_GALLERYDL_COOKIES_DIR="$audit_copy/cookies" \
  MONLOADER_PTR_ENABLED=false \
  MONLOADER_LOG_LEVEL=info \
    go run ./cmd/monloader -desktop=false -config "$audit_copy/monloader.toml"
)
run_meloader_upgrade
```

**Browser — Meloader.** Open [127.0.0.1:8456](http://127.0.0.1:8456/) and verify that `Audit_A` is selected for the job. Site-specific or job-level routing can affect the destination. Use ordinary download only after confirming the destination and bound.

**Browser — Mebooru.** Check that the footer reports a connection to Monloader. The forward API is Meloader → Mebooru on 8455. Mebooru's reverse `[monloader].api_url` points to `http://127.0.0.1:8456`. Use the existing pairing. A trace that reports `connection refused` for 8455 can still preview outgoing tags but has not tested the gallery's resolution. An occupied 8456 requires identifying the listener before restarting.

## Reproduce the upgrade smoke test

1. **Browser — Meloader.** Open **Settings → Trace a post** and enter `https://derpibooru.org/images/3796956`. The validated trace previewed 19 tags and filled the `Audit_A` column. Check `safe → rating:general` and the artist, character, species, and meta namespaces. This is a metadata preview.
2. **Browser — Meloader.** Submit a fixed, bounded set of five safe-rated posts that have yet to be fetched in the copy. Use a multi-post search for the archive test. For a fixed set, use a Derpibooru search expression such as `safe,(id:3931277 OR id:3931184 OR id:3931173 OR id:3931162 OR id:3931149)` and replace the IDs with five unfetched posts when testing creation. The displayed IDs belong to the [recorded run](negative-path-throughput.md#7-post-merge-derpibooru-upgrade-smoke-test) and should skip in the validated archive. Expect five creations for a fresh set.
3. **Browser — Mebooru.** Open images from that job in `Audit_A`. Check image-specific `rating:general`, categorized tags, the Derpibooru source link, and an original source link where the post provides one. Record post IDs alongside local image IDs.
4. **VS Code — Meloader terminal.** Once the job completes, press Ctrl+C, wait for the prompt, and run `run_meloader_upgrade` again. Keep Mebooru running.
5. **Browser — Meloader.** Refresh the queue. The completed job should be visible immediately. Submit the same fixed search with ordinary Download. Expect `0 created`, `5 skipped`, `0 failed`, and `already fetched` on all five rows. A single-post download can bypass the archive, so use the same multi-post search for both runs.
6. **Browser — Meloader.** Trace `3796956` again. **VS Code — Meloader terminal.** Verify successful enrichment and zero Derpibooru taxonomy HTTP attempts for that warmed trace. A taxonomy cache hit reuses tag category knowledge. The download archive accounts for `already fetched`, and the gallery can reuse an existing tag while associating it with a newly imported image.

## Refresh a copy only when needed

Stop the audit Meloader process before copying. Check for queued, running, or scheduled jobs and review them before launching copied state. Use a new unused destination and retain the source audit directory. Copy the whole state directory, including `monloader.toml`, `profiles/`, `queue.sqlite`, `taxonomy/`, gallery-dl config, and any SQLite sidecars present. Check where the original effective archive is configured, then copy that archive and any corresponding sidecars into the copy as `upgrade-archive.sqlite` with matching sidecar names. Check for absolute paths or symlinks in the copied config, profiles, and extractor options that still reference original state. Review effective routing and keep watches inactive for manual QA. Keep all config and credentials local.

## Evidence to capture

Record app SHAs, launch modes, tool versions, gallery, source and post IDs, result counts, and relevant redacted logs. A transport reset, HTTP refusal, missing executable, or rate limit must be distinguished from an actual mapping failure. An opt-in live pipeline test may write a synthetic valid-control PNG to `Audit_A`. See the [malformed-rating audit](negative-path-throughput.md#5-malformed-source-rating-against-the-running-audit-gallery) before rerunning it.

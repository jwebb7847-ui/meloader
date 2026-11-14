# Loader integration

Mebooru supplies the Loader navigation and its gallery switcher. The Queue, Watches, PTR, and Settings panels run on Meloader's browser origin. Meloader retains the queue, archive, credentials, scheduler and download policy. The frames use Meloader's own login and CSRF protections. Meloader permits framing from the origin of its configured Mebooru Web URL. The Mebooru page uses the separate Web URL from **Settings → Plugins** and does not expose the pairing token to the frame.

Queue displays all galleries and pins a new import to the gallery displayed in its form. The existing bulk controls and strict verification still apply. A queue result with a hash and gallery opens that gallery. Watches created from Loader start in manual mode. Their routing still follows Meloader's source and default gallery settings. Existing watch intervals continue to work in Meloader.

## Local regression

Both Go race suites and `go vet` passed. The Meloader suite included the opt-in gallery-dl 1.32.19 strict transfer test. A separate loopback run used both candidate binaries, real gallery storage in two temporary folders, gallery-dl 1.32.19, and synthetic e621 metadata and PNGs. It exercised the rendered pages and their form endpoints.

| Check | Observed result |
| --- | --- |
| One search capped at 200 with strict verification | 180 created, 20 blacklisted, zero failed. The media server received 180 requests, all for allowed posts. |
| Switch from `Audit_A` to `Audit_B` during import | The job and all 180 files stayed in `Audit_A`. A result link opened `Audit_A` from `Audit_B`. |
| Replay the same search for five previously archived posts targeting `Audit_B` | All five skipped, with zero new media requests. The current archive can suppress a second-gallery import. The existing **Force download** control is available for an intentional replay. |
| Watches, PTR and Settings | Manual watch creation and **Check now** worked. PTR rendered with the index disabled. Meloader saved strict blacklist settings through its existing endpoint. |

The bulk run first observed an image file at about 2.8 seconds and completion at 4.2 seconds. These are loopback fixture timings. Browser execution and live-source timing are part of the Mac check.

## Mac acceptance

Use the paired audit configuration in the [local runbook](local-audit.md). Launch both candidate revisions and leave both terminals running. Mebooru's browser-facing Meloader Web URL should be `http://127.0.0.1:8456`. Meloader's browser-facing Mebooru Web URL should be `http://127.0.0.1:8455`. Open Mebooru with the same scheme, hostname, and port configured in Meloader.

1. **Browser — Mebooru.** Open **Loader**, select `Audit_A`, and check that Queue says **To Audit_A**. Open **Watches**, **PTR**, and **Settings → Meloader**. Verify that the panels load and the existing strict blacklist setting is enabled. If Meloader uses password login, sign in on its own origin.
2. **Browser — Mebooru.** Use one safe-rated search on a supported strict-mode booru, preferably a search with enough previously unfetched posts to give a timing sample. Select **up to 200**, confirm the destination, and submit it from Loader. Record time to first visible import, time to completion, and counts for created, archive skips, blacklisted and failed. Use the job's explicit gallery to verify its files and result links.
3. **Browser — Mebooru.** Switch galleries while the job runs and confirm the queued destination stays fixed. Verify the expected unavailable state with Meloader stopped after completion. Use existing watch rows or a bounded manual check for Watches. Keep scheduled audit watches inactive.

## Live Mac bulk result

On 2026-11-14, the operator ran one strict Derpibooru search through Loader with Mebooru PR #1 at `74a1794`, Meloader PR #12 at `ed0bb36`, and gallery-dl 1.32.19. The operator reported using a VPN. Job 31 succeeded with 195 created and five skipped out of 200 posts. The gallery switch left the job in `Audit_A`. Opening an imported item from `Audit_B` switched to `Audit_A`, and the image rendered with source and tag metadata.

| Stage | Measured result |
| --- | --- |
| Resolve | 6.888 seconds. The extractor resolved 201 records to detect more results and capped the job at 200. |
| Strict taxonomy verification | 261.652 seconds summed over 200 successful enrichments. There were 210 taxonomy HTTP attempts, with 182 HTTP 200s and 28 HTTP 429s. Logged HTTP attempts took 28.485 seconds in total. The rest of the enrichment time was predominantly retry waits. |
| Media phase | About 4 minutes 12 seconds between the last preflight taxonomy log and the first upload-side enrichment log. This interval includes gallery-dl setup, transfer pacing, and file transfers. The log has no separate timing for those steps. |
| Import | About 10 seconds from the first upload-side enrichment log to completion. All 195 taxonomy enrichments in this phase used cached evidence without further taxonomy HTTP requests. |
| Whole job | 8 minutes 51.258 seconds in the server log. The operator estimated 8 minutes 45 seconds and first noticed a downloaded row around minute five. |

The first download is gated on strict verification of the window. The timing identifies cold taxonomy request volume as the first optimization target. Evaluate batching distinct tag IDs across posts within the existing request limit while preserving post-level verification and retry behavior. The VPN speed test measured a different server and cannot isolate the Derpibooru media route or rate limits.

## Cross-post taxonomy batching

Strict Derpibooru acquisition now deduplicates uncached tag lookups across the current window and requests up to 50 distinct IDs per call. Every post still checks its own source identity, rating, taxonomy, aliases and blacklist before gallery-dl may request media. An omitted record uses the existing exact lookup. Contradictory evidence and exhausted retries withhold the affected posts.

The gallery-dl 1.32.19 loopback regression used the same 200 posts and simulated 10 ms of latency per taxonomy request. Cross-post batching reduced taxonomy calls from 200 to five. Time to the first media request was about 3.69 seconds for the per-post path and 1.39 seconds for the batched path in one run. Both paths produced 197 creates, one blacklist skip and two failures. The alias-blocked post, the post with unverified taxonomy, and the post whose tags changed between listing and download made zero media requests. The taxonomy parity test also verified zero requests with a warm cache. These timings are fixture results. Live Derpibooru and VPN timing still varies with rate limits and media transfer.

## Strict download timing

Meloader logs strict acquisition timing at `info` with a queue job ID on each stage. Times are wall-clock measurements. A window that rejects every post stops after `strict-preflight`.

| Stage | What the log measures |
| --- | --- |
| `resolve` | Initial source listing. The count can include the one extra record used to detect another page. |
| `strict-preflight` | Rule loading, source evidence and taxonomy verification, and blacklist decisions across the capped window. `approved` counts posts eligible for the transfer pass. The transfer can still skip archived posts and withhold posts whose evidence changed. |
| `strict-transfer` | Manifest generation, pinned gallery-dl version check, filter probes, the gallery-dl download run, and final evidence comparison. `download_process` includes re-listing, pacing, retries, transfer, and subprocess output handling. `first_file` is elapsed from the start of that run to its first completed file with a readable sidecar. It is `none` for an archive-only window. |
| `strict-ingest` | Processing the download results, including local metadata handling, mapping, and Mebooru uploads. `transfer_rows` includes archive skips reported by gallery-dl. |
| `total` | Worker time for the complete job, including stages outside the strict path measurements. |

Transfer `file_bytes` counts the final sizes of newly landed, evidence-verified files, excluding archive skips. A nonzero `size_errors` makes that count partial. The configured `sleep` and `sleep_request` values are in seconds. `extractor_default` means the managed config leaves that value to gallery-dl. The measurements do not isolate HTTP transfer time, the first media request, or time spent asleep. Keep the VPN and pacing settings consistent with the earlier live run, and compare the stage durations alongside the number of new files and their total bytes.

**Browser — Mebooru.** Use the previous safe Derpibooru job in `Audit_A` and click **Get next 200** once. Keep strict blacklist verification enabled. The current search can shift as posts are added, so record any archive skips. Collect the completed queue counts and the timing lines for the new job ID. A continuation measures a new window under the current cache conditions.

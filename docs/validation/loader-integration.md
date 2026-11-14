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

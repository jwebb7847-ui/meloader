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

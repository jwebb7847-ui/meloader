# Mebooru and Meloader project handoff

Status reviewed 2026-11-14.

## Start here

| Document | Use it for |
| --- | --- |
| [ROADMAP.md](../../ROADMAP.md) | Milestones, current baseline, next work, and acceptance criteria |
| [Local audit runbook](../validation/local-audit.md) | Mac and VS Code setup, exact launch commands, smoke test, and troubleshooting |
| [Source semantics contract](../architecture/source-semantics-proposal.md#product-decisions) | Approved rating, taxonomy, provenance, and filing behavior |
| [Rating integrity](../validation/rating-integrity.md) and [negative-path and throughput evidence](../validation/negative-path-throughput.md) | Tests, reproducible cases, bounded live results, and benchmark limits |
| [e621 validation](../validation/e621.md) and [blacklist timing](../validation/blacklist-timing.md) | Generic mapper acceptance, approved lore/contributor mappings, and strict blacklist acceptance |
| [MEBOORU.md on mebooru-foundation](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/MEBOORU.md) | Library direction, branch baseline, and upstream compatibility |

## Repository state

- [Meloader](https://github.com/jwebb7847-ui/meloader) develops from `main`. [PR #5](https://github.com/jwebb7847-ui/meloader/pull/5) merged Item 3 at `3aa5aca`. Live e621 and Danbooru acceptance are complete. [PR #6](https://github.com/jwebb7847-ui/meloader/pull/6) merged the first Item 4 fixes at `18edb38`. [PR #7](https://github.com/jwebb7847-ui/meloader/pull/7) merged strict blacklist verification at `5bcd092`. The Mac check used `025a3e5` for acquisition and `5bcd092` for the debug dialog's strict-mode behavior. The Trace URL prefill passed the Mac check at `eb6edd3` and merged in [PR #8](https://github.com/jwebb7847-ui/meloader/pull/8) at `b0a0213`.
- [Mebooru](https://github.com/jwebb7847-ui/mebooru) has its gallery-switching and branding work on `mebooru-foundation` (last code checkpoint `fbb5232`). Its `main` remains at the upstream-derived v1.24.0 baseline (`0ac84b9`). Fetch the correct fork and inspect the current branch head before making changes in either repo.
- The runbook describes the last observed MacBook Air audit setup. Check the local commit, listeners, and explicit config paths before relying on a running instance. GitHub changes do not restart local apps.

## Intended end state

Mebooru is a local-first media library with independent galleries, fast browsing and switching, durable search and tags, source attribution and relationships, and optional on-device AI enrichment. Meloader is the explicitly WAN-capable companion. It uses gallery-dl, optional source-specific enrichment, and one generic download, mapping, and upload pipeline. The preferred integration is a selectable acquisition tab in Mebooru backed by a separate Meloader process. Keep Mebooru's library core free of new outbound dependencies and design future remote clients around APIs. Keep upstream internal names and config/API compatibility until there is a concrete migration need.

Source taxonomy answers what a source says a tag means. Cross-source synonyms and local AI classifications are separate library concerns. Preserve source identity, category evidence, and source rating, with user profiles controlling approved filing choices. Required invalid ratings or taxonomy failures need visible diagnostics. Never flatten unresolved required categories as a shortcut to performance.

## Working with the operator

- Label every instruction block with its interface, for example **VS Code — Meloader terminal.**, **VS Code — Mebooru terminal.**, **Browser — Meloader.**, or **Browser — Mebooru.** Name the correct checkout and say when to leave a server running in its terminal.
- Provide paste-ready commands with real paths from the runbook, prerequisites, and expected output. Check listeners and identify the active executable when process state is uncertain. For browser steps, name the setting or action and its visible success condition.
- The collaborator can inspect GitHub and run hermetic checks in its own environment. The operator runs Mac-specific and browser checks and reports logs or screenshots. Record both code SHAs, the relevant tool versions and audit setup, and whether evidence is automated, live, or operator-reported.
- Discuss changes to product behavior, source trust, privacy, or visible workflow before implementation. Proceed with in-scope reliability work and verification. Prefer targeted checks and reuse accepted evidence.
- Keep test imports in `Audit_A`. Keep tokens, account/session material, real gallery contents, and raw private config out of GitHub and shared logs.

## Resume here

Resume [Item 4](../../ROADMAP.md#4-further-booru-coverage--next) with the remaining live Derpibooru alias check. Named original character and unusual namespace filing are approved. [Filing and alias evidence](../validation/derpibooru-filing.md) records the compatibility checks and the requirement that confirmed aliases cannot evade the strict blacklist. The trust and fallback fixes are merged. The [opt-in pre-download blacklist](../validation/blacklist-timing.md) passed the Mac audit. The [architecture defaults](../architecture/source-semantics-proposal.md#product-decisions) are approved. Item 3 is complete, including [live Danbooru acceptance](../validation/negative-path-throughput.md#8-live-danbooru-acceptance). Larger end-to-end benchmarks remain deferred. Acquisition-tab design follows under Item 5.

# Meloader / Mebooru project roadmap

> Working architecture and development plan, maintained for this fork.
> Last reviewed: 2026-11-13. This document is a plan, **not** a declaration that pending implementation is already tested or merged.

## North star

Meloader retrieves images **with the correct meaning of their source metadata**, using gallery-dl for broad source compatibility and small optional source-specific adapters when more precision is required. Mebooru maintains a coherent local library with trustworthy source provenance, browsing, search, tags, and relationships.

Keep the network boundary intact: Meloader is the explicitly WAN-capable acquisition companion; the Mebooru library core does not gain new outbound dependencies. They can eventually feel like one application through an integrated acquisition interface while remaining separate services at first.

**Important distinction:** Recovering *source taxonomy* (e.g. whether a tag is a character) is not the same as reconciling *cross-source vocabulary* (e.g. synonyms such as `flight` and `flying`). The latter is a separate library-level problem. Do not silently conflate the two.

## Architecture invariants

1. One generic gallery-dl / queue / download / upload pipeline. Do not reimplement it separately in each adapter.
2. Source profiles remain the broad compatibility fallback. An unrecognized source works exactly as before when it has no adapter.
3. Adapters explain source semantics; generic rules, blacklist policy, destination selection, file handling, deduplication, and Mebooru communication stay outside adapters.
4. A source with trustworthy, fully categorized metadata must not incur network enrichment merely because an adapter is registered.
5. Preserve original source identity, attribution, and Mebooru tag/source provenance. Do not turn known-invalid metadata into silently accepted general tags.
6. Keep user overrides meaningful; discuss precedence and error/fallback behavior before changing their semantics.
7. Prefer bounded concurrency, batched requests, shared persistent caching, and explicit failure paths to unbounded per-post work.
8. Discuss changes to behavior, trust, privacy, or user-facing concepts before implementation. Reliability, testing, and nonbehavioral internal refactors may proceed independently.
9. Require tests and meaningful bulk-workload measurements before claiming a generalized implementation preserves speed.

## Current work and branches

- `main`: tracks upstream-derived Meloader; **not** yet the home of the source-taxonomy feature work.
- `mebooru-derpi-taxonomy`: earlier Derpibooru taxonomy implementation.
- `meloader-taxonomy-speed`: hardened Derpibooru enrichment, caching and performance work.
- `meloader-source-adapter-registry`: source-keyed taxonomy dispatch and registry tests; intended baseline for item 3.
- `meloader-item3-optional-capabilities`: item 3A changes in [draft PR #1](https://github.com/jwebb7847-ui/meloader/pull/1). These changes are **not merged**. The initial Go race-test and vet workflow passed on 2026-11-13; integration and performance acceptance remain outstanding.
- Design proposal branch: `meloader-item3-semantic-contract-proposal` (stacked on e621 validation); a documented contract for review, not a runtime policy implementation.
- Current rating-integrity implementation: `meloader-item3-rating-integrity` ([draft PR #4](https://github.com/jwebb7847-ui/meloader/pull/4)); approved narrow policy change, covered by Go tests and local mock-HTTP upload integration; running-Mebooru validation still pending.

The initial taxonomy registry intentionally lives in `internal/taxonomy`. Do not move the namespace prematurely; it should only be promoted when the final source-semantics contract warrants it.

## Milestones

### 1. Derpibooru taxonomy foundation — implemented on feature branches

Recover authoritative category information when gallery-dl emits flattened tags. Maintain a persistent cache, handle network/metadata failures, support lookups/warming, and preserve the generic mapper. The long-tail taxonomy-semantic audit is deliberately deferred.

### 2. Validate source taxonomy and optimization — largely implemented on feature branches

Cache persistence, batch lookup, in-flight deduplication, retry/error handling, and focused tests were developed. Revalidate before merging to `main`.

### 3. Generalized source adapters — **current priority**

- [x] Initial source registry and dispatch in `meloader-source-adapter-registry`.
- [x] Draft optional enrichment/warming interfaces and tests in PR #1 (not merged; CI result pending).
- [x] Establish a working automated Go test and race-check gate and verify item 3A ([passing run](https://github.com/jwebb7847-ui/meloader/actions/runs/43335802201)); later implementation commits still require their own checks.
- [ ] Review [item 3B source-semantics contract](docs/architecture/source-semantics-proposal.md). **Rating integrity was approved 2026-11-13** and is implemented in the `meloader-item3-rating-integrity` branch pending CI/merge. Other source authority, fallback, category fidelity and provenance policies remain under review.
- Local Apple M5 / macOS Go acceptance rerun (2026-11-13, Meloader commit `844b6b2`) passed mapping validation, source-rating error propagation, trace diagnostics, HTTP upload boundary and CBZ rejection under `go test -race`. For live e621 extraction, standalone gallery-dl **1.32.19** was installed in an isolated Python venv after no managed executable was found. The first `--dump-json` output parsed as JSON but contained **no e621 post metadata**; the reason is still undetermined. The [validation guide](docs/validation/rating-integrity.md) now inspects gallery-dl's embedded `-1` error-message records instead of assuming shell exit zero implies success. **Do not mark live extraction accepted yet.**
- [ ] Complete **live** integration acceptance for approved source-rating integrity in `meloader-item3-rating-integrity`. Code, validator and actual **pipeline -> local mock HTTP upload** tests have passed [Go race/vet CI](https://github.com/jwebb7847-ui/meloader/actions/runs/43346314936) (commit `16d4908`). They show valid e621 and generic unrated images send uploads; invalid/missing e621 ratings and invalid ratings in a multi-page bundle never reach upload. [Repeatable local acceptance guide](docs/validation/rating-integrity.md). **Actual gallery-dl source metadata and running-Mebooru verification still pending before merge.** A future Mebooru unrated filter / local AI rating audit is not implemented.
- [ ] Extend only source-specific capabilities actually needed: taxonomy, rating interpretation, canonical identity/URL, integrity validation, optional enrichment and declared capabilities.
- [ ] Preserve profile/user mapping behavior and the existing Mebooru metadata contract.
- [ ] Validate a second source with different metadata semantics (e621). Offline fixture tests passed on [CI run 43337943681](https://github.com/jwebb7847-ui/meloader/actions/runs/43337943681); opt-in live extractor confirmation remains open; local synthetic processing benchmark was run on Apple M5 (results below). See [e621 validation protocol](docs/validation/e621.md).
  - **e621 selected:** gallery-dl 1.32.0+ emits authoritative `tags_artist`, `tags_character`, `tags_species` and other categorized fields alongside a flattened `tags` list. The existing profile/mapper should consume the categorized data locally; **do not add a dedicated e621 taxonomy adapter unless testing reveals a specific need**.
  - In-progress validation branch: `meloader-item3-e621-validation` (synthetic extractor-shaped fixture; local category/identity/rating/blacklist/override tests; local-only throughput benchmark). The offline Go race tests and vet passed; a local synthetic benchmark has been recorded; live extractor confirmation and end-to-end acquisition tests remain outstanding.
  - **Semantic audit flag:** the current generic category map folds e621 `lore` into `meta` and `contributor` into `artist`. These are existing behaviors, not newly approved equivalences. Discuss whether meaning is lost before changing them.
- [x] Record local adapter-free e621 mapping benchmark on Apple M5: median of three single-pass runs was **1.66 ms for 200 records**, **6.20 ms for 1,000**, and **34.94 ms for 10,000**. Reused one synthetic fixture and performed no network, file I/O or Mebooru pushes; not a measurement of full acquisition speed. See [protocol/results](docs/validation/e621.md).
- [ ] Measure cold/warm 200-, 1,000-, and 10,000-image **end-to-end acquisition** workflows, including time to first import, throughput, extra HTTP requests, peak RAM/goroutines and failure rates.
- [ ] Review potential bulk-path hotspots: concurrent cache warmers, cross-post request batching, repetitive cache snapshots, and sequential post-import blocking. **Optimize from measurements, not assumptions.**
- [ ] Merge only after tests, performance acceptance, and any relevant conceptual approvals.

### 4. Further booru coverage

Add narrowly scoped adapters for sources that require specialized semantics. Preserve generic support for other gallery-dl sources. Revisit unproven Derpibooru taxonomy edge cases.

### 5. Meloader / Mebooru user integration

Make source acquisition feel native within Mebooru, potentially as a top-level tab, while initially keeping Meloader a separate explicitly WAN-capable service and retaining Mebooru's library privacy boundary.

### 6. Reliability and observability

Improve acquisition diagnostics, retry visibility, structured timing, failure distinctions and regression monitoring without sacrificing snappiness.

### 7. Broader Mebooru library experience

Develop browsing, review, organization, relationships and cross-source vocabulary consistency as distinct capabilities beyond source-taxonomy interpretation.

### 8. Naming and packaging cleanup — deferred

Complete invasive Monloader/Monbooru naming migration only when architecture and compatibility are stable.

## Local VS Code testing protocol

- **Default test launches are source-based:** `go run ./cmd/monloader -config "<existing monloader.toml path>"` for Meloader and `go run ./cmd/monbooru -config "<existing monbooru.toml path>"` for Mebooru, from each repository's intended checkout. Avoid launching old compiled executables for new validation unless comparing builds deliberately.
- `go run` builds the **local checkout**, not the latest GitHub commit automatically. Before every important test: `git fetch origin --prune`, confirm `git status --short` is clean, explicitly select or verify the intended remote development ref, and log `git rev-parse HEAD`. Do not force-switch, reset or clean a dirty tree.
- **Current source refs for rating acceptance as of 2026-11-13:** Meloader `meloader-item3-rating-integrity`; Mebooru `mebooru-foundation`. For earlier e621-only comparisons, use `meloader-item3-e621-validation`. These refs may advance; record exact commit SHAs for reproducible results.
- Existing test/audit configuration and application data live outside Git. Preserve their explicit paths unless a separate controlled reset is agreed. In particular, a previously used Meloader config was at `~/Library/Application Support/Monloader Audit/monloader.toml`; verify it exists before launch. Git synchronization does not update running processes or external config/data.
- Run foreground instances in separate VS Code terminals and stop them with Ctrl+C. If a port is occupied, inspect the listener and stop the *known* previous test process; do not indiscriminately kill everything on port 8455 or 8456.
- Include the source commit, launch command, config-path identity (never credentials), tool versions (including gallery-dl where relevant), and observed test results in meaningful validation records.
- End-to-end integration testing requires both applications running on known compatible revisions; standalone mapper/registry tests and Go benchmarks do not require either server to be running.

## Merge/acceptance gates

- Run `go test -race ./...` and `go vet ./...` on supported Go version. Live integration tests are separately opt-in.
- Verify unknown-source fallback, source provenance, ratings, user overrides, blacklist behavior and consistent errors.
- Ensure source metadata is never silently flattened or reclassified due to failure of a specialized adapter.
- Compare the existing source-adapter baseline and the candidate under representative warm-cache workloads; reject a meaningful user-perceived speed regression.
- For cold-cache workloads, measure requests, waiting, error handling, throughput and memory before making promises.
- Keep the proposed behavior-changing semantics in review until discussed with the product owner.

## Known open decisions

- How to represent an adapter's answer at the field level (resolved / absent / invalid) and distinguish authoritative uncertainty from a broken payload.
- Exactly how authoritative source interpretations, profile fallbacks, user overrides and generic rules compose without changing existing trust or preferences.
- Where to draw the contract boundary between `internal/taxonomy` and a future more general source-adapter package.
- e621 is the selected second-source contrast. Confirm its direct categorized path and profile fallback before considering any new adapter; preserve unresolved lore/contributor category semantics for explicit discussion.

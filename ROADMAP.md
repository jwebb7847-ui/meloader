# Meloader / Mebooru project roadmap

> Working architecture and development plan, maintained for this fork.
> Last reviewed 2026-11-13. [Project handoff](docs/project/handoff.md) · [Local audit runbook](docs/validation/local-audit.md)

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

- **Meloader `main`** contains Items 1–3 and the first Item 4 fixes. [PR #5](https://github.com/jwebb7847-ui/meloader/pull/5) merged the stacked taxonomy and source-semantics work at `3aa5aca`. [PR #6](https://github.com/jwebb7847-ui/meloader/pull/6) merged the Derpibooru integrity fixes at `18edb38` after Mac validation. PRs #1–#4 record the earlier stacked reviews.
- **Mebooru `mebooru-foundation`** contains the fork's library UI work. Its `main` remains the upstream-derived v1.24.0 baseline. See [MEBOORU.md](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/MEBOORU.md) for the library's direction and baseline.
- **Next milestone: Item 4.** Resolve the remaining taxonomy and source-adapter decisions.

## Milestones

### 1. Derpibooru taxonomy foundation — merged

Recover authoritative categories when gallery-dl emits flattened tags. Use the shared registry in `internal/taxonomy`, a persistent category cache, and the generic mapper. Revisit long-tail taxonomy semantics in Item 4.

### 2. Validate source taxonomy and optimization — bounded acceptance passed

- A 20-post Derpibooru taxonomy workload took **22.99 s cold**, with 17 HTTP attempts including two rate-limit responses. Three warm reloads reproduced the same 442 mapped tag assignments, made **zero taxonomy HTTP attempts**, and took **1.38 ms median** for enrichment and mapping. This measures resolver and mapper work. [Cold/warm audit](docs/validation/negative-path-throughput.md#6-live-derpibooru-taxonomy-cache-audit)
- The bounded Safebooru continuation created 20 images in about **35 s**. The prior window created 15 and skipped five. [Live acquisition results](docs/validation/negative-path-throughput.md#bounded-20-post-safebooru-run-and-continuation--passed)
- After merging, a copied-state Derpibooru audit created five images, preserved ratings, categories and source links, restored queue history after restart, reused the taxonomy cache, and skipped all five on replay through the download archive. [Upgrade smoke test](docs/validation/negative-path-throughput.md#7-post-merge-derpibooru-upgrade-smoke-test)
- Larger end-to-end tests at 200, 1,000 and 10,000 images remain deferred. Measure time to first import, throughput, added HTTP requests, peak memory/goroutines and failures before making bulk performance claims. Use the separate synthetic results only as local processing baselines.

### 3. Generalized source adapters — complete

- Optional enrichment and warming preserve generic profile support. Keep the registry in `internal/taxonomy` until a broader contract requires a move.
- Approved rating and taxonomy semantics preserve source authority and valid user filing overrides. Invalid required metadata fails visibly. Relevant download, refresh, lookup, replacement and bundle paths enforce the policy. Known-source ratings use the shipped family rules; a blank Philomena rating field can use the source rating tag. Bundles check each page after enrichment and before archive creation. [Approved contract](docs/architecture/source-semantics-proposal.md#product-decisions) · [Rating and malformed-input tests](docs/validation/rating-integrity.md)
- e621 categorized extraction, preview and one live import passed with the generic mapper. Retain **`lore → meta` and `contributor → artist`**. [e621 validation and decision](docs/validation/e621.md)
- Danbooru automated coverage and authenticated live acceptance passed. Post `294929` mapped `s` to `rating:sensitive` and imported into `Audit_A` with categorized tags and original attribution. [Live Danbooru result](docs/validation/negative-path-throughput.md#8-live-danbooru-acceptance)

### 4. Further booru coverage — next

[PR #6](https://github.com/jwebb7847-ui/meloader/pull/6) shipped the approved trust and fallback fixes for malformed taxonomy, tag ID verification, and canonical post provenance. The Mac audit confirmed a live import and cache reuse after restart. The [opt-in pre-download blacklist](docs/validation/blacklist-timing.md) has loopback media-request coverage and awaits Mac validation. Remaining filing decisions cover verified aliases, unusual namespaces, and named original characters. Use targeted fixtures or metadata-only probes, and check request counts and cache behavior for regressions.

### 5. Meloader / Mebooru user integration

Make source acquisition feel native within Mebooru, potentially as a top-level tab, while initially keeping Meloader a separate explicitly WAN-capable service and retaining Mebooru's library privacy boundary.

### 6. Reliability and observability

Improve acquisition diagnostics, retry visibility, structured timing, failure distinctions and regression monitoring without sacrificing snappiness.

### 7. Broader Mebooru library experience

Develop browsing, review, organization, relationships and cross-source vocabulary consistency as distinct capabilities beyond source-taxonomy interpretation.

### 8. Naming and packaging cleanup — deferred

Complete invasive Monloader/Monbooru naming migration only when architecture and compatibility are stable.

## Local VS Code testing protocol

Follow the [local audit runbook](docs/validation/local-audit.md) for checkout, config, launch, restart, and browser instructions. Historical evidence belongs in the linked validation notes.

## Merge/acceptance gates

- Run `go test -race ./...` and `go vet ./...` on supported Go version. Live integration tests are separately opt-in.
- Verify unknown-source fallback, source provenance, ratings, user overrides, blacklist behavior and consistent errors.
- Ensure source metadata is never silently flattened or reclassified due to failure of a specialized adapter.
- Compare the existing source-adapter baseline and the candidate under representative warm-cache workloads; reject a meaningful user-perceived speed regression.
- For cold-cache workloads, measure requests, waiting, error handling, throughput and memory before making promises.
- Keep the proposed behavior-changing semantics in review until discussed with the product owner.

## Known open decisions

The [approved architecture defaults](docs/architecture/source-semantics-proposal.md#product-decisions) cover source field outcomes, provenance retention, taxonomy and vocabulary ownership, and the adapter package boundary.

- Decide individual filing rules for aliases, original characters, and unusual namespaces from targeted source evidence.
- Define richer diagnostic fields and retention when a concrete audit or replay use case requires them.

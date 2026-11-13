# Item 3B — Source semantics contract (proposal)

**Status: PARTIALLY APPROVED.** The invalid-rating versus genuinely unrated distinction was approved on 2026-11-13 and is being implemented on a separate feature branch. Other questions in this document remain proposals; do not consider them approved merely because they appear here.

**Reviewed against:** Meloader `meloader-item3-e621-validation` at `67685c8`, including `internal/taxonomy/registry.go`, `internal/taxonomy/derpibooru.go`, `internal/mapping/map.go`, and `internal/pipeline/processor.go`; source-side expectations validated with pinned gallery-dl v1.32.19. See [ROADMAP.md](../../ROADMAP.md).

## North star

Make metadata reliable *without* making the downloader source-specific. Meloader acquires a source's information and correctly interprets it. Mebooru retains a coherent library and provenance. Synonyms and conceptual equivalence across sources are a separate, later vocabulary concern.

Each source only supplies the specialized interpretation it can actually justify. **An ordinary source must not need an adapter**. Keep the gallery-dl, queue, download, blacklist, mapping, and Mebooru push pipeline generic.

## Separating facts from policy

The proposed logical sequence is:

1. **Acquire raw source metadata** via gallery-dl.
2. **Interpret source-specific facts, when necessary** (e.g. Derpibooru's authoritative character category). A registered adapter may augment or correct extraction *semantics*, not apply user preferences.
3. **Apply profile-defined site conventions and generic transformations** for fields the adapter did not authoritatively resolve (e.g. the source's `s` rating convention).
4. **Apply user-controlled overrides, tag rules, and blacklist/drop preferences** at their established points in the current pipeline. Preserve the distinction between source facts and the user's chosen final organization.
5. **Validate and export** the final source identity, normalized tags, rating, commentary, URLs, and other available metadata using the existing Mebooru contract.

This is a **conceptual separation**, not a proposal to reorder existing production code indiscriminately. In today's pipeline, blacklist filtering already occurs before downloads where possible and again after enrichment; do not remove that two-pass behavior. Some decisions (such as flattened-category validation) already have a specific position and must be preserved until tests justify changes.

## Source claims: sparse, not heavyweight

A source adapter should report only fields it is qualified to interpret. An adapter with no claim for `rating` must not cause a new rating conversion or network lookup.

The useful **conceptual** outcomes for each claimed field are:

| Outcome | Meaning | Proposed treatment |
| --- | --- | --- |
| Not claimed | This adapter has no special knowledge of this field | Continue through existing profile/generic handling |
| Resolved | The source's meaning is known and checks out | Use the interpretation, then apply user policy; record source evidence where appropriate |
| Unavailable | The source genuinely omitted or does not supply a field | Use only an explicitly permitted profile fallback; otherwise leave unknown, not invented |
| Invalid / conflicting | An expected field is malformed, contradictory, or fails integrity checks | Surface a diagnostic; **do not** silently turn categorized tags into general tags or invent a safe rating |
| Temporary lookup failure | The adapter cannot yet verify a field it requires (network timeout, throttle, etc.) | Keep existing explicit error/retry behavior for required authoritative enrichment; do not silently downgrade |

These are **design concepts**, not a mandate to construct a per-field heap object or an enormous universal schema for every post. Implement as narrowly as the measured requirements justify. Diagnostic details should identify the *source* and *field*, not leak credentials.

## Concrete contrasting cases

### e621 — no adapter

Pinned gallery-dl 1.32.19 already emits `tags_artist`, `tags_character`, `tags_species`, `tags_general`, etc., plus a redundant flat `tags` list. The current generic mapper consumes the categorized fields, and the current `e621` profile interprets `s` as safe/general. Keep all of this local. The source adapter registry has **no e621 adapter**.

Acceptance: no extra taxonomy HTTP calls, no double-counting flat tags, correct source/post identity and ratings, existing user override precedence, and a refusal when a categorizing source unexpectedly supplies *only* flat tags. No new e621 adapter just to fill a design slot.

**Existing semantic audit flag:** `lore -> meta` and `contributor -> artist` are generic mappings inherited from the current code. They may be lossy. Preserve them until a separately reviewed taxonomy choice is made; do not falsely claim the conversion is the source's literal category.

### Derpibooru — adapter recovers missing semantics

The current source adapter retrieves Derpibooru's authoritative taxonomy for flat tags, caches results, and rewrites the map's working `tags` only after all tags have resolved. The generic mapper then applies normal category/tag/rating mapping. If the required taxonomy cannot be verified, current behavior fails that item instead of silently sending mislabeled general tags.

Acceptance: preserve known taxonomy quality, batch/cache handling, no successful partial rewrite on a failed post, the Philomena rating path, blacklist rules and Mebooru provenance. Do not replace the existing implementation until parity tests exist.

### Unrecognized / simple gallery-dl source — generic profile

No adapter required. No new network request. Use existing generic profile conventions and valid post-link metadata. An unsupported semantic field is **not the same as invalid metadata**.

## Identity and provenance

Keep source/site identity, source-post ID, canonical post URL, parent references, and original attribution separate. A URL is a human-facing reference, not an automatic substitute for a verified per-site post ID. Never use an entire search/listing URL as a fabricated canonical post identity.

Mebooru currently records source membership and tag-origin provenance, but this does **not** mean it persists every upstream raw metadata value. Decide raw metadata archival separately if later audits need it. Do not silently overwrite source-evidence records with user-facing vocabulary decisions.

## Ratings need a safety decision

Today, a malformed or unknown rating can map to the empty string; the code comments note that an unrated item is visible under every Mebooru rating ceiling. This is an existing policy behavior, **not** an endorsement of treating an invalid rating as safe.

**Approved decision (2026-11-13):** A known rating-bearing booru family must provide a valid and nonconflicting rating or the item is refused before automatic import. Generic and unprofiled sources are allowed to remain unrated. Current validation accepts known family rating syntax and built-in profile rating overrides, then applies user overrides to the final rating; a user override does not retroactively make a malformed source claim authoritative. For Philomena, contradictory `safe`/`suggestive`/`questionable`/`explicit` rating tags or conflict between a rating field and tags are refused. Profile defaults can justify a truly missing rating, but not an explicitly malformed one. Use existing `mapping_failed` reporting; this is not an automatic Mebooru holding-folder implementation. A future local AI audit of genuinely unrated library entries remains a separate feature.

## Performance constraints

- Adapter lookup is local and cheap. No extra HTTP request merely because an adapter exists.
- Avoid allocating per-field result objects for ordinary pass-through sources.
- Remote lookups are opt-in, cached and batched where supported; preserve API rate limits and avoid duplicate in-flight lookups.
- Warm and cold paths need separate measurement, especially on 200/1,000/10,000-image acquisitions.
- **Observed candidate for later measurement:** `Processor.warmTaxonomy` currently creates a short-lived goroutine for each eligible resolved item whenever its taxonomy service implements `Warmer`. The registry itself implements `Warmer`, even for unregistered sources such as e621, so no-op warm goroutines can occur. This makes **no extra taxonomy HTTP requests**, but is not literally zero work. Before changing it, quantify the cost and evaluate a cheap capability check.
- e621 Apple M5 local mapping microbenchmark (synthetic reused fixture): median 1.66 ms per 200 records, 6.20 ms per 1,000, 34.94 ms per 10,000. This is **not** a full download, memory-peak or import benchmark.

## Behavioral invariants and acceptance gates

1. Source categories and ratings retain their site-specific meanings.
2. User overrides and existing blacklist/drop behavior still work, including the pre-download blacklist and post-enrichment safety check.
3. No unknown source is blocked solely because it lacks an adapter.
4. Expected categorized sources do not silently flatten to generic tags on failure.
5. No silent new network access in the Mebooru library core. Meloader remains the network-capable companion.
6. Tests cover known-good, missing, invalid and transient-failure paths without inventing data.
7. Comparing against the current branch proves no material warm-path regression; a future controlled source-to-library import is required before making end-to-end speed claims.

## Product decisions awaiting approval

1. **Source authority vs user control:** adapters establish what a tag means *at the source*; users can still reclassify or suppress what is saved in Mebooru, but cannot retroactively change the reported source fact. Recommend **yes**.
2. **Invalid required taxonomy:** when a categorizing site unexpectedly supplies a flat-only payload or required authoritative lookup fails, prefer a visible failed/retryable item over silently misclassifying every tag. This matches the current Derpibooru/e621 guard. Recommend **retain**.
3. **Invalid required rating — APPROVED 2026-11-13:** A source from a known rating-bearing booru family with missing, malformed, or conflicting rating evidence must fail visibly before its metadata/file is automatically pushed. A genuinely unrated generic source may remain unrated. This is approved separately from any Mebooru default filter or future local AI classification workflow. Implementation is in `meloader-item3-rating-integrity` (pending CI and review), with no change to the Mebooru contract.
4. **Category fidelity:** should `lore` and `contributor` (and other native categories) have distinct Mebooru categories when available, or remain mapped to today's broader `meta`/`artist`? **Needs a category-model discussion**, not a rushed source-adapter patch.
5. **Raw metadata retention:** retain enough raw evidence for debugging versus persist full upstream payloads. **Defer** until storage/privacy/operational costs are defined.

Once these decisions are made, implement the narrowest possible interface and test only genuine gaps. An adapter abstraction is successful when most sources need **no adapter at all**.

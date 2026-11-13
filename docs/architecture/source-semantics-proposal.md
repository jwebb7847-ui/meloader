# Item 3B — Source semantics contract (proposal)

**Status (2026-11-13).** The approved Item 3 behavior shipped in [PR #5](https://github.com/jwebb7847-ui/meloader/pull/5) on `main`. The approved scope covers rating integrity, source authority with user-controlled filing, and visible refusal when required taxonomy is unavailable or invalid. The [e621 filing decision](../validation/e621.md#live-lore-and-contributor-traces--2026-11-13) retains the current mapper rules. Category fidelity for other mappings and raw metadata retention remain open.

**Original proposal reviewed against:** Meloader `meloader-item3-e621-validation` at `67685c8`, including `internal/taxonomy/registry.go`, `internal/taxonomy/derpibooru.go`, `internal/mapping/map.go`, and `internal/pipeline/processor.go`; source-side expectations validated with pinned gallery-dl v1.32.19. See [ROADMAP.md](../../ROADMAP.md).

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

**Approved e621 filing policy:** retain `lore → meta` and `contributor → artist`. These are destination categories. The trace displays the native e621 categories.

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

**Approved decision (2026-11-13):** A known rating-bearing booru family must provide a valid and nonconflicting rating or the item is refused before automatic import. Generic and unprofiled sources are allowed to remain unrated. Current validation accepts known family rating syntax and built-in profile rating overrides, then applies user overrides to the final rating; a user override does not retroactively make a malformed source claim authoritative. For Philomena, contradictory `safe`/`suggestive`/`questionable`/`explicit` rating tags or conflict between a rating field and tags are refused. A shipped profile's default can cover a truly missing rating for a known source. Custom-only sources can declare their family and rating conventions in their own profile. Use existing `mapping_failed` reporting; this is not an automatic Mebooru holding-folder implementation. A future local AI audit of genuinely unrated library entries remains a separate feature.

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

## Product decisions

1. **Source authority and user control — APPROVED.** Source taxonomy determines how the source described a tag. User overrides and rules determine how Meloader sends it to Mebooru, including reclassification and suppression. The tag trace shows the source field and spelling alongside the mapped category and the tag sent. Derpibooru trace uses the same taxonomy adapter as acquisition. Source identity and attribution continue to travel with the mapped tags.
2. **Invalid or unavailable required taxonomy — APPROVED.** When an extractor expected to categorize tags supplies flat tags without usable category data, the item fails visibly with `mapping_failed`. Empty, unreadable and bookmark-only fields cannot satisfy this check. If the required Derpibooru lookup fails, the item fails before sending image or tag data. This applies to downloads, metadata refresh, online lookup, replacement and bundle pages. Generic flat-tag imports still use the existing mapper.
3. **Invalid required rating — APPROVED 2026-11-13:** A source from a known rating-bearing booru family with missing, malformed, or conflicting rating evidence must fail visibly before its metadata/file is automatically pushed. A genuinely unrated generic source may remain unrated. This is approved separately from any Mebooru default filter or future local AI classification workflow. Implementation is in `meloader-item3-rating-integrity`; [Go race tests and vet passed](https://github.com/jwebb7847-ui/meloader/actions/runs/43344451398), with live positive and malformed-rating checks completed against local Mebooru for reachable sources. No change to the Mebooru contract.
4. **e621 category fidelity — APPROVED:** keep `lore → meta` and `contributor → artist`. The library does not need a separate lore filter, and Artist includes creative contributors. Category fidelity for other mappings remains open.
5. **Raw metadata retention:** retain enough raw evidence for debugging versus persist full upstream payloads. **Defer** until storage/privacy/operational costs are defined.
6. **Derpibooru source integrity — APPROVED for Item 4.** Require a well-formed native tag list and verified tag records. An explicit null category represents an uncategorized tag. Refresh older cache entries that omitted this evidence. Match usable tag IDs to their listed names and keep exact-name lookup for tags whose ID evidence is absent or unusable. Refuse contradictory evidence with `mapping_failed`. Automatic post writes require the extractor's positive native post ID and use its canonical Derpibooru URL. Original attribution travels separately.

Once these decisions are made, implement the narrowest possible interface and test only genuine gaps. An adapter abstraction is successful when most sources need **no adapter at all**.

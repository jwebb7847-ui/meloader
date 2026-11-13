# Blacklist timing review

Reviewed on 2026-11-13 against Meloader `main` at `1eea1f8` and gallery-dl `1.32.19`. This is a policy review ahead of implementation.

## Product requirement

The operator wants a user-selected content blocklist that stops matching media before Meloader downloads it. Violence and other operator-defined unwanted content are independent of sexual rating. A nonviolent post can pass at any valid source rating. Blocking a violent depiction of a selected character takes priority over collecting every depiction of that character. Keep the rules configurable and preserve a lightweight, metadata-based workflow. Classification of the media itself is outside this pass.

## Current behavior and evidence

The existing global and per-site blacklist supports one rule per line, with all terms on a line required to match. Bare tags, category-qualified tags, negation, wildcards, and rating terms are available. The following used synthetic fixtures with no real source media or gallery writes.

| Probe | Observed result |
| --- | --- |
| Bare violence tag and a conjunction of two bare tags | Meloader skipped the item before calling the downloader. |
| Category-qualified character term paired with a violence tag on flat Derpibooru metadata | Meloader called the downloader, then resolved taxonomy and skipped the item before upload. |
| Negated character term with that character present in flat Derpibooru tags | The early check skipped the item before the category was known. |
| A rejected post shifted into an allowed position between listing and download | Meloader fetched the synthetic file and discarded it before upload. |
| Required Derpibooru taxonomy lookup failed | Meloader reported `mapping_failed` after the synthetic file was fetched and made no upload. |
| An explicitly rated control without the blocked tag | The violence-only rule allowed the mock upload. |

The Go probe exercised the existing acquisition pipeline with a fake downloader and mock gallery. A second probe used the actual pinned gallery-dl downloader and counted loopback media GETs. A position filter fetched the shifted post. An ID filter blocked the shifted post and fetched only the eligible ID. An ID filter alone still fetched an eligible ID whose tags had changed. A filter checking that ID and its original tag snapshot made zero media GETs when the tags changed. A malformed gallery-dl filter logged a warning and continued to fetch the files. These observations establish where a transfer gate could work and why an unverified filter string is insufficient.

The current code also handles deep dispatch and bundles differently from ordinary listings. Replacement has its own download path. A direct media URL may offer no usable source tags. A strict mode needs explicit behavior for each route it covers. Trace currently previews the enriched result and cannot establish whether the downloader would request media first.

## Options to decide

- **Keep the current early checks and fix category timing at the upload boundary.** This preserves throughput and compatibility but can fetch files a selected rule ultimately blocks.
- **Resolve applicable rules before authorizing transfer.** This meets the requirement for the supported, verified routes. Bare matches can be rejected immediately. Resolve categories only where they could change the verdict. If required evidence is missing, malformed, temporarily unavailable, or changes before transfer, skip or pause with a visible reason. The costs are extra waiting on cold taxonomy lookups and skipped items on sources with weak metadata.
- **Apply strict handling to every source immediately.** This offers one rule across all routes but would withhold direct links and other items whose metadata cannot establish a verdict. The route coverage needed for a reliable rollout is still open.

Recommendation for decision: make strict pre-download enforcement opt-in with the operator's selected rules. Start with ordinary booru posts and searches. Tie permission to a verified source/post identity and the relevant evidence used for the decision, or transfer the already checked candidate through a path that preserves that association. Keep the existing pre-upload check. Treat unsupported or unverifiable acquisition routes explicitly before presenting them as covered. Make Trace distinguish a pre-download block, an inability to verify, and an allowed transfer.

A future implementation should count actual media requests for blocked matches, changed metadata, shifted listings, and failed verification. Include nonviolent explicit and nonviolent character controls. Source tags determine what this can detect. Missing or inaccurate content tags can still let an unwanted post through when the supplied metadata appears valid.

[Current matcher](../../internal/mapping/blacklist.go) · [Acquisition pipeline](../../internal/pipeline/processor.go) · [gallery-dl file dispatch and predicates](https://github.com/mikf/gallery-dl/blob/v1.32.19/gallery_dl/job.py#L252)

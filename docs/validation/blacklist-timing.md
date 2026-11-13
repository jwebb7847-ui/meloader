# Blacklist timing

## Product requirement

The operator wants a user-selected content blocklist that stops matching media before Meloader downloads it. Violence and other operator-defined unwanted content are independent of sexual rating. A nonviolent post can pass at any valid source rating. Blocking a violent depiction of a selected character takes priority over collecting every depiction of that character. Keep the rules configurable and preserve a lightweight, metadata-based workflow. Classification of the media itself is outside this pass.

## Approved policy

**Settings → Rules → Require blacklist verification before downloading** is off by default. It uses the existing global and per-site rules. The initial scope is ordinary Derpibooru, Danbooru, and e621 posts and searches with gallery-dl `1.32.19`. Direct links, pools, extractor handoffs, and replacement downloads are withheld while this mode is active. Mebooru's library gains no outbound calls.

Meloader obtains the source metadata first. A positive match in raw tags can stop a post immediately. Otherwise Meloader verifies the native tag list and rating and, for Derpibooru, obtains authoritative taxonomy. A known blacklist match is a skip. Missing, malformed, or unavailable evidence needed to clear a post produces a visible failure. Meloader binds approval to the verified source, post ID, media URL, tags, and rating. gallery-dl rechecks them before requesting the media, and Meloader checks the download result before upload. Trace reports whether the metadata passed, matched a rule, or could not be verified.

This uses source-supplied metadata. A valid-looking post with an omitted or inaccurate violence tag can still pass. Rules can cover known tag variants and combine tags using the existing syntax. Inspecting the image locally would require receiving its bytes first. Image classification is outside this scope.

## Baseline evidence

Reviewed on 2026-11-13 against Meloader `main` at `1eea1f8` and gallery-dl `1.32.19`.

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

## Strict mode verification

The opt-in test uses gallery-dl `1.32.19` with synthetic post metadata and a loopback media server. It counts actual media requests and writes only to a mock `Audit_A` gallery. The three nonviolent explicit controls transfer. The category-qualified matches, a taxonomy failure, a new post that shifted into the search, changed tags, a changed media URL, and unsupported direct links make zero media requests. Archive replay adds no media request. A separate test confirms Meloader's preflight catches a malformed gallery-dl predicate before the download pass.

Run the opt-in checks with `MELOADER_STRICT_TEST_GDL` set to the pinned gallery-dl virtualenv executable and run `go test ./internal/gdl ./internal/pipeline -run TestStrict -v`. Live Mac validation is pending.

[Current matcher](../../internal/mapping/blacklist.go) · [Acquisition pipeline](../../internal/pipeline/processor.go) · [gallery-dl file dispatch and predicates](https://github.com/mikf/gallery-dl/blob/v1.32.19/gallery_dl/job.py#L252)

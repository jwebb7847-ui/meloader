# Derpibooru filing and alias evidence

## Approved filing

| Source evidence | Outgoing form |
| --- | --- |
| Named OC `oc:milky way` | `character:oc:milky_way` |
| Separate Character tag `milky way` | `character:milky_way` |
| Generic OC markers `oc` and `oc only` | `meta:oc` and `meta:oc_only` |
| Fan-made `fanfic:the bridge` or a `series:` title | Meta with the original prefix in the name |
| Origin credit `colorist:ironhades` | `meta:colorist:ironhades` |
| Verified uncategorized `parent:rainbow dash` | General `parent:rainbow_dash` |

The mapper keeps the verified category separate from the tag's literal spelling. Trace shows the original source spelling. The regression fixture checks standard blacklist evaluation, strict blacklist evaluation, and Trace with the previous named OC form, the new form, a wildcard, and a negated rule. The relationship tag does not satisfy a Character rule. The fixture also verifies one cold batched taxonomy request and zero additional requests after a cache reload. The code applies to future acquisitions and refreshes. Existing library entries are not migrated in bulk.

## Mac Trace acceptance

The operator tested [PR #10](https://github.com/jwebb7847-ui/meloader/pull/10) at `b8c92ca` with the paired `Audit_A` setup and strict blacklist verification enabled.

| Check | Observed result |
| --- | --- |
| Search for `safe,oc:milky way,-milky way` | Post `3929135` passed strict verification. Trace showed `oc:milky way` as the source spelling, `character:oc:milky_way` as the outgoing tag, and both the new and previous spellings under Blacklist. |
| Search for `safe,fanfic:the bridge` | Post `3686031` passed strict verification. Trace showed `meta:fanfic:the_bridge` as the outgoing tag. |
| Repeat the first search with the temporary `character:milky_way` rule | Trace reported **Blocked before download** and named the legacy rule. |

These checks exercised live metadata, the strict verdict and the gallery preview. They used Trace without an import.

## Live alias evidence

The July 2024 tag snapshot lists `dashie`, `rd`, and `rainbowdash` as aliases of `rainbow_dash`. Philomena exposes the alias target slug as `aliased_tag` and incoming alias slugs as `aliases`. Its tag-write code resolves aliases to their canonical record. [Historical tag snapshot](https://github.com/Siberpone/derpi-tac-a1111/blob/d7ed49593f035546a5da2d8bdb7ae72d18c081a8/derpibooru.csv) · [Philomena tag API](https://github.com/philomena-dev/philomena/blob/323beaeb3fd5dca92dcc4e114d8f93356c3501d7/lib/philomena_web/views/api/json/tag_view.ex) · [Philomena tag-write code](https://github.com/philomena-dev/philomena/blob/323beaeb3fd5dca92dcc4e114d8f93356c3501d7/lib/philomena/tags.ex#L343)

The operator's Mac received live Derpibooru JSON for all four metadata requests. The canonical Character record `rainbow dash` (ID `39229`, slug `rainbow+dash`) listed `rd`, `rainbowdash`, and `dashie` as incoming aliases. The separately queried `dashie` (ID `24568`) and `rd` (ID `63932`) records each had a null category and explicitly pointed to `rainbow+dash`. The existing audit post `2623405` carried only the canonical spelling and ID from this family. This establishes the normal post payload for that example. The independently retrieved records cover `dashie` and `rd`. `rainbowdash` was observed in the canonical record's incoming list.

**VS Code — Meloader terminal.** On a connection that can reach Derpibooru, run this metadata-only check. The script prints tag names and IDs without printing media URLs or writing to `Audit_A`.

```sh
python3 - <<'PY'
import json
import urllib.request

base = "https://derpibooru.org/api/v1/json"
for route in ("tags/rainbow%2Bdash", "tags/dashie", "tags/rd", "images/2623405"):
    try:
        request = urllib.request.Request(
            f"{base}/{route}", headers={"Accept": "application/json"}
        )
        with urllib.request.urlopen(request, timeout=15) as response:
            data = json.load(response)
        if "tag" in data:
            tag = data["tag"]
            keys = ("id", "name", "slug", "category", "aliased_tag", "aliases")
            print(route, {key: tag.get(key) for key in keys})
        else:
            post = data["image"]
            wanted = {"rainbow dash", "dashie", "rd"}
            print(route, "post", post["id"], [
                (tag, tag_id) for tag, tag_id in zip(post["tags"], post["tag_ids"], strict=True)
                if tag in wanted
            ])
    except (OSError, ValueError, KeyError) as err:
        print(route, type(err).__name__, str(err))
PY
```

## Alias implementation acceptance

For a confirmed alias family, the strict rule `character:rainbow_dash violence` must block a post that lists the canonical tag or any verified member of that family. A rule written with a verified alias should also match the canonical tag the post normally supplies. Keep the alias's null category distinct from the target's verified Character category. Use synthetic metadata and count actual media requests for these cases. Alias resolution is outside the filing change.

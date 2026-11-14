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

## Alias evidence and remaining check

The July 2024 tag snapshot lists `dashie`, `rd`, and `rainbowdash` as aliases of `rainbow_dash`. Philomena exposes the alias target slug as `aliased_tag` and incoming alias slugs as `aliases`. Its tag-write code resolves aliases to their canonical record. [Historical tag snapshot](https://github.com/Siberpone/derpi-tac-a1111/blob/d7ed49593f035546a5da2d8bdb7ae72d18c081a8/derpibooru.csv) · [Philomena tag API](https://github.com/philomena-dev/philomena/blob/323beaeb3fd5dca92dcc4e114d8f93356c3501d7/lib/philomena_web/views/api/json/tag_view.ex) · [Philomena tag-write code](https://github.com/philomena-dev/philomena/blob/323beaeb3fd5dca92dcc4e114d8f93356c3501d7/lib/philomena/tags.ex#L343)

The metadata-only probe requested tag records for `rainbow dash`, `dashie`, and `rd` and the existing safe-rated audit post `2623405`. The execution environment received HTML in place of JSON. The current Derpibooru alias records and the post's native tag list remain unverified.

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

Before changing alias behavior, confirm the exact alias and canonical records and inspect the post's `tags` and `tag_ids` on a working connection. For a confirmed alias family, the strict rule `character:rainbow_dash violence` must block a post that lists the canonical tag or any verified member of that family. Use synthetic metadata and count actual media requests for these cases. Alias resolution is outside the filing change.

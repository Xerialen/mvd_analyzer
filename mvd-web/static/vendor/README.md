# Vendored frontend dependencies

These files are the frontend's only third-party runtime code. They are
committed to the repo (not gitignored) and shipped to `dist/` by
`make build`, so the app has **no runtime CDN dependencies** — the Locs &
Regions tab (Cytoscape loc graph) and the site fonts work offline / when
unpkg or Google Fonts are unreachable.

## Cytoscape + fcose (Loc Graph tab)

Fetched from unpkg at the exact pinned versions referenced by
`../index.html`. `index.html` loads them in this order — it matters,
because each UMD bundle reads the previous one's `window` global
(`cytoscape` is independent; `layout-base` → `window.layoutBase` →
`cose-base` reads it and sets `window.coseBase` → `cytoscape-fcose` reads
that and sets `window.cytoscapeFcose`, which `app.js`
`registerCytoscapeExtensions()` hands to `cytoscape.use()`).

| File | Version | Source URL | sha256 | bytes |
|---|---|---|---|---|
| `cytoscape-3.30.2.min.js` | 3.30.2 | https://unpkg.com/cytoscape@3.30.2/dist/cytoscape.min.js | `83e8c54a6bec655bfd81df07df605649c268af69aeca67a5ea2da54ea42dac81` | 373304 |
| `layout-base-2.0.1.js` | 2.0.1 | https://unpkg.com/layout-base@2.0.1/layout-base.js | `ec15ab5df9af3f20708f4faab994accf91cda71848cd5bb10a23432cc50b6745` | 147958 |
| `cose-base-2.2.0.js` | 2.2.0 | https://unpkg.com/cose-base@2.2.0/cose-base.js | `7cae9509bd36235a63a85e71c8d9fa2cd0bc1d0c1ecc5b5a737976f39d040ddf` | 118906 |
| `cytoscape-fcose-2.2.0.js` | 2.2.0 | https://unpkg.com/cytoscape-fcose@2.2.0/cytoscape-fcose.js | `4b1cab218d74996aa59cd8473f9239cc6398b8c1774d84d7e59ad9a68959cb57` | 57239 |

Licenses: Cytoscape.js, layout-base, cose-base and cytoscape-fcose are
all **MIT** licensed (© The Cytoscape Consortium / i-Vis lab). MIT
permits redistribution; the copyright headers remain in each file.

### Verifying the hashes

```bash
cd mvd-web/static/vendor
sha256sum cytoscape-3.30.2.min.js layout-base-2.0.1.js cose-base-2.2.0.js cytoscape-fcose-2.2.0.js
# each should equal the table above and match what unpkg serves for that version.
```

## Fonts (`fonts/`)

`fonts/fonts.css` is the Google Fonts CSS for the two families the UI uses
— **Rajdhani** (weights 600, 700; headings / UI chrome) and **Inter**
(weights 400, 500, 600; body) — fetched with a Chrome UA so Google returns
`woff2`, then rewritten so every `url()` points at a sibling `.woff2` in
this directory instead of `fonts.gstatic.com`. All subsets Google emits
(latin, latin-ext, cyrillic, greek, vietnamese, devanagari) are kept with
their `unicode-range` rules intact, so international player names still
render and the browser still downloads only the subset it needs. 13 unique
`woff2` files (Inter is a variable font — one file per subset shared across
its three weights). Source:

    https://fonts.googleapis.com/css2?family=Rajdhani:wght@600;700&family=Inter:wght@400;500;600&display=swap

Font licenses: Rajdhani and Inter are both under the **SIL Open Font
License 1.1**, which permits bundling and redistribution.

## Bumping a version

1. Download the new pinned file into this directory (keep the version in
   the filename), e.g.
   `curl -fL -o cytoscape-3.x.y.min.js https://unpkg.com/cytoscape@3.x.y/dist/cytoscape.min.js`.
2. Update the matching `<script src="vendor/...">` (or the fonts `<link>`)
   in `../index.html`.
3. Update the row (version, URL, sha256, bytes) in this file; delete the
   old file.

For fonts, re-fetch the CSS with a Chrome UA, re-download the referenced
`woff2` files, and rewrite the `url()`s to relative paths (dedupe by URL —
Inter shares one file per subset across weights).

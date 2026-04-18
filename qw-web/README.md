# qw-web

Layer 3 of the mvd-analyzer workspace: a browser UI for the analysis
pipeline, built as a Go WASM bundle plus a small static frontend that
talks to it through a JS shim.

## What's in the box

- `cmd/wasm/` — WASM entry point. Exports a single `analyzeMVD(bytes,
  filename)` function to the JS global scope; everything else in the
  pipeline is in qwanalytics.
- `static/` — the browser frontend.
  - `index.html`, `styles.css`, `app.js` — main page and the tabbed
    analyzer UI (scoreboard, timeline, map, chat, loc graph, ...).
  - `worker.js` — wraps the WASM module in a Web Worker so analysis
    doesn't block the main thread. Also provides `fetchLocSync` which
    the WASM-side loc loader calls (sync XHR is still allowed inside
    Web Workers).
  - `wasm_exec.js` — Go runtime glue, copied from the Go toolchain at
    build time.
  - `maps/` — pre-generated per-map floor polygon JSON. Committed; the
    frontend fetches `maps/<basename>.json` at demo load.
  - `probe.html` — tiny dev page used to probe runtime features.

## Build and deploy

From the repo root:

```bash
make build                    # -> dist/
make serve                    # build + python3 -m http.server 8080
```

`make build` produces:

```
dist/
  analyzer.wasm               ~4 MB, the WASM bundle
  wasm_exec.js                Go glue
  index.html, styles.css,
  app.js, worker.js           frontend
  maps/                       pre-generated map geometry
  locs/                       .loc files copied from qwanalytics/loc/data
  items/                      per-map item corpus copied from qwanalytics/items/data
```

### Netlify deploy

`netlify.toml` at the repo root runs `make build` and publishes `dist/`.
Every push to a branch with Netlify connected will rebuild and deploy.

## How the pieces fit

1. User drops an MVD file on the page (or pastes a hub.quakeworld.nu URL).
2. `app.js` hands the bytes to `worker.js` via `postMessage`.
3. The worker calls `analyzeMVD(bytes, filename)` on the WASM instance.
4. WASM code (`cmd/wasm/main.go`) runs the qwanalytics default pipeline,
   marshals the Result to JSON, returns it as a string.
5. Worker sends the JSON back to `app.js`, which renders it across the
   tabs.

The WASM boundary is the only place that bridges Go and JS. The rest of
the frontend is dependency-free JS plus a sprinkle of CSS.

## Loc files at runtime

WASM builds do not embed the `.loc` corpus (would add ~6.7 MB to the
bundle). Instead, when the analyzer needs a loc file, it calls
`fetchLocSync(mapName)`, which the worker implements as a synchronous
XHR against `locs/<name>.loc`. `make build` copies the corpus from
`qwanalytics/loc/data/` into `dist/locs/`.

## Item corpus at runtime

Same pattern for the per-map pickup-item JSON consumed by the
`ItemAnalyzer`. `qwanalytics/items/loader_wasm.go` calls
`fetchItemsSync(mapName)`; `worker.js` does a sync XHR against
`items/<name>.json`. `make build` copies `qwanalytics/items/data/` →
`dist/items/`. Native builds skip this path and use the `go:embed`
corpus.

## Map-tab item overlay

When the result contains an `items` field (KTX demos), the map tab
renders every tracked item as a small square and surfaces a sidebar
panel listing each item with live status (`up`, countdown to
respawn, or `held` while an MH rot is in progress) and its loc
region. Armors render as solid-filled coloured squares (RA/YA/GA);
weapons, MH and powerups are black squares with a coloured outline
matching the timeline palette plus a short text label (RL, LG, MH,
Q, P, …). Items currently taken are dimmed on the map and
highlighted-dim in the sidebar so verifying the event stream
against gameplay is visual.

## Regenerating map geometry

Per-map floor polygon JSON under `static/maps/` is produced by the
`mapgen` developer tool, which reads Quake 1 BSPs from an off-repo
working directory. See
[qwanalytics/README.md](../qwanalytics/README.md#mapgen) and the
[top-level README](../README.md#map-geometry) for the workflow.

## Module boundary

qw-web depends on qwdemo (to open MVD byte streams) and qwanalytics
(to run the pipeline). It has no source of its own that qwdemo or
qwanalytics depends on.

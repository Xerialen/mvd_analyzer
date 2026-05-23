# mvd-web

Layer 3 of the mvd-analyzer workspace: a browser UI for the analysis
pipeline, built as a Go WASM bundle plus a small static frontend that
talks to it through a JS shim.

## What's in the box

- `cmd/wasm/` — WASM entry point. Exports `analyzeMVD(bytes, filename)`
  for the parse-and-pin call, plus the schema-v7 query API as bridge
  functions: `getDefaultBuckets()` (legacy 50 ms []HighResBucket shape
  for the existing panels), `getBuckets(optsJSON)`,
  `getEvents(filterJSON)`, `getStreamSlice(optsJSON)`,
  `getStateAt(optsJSON)`, `getLocTrails(optsJSON)`, and
  `recomputeRegionControl(regionsJSON)`. All take a JSON-string argument
  (or none for `getDefaultBuckets`) and return a JSON string; under the
  hood they call into `mvd-analytics/view` over the cached `lastResult`.
- `static/` — the browser frontend.
  - `index.html`, `styles.css`, `app.js` — main page and the tabbed
    analyzer UI (scoreboard, timeline, map, chat, loc graph, ...).
  - `worker.js` — wraps the WASM module in a Web Worker so analysis
    doesn't block the main thread. Provides the host callbacks the
    WASM side calls synchronously: `fetchLocSync(mapName)` for the
    per-map `.loc` corpus and `fetchBspSync(mapName)` for the per-map
    BSP used by the visibility-aware loc attribution (locvis). Sync
    XHR is still allowed inside Web Workers.
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
  locs/                       .loc files copied from mvd-analytics/loc/data
  bsps/                       BSP files from `make bsps` for the locvis
                              visibility filter (skipped if bsps/ is empty)
```

### Netlify deploy

`netlify.toml` at the repo root chains `make bsps && make build` and
publishes `dist/`. Every push to a branch with Netlify connected
rebuilds and deploys. `make bsps` runs on Netlify's build container
(it has `curl` and `bash`), fetches the ~14 competitive-map BSPs from
the public mirrors documented in `scripts/fetch-bsps.sh`, and verifies
each sha256 — a missing or corrupt BSP hard-fails the deploy, which
is preferred to a silent V1-everywhere regression.

## Layout

A slim top bar (wordmark + commit-hash version + GitHub link) sits
above a Grafana-style frame: a fixed left **sidebar** with one button
per analysis tab, and a **main pane** that fills the rest of the
viewport (no width cap). Sidebar order is `Search`, `Summary`,
`Timeline`, `Chat`, `Map`, `Loc Graph`, `Key Moments`, `Pack Drops`.

The Search tab is the first tab and is always available — it holds the
file picker, the hub-URL load row, and the filter form for browsing
the hub. The other tabs are always present in the sidebar; until a
demo is loaded they show a short "Load or search a demo to begin"
placeholder (CSS-driven via a `body.no-demo` class). After a successful
load the placeholder is hidden, the Summary tab activates, and the
real content renders.

On viewports below 800 px the sidebar reflows into a horizontal scroll
strip above the main pane.

## How the pieces fit

1. User drops an MVD file on the Search tab, pastes a hub.quakeworld.nu
   URL, or picks a row from the search results.
2. `app.js` hands the bytes to `worker.js` via `postMessage`.
3. The worker calls `analyzeMVD(bytes, filename)` on the WASM instance.
4. WASM code (`cmd/wasm/main.go`) runs the mvd-analytics default pipeline
   and marshals the Result to JSON. The worker posts this back
   **immediately** as a `result` message — the main thread renders the
   Summary and the other non-bucket tabs right away.
5. **Then**, off the critical path, the worker runs the two schema-v7
   bridge calls — `getDefaultBuckets()` (builds the legacy 50 ms
   []HighResBucket array via `view.Buckets`) and
   `recomputeRegionControl(defaults)` (region-control bucket states at
   50 ms) — and posts them as a second `buckets` message. These are
   expensive (the bucket build alone is multiple seconds in WASM), and
   only the Timeline/Map tabs need them, so deferring them roughly halves
   time-to-interactive. They exist at all because the existing panels
   still read `result.timelineAnalysis.highResBuckets` and
   `.regionControl.bucketStates`; Phase 1.5 (per-panel
   `getBuckets({windowMs})` calls) will drop the bridge step.
6. On `result`, `app.js` parses the JSON, clears the no-demo class,
   switches to the Summary tab, and renders all tabs. The main-thread
   inits are cheap, so they run now even though the bucket-derived
   fields are still empty — the scoreboard, chat, pack drops, pickups,
   key moments and loc graph are fully populated; only the timeline
   graph, map trails and region overlay are blank. On the later
   `buckets` message, `applyDeferredBuckets()` stashes the payload onto
   `result.timelineAnalysis.highResBuckets` / `.regionControl
   .bucketStates`, **re-runs** the bucket-dependent inits
   (`initRegionControlData`, `displayTimelineAnalysis`, `initMapView`),
   and re-renders the active tab so Timeline/Map fill in. The win is
   purely that the worker no longer blocks on the bucket build before
   delivering the result.

`cmd/wasm/main.go` also exports `getDemoInfo()`, which returns just the
KTX demoinfo summary (`result.DemoInfo` — map, players, teams, scores,
date) from the pinned `lastResult` as JSON. It is zero extra cost (the
data is already computed) and lets a consumer read the match summary
without re-marshalling the full Result. Note: the demoinfo block is
written near the **end** of the MVD stream, so obtaining it still
requires decoding the whole demo — cheap to *read*, not cheap to *skip
ahead to*.

The WASM boundary is the only place that bridges Go and JS. The rest of
the frontend is dependency-free JS plus a sprinkle of CSS.

## Performance timing (console)

Every demo load prints a structured per-stage breakdown to the browser
console (look for the `[mvd-timing]` group) and stashes the same object
on `window.__mvdTimings`. It is dev-facing instrumentation only — there
is no UI for it. Stages reported, in load order:

- **wasm load** (one-time): fetch + `instantiateStreaming` + `go.run`,
  timed in `worker.js` and sent on the `ready` message.
- **network**: `gameInfoFetch` (Supabase metadata) and `demoDownload`,
  timed on the main thread in `app.js`.
- **WASM analyze**: total wall time of the `analyzeMVD` call, plus the
  Go-side per-phase split from `getAnalysisTimings()` — `init`,
  `eventPass` (decode + gzip + all OnEvent dispatch), one
  `finalize:<analyzer>` row per analyzer (so `finalize:timelineAnalysis` — the
  loc-resolution work — is isolated), one `post:<name>` row per
  post-processor (`locGraphPost`, `regionControlPost`), and `marshal`.
- **loc/bsp fetch**: per-map `fetchLocSync` / `fetchBspSync` durations.
  These run **synchronously inside** the `analyzeMVD` call, so their
  time is already included in the WASM analyze wall time *and* inside
  `finalize:timelineAnalysis`. To get the **pure loc-resolution compute**,
  subtract `locFetch + bspFetch` from `finalize:timelineAnalysis`.
- **result JSON.parse** (main thread), each tab render
  (`displayTimelineAnalysis`, `displayKeyMoments`, `displayPackDrops`,
  `displayPickupsTab`, `initMapView`, `initLocGraphView`), and the
  async `map geometry fetch` (logged separately as it resolves after
  the UI is shown).

The breakdown reflects **time-to-interactive** — it ends when the Summary
and non-bucket tabs are painted. The deferred 50 ms bucket build
(`getDefaultBuckets` + `recomputeRegionControl`) runs after that and is
logged on its own line (`deferred bucket build (off critical path)`),
followed by a `Timeline/Map ready` line when `applyDeferredBuckets`
finishes wiring those tabs.

This exists to replace guesswork about where load time goes (e.g. "is
loc the slowest?") with measured data before optimizing. It is what
surfaced the two big costs — the parse event pass and the (now deferred)
bucket build.

## Demo search

The Search tab queries the same Supabase `v1_games` table that the
hub-loader uses (so no backend of our own) and lets the user filter by
player, team, map, mode (1v1 / 2v2 / 4v4 / FFA / CTF), game tag, and
date range. All filters are AND-combined, empty fields act as
wildcards, and the latest 20 matches sorted by date descending are
listed. Clicking a row downloads the demo and runs the normal analysis
pipeline; the user lands on the Summary tab.

Search state is reflected in the URL so links are shareable. Supported
params: `player`, `team`, `map`, `mode`, `tag`, `from`, `to`. For
example:

- `?player=nexus` opens the page on the Search tab with the player
  field pre-filled and the search auto-executed.
- `?player=nexus&mode=1on1&map=aerowalk` pre-fills three fields.
- `?gameId=212607&player=nexus` loads the demo (and lands on Summary)
  *and* pre-populates the Search tab; clicking Search shows the
  filters and the result list.

The demo-load URL parameter is `gameId` (matching hub.quakeworld.nu's
own URL scheme); the legacy `?hub=<id>` form is still accepted on read
for any links that already exist in the wild.

## Loc files at runtime

WASM builds do not embed the `.loc` corpus (would add ~6.7 MB to the
bundle). Instead, when the analyzer needs a loc file, it calls
`fetchLocSync(mapName)`, which the worker implements as a synchronous
XHR against `locs/<name>.loc`. `make build` copies the corpus from
`mvd-analytics/loc/data/` into `dist/locs/`.

## BSPs at runtime (visibility filter)

The locvis visibility filter (see [`mvd-analytics/locvis/`](../mvd-analytics/locvis/))
loads per-map BSP files on demand via `fetchBspSync(mapName)`, which
worker.js implements identically to `fetchLocSync` but against
`bsps/<name>.bsp`. `make bsps` populates a gitignored top-level
`bsps/` directory from the curated set in
[`scripts/fetch-bsps.sh`](../scripts/fetch-bsps.sh) — id-stock maps
(dm2/dm3/dm6/e1m2) from [id-maps-gpl](https://github.com/quakeworld/id-maps-gpl)
gzipped, community competitive maps from
[maps.quakeworld.nu/core](https://maps.quakeworld.nu/core/), each
sha256-pinned. `make build` then copies them into `dist/bsps/`. When
a map has no BSP available the WASM side returns `null` and locvis
transparently degrades to the V1 Euclidean nearest-neighbour
attribution — no UI change beyond losing the wall-bleed correction
for that map. Skipping `make bsps` entirely is supported for local
dev; the build still works, you just get V1 everywhere.

The Netlify deploy chains `make bsps && make build`, so production
gets the visibility filter on every push.

## Pack Drops tab

The Pack Drops tab shows every RL / LG backpack drop as one row,
joined with its pickup outcome. The drop side comes from
`result.backpacks`; the pickup side from `result.weaponPickups` entries
with `source == "backpack"`, joined on `(backpackEnt, dropTime)` —
the compound key is needed because QW servers recycle backpack
edict numbers across drops, so `entNum` alone would collide. A drop
with no matching pickup is shown as `expired`.

The "RL / LG only" scope is a wire-protocol limit, not a UI
decision: KTX's `//ktx drop` and `//ktx bp` directives fire only
for RL/LG packs, and the print-based fallback for other pack
classes is stripped from competitive MVDs by mvdsv's `messagelevel`
filter. See [`mvd-reader/MVD_FORMAT.md` → Practical gap — non-RL/LG
backpack pickups on competitive demos](../mvd-reader/MVD_FORMAT.md#svc_stufftext-9)
for the full mechanics.

Columns: Time, Dropper, Drop Team, Weapon, Drop (hub link),
Status, Picker, Pick Team, Kills, Run (hub link). Five filter
dropdowns above the table narrow rows by Dropper, Drop Team,
Picker, Pick Team, or Status label; each dropdown is populated
from the distinct values present in the current demo, and
selections persist across demo reloads when the same value is
still available in the new data.

Status column derivation:

| condition                               | label        |
|-----------------------------------------|--------------|
| no matching pickup                      | `expired`    |
| same team as dropper, picker !hadBefore | `xfer`       |
| same team as dropper, picker hadBefore  | `xfer RL/LG` |
| enemy team, picker !hadBefore           | `enemy`      |
| enemy team, picker hadBefore            | `enemy RL/LG`|

The `Kills` column is `weaponPickups[i].kills` — frags the picker
scored with the pack's weapon before their next death. Only
pickups that actually granted the weapon (the picker didn't have
it yet) are eligible for kill credit; redundant grabs — where
`hadBefore` is true and the pickup didn't give the picker anything
new — always show 0 and are dimmed. The denial semantics still
show through the status chip (`enemy RL`, `xfer RL`).

The `Drop` and `Run` columns are hub.quakeworld.nu replay links.
`Drop` spans 10 s leading into the drop, tracking the dropper;
`Run` spans 3 s before pickup to the picker's next death (or +15 s
if they survived to match end), tracking the picker.

## Map-tab item overlay

When the result contains an `items` field (any MVD source — KTX,
ktpro, CustomTF, etc.), the map tab renders every tracked item as a
small square and surfaces a sidebar panel listing each item with
live status (`up` or countdown to respawn) and its loc region.
Armors render as solid-filled coloured squares (RA/YA/GA); weapons,
MH and powerups are black squares with a coloured outline matching
the timeline palette plus a short text label (RL, LG, MH, Q, P, …).
Items currently taken are dimmed on the map and highlighted-dim in
the sidebar so verifying the event stream against gameplay is
visual. The panel updates live during playback via the 200 ms
full-sync tick in `animatePlayback`.

## Regenerating map geometry

Per-map floor polygon JSON under `static/maps/` is produced by the
`mapgen` developer tool, which reads Quake 1 BSPs from an off-repo
working directory. See
[mvd-analytics/README.md](../mvd-analytics/README.md#mapgen) and the
[top-level README](../README.md#map-geometry) for the workflow.

## Module boundary

mvd-web depends on mvd-reader (to open MVD byte streams) and mvd-analytics
(to run the pipeline). It has no source of its own that mvd-reader or
mvd-analytics depends on.

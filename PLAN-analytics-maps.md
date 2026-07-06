# PLAN-analytics-maps — review of map/geometry/support packages

> Updated 2026-07-06 after implementation phases 0–5 (branches phase-0…phase-5). Only open items remain below; resolved findings are in the ledger at the bottom and in git history.

Scope: `mvd-analytics/{mapgen,mapbsp,mapents,mapclip,bspvis,loc,locvis,hubfetch,cmd}`.
Overall assessment after phases 0–5: the systemic issues this review found are
now closed — the 5–6× BSP re-read is a one-entry generation-guarded cache pair
(mapbsp bytes + locvis Finder), the four platform-loader copies share one
`internal/jshost` bridge, bspvis validates corrupt input at load instead of
panicking mid-query, the dead exported surface is deleted, and the whole scope
is gofmt-clean. What remains is one structural item (A2, the deliberate
~150-line parser overlap between `mapgen/bsp` and `bspvis`, scheduled as
Phase 12 of PLAN-implementation-order), two doc-drift slivers left over from
F4, and a short nit list re-verified below against the current tree.

## Open items

**A2. Parser overlap between `mapgen/bsp` and `bspvis` — REMAINS (now Phase 12 / task #13 of `PLAN-implementation-order.md`).**
Duplicated near-verbatim, refs re-verified at 22629a6: `Vec3`/`Plane` structs
(`mapgen/bsp/types.go:12-21` vs `bspvis/load.go:37-46`), `Model`
(`types.go:44-52` vs `load.go:85-93`), lump-index tables
(`mapgen/bsp/lumps.go:6-20` vs `load.go:108-127`), magic/version sniffing
(`mapgen/bsp/bsp.go:50-64` vs `load.go:161-175`), the plane decode loop
(`bsp.go:73-94` vs `load.go:202-218`), the model decode loop (`bsp.go:197-220`
vs `load.go:355-377`), and `readF32` (`bsp.go:321-323` vs `load.go:416-418`).
Phase 5 shrank the overlap by one item — `mapgen/bsp.ParseBytes` now calls the
shared bounds-checked `lumpBytes` (`entities.go:163-202`) instead of carrying
its own dentry/closure copy (old F9) — but bspvis still has its own dentry
array + `lumpBytes` closure (`load.go:177-199`). `bspvis/load.go:6-9`
documents the standalone-package choice, but both packages live in the same
module and are co-consumed by the analyzer, so the isolation buys nothing.
Suggested direction unchanged: extract the header/directory/plane/model layer
into one low-level package (or fold bspvis's node/leaf/vis decoding into
`mapgen/bsp`), keeping the query code (`PointInLeaf`, `RayHitsSolid`, PVS) in
`bspvis`. Any future format fix (e.g. a new BSP2 quirk) currently has to be
made twice. When this lands, also drop the never-read `Node.FirstFace/NumFaces`
and `Leaf.FirstMarkSurface/NumMarkSurfaces/Ambient` fields (see nits).

**A3 remnant — the caches landed (with generation guards); one cache edge stays open by design.**
Phase 5's one-entry caches (`mapbsp/mapbsp.go:36-89` for raw bytes,
`locvis/loader.go:23-52` for the built Finder) are generation-guarded:
`SetDir`/`SetBspDir` bump a generation so an in-flight load that started
against the old directory discards its store instead of re-caching stale
data, and concurrent `-race` stress tests cover load-vs-invalidate. The one
edge deliberately left open, documented in code: `loc.SetLocDir` does **not**
invalidate locvis's memoised Finder (which bakes in the loc table) —
`loc/loader_native.go:25-29` spells out why (today's only callers, cmd/mapgen
and tests, set it once at startup before any analysis) and says to add the
invalidation if a mid-process corpus switch is ever needed. Nothing to do
unless that caller appears.

**F4 remnants (doc drift — bulk fixed in phase 0, two slivers left).**
(d) still open: `bspvis/load.go:23-25` and `locvis/locvis.go:17-18` cite
`experiments/locattr/{RESEARCH_BSP.md,V2b-V6-HANDOFF.md}` — documents that are
still not in the repo (the `experiments/` tree is untracked, per-machine).
Either commit them or reword the comments to note they are historical.
(a) re-drifted: the JS-mirror contract refs that phase 0 pointed at the real
`app.js` lines (`mapgen/mapgeom/normalize.go:5,15` → `app.js:5534/5537`) were
shifted again by the phase-5 web consolidation — the actual locations are now
`mvd-web/static/app.js:5500` (`ITEM_KEYWORDS`) and `:5503`
(`normalizeLocationName`). The symbol names still match, only the line numbers
rot; consider citing symbols without line numbers in that comment, since its
whole job is to survive exactly this kind of churn.

## Low priority / nits (each re-verified 2026-07-06 at 22629a6)

- `mapgen/mapgeom/mapgeom.go:606-607`: the `se == 0` default branch of
  `assembleRing` still indexes `b.Edges[0]` without a `len(b.Edges) > 0`
  guard (the positive/negative branches check at `:595,:601`). Corrupt-input
  only; add the guard for symmetry.
- `loc/loader.go:87`: `strings.NewReader(string(data))` copies the whole
  file; `bytes.NewReader(data)` avoids it.
- `mapgen/mapgeom/prune.go:77-80,171-184`: `minF`/`maxF` duplicate the Go
  1.21+ builtins `min`/`max` (module is at go 1.25); `absF` stays.
- `cmd/qw-analyze/main.go:569-594`: `runBulk` still increments `processed`
  before attempting (inclusive of failures), while
  `cmd/mapgen/main.go:121-141` counts exclusively (`processed++` only on
  success). Pick one convention.
- `cmd/qw-analyze/main.go:212-227`: `-format md`/`-format events` still
  silently ignore `-view` and all view flags (`runOne` routes md/events
  without consulting `vopts`); consider erroring on the meaningless
  combination.
- **[RESOLVED on main — #96; insurance only]** `cmd/mapgen/main.go:123,210` +
  `entities.go:79`: output filenames use the raw lowercased BSP basename, not
  `loc.NormalizeMapName`, while every runtime loader normalizes. #96 emptied
  `mapAliases`, so normalize is now just lowercase-basename and no
  emit/load divergence is reachable. Normalizing at emit time remains cheap
  insurance if an alias is ever reintroduced.
- `cmd/mapgen` reads the same BSP file three times per map (`bsp.Parse`,
  `main.go:189`; `bsp.ReadEntities`, `entities.go:25`; `bsp.ReadModelBounds`,
  `entities.go:31`). Offline tool, so harmless; read once and pass bytes if
  it ever grows.
- `bspvis/load.go:61-62,77-79`: `Node.FirstFace/NumFaces`,
  `Leaf.FirstMarkSurface/NumMarkSurfaces/Ambient` are decoded and stored but
  never read — ~16 bytes per node/leaf on every loaded map; drop them when
  the parser layers are unified (A2 / Phase 12).
- `mapgen/mapgeom/mapgeom.go:229-233,399-412` (`Stats.WallsKept/WallTris`):
  wall classification and per-triangle area math survive solely to feed
  verbose counters for the removed "solid" render — now at least documented
  as such in a why-comment at `:400-401`. Still a candidate for deletion next
  time this file is touched.
- `mapgen/mapgeom/mapgeom.go:366-384` still re-implements the
  nearest-loc linear scan inline (it needs the winning *index* for the
  ceiling cap at `:389-393`, and `loc.findNearestLinear` is unexported).
  Phase 5's F11 fix added a linear *fallback* inside `loc.FindNearest`
  (`loc/finder.go:33-40`) but did not export an index-returning API, so the
  copy remains; exporting a `(*Finder) FindNearestIndex` would remove it.

## Resolved (implementation phases 0–5)

| ID | What | Phase / commit |
|---|---|---|
| A3 | 5–6 BSP reads + 3 PVS precomputes per analysis → one-entry generation-guarded caches: mapbsp `(map → bytes)` (`mapbsp/mapbsp.go`), locvis `(map → *Finder)` (`locvis/loader.go`); on WASM 5–6 blocking multi-MB fetches collapse to one; also pays off on the mvd-api shot-stream re-parse path | Phase 5 maps, bcfb8ce |
| A4 / F5 | Four platform-loader copies → one `internal/jshost.FetchSync` bridge shared by loc/mapents/mapbsp WASM loaders (legacy-string handling unified); locvis native/wasm split collapsed into one untagged `loader.go` | Phase 5 maps, bcfb8ce |
| A5 (+ its nit) | `mapents` corpus files with a stale shape version rejected at load (`mapents/types.go:37-38`); loc→mapents regeneration dependency documented in the package doc (`types.go:9-15`) | Phase 5 maps, bcfb8ce |
| F1 | bspvis validates `Node.PlaneID` and children ranges once at `LoadBytes` (`load.go:386-412`, mirroring mapclip's clipnode validation); scattered per-query checks deleted; previously-unchecked `PointInLeaf`/`boxLeafs` covered by tests | Phase 5 maps, bcfb8ce |
| F2 | `Stats.FacesDropped` incremented at the three drop sites (`mapgeom.go:309,322,350`) — the CLI counter is no longer always zero | Phase 0, 7d1a8e2 |
| F3 | Six dead exported symbols deleted (`PointSolid`, `CountPVSVisible`, `FindLocationsInRadius` ×2, `Finder.Base`, `ReadEntitiesBytes`, `FloorUsage.Demos`) plus the two doc comments claiming callers that didn't exist | Phase 4, f494471 |
| F4 | Doc-drift cluster: JS-mirror contract re-pointed, stale brush-entity sentence deleted, qw-web/qwanalytics names fixed, CLAUDE.md hubfetch path corrected (two slivers remain — see F4 remnants above) | Phase 0, 7d1a8e2 |
| F6 | `findDemos` propagates its `WalkDir` error (`cmd/mapgen/main.go:300-313`) — a typo'd `-demos` fails the run instead of silently emitting an unpruned corpus | Phase 5 maps, bcfb8ce |
| F7 | `-view state-at -time 0` accepted: presence tracked via `timeSet` (`cmd/qw-analyze/main.go:59,186,360`) | Phase 5 maps, bcfb8ce |
| F8 | One `analyzePath` preamble + `analyzeOptions` knobs shared by json/view/md dumpers (`main.go:229-273`) — the `-include` registry knobs no longer live in the json copy alone | Phase 5 maps, bcfb8ce |
| F9 | `ParseBytes` uses the shared bounds-checked `lumpBytes` helper (`bsp.go:66-74`); its private dentry/closure copy deleted | Phase 5 maps, bcfb8ce |
| F10 | `var _` filler blocks and the dead `io` import deleted (bsp.go, lumps.go, bspvis/load.go) | Phase 0, 7d1a8e2 |
| F11 | Pencil-index shell cap (`r <= 16`) falls back to the exhaustive linear scan, so the two paths can no longer disagree on a far nearest (`loc/finder.go:33-40`) | Phase 5 maps, bcfb8ce |
| F12 | gofmt sweep — the eleven flagged files (and drifted neighbours) reformatted; whole scope verified gofmt-clean at 22629a6 | Phase 0, ff47a76 |
| F13 | `hubfetch.Download` reports the actual CDN failure when no `demo_source_url` fallback exists (`hub.go:138-144`) | Phase 5 maps, bcfb8ce |
| F14 | `mapgeom.Params` doc names its real consumers (tests + cmd/mapgen prune flags); unused JSON tags dropped | Phase 0, 7d1a8e2 |

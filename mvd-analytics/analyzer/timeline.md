# timeline analyser

**Phase:** Derived
**Inputs:** `PrintEvent`, `IntermissionEvent`, `PlayerPositionEvent`,
            `StatUpdateEvent`, `FragUpdateEvent`, `DeathEvent`,
            `SpawnEvent`, `UserInfoEvent`
**Reads from CoreOutputs:** `co.DemoInfo`, `co.Names`, `co.FragEntries`
**Writes to Result:** `result.Streams` (canonical event-rate storage)
                    plus `result.TimelineAnalysis` (frag / powerup /
                    streak event records, loc table, region control
                    stats, hub-viewer offset).

## What it does

Reconstructs per-player state at native event rate into
`result.Streams` — sparse change streams (health, armor, armor type,
loc, ammo) + interval lists (weapons, powerups) + a columnar position
track at the demo's native sampling cadence (~77 Hz). All higher-order
analyses (frag streaks, region control, powerup events) are computed
during finalize from this storage. Bucketed views are no longer baked
at parse time; `mvd-analytics/view.Buckets` produces them on demand at
any window resolution.

The analyser is split across several files:

| File | Role |
|---|---|
| [`timeline.go`](timeline.go) | `OnEvent` dispatch — updates running per-player cursor + appends transitions to the stream builders |
| [`timeline_streams.go`](timeline_streams.go) | Stream builder type, dedup rules, loc resolution + blip filter on `PositionTrack.Li`, finalize-time stream assembly |
| [`timeline_powerups.go`](timeline_powerups.go) | Quad/Pent/Ring interval closure → `PowerupEvent` records |
| [`timeline_streaks.go`](timeline_streaks.go) | Top-N spawn-to-death frag streaks |
| [`timeline_regions.go`](timeline_regions.go) | Map region auto-detect + per-map overrides |
| [`timeline_finalize.go`](timeline_finalize.go) | Orchestrates the pipeline above |

## How it works

1. **Per-event recording.** Every `OnEvent` dispatch updates the
   running cursor (`timelinePlayerState`) AND the historical record
   (the `streamBuilder` substruct). The cursor tells "what is X
   right now"; the builder is the append-only ledger that becomes
   `result.PlayerStream` at finalize. Append rules:
   - Change streams (health, armor, ammo, loc) dedup against the
     previous value — every entry is a transition.
   - Position appends every native sample (no dedup; positions
     almost always differ between samples).
   - Interval streams (weapons, powerups) open an anchor on
     `false→true` and close on `true→false`.
   - Spawn/death timestamps just append.
2. **Match window gating.** `MatchTimingDetector` gates ordinary stream
   emission. Pre-match and post-intermission events bypass those streams so
   warmup state doesn't pollute the output. The CLI's explicit
   `diagnostic-buckets` profile is the sole exception: it captures native
   position samples across the complete demo and emits only the closed
   diagnostic projection, never the intermediate `Result`.
3. **Loc resolution + blip filter** (finalize, in
   `resolveLocsAndFilterBlips`): walk each player's `PositionTrack`
   and call `loc.Finder.FindNearest(x, y, z)` per native sample,
   populating `PositionTrack.Li` (int16 column parallel to T/X/Y/Z).
   Then run the blip filter on the Li column — collapsing
   short-residence wall-bleed onto adjacent stable runs at the
   native sample rate, split at spawn/death boundaries. Finally
   emit the sparse `PlayerStream.Loc` change stream from the
   smoothed Li column.
4. **Frag events.** `co.FragEntries` are joined with demoinfo
   names + teams into `TimelineFragEvent` records.
5. **Streaks.** Spawn-to-death runs (from `Spawns`/`Deaths` streams)
   are paired and scored by frag count; top 10 emit as
   `FragStreakEvent`.
6. **Regions.** Region definitions come from per-map JSON
   (`mvd-analytics/config/regions/<map>.json`) or auto-detection in
   `timeline_regions.go`. A CLI flag (`-regions <path>`) or
   `Registry.SetRegionsOverride` can replace them at runtime.
7. **Region control** (`view.RegionControl` in
   [`../view/region_control.go`](../view/region_control.go)):
   walks each player's `PositionTrack` natively. For each bucket
   window, sample the player's Li at `bucketStart` (carry-forward
   from the latest position sample) and the armed state via the
   RL/LG interval streams. Classify each bucket into one of seven
   states (empty, teamA[Weak]Control, teamB[Weak]Control,
   contested, weakContested). Output is per-region `bucketStates`
   (one ASCII char per bucket) + match-aggregate `stats`. Region
   definitions and team labels come from `TimelineAnalysisResult.
   RegionControl` (populated by analyzer Finalize); `BucketStates`
   and `Stats` are filled by the `regionControlPost` post-processor
   calling the view function with defaults. WASM exposes
   `recomputeRegionControl` for the web UI's region-edit flow,
   which calls `view.RegionControl` directly with the edited
   regions as an option override.
8. **Powerups.** Each player's Quad/Pent/Ring interval list maps
   directly to `PowerupEvent` records (one per closed interval).
   Frag-during-powerup counts attach during finalize via
   `co.FragEntries`.

### Stream JSON shape (compact keys)

Every per-player field lives on `result.Streams.Players[].*` with
short JSON keys — see `mvd-analytics/RESULT_SCHEMA.md` for the
authoritative table. Summary:

- `h` / `a` — health / armor change streams (`[]ChangeI16`).
- `at` — armor type (`""` / `"ga"` / `"ya"` / `"ra"`) change stream.
- `li` — loc index (into `TimelineAnalysisResult.LocTable`) change
  stream.
- `pos` — native-rate position track (`PositionTrack` with parallel
  T/X/Y/Z/Li columns).
- `rl` / `lg` / `gl` / `ssg` / `sng` — weapon-held intervals.
- `q` / `pe` / `r` — Quad / Pent / Ring intervals.
- `sh` / `nl` / `rk` / `cl` — ammo change streams.
- `sp` / `d` — spawn / death timestamps.

## Limitations / known issues

- The blip filter is a heuristic — pathological maps with many
  near-equidistant locs (e.g. dm6's GA cluster) can still produce
  short residences that survive the threshold.
- Powerup detection fires on `STAT_ITEMS` bit transitions. A player
  who picks up the same powerup with zero gap (warm pent grab)
  emits as one continuous interval, not two.
- `co.FragEntries` is required for streak / powerup-frag
  attribution; if the frag analyser is unregistered, those derived
  fields are empty (but the streams still populate normally).
- `PositionTrack.Li` is populated only when the loc finder loaded
  for the demo's map. Maps with no `.loc` file produce streams
  with empty `Li` and no derived loc graph / region control.

## Reference

- Native position sampling cadence is the QW server tick rate
  (typically ~77 Hz, ~13 ms between samples; varies with
  `sv_demoPings` and per-player update rate).
- Region / loc heuristics: see `mvd-analytics/loc/data/*.loc`.
  Auto-detect keywords live in `timeline_regions.go`; per-map
  region overrides ship as JSON in
  `mvd-analytics/config/regions/<map>.json` (drop a new file in that
  directory, no Go code change needed). Maps with no RA loc fall
  back to YA in the auto-detector. The web UI's Region Control
  panel saves/loads this exact JSON shape.

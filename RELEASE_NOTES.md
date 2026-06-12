# Release notes

Feature-level changes as they land on `main`, newest first. Dates are
the merge dates on `main`; schema bumps reference
[RESULT_SCHEMA.md](mvd-analytics/RESULT_SCHEMA.md) for field-level
detail.

## 2026-06-12

- **Per-sample floor height, airgibs, movers, liquids** (schema v24–v30,
  [#84](https://github.com/galfthan/mvd_analyzer/pull/84)). Every
  position sample now carries the player's height above the floor
  (BSP clip-hull traces, footprint-aware, standing on lifts/doors at
  their demo-streamed poses) and a water/slime/lava submersion state.
  On top of it: the **airgibs** Key Moment — direct enemy rocket hits on
  airborne victims, with lethality, Hub shooter links, and the victim's
  height above the shooter as the headline "how spectacular" number.

## 2026-06-07

- **Wall-clock timing for demos** (schema v23,
  [#82](https://github.com/galfthan/mvd_analyzer/pull/82)). Recovers a
  real-world clock anchor for each demo so any match-relative time maps
  to wall-clock time; pause segments are accounted for in the mapping.
- **Demo-start timestamp decoding**
  ([#83](https://github.com/galfthan/mvd_analyzer/pull/83)). Parses the
  mvdhidden `0x000B` block (ULEB128 Unix-ms) — the millisecond-accurate
  demo-open anchor the wall-clock mapping builds on.

## 2026-06-03

- **Per-hit damage end to end** (schema v20,
  [#81](https://github.com/galfthan/mvd_analyzer/pull/81)). The KTX
  hidden damage stream becomes a full per-hit log with
  attacker→victim matrices, per-weapon aggregates, and EWep
  victim-weapon buckets; telefrags and stomps are surfaced separately.
- **Corrected scoreboard stats** (schema v19,
  [#80](https://github.com/galfthan/mvd_analyzer/pull/80)). Kills,
  deaths and suicides corrected from the frag log for every consumer;
  efficiency is kills-based to match hub.quakeworld.nu.

## 2026-06-02

- **Player timelines**
  ([#79](https://github.com/galfthan/mvd_analyzer/pull/79)). Per-player
  timeline view of vitals, weapons and powerups across the match.

## 2026-05-30

- **Static map-entity corpus + map endpoints** (schema v14,
  [#77](https://github.com/galfthan/mvd_analyzer/pull/77)). Item and
  spawn locations extracted from map BSPs ship embedded, with REST
  endpoints to serve per-map geometry and entities.

## 2026-05-29

- **Schema reference reconciled with code**
  ([#76](https://github.com/galfthan/mvd_analyzer/pull/76)).
  RESULT_SCHEMA.md brought back in lock-step with `result/` after
  drift; version history table became the single change record.

## 2026-05-25

- **Reconnect identity unified** (
  [#75](https://github.com/galfthan/mvd_analyzer/pull/75)). A player
  rejoining mid-match keeps one identity across slots; deaths and
  pickups reconcile against KTX's authoritative counters.

## 2026-05-24

- **Locs & Regions tab**
  ([#74](https://github.com/galfthan/mvd_analyzer/pull/74)).
  Combat-posture loc graphs (armed/unarmed movement between locs) plus
  sortable loc heatmap and region tables in the web UI.

## 2026-05-23

- **Column-major bucket format** (schema v11,
  [#72](https://github.com/galfthan/mvd_analyzer/pull/72)). Bucketed
  timelines ship as columnar arrays; the legacy HighResBucket shape is
  dropped.
- **Web load perf tuning**
  ([#70](https://github.com/galfthan/mvd_analyzer/pull/70)). Profiling,
  deferred bucket builds, and a faster `view.Buckets` cut initial load
  time.
- **Chat dedup on KTX demos**
  ([#68](https://github.com/galfthan/mvd_analyzer/pull/68)). Per-recipient
  copies of the same chat line collapse to one message.

## 2026-05-20

- **Visibility-aware loc attribution** (schema v9–v10,
  [#64](https://github.com/galfthan/mvd_analyzer/pull/64)). Loc
  resolution gains a BSP PVS veto (locvis V6) so positions no longer
  bleed through walls to the nearest loc point; death/spawn handling
  rebuilt on top.
- **API/MCP loc representation**
  ([#65](https://github.com/galfthan/mvd_analyzer/pull/65)). Views
  return loc names by default with an opt-in index mode; analyzer
  errors surface properly through the API.
- **MCP fixes**
  ([#67](https://github.com/galfthan/mvd_analyzer/pull/67)). Array tool
  outputs wrapped for spec compliance; `getItems` filter vocabulary
  corrected.

## 2026-05-16

- **All times become int32 milliseconds** (schema v8,
  [#62](https://github.com/galfthan/mvd_analyzer/pull/62)). Every
  timestamped field migrates from float seconds to the MVD wire
  format's native integer-ms unit, eliminating float drift at
  boundaries.
- **Region control as a normal view**
  ([#63](https://github.com/galfthan/mvd_analyzer/pull/63)). Region
  control re-derives from streams like every other view instead of
  being a parse-time special case.

## 2026-05-15

- **REST API + MCP server** (
  [#61](https://github.com/galfthan/mvd_analyzer/pull/61)). `mvd-api`
  serves analysis over HTTP with a demo cache, and an MCP server
  exposes the same views to AI tooling; repository reorganised into the
  three-module workspace.

## 2026-05-11

- **Streams as canonical storage** (schema v7,
  [#60](https://github.com/galfthan/mvd_analyzer/pull/60)). Per-player
  change streams, intervals, and the native-rate position track replace
  parse-time buckets as the single event-rate source all views derive
  from.

## 2026-05-09

- **Timeline GL/ammo, clean chat text, Go region control** (schema v6,
  [#59](https://github.com/galfthan/mvd_analyzer/pull/59)). Timeline
  gains grenade launcher and ammo tracking, chat messages get a
  markup-stripped `messageClean`, and region control moves from the
  frontend into the Go analyzer.

## 2026-05-08

- **Match in the header**
  ([#57](https://github.com/galfthan/mvd_analyzer/pull/57)). The web UI
  shows the loaded match in the header bar and tab title.
- **Timeline rendering rewrite**
  ([#56](https://github.com/galfthan/mvd_analyzer/pull/56)). Scanline
  rendering fixes resize artifacts and speeds the timeline up.

## 2026-05-07

- **Per-map regions from JSON** (
  [#55](https://github.com/galfthan/mvd_analyzer/pull/55)). Embedded
  per-map region definitions fully replace the auto-detection
  heuristic.

## 2026-05-03

- **Pickups tab**
  ([#54](https://github.com/galfthan/mvd_analyzer/pull/54)). Per-player
  item pickup breakdown in the web UI, with the KTX weapon-pickup
  counter semantics documented.

## 2026-05-02

- **Search tab**
  ([#53](https://github.com/galfthan/mvd_analyzer/pull/53)). Search for
  demos from the web UI, with a reshaped tab layout around it.

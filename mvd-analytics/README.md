# mvd-analytics

Layer 2 of the mvd-analyzer workspace: take an `events.Source` from mvd-reader
(or any other compatible source) and produce a structured `result.Result`
that downstream consumers render, summarise, or feed to an agent.

## What's in the box

- `result/` — the **stable JSON schema** every pipeline run produces.
  Consumers (web UI, CLI, AI agent) should import this package and pin
  against `result.CurrentSchemaVersion`. At v7 the canonical event-rate
  storage is `Streams` (per-player change streams + intervals + native
  position track). v8 stores **every timestamped field** as `int32`
  milliseconds rather than float seconds — the wire format delivers
  ms deltas and integer storage eliminates float-precision drift end
  to end. v9 adds visibility-aware loc attribution (see `locvis/` and
  `bspvis/` below) — field shapes are unchanged but `PlayerStream.Loc`
  no longer reports phantom wall-bleed visits on maps with a BSP. v10
  switches DeathEvent / SpawnEvent to derive primarily from the
  `DF_DEAD` bit in `svc_playerinfo` (broadcast every frame for every
  player) so deaths previously hidden in `dem_stats` blocks addressed
  to a different player slot are no longer dropped — `PlayerStream`
  Spawns/Deaths counts rise and the downstream LocGraph / LocTrails /
  RegionControl / WeaponPickups shift across the new boundaries.
  Public view-layer outputs still emit seconds. Full field reference
  in [RESULT_SCHEMA.md](RESULT_SCHEMA.md).
- `analyzer/` — the `Analyzer` interface, the read-only event/userinfo
  `Context`, the typed `CoreOutputs` bundle that producer analysers
  populate for downstream consumers, and the `Registry` that drives a
  run. `NewDefaultRegistry()` wires up the production analysers split
  into two phases: **core** (`demoinfo`, `identity`, `frag` — the
  producers that fill `CoreOutputs`) finalise first; **derived** (`metadata`, `match`,
  `messages`, `timeline`, `items`, `damage`, `backpacks`, `weapon_pickups`)
  finalise after, with `CoreOutputs` already populated. Six default
  result post-processors run last (victim-named teamkill recovery, time
  normalisation, duel team rewrite, scoreboard kills/deaths/suicides
  correction, locgraph synthesis, region-control classification) — see
  `postprocess.go`
  and `teamkill_telefrag.go`.
- `view/` — **time-parameterised query API** over a finalised
  `*Result`. Six pure functions (`Buckets`, `Events`, `StreamSlice`,
  `StateAt`, `LocTrails`, `RegionControl`) read `result.Streams` and
  produce derived shapes (bucketed timelines, raw stream slices,
  point-in-time state, loc trails, region-control bucket states) at
  the caller's chosen window / fields / reducers. Every entry takes
  at least one time-related option that the caller controls; static
  derivations (`FragResult`, `LocGraphResult`, `MetadataResult`, …)
  don't belong here and are served directly from result fields. Used
  by the CLI's `-view` family of flags and the WASM bridge's
  `getBuckets` / `getEvents` / `getStreamSlice` / `getStateAt` /
  `getLocTrails` / `recomputeRegionControl` exports.
- `loc/` — `.loc` file parser. For native builds the corpus is embedded
  via `//go:embed data/*.loc` (466 maps today); for WASM builds the host
  provides `fetchLocSync` so only the loc for the current demo is
  downloaded.
- `mapgen/` — the Quake 1 BSP reader (`bsp/`) and floor-face extractor
  (`mapgeom/`) used by the mapgen developer tool. The geometry/entity
  extraction is not part of the runtime pipeline — it generates static
  per-map JSON ahead of time — but `bsp/` itself *is* used at runtime by
  `mapclip` to read the `CLIPNODES` collision hull. The BSP
  entities-lump decoder (`bsp/entities.go`) is available for callers
  that want static map-item data, though the item analyzer itself
  derives item state purely from the demo now and requires no map
  preprocessing.
- `mapbsp/` — the single best-effort source of raw per-map BSP bytes at
  analyze time (native dir lookup / WASM `fetchBspSync`), shared by
  `locvis` (visibility) and `mapclip` (floor height).
- `mapclip/` — worldspawn player clip hull + downward floor trace that
  fills `PositionTrack.H`. See "Floor height" below.
- `diagnostic/` — opt-in integration harness that runs a demo corpus
  through the parser in warning-collection mode and runs data-quality
  checks on the analysis result.
- `cmd/mapgen/` — developer tool: reads BSP + loc files, writes per-loc
  floor-polygon JSON for the web viewer
  (`mvd-web/static/maps/<name>.json`). Geometry version 4: triangles
  carry per-vertex x,y,z (9 floats each) so the map tab can render the
  floor plan in 3D, and optional
  `liquids` (water/slime/lava volume meshes from the engine's turbulent
  `*` textures) and `submodels` (brush-model lifts/doors, keyed by their
  `*id` index and posed at runtime from the result's mover streams) (v4).
  (A v3 top-level `walls` list fed a since-removed occluding "solid"
  render; walls are still classified for diagnostics but no longer
  emitted.) Degenerate zero-area fan triangles are dropped. Extraction thresholds
  (floor slope, roof cap, origin height) are tunable via
  `mapgeom.Params` / `BuildParams`. The optional
  `-demos <dir>` flag turns on usage-based pruning: every `.mvd`/`.mvd.gz`
  under the directory is analyzed (a fresh registry per demo), the floor
  surface beneath each grounded, non-swimming sample
  (`(X, Y, Z − PlayerFeetOffset − H)`) is collected per map, and floor
  faces no sample lands on (within `-prune-xy-tol`, default
  `mapclip.FootprintReach` = 24, and `-prune-z-tol`, default 16) are
  dropped. Pruned files carry a `pruned` provenance block; maps with no
  matching demos emit unpruned. The committed 3D corpus (the ~18 maps
  with BSPs) is pruned this way against recent competitive games —
  [`scripts/prune-demos.tsv`](../scripts/prune-demos.tsv) records the
  exact map → mode → hub gameIds used, so the prune is reproducible.
- `cmd/qw-analyze/` — CLI consumer. `qw-analyze demo.mvd` produces Result
  JSON; `-format md` produces a human summary; `-format events` dumps the
  raw event stream; `-bulk -out-dir dir/` processes a directory.

## Pipeline architecture

A run flows through three concentric loops over a single pass of the
event stream, then a post-pass on the assembled `Result`:

```
  events.Source ──▶ Init (core, then derived)
                    │
                    ▼
            ┌─ for each event ─────────────────────────┐
            │   registry sets ctx.{ServerData,         │
            │     Players, FragsBySlot} from event     │
            │   for a in core    : a.OnEvent(event)    │
            │   for a in derived : a.OnEvent(event)    │
            └──────────────────────────────────────────┘
                    │
                    ▼
            ┌─ Phase 1: Finalize core ────────────────┐
            │   demoinfo.Finalize → result.DemoInfo   │
            │   demoinfo.PopulateCore →               │
            │     co.{DemoInfo, Names, Slots}         │
            │   frag.UseCoreOutputs(co) // reads Names│
            │   frag.Finalize → result.Frags          │
            │   frag.PopulateCore → co.FragEntries    │
            └─────────────────────────────────────────┘
                    │
                    ▼
            ┌─ Phase 2: Finalize derived ─────────────┐
            │   each derived analyser:                │
            │     a.UseCoreOutputs(co)  // optional   │
            │     a.Finalize(result)                  │
            │   (no analyser writes to co here)       │
            └─────────────────────────────────────────┘
                    │
                    ▼
            ┌─ Result post-processors ─────────────────┐
            │   normalizeMatchRelativeTimes(result)    │
            │   normalizeDuelTeams(result)             │
            │   buildLocGraphPost(result)              │
            └──────────────────────────────────────────┘
                    │
                    ▼
                 *Result
```

Each run records per-phase wall-clock durations (init, event pass, every
analyzer's `Finalize`, every post-processor) into `Registry.PhaseTimings`
for instrumentation. It is repopulated on each `Analyze*` call and is
**not** part of the `Result` JSON schema; the WASM build surfaces it to
the browser console (see `mvd-web/README.md`). CLI/API callers can ignore
it.

### What goes where

| Slice | Default analysers | Why |
|---|---|---|
| **Core** | [`demoinfo`](analyzer/demoinfo.md), [`identity`](analyzer/identity.md), [`frag`](analyzer/frag.md) | Implement `CoreProducer`. Everything they emit (`DemoInfo`, `Names`, `Slots`, `Sessions`, `FragEntries`) is the canonical input some derived analyser consumes during its own Finalize. |
| **Derived** | [`metadata`](analyzer/metadata.md), [`match`](analyzer/match.md), [`messages`](analyzer/messages.md), [`timeline`](analyzer/timeline.md), [`items`](analyzer/items.md), `damage`, `map_entities`, [`backpacks`](analyzer/backpacks.md), [`weapon_pickups`](analyzer/weapon_pickups.md) | Either implement `CoreConsumer` (read `co.*`) or are independent peers. They never write to `CoreOutputs`. `map_entities` loads the static `mapents` corpus by map name. `damage` reconstructs per-hit damage from the KTX `mvdhidden_dmgdone` stream and reads `co.DemoInfo` for the scoreboard cross-check. |
| **Post-processors** | `recoverTelefragTeamkills`, `normalizeMatchRelativeTimes`, `duelTeamNormalize`, `scoreboardStatsPost`, `locGraphPost`, `regionControlPost` | Operate on the assembled `Result` after every Finalize has run. Order matters within the slice (telefrag recovery runs before time normalisation so positions/frag-events/obituaries share the demo-relative clock; `scoreboardStatsPost` copies the frag-log-corrected kills/deaths/suicides onto `match.players`, joining on the final display name after teamkill recovery; time normalisation must run before locgraph). |
| **Shelved** | [`tracks`](analyzer/tracks.md) | Code present, not registered. Awaiting a mvd-web consumer. |

Each analyser has a one-page README in `analyzer/` covering what it
consumes / produces, key algorithm steps, and known limitations. Read
those before adding a new analyser or chasing a data-quality issue
specific to one of them.

### Why the split

The two-phase ordering exists so cross-analyser dependencies are
expressed as types, not registration discipline. Before the cleanup,
adding a derived analyser that read `ctx.FragEntries` only worked if
the author knew to register it after `frag` — there was no compile-time
guard. Now the contract is:

- Anything you write into `CoreOutputs` requires `CoreProducer` and
  `RegisterCore`. The slice is small by design.
- Anything you read from `CoreOutputs` requires `CoreConsumer`. The
  registry guarantees `co` is fully populated before any derived
  Finalize runs.
- Anything that operates on the assembled `Result` is a
  `ResultPostProcessor`, not an analyser.

### CoreOutputs shape

```go
type CoreOutputs struct {
    DemoInfo    *DemoInfoResult            // KTX JSON metadata
    Names       *NameTable                 // exact + normalized name → team
    Slots       map[int]SlotInfo           // per-slot resolved display name + team (final occupant)
    Sessions    map[int][]ResolvedSession  // per-slot, time-sorted, reconnect-unified occupancies
    FragEntries []FragEntry                // canonical frag log
}
```

Producers populate fields via `PopulateCore`; consumers read whatever
they need via the field names directly, or via tiny helpers like
`co.SlotName(slot)`.

**Reconnect-aware resolution.** `Slots` / `SlotName(slot)` map a wire
slot to its *final* occupant — wrong when a player disconnects and
reconnects onto a different slot mid-match and their old slot is reused
(or stamped with a late userinfo name). The `identity` analyser builds
`Sessions` (one entry per contiguous slot occupancy, folded into one
canonical identity across reconnects — KTX `rejoins`/`reenters` prints
first, then a per-session demoinfo login/name join, then a bare-demo
name fallback). Any Finalize site that has an event timestamp should
resolve via `co.SlotIdentityAt(slot, tMs)` rather than `SlotName`, so a
player's pre-reconnect events stay attributed to them. The streams
output groups per-slot builders by `ResolvedSession.IdentityKey` to emit
one merged `PlayerStream` per player.

## Using mvd-analytics

### Run the default pipeline over a demo file

```go
import (
    "github.com/mvd-analyzer/mvd-analytics/analyzer"
    mvdsource "github.com/mvd-analyzer/mvd-reader/source/mvd"
)

src, err := mvdsource.Open("demo.mvd.gz")
if err != nil { return err }
defer src.Close()

reg := analyzer.NewDefaultRegistry()
res, err := reg.AnalyzeSource(src, "demo.mvd.gz")
// res is *result.Result
```

Three equivalent entry points:

| Method | Input | When to use |
|---|---|---|
| `Analyze(path)` | file path | You have a local file |
| `AnalyzeReader(r, name)` | `io.Reader` | You have bytes in hand (WASM, HTTP body) |
| `AnalyzeSource(src, name)` | `events.Source` | You have a non-MVD source |

All three fill the same `Result`. `AnalyzeSource` is the source-agnostic
primitive; the other two wrap an MVD source around the input.

### Custom pipeline

Drop or add analyzers:

```go
reg := analyzer.NewRegistry()
reg.Register(analyzer.NewDemoInfoAnalyzer())
reg.Register(analyzer.NewMatchAnalyzer())
// Skip frag/timeline/etc — only match summary needed
res, err := reg.AnalyzeSource(src, "demo.mvd.gz")
```

## The Result schema

For the full field-by-field reference, see
[**RESULT_SCHEMA.md**](RESULT_SCHEMA.md). The sections below cover the
high-level shape and the noteworthy design decisions; the reference
doc is the source of truth for every JSON key and its intent.

`result.Result` has one sub-result per analyzer:

```go
type Result struct {
    SchemaVersion    int
    FilePath         string
    Match            *MatchResult             // match summary
    Frags            *FragResult              // frag tally + individual entries
    Messages         *MessagesResult          // frag + chat stream for timeline
    DemoInfo         *DemoInfoResult          // KTX authoritative stats
    TimelineAnalysis *TimelineAnalysisResult  // bucketed player state + region control
    Metadata         *MetadataResult          // serverinfo + match settings
    LocGraph         *LocGraphResult          // loc-to-loc movement graph
    Items            *ItemsResult             // per-item pickup / respawn timeline (all MVD sources)
    MapEntities      *MapEntitiesResult       // static map layout from the BSP entity corpus (mapents)
    Backpacks        []BackpackDrop           // RL/LG backpack drops (from KTX //ktx drop hint)
    WeaponPickups    []WeaponPickup           // slot-weapon pickups + kills-before-next-death metric
    Errors           []string
}
```

Each sub-type is defined in its own file under `result/`. The JSON shape
is the wire contract with every consumer; breaking changes bump
`CurrentSchemaVersion` (currently `18`). For "how long was the match"
read `Match.Duration` (float, parser-derived) or `DemoInfo.Duration`
(integer, KTX-authoritative); the legacy top-level `duration` was
removed in v6.

### Items result

`result.Items` carries one `ItemTimeline` per observed item entity
(every armor, health pack, weapon, ammo box, megahealth, and
powerup). Each timeline has deterministic name (`ra`,
`mh_1`/`mh_2`, `rl_1`/`rl_2`, `quad`, …), the server edict number,
world position, nearest loc name, and an ordered `Phases` list:

```go
type ItemPhase struct {
    AvailableFrom float64 // item became available at this time
    TakenAt       float64 // someone picked it up
    TakenBy       string
    Team          string
    RespawnAt     float64 // when it came back up (observed, not predicted)
}
```

Sources: `ItemAnalyzer` consumes `ItemSpawnEvent` and `ItemStateEvent`
that the parser synthesises from `svc_spawnbaseline` +
`svc_packetentities` / `svc_deltapacketentities` — the wire-level
entity-state stream. Item classification uses standard Quake 1 model
paths (no KTX-specific data), so *every* item with a visible model
gets tracked on *any* demo source, including ktpro, CustomTF, or
non-KTX servers. No map preprocessing is required. `RespawnAt` is
observed directly, so MH rot (which varies with damage taken) falls
out naturally — no special case.

`TakenBy` attribution uses a **layered signal pipeline** rather than
nearest-player snapping. The four layers, in priority order:

1. **`ItemPickupHintEvent`** (`//ktx took`) — keyed by entNum.
   Authoritative for KTX demos; covers MH, armors, weapons, powerups.
2. **`ItemPickupPrintEvent`** — per-client `svc_print` "You got the X"
   / "You receive N health" strings. Authoritative when present, but
   `mvdsv` filters PRINT_LOW prints by the picker's `messagelevel`
   cvar; competitive players widely use `msg 2` so this signal is
   partial in practice. Covers the same set as L1 plus H15 / H25 /
   ammo boxes when present.
3. **Stat-delta evidence** — diffs each `StatUpdateEvent` against a
   per-slot snapshot. IT_* bit 0→1 transitions identify armor /
   weapon / powerup pickups; positive STAT_HEALTH deltas in [1, 25]
   identify small healths (KTX caps health at 100, so partial-cap
   pickups give less than the nominal +15 / +25 — the kind filter at
   synthesis time disambiguates h15 vs h25); positive STAT_AMMO_*
   deltas identify ammo boxes. Universal fallback that works on every
   demo regardless of client config.
4. **Distance corroborator** — last resort. Iterates slots whose last
   `PlayerPositionEvent` is within 250 ms of the pickup time and
   returns the closest within 256² units squared of the item origin;
   refuses to attribute when no candidate is in radius.

A pickup with no signal in any layer gets `TakenBy=""` (omitempty in
JSON). Distance is intentionally last because in QW the `findradius` /
`touch` resolution order for simultaneous touches is effectively
random rather than nearest-wins, so a nearest-player heuristic
mis-attributes contested pickups even when the geometry looks
unambiguous. See [`PICKUP-SIGNALS-INVESTIGATION.md`](../PICKUP-SIGNALS-INVESTIGATION.md)
for the underlying protocol analysis.

**Insta-regrab synthesis**: when an item respawns and is touched again
within the same server tick the wire never emits a "visible"
transition, so the entity-state trigger items.go usually relies on is
silent. The analyser closes that gap with two complementary synthesis
paths — hint-driven (immediate, when `//ktx took` arrives for an
already-taken entity; covers MH, armors, weapons, powerups) and
stat-delta-driven (predicted respawn time + matching stat evidence +
proximity check; covers small healths and ammo). Synthetic phases
carry `attributionSource = "hint"` or `"synthetic"` internally and
are validated against KTX's `demoInfo.players[*].items[*].took` by
[`pickup_invariant_test.go`](analyzer/pickup_invariant_test.go) — the
hub corpus matches exactly on 9 of 10 demos (the lone residual is two
same-magnitude small healths contested in one frame). See
[`analyzer/items.md`](analyzer/items.md#insta-regrab-synthesis) for
the full algorithm.

Residual limitation: when an item respawns and is immediately
regrabbed within the same server tick AND no synthesis signal fits
(very rare — typically a damage hit in the same frame as a small
heal, masking the stat delta), we don't record a phase for that
cycle. The resulting phase will span the whole contested window
(e.g. "RA taken at 31s, respawn observed at 91s" means the RA was
never practically available in that 60 s window).

### Backpacks

`result.Backpacks` is a flat list of RL and LG backpack drops,
driven by `BackpackAnalyzer`. Each entry is emitted when KTX fires
its `//ktx drop <ent> <items> <player_ent>` STUFFCMD_DEMOONLY
directive (ktx/src/items.c:2740). The hint is the authoritative
source — it fires exactly once per real drop, with weapon and
dropper slot already attributed, so the analyzer doesn't guess.

Coverage caveats:

- **RL and LG only — drops *and* pickups.** KTX only emits `//ktx
  drop` and `//ktx bp` for packs containing RL or LG, and on
  competitive demos there is no other authoritative wire signal
  for non-RL/LG packs (`BackpackPickupPrintEvent` would help, but
  `SV_ClientPrintf` strips PRINT_LOW prints before the MVD write
  whenever the picker has `msg >= 1`, and competitive players
  overwhelmingly run `msg 2`). See
  [`mvd-reader/MVD_FORMAT.md` → Practical gap — non-RL/LG backpack
  pickups on competitive demos](../mvd-reader/MVD_FORMAT.md#svc_stufftext-9)
  for the full mechanics. Net effect: SSG/NG/SNG/GL/ammo-only
  packs do not appear in `result.Backpacks`, and corresponding
  pickups do not appear in `result.WeaponPickups`.
- **Pickup side lives in `WeaponPickups`, not `Backpacks`.**
  `BackpackAnalyzer` only records drops. The pickup side — who
  grabbed the pack, whether they already owned the weapon, how many
  frags they scored with it before dying — is emitted by
  `WeaponPickupsAnalyzer` and exposed as `result.WeaponPickups`.
  Frontends join the two lists by `BackpackDrop.EntNum` ==
  `WeaponPickup.BackpackEnt` (paired with `dropTime` to disambiguate
  recycled edict numbers).

```go
type BackpackDrop struct {
    Time   float64    // drop time (match-relative)
    Player string     // dropper display name
    Team   string
    Weapon string     // "rl" or "lg"
    Origin [3]float32 // dropper's position at hint time
    Loc    string     // nearest named loc
    EntNum int        // server edict of the backpack entity
}
```

### Weapon pickups

`result.WeaponPickups` is a flat, time-ordered list of slot-weapon
acquisition events produced by `WeaponPickupsAnalyzer`. Each entry
pairs a pickup with its effectiveness outcome: did the picker
already own the weapon, and how many frags did they score with it
before their next death.

Signal sources, both KTX STUFFCMD_DEMOONLY hints (authoritative, not
filtered by the `messagelevel` cvar):

- **World pickups** — `ItemPickupHintEvent` (`//ktx took`,
  ktx/src/items.c:1048). `ItemSpawnEvent` provides the entNum → Kind
  map for classification. Only weapon kinds (`rl`, `lg`, `gl`, `ssg`,
  `sng`, `ng`) are recorded; armor / health / powerup hints are
  ignored.
- **Backpack pickups** — `BackpackPickupHintEvent` (`//ktx bp`,
  ktx/src/items.c:2471), paired with the earlier
  `BackpackDropHintEvent` to attribute weapon and dropper. Only RL
  and LG packs emit the hint; other pack classes are absent here.

`HadBefore` reads the picker's STAT_ITEMS bit at pickup time. The
analyzer shadows STAT_ITEMS live; the server sends the STAT_ITEMS
update on the packet after the pickup hint, so the cached bitfield
is the pre-pickup state.

`Kills` is credited only to pickups that actually granted the
weapon (`HadBefore=false`). Redundant grabs (`HadBefore=true` — the
picker already held the weapon) always report 0 kills, because
those kills would have happened anyway with the weapon the player
already had. Each frag goes to the most-recent granting pickup
whose window `(Time, NextDeathTime]` contains the frag time, drawn
from `ctx.FragEntries` (so `WeaponPickupsAnalyzer` must run after
`FragAnalyzer`). Teamkills and suicides are excluded.
`NextDeathTime` is 0 when the picker never dies before match end —
kills are then unbounded on the right. The redundant-grab rows
stay in the output so frontends can still surface denial semantics
(the `enemy RL` / `xfer RL` chips), they just carry 0 kills.

```go
type WeaponPickup struct {
    Time          float64 // pickup time (match-relative)
    Player        string  // picker display name
    Team          string
    Weapon        string  // "rl","lg","gl","ssg","sng","ng"
    Source        string  // "world" | "backpack"
    HadBefore     bool    // picker already owned the weapon
    Kills         int     // kills with Weapon before NextDeathTime
    NextDeathTime float64 // 0 if picker never died before match end

    // Backpack-source only:
    BackpackEnt int     // join key with BackpackDrop.EntNum
    Dropper     string
    DropperTeam string
    DropTime    float64
}
```

**Known limitation — backpack pickups undercount KTX for SSG/SNG/GL/NG.**
KTX only emits the `//ktx bp` backpack hint for RL and LG packs
(ktx/src/items.c:2471); there is no hint for a super-shotgun,
super-nailgun, nailgun or grenade-launcher taken off a dropped pack.
World (spawn) pickups of every weapon are captured via `//ktx took`, so
per-weapon counts reconcile with KTX's `weapons.<w>.pickups.spawn-taken`,
but they fall short of `total-taken` by exactly the backpack grabs for
those weapons — systemically across all players (RL/LG reconcile fully).
Closing this needs either a wider KTX hint or synthesising the SSG/SNG/GL
backpack pickup from the picker's STAT_ITEMS bit flip at backpack-touch
time; until then treat SSG/SNG/GL/NG pickup totals as world-pickup counts,
not total acquisitions.

## Writing a new analyzer

Implement the `analyzer.Analyzer` interface. Each analyzer writes
its slice of `result.Result` directly from `Finalize`:

```go
type MyAnalyzer struct {
    ctx *analyzer.Context
}

func (a *MyAnalyzer) Name() string { return "my" }

func (a *MyAnalyzer) Init(ctx *analyzer.Context) error {
    a.ctx = ctx
    return nil
}

func (a *MyAnalyzer) OnEvent(ev events.Event) error {
    switch e := ev.(type) {
    case *events.PrintEvent:
        _ = e
    }
    return nil
}

func (a *MyAnalyzer) Finalize(result *analyzer.Result) error {
    result.My = &MyResult{ /* ... */ }
    return nil
}
```

If your analyzer needs to read another analyzer's output (frag entries,
demoinfo player table, …), implement `CoreConsumer`. The registry
hands you the running `*CoreOutputs` immediately before your `Finalize`
runs:

```go
type MyAnalyzer struct {
    ctx  *analyzer.Context
    core *analyzer.CoreOutputs
}

func (a *MyAnalyzer) UseCoreOutputs(co *analyzer.CoreOutputs) {
    a.core = co
}
```

If your analyzer *produces* a field that other analyzers will consume,
implement `CoreProducer`. The registry calls `PopulateCore` after your
`Finalize` so analysers registered later in the pipeline see your
output:

```go
func (a *MyAnalyzer) PopulateCore(co *analyzer.CoreOutputs) {
    co.MyOutput = a.computed
}
```

Then add the type to `result/` and register the analyzer. Choose
`RegisterCore` for producers (anything implementing `CoreProducer`) and
`RegisterDerived` for everything else:

```go
reg := analyzer.NewDefaultRegistry()
reg.RegisterDerived(&MyAnalyzer{})
```

Core analysers finalize before any derived analyser. Within each slice
registration order is preserved, so a later core entry can read a
field populated by an earlier core entry (e.g. Frag reads `co.Names`
produced by DemoInfo).

If your analyzer is a post-pass that operates on the assembled Result
(not on the event stream), register it via
`reg.RegisterPostProcessor(func(*Result, *CoreOutputs))` instead.
Built-ins like `normalizeMatchRelativeTimes`, `normalizeDuelTeams`,
and `BuildLocGraph` are wired this way (see `analyzer/postprocess.go`).

## Loc files

`loc.LoadForMap(name)` returns a `*Finder` with the named loc points for
that map. Native builds read from the embedded corpus; WASM callers hit
the JS host via `fetchLocSync`. `loc.SetLocDir(dir)` overrides the
native source (used by `cmd/mapgen` when pointing at a working copy).

## Visibility-aware loc attribution (`locvis` + `bspvis`)

Analyzers consume loc attribution via [`locvis.LoadForMap`](locvis/)
rather than `loc.LoadForMap` directly. `locvis.Finder` wraps `loc.Finder`
with a per-map BSP-backed visibility veto:

- **V1** (the bare `loc.Finder.FindNearest`) picks the geometrically
  closest loc-point and is unaware of intervening walls. It produces
  brief "wall-bleed" loc visits when a wall sits between the player
  and the chosen loc.
- **V6** picks the Euclidean-nearest loc whose containing BSP leaf is
  in the player's PVS row. Falls back to V1 if no loc is visible from
  the player's leaf (or the wiggle can't escape solid). Validated on
  demo 216406 (e1m2): 178 wall-bleed spans corrected, zero false
  positives.

Default: `AlgoV6`. Change `ActiveAlgorithm` in
[`locvis/locvis.go`](locvis/locvis.go) and rebuild to A/B against V1.
An earlier raycast variant (V6a) was prototyped but dropped — it was
strictly more expensive than V6 and produced false positives in the
research corpus.

Implementation note: at `LoadForMap` we precompute, for every non-
solid leaf L, the list of loc indices whose containing leaf is in L's
PVS row (`leafVisLocs[L]`). Each `FindNearest` call is then a
`PointInLeaf` for the player + a linear scan over a pre-filtered
candidate list (M ≈ 30–80 on competitive maps) — no per-query sort,
no per-query PVS decompression. The preprocessing cost is one-shot
per map load (~300 µs on dm6-class maps).

When no BSP is available for the current map (file missing, parse
error, WASM host did not install `fetchBspSync`), `locvis.Finder`
degenerates to `loc.Finder` — bit-identical V1 behaviour. The full
pipeline therefore always works whether or not `make bsps` has been
run.

The BSP parser is in [`bspvis/`](bspvis/) (Q1 v29 / 2PSB / BSP2,
~1000 LOC, no cgo). It is intentionally separate from
[`mapgen/bsp/`](mapgen/bsp/) — that package reads the geometry lumps
(vertices, edges, faces, models) for the floor-polygon `mapgen` tool
*and the `CLIPNODES` collision hull for `mapclip`*; bspvis reads the
visibility lumps (planes, nodes, leaves, visdata).

Both BSP consumers pull their bytes from one place,
[`mapbsp.LoadBytes`](mapbsp/) (native: `SetBspDir` / `$MVDA_BSP_DIR` /
`./bsps`; WASM: host `fetchBspSync`), so a deployment provisions BSPs
once and both features light up — or degrade — together.

Background and validated case study:
[`experiments/locattr/V2b-V6-HANDOFF.md`](../experiments/locattr/V2b-V6-HANDOFF.md).

## Floor height (`mapclip`)

The per-sample height of each player above the floor beneath them —
`PositionTrack.H` (`pos.h`, schema v24) — is produced by
[`mapclip`](mapclip/). At finalize the timeline analyzer loads the map's
worldspawn **player clip hull** (hull 1) straight from the BSP
`CLIPNODES` lump via `mapclip.LoadForMapWithMovers` (same `mapbsp` byte
source as `locvis`), then traces straight down from every native-rate
position through it, reproducing the server's `PM_CategorizePosition`
floor test (`mvdsv/src/cmodel.c` `RecursiveHullTrace`). Because it
traces the collision hull — the rendering geometry inflated by the
32×32×56 player box — the floor is width-aware, multi-level, and
slope-correct, with no edge artifacts.

Since schema v27 the trace scene also includes **moving brush-model
entities**: the parser streams every inline `"*N"` submodel entity
(lift, door, train) as `MoverSpawn`/`MoverState` events, the loader
builds hull 1 of each referenced submodel, and the floor pass poses
those hulls at the entity's origin for the sample's timestamp
(`mapclip.HeightAboveFloorBoxScene`, mirroring the client's
`CL_SetSolidEntities` physent setup) — the highest floor across all
hulls wins. A player riding the dm2 RA lift reads ~0 instead of the
height to the shaft floor far below. Since schema v32 those same mover
tracks are also exported as `streams.movers` (`MoverStream`, one per
brush-model entity) so the web map can animate lifts/doors at their
demo-streamed poses.

Since schema v26 the height is **footprint-aware** (`HeightAboveFloorBox`):
rather than the single origin column, it traces a 3×3 grid of columns
sampled ±8 around the origin and keeps the **highest** floor any of them
finds. The hull is already inflated by the ±16 box, so the centre column
alone is the true 32-wide box; the ±8 ring only adds a small safety band
(effective reach ~48 wide). A player skimming a ledge or well rim has their origin briefly
over the pit while the box overhangs the rim — a single-column trace there
plunges to the floor far below and reads a huge height; the footprint
query finds the near rim and reads small. (This is what stopped the well
rim of anwalked's RA from logging a 553-unit "airgib" that was really a
rim skim.) The single-column primitive remains as `HeightAboveFloor`.

`h` reads ~0 when grounded and grows during a jump or airborne hit
(airgib); the player-feet offset (24) is folded in so the value is 0 on
the ground without the consumer knowing the hull dimensions (the
absolute floor, if wanted, is `z − 24 − h`). `result.NoFloor` marks
samples with no floor to measure from: over a void/pit, or an
embedded/zero origin. There is no generated corpus to keep in sync; a
map update is just a new `.bsp`.

Since schema v28 the same pass also classifies each sample's **liquid
state** into `PositionTrack.Lq` (`pos.lq`, packed `(type<<2)|level`) by
mirroring the engine's `PM_CategorizePosition` probes against the
render BSP (`bspvis.WaterLevel`), and liquids participate in `h`: a
submerged sample (level ≥ 1) reads `h = 0` by definition, and a dry
sample airborne above water/slime/lava measures down to the liquid
surface (`bspvis.LiquidSurfaceBelow`) when it is the highest support.
See [RESULT_SCHEMA.md](RESULT_SCHEMA.md) for the `lq` vocabulary.

## View direction (`PositionTrack.VP` / `VYa`)

Each native-rate position sample also carries the player's **view
direction** (pitch, yaw) — `pos.vp` / `pos.vya`, schema v31. These come
free from the same `svc_playerinfo` message as x/y/z (no BSP needed):
the parser keeps the **raw `angle16` wire shorts** losslessly in
`PlayerPositionEvent.Angles`, carrying forward omitted delta-compressed
components so every position sample has the current full view state.
The timeline analyzer records pitch/yaw alongside x/y/z. Decode to
degrees with `uint16(v) * 360/65536` (values `[0,360)`, pitch > 180° =
looking up); roll is dropped (the server zeroes it). See MVD_FORMAT.md
"View-angle semantics" for the wire derivation
(the `*−3` model-pitch recovery) and RESULT_SCHEMA.md for the forward-
vector formula.

The view-layer query API and CLI let consumers select position channels
independently — `pos` is strictly x/y/z, and `view` / `hgt` / `lq` /
`vel` are opt-in field codes. The CLI mirrors this:
`-include positions,view,height,liquid,velocity` each keep their column
set in the full-result JSON (default strips the whole heavy track).

## Velocity (`PositionTrack.VX` / `VY` / `VZ`)

Per-sample velocity in Quake units/sec (`pos.vx`/`vy`/`vz`, schema v32)
is **derived** from the position columns in `resolveVelocities` (the
finalize pass, per-slot before the reconnect merge, no BSP needed) by a
central-difference estimator — `v[i] = (p[i+1]-p[i-1]) / (t[i+1]-t[i-1])`,
second-order accurate, one-sided at a segment end. The estimator refuses
to differentiate across a discontinuity that isn't real movement: a
respawn (a spawn timestamp between the samples), an abnormal time gap
(`velGapCapMs`, death / pause / reconnect), or a teleporter-sized
displacement (`velTeleportSpeedUps`, above the server's `sv_maxvelocity`
clamp) — each reads ~0 instead of a tens-of-thousands-ups spike. Since
schema v33 the source positions are `float32` (the wire-native sub-unit
origin, no longer rounded to whole units), so the raw derivative is
sub-unit precise — the old ±1-unit quantization noise is gone; smooth
client-side only for a softer curve. Exposed via the opt-in `vel` field
code (and `-include velocity`).

## Running tests

```bash
go test ./mvd-analytics/...
```

Three layers exercise different things:

1. **Per-analyzer unit tests** (`*_test.go` next to each analyzer) drive
   each analyzer with synthetic event streams and assert on its
   `Finalize()` output. No MVD bytes; pure-Go, ~milliseconds total.
2. **Golden corpus** (`analyzer/golden_test.go`) runs the full pipeline
   against a manifest of hub.quakeworld.nu game IDs in
   `testdata/corpus.json`. On first run it downloads each demo into
   `testdata/cache/<gameId>.mvd.gz` (gitignored) and pins the
   serialised `Result` against `testdata/golden/<label>.json`. The
   manifest currently ships with ten demos (three 1on1, three 2on2,
   four 4on4); `t.Skip` keeps `make test` green if it is ever emptied.
   Regenerate goldens after an intentional change:

   ```bash
   go test ./mvd-analytics/analyzer/... -run TestGoldenCorpus -args -update-golden
   ```

   (Use `./mvd-analytics/analyzer/...`, not the wider `./mvd-analytics/...`
   — `-update-golden` is registered only in this test package and
   wider scopes fail in `mapgen` with "flag provided but not defined".)

   Golden output depends on the curated BSP corpus: the package's
   `TestMain` (`setup_test.go`) points `MVDA_BSP_DIR` at the repo-root
   `bsps/` directory, which feeds both the locvis visibility filter
   (loc names) and the mapclip floor-height column (`pos.h`,
   `airgibs`). Run `make bsps` before regenerating. If a demo's BSP is
   not resolvable the test no longer degrades silently — it **skips**
   that demo in compare mode and **hard-fails** an `-update-golden`
   run, so a machine without the BSP corpus can't overwrite a good
   golden with V1/no-height data.

   `filePath` is stripped before comparison (per-machine cache path).
   At schema v7 the parse-time `highResBuckets` is gone; the canonical
   storage is `streams` (per-player change streams + intervals + native
   position track). Per-player time series in `streams.players[]` are
   sliced to three 15 s windows (`[0, 15]`, `[60, 75]`, last 15 s)
   before comparison — `sampleStreams` in `golden_test.go` handles
   this so a 4on4 demo's ~10 MB native position track doesn't bloat
   the committed corpus. On top of that, the dense per-sample
   position/view track (`streams.players[].pos`: x/y/z, vp/vya, h/lq/li,
   velocity) is pinned on only two demos — a full 4on4 and a duel
   (`densePosDemos`) — and dropped from the other eight by
   `dropPositionTracks`, since the emitter / BSP-trace code is identical
   across demos. That keeps the committed corpus ~13 MB (was ~34 MB)
   while still exercising the position pipeline on two demos and every
   aggregate on all ten. Everything else — `locGraph`, `schemaVersion`,
   ammo counts,
   frag totals, weapon stats, items, powerup events — is pinned in
   full, so any unintended drift surfaces. (The `locGraph` slices
   are sorted in `BuildLocGraph` for run-to-run determinism;
   map-keyed sub-objects already serialise alphabetically.)

3. **Diagnostic corpus** (`diagnostic/diagnostic_test.go`) is opt-in
   and complementary — it runs data-quality invariants
   (frag-total parity, impossible stat values, …) rather than pinning
   output. Drop demos into `mvd-analytics/diagnostic/testdata/` to enable:

   ```bash
   cp ~/quake/demos/*.mvd.gz mvd-analytics/diagnostic/testdata/
   go test -v -run TestDiagnosticParseDemos ./mvd-analytics/diagnostic/
   ```

## Module boundary

mvd-analytics depends on mvd-reader (for events + Source) and the standard
library. It does not depend on mvd-web — consumers like mvd-web depend
on *it*, not the other way around.

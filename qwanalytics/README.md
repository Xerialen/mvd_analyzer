# qwanalytics

Layer 2 of the mvd-analyzer workspace: take an `events.Source` from qwdemo
(or any other compatible source) and produce a structured `result.Result`
that downstream consumers render, summarise, or feed to an agent.

## What's in the box

- `result/` — the **stable JSON schema** every pipeline run produces.
  Consumers (web UI, CLI, AI agent) should import this package and pin
  against `result.CurrentSchemaVersion`.
- `analyzer/` — the `Analyzer` interface, the read-only event/userinfo
  `Context`, the typed `CoreOutputs` bundle that producer analysers
  populate for downstream consumers, and the `Registry` that drives a
  run. `NewDefaultRegistry()` wires up nine production analysers split
  into two phases: **core** (`demoinfo`, `frag` — the producers that
  fill `CoreOutputs`) finalise first; **derived** (`metadata`, `match`,
  `messages`, `timeline`, `items`, `backpacks`, `weapon_pickups`)
  finalise after, with `CoreOutputs` already populated. Four default
  result post-processors run last (time normalisation, duel team
  rewrite, locgraph synthesis, denials/hoovers derivation) — see
  `postprocess.go` and `denials.go`.
- `loc/` — `.loc` file parser. For native builds the corpus is embedded
  via `//go:embed data/*.loc` (466 maps today); for WASM builds the host
  provides `fetchLocSync` so only the loc for the current demo is
  downloaded.
- `mapgen/` — the Quake 1 BSP reader (`bsp/`) and floor-face extractor
  (`mapgeom/`) used by the mapgen developer tool. Not part of the
  runtime pipeline — it generates static per-map JSON ahead of time.
  The BSP entities-lump decoder (`bsp/entities.go`) is available for
  callers that want static map-item data, though the item analyzer
  itself derives item state purely from the demo now and requires no
  map preprocessing.
- `diagnostic/` — opt-in integration harness that runs a demo corpus
  through the parser in warning-collection mode and runs data-quality
  checks on the analysis result.
- `cmd/mapgen/` — developer tool: reads BSP + loc files, writes per-loc
  floor-polygon JSON for the web viewer
  (`qw-web/static/maps/<name>.json`).
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
            │   buildDenialsPost(result)               │
            └──────────────────────────────────────────┘
                    │
                    ▼
                 *Result
```

### What goes where

| Slice | Default analysers | Why |
|---|---|---|
| **Core** | [`demoinfo`](analyzer/demoinfo.md), [`frag`](analyzer/frag.md) | Implement `CoreProducer`. Everything they emit (`DemoInfo`, `Names`, `Slots`, `FragEntries`) is the canonical input some derived analyser consumes during its own Finalize. |
| **Derived** | [`metadata`](analyzer/metadata.md), [`match`](analyzer/match.md), [`messages`](analyzer/messages.md), [`timeline`](analyzer/timeline.md), [`items`](analyzer/items.md), [`backpacks`](analyzer/backpacks.md), [`weapon_pickups`](analyzer/weapon_pickups.md) | Either implement `CoreConsumer` (read `co.*`) or are independent peers. They never write to `CoreOutputs`. |
| **Post-processors** | `normalizeMatchRelativeTimes`, `duelTeamNormalize`, `locGraphPost`, [`buildDenialsPost`](analyzer/denials.md) | Operate on the assembled `Result` after every Finalize has run. Order matters within the slice (time normalisation must run before locgraph; denials reads both Items and LocGraph so it runs last). |
| **Shelved** | [`tracks`](analyzer/tracks.md) | Code present, not registered. Awaiting a qw-web consumer. |

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
    DemoInfo    *DemoInfoResult        // KTX JSON metadata
    Names       *NameTable             // exact + normalized name → team
    Slots       map[int]SlotInfo       // per-slot resolved display name + team
    FragEntries []FragEntry            // canonical frag log
}
```

Producers populate fields via `PopulateCore`; consumers read whatever
they need via the field names directly, or via tiny helpers like
`co.SlotName(slot)`.

## Using qwanalytics

### Run the default pipeline over a demo file

```go
import (
    "github.com/mvd-analyzer/qwanalytics/analyzer"
    mvdsource "github.com/mvd-analyzer/qwdemo/source/mvd"
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

`result.Result` has one sub-result per analyzer:

```go
type Result struct {
    SchemaVersion    int
    FilePath         string
    Duration         float64
    Match            *MatchResult             // match summary
    Frags            *FragResult              // frag tally + individual entries
    Messages         *MessagesResult          // frag + chat stream for timeline
    DemoInfo         *DemoInfoResult          // KTX authoritative stats
    TimelineAnalysis *TimelineAnalysisResult  // bucketed player state
    Metadata         *MetadataResult          // serverinfo + match settings
    LocGraph         *LocGraphResult          // loc-to-loc movement graph
    Items            *ItemsResult             // per-item pickup / respawn timeline (all MVD sources)
    Backpacks        []BackpackDrop           // RL/LG backpack drops (from KTX //ktx drop hint)
    WeaponPickups    []WeaponPickup           // slot-weapon pickups + kills-before-next-death metric
    Denials          *DenialsResult           // denied (from-enemy) + hoovered (from-team) pickups
    Errors           []string
}
```

Each sub-type is defined in its own file under `result/`. The JSON shape
is the wire contract with every consumer; breaking changes bump
`CurrentSchemaVersion` (currently `6`).

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
hub corpus matches exactly on 8 of 9 demos. See
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
  [`qwdemo/MVD_FORMAT.md` → Practical gap — non-RL/LG backpack
  pickups on competitive demos](../qwdemo/MVD_FORMAT.md#svc_stufftext-9)
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

## Running tests

```bash
go test ./qwanalytics/...
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
   manifest currently ships with nine demos (three each of 1on1, 2on2,
   4on4); `t.Skip` keeps `make test` green if it is ever emptied.
   Regenerate goldens after an intentional change:

   ```bash
   go test ./qwanalytics/analyzer/... -run TestGoldenCorpus -args -update-golden
   ```

   (Use `./qwanalytics/analyzer/...`, not the wider `./qwanalytics/...`
   — `-update-golden` is registered only in this test package and
   wider scopes fail in `mapgen` with "flag provided but not defined".)

   Two transforms are applied before comparison: `filePath` is
   stripped (per-machine cache path), and `timelineAnalysis.highResBuckets`
   is sliced to three 15 s windows (`[0, 15]`, `[60, 75]`, last 15 s).
   The high-res slice is necessary because the full 50 ms position
   track is ~20 MB per 4on4 demo, and the three windows are enough
   sampling to catch bucketer / position-extractor drift. Everything
   else — `locGraph`, `schemaVersion`, ammo counts, frag totals,
   weapon stats, items, powerup events — is pinned in full, so any
   unintended drift surfaces. (The `locGraph` slices are sorted in
   `BuildLocGraph` for run-to-run determinism; map-keyed sub-objects
   already serialise alphabetically.)

3. **Diagnostic corpus** (`diagnostic/diagnostic_test.go`) is opt-in
   and complementary — it runs data-quality invariants
   (frag-total parity, impossible stat values, …) rather than pinning
   output. Drop demos into `qwanalytics/diagnostic/testdata/` to enable:

   ```bash
   cp ~/quake/demos/*.mvd.gz qwanalytics/diagnostic/testdata/
   go test -v -run TestDiagnosticParseDemos ./qwanalytics/diagnostic/
   ```

## Module boundary

qwanalytics depends on qwdemo (for events + Source) and the standard
library. It does not depend on qw-web — consumers like qw-web depend
on *it*, not the other way around.

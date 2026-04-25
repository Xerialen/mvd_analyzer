# qwanalytics

Layer 2 of the mvd-analyzer workspace: take an `events.Source` from qwdemo
(or any other compatible source) and produce a structured `result.Result`
that downstream consumers render, summarise, or feed to an agent.

## What's in the box

- `result/` — the **stable JSON schema** every pipeline run produces.
  Consumers (web UI, CLI, AI agent) should import this package and pin
  against `result.CurrentSchemaVersion`.
- `analyzer/` — the Analyzer interface, shared `Context`, and `Registry`
  that drives a run. `NewDefaultRegistry()` wires up the eight
  production analyzers (demoinfo, metadata, match, frag, messages,
  timeline, items, backpacks).
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
    Errors           []string
}
```

Each sub-type is defined in its own file under `result/`. The JSON shape
is the wire contract with every consumer; breaking changes bump
`CurrentSchemaVersion` (currently `5`).

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
non-KTX servers. No map preprocessing is required. `TakenBy`
attribution currently uses the nearest-player-origin at the moment of
pickup (best-effort label since the entity-state stream doesn't carry
"who touched it"). `RespawnAt` is observed directly, so MH rot
(which varies with damage taken) falls out naturally — no special
case.

Known limitation: when an item respawns and is immediately
regrabbed within the same server tick, the entity is never
visible on the wire for that cycle, so we don't record a phase
for it. The resulting phase will span the whole contested window
(e.g. "RA taken at 31s, respawn observed at 91s" means the RA was
never practically available in that 60 s window).

Authoritative alternative (not yet wired into this analyzer): the
parser now emits `ItemPickupHintEvent` for every KTX `//ktx took`
directive, pinning each pickup to a concrete player edict without the
nearest-origin heuristic. It covers MH / armors / heavy weapons /
powerups on KTX servers; a future refactor will consume these as the
primary `TakenBy` source and keep the nearest-origin path as a
fallback for non-KTX sources.

`ItemPickupPrintEvent` (parsed from per-client `svc_print` "You got
the X" / "You receive N health" strings) fills the remaining gap:
ammo boxes (no `//ktx took`) and H15/H25. Caveat: mvdsv filters
PRINT_LOW prints by the picking player's `messagelevel` cvar before
recording, so competitive demos where players set `msg 2` contain no
pickup prints at all — coverage is per-player and per-demo. See
`qwdemo/MVD_FORMAT.md` for the full filter mechanics.

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

Implement the `analyzer.Analyzer` interface:

```go
type MyAnalyzer struct {
    ctx *analyzer.Context
    // ...
}

func (a *MyAnalyzer) Name() string { return "my" }

func (a *MyAnalyzer) Init(ctx *analyzer.Context) error {
    a.ctx = ctx
    return nil
}

func (a *MyAnalyzer) OnEvent(ev events.Event) error {
    switch e := ev.(type) {
    case *events.PrintEvent:
        // ...
    }
    return nil
}

func (a *MyAnalyzer) Finalize() (interface{}, error) {
    return &MyResult{ /* ... */ }, nil
}
```

Wire it into a Registry:

```go
reg := analyzer.NewDefaultRegistry()
reg.Register(&MyAnalyzer{})
```

If your analyzer's output needs a home on `result.Result`, add the type
to `result/` and add a case to the switch in
`analyzer/registry.go:analyzeSource` that promotes the Finalize output
into the right top-level field. Order matters: analyzers run in
registration order, so put an analyzer that reads DemoInfo *after* the
DemoInfo analyzer.

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

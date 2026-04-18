# qwanalytics

Layer 2 of the mvd-analyzer workspace: take an `events.Source` from qwdemo
(or any other compatible source) and produce a structured `result.Result`
that downstream consumers render, summarise, or feed to an agent.

## What's in the box

- `result/` — the **stable JSON schema** every pipeline run produces.
  Consumers (web UI, CLI, AI agent) should import this package and pin
  against `result.CurrentSchemaVersion`.
- `analyzer/` — the Analyzer interface, shared `Context`, and `Registry`
  that drives a run. `NewDefaultRegistry()` wires up the seven
  production analyzers.
- `loc/` — `.loc` file parser. For native builds the corpus is embedded
  via `//go:embed data/*.loc` (466 maps today); for WASM builds the host
  provides `fetchLocSync` so only the loc for the current demo is
  downloaded.
- `items/` — per-map pickup-item corpus (armor / weapon / MH / powerup
  positions and kinds, generated from BSP entities). Same two-path
  dispatch as `loc/`: `//go:embed data/*.json` for native, host-provided
  `fetchItemsSync` for WASM. Consumed by `ItemAnalyzer` to bind KTX
  `//ktx took` entity numbers to concrete map items.
- `mapgen/` — the Quake 1 BSP reader (`bsp/`, including the new
  `entities.go` decoder for the entities lump) and floor-face extractor
  (`mapgeom/`) used by the mapgen developer tool. Not part of the
  runtime pipeline — it generates static per-map JSON ahead of time.
- `diagnostic/` — opt-in integration harness that runs a demo corpus
  through the parser in warning-collection mode and runs data-quality
  checks on the analysis result.
- `cmd/mapgen/` — developer tool: reads BSP + loc files, writes two
  outputs per map — the per-loc floor-polygon JSON for the web viewer
  (`qw-web/static/maps/<name>.json`) and a slim pickup-items JSON
  (`qwanalytics/items/data/<name>.json`) consumed by `ItemAnalyzer`.
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
    Items            *ItemsResult             // per-item pickup / respawn timeline (KTX demos)
    Errors           []string
}
```

Each sub-type is defined in its own file under `result/`. The JSON shape
is the wire contract with every consumer; breaking changes bump
`CurrentSchemaVersion` (currently `2`).

### Items result

`result.Items` carries one `ItemTimeline` per bound map item (armors,
weapons, megahealth, powerups). Each timeline has deterministic name
(`ra`, `mh_1`/`mh_2`, `rl_1`/`rl_2`, `quad`, …), world position,
nearest loc name, and an ordered `Phases` list:

```go
type ItemPhase struct {
    AvailableFrom float64 // item became available at this time
    TakenAt       float64 // someone picked it up
    TakenBy       string
    Team          string
    RespawnAt     float64 // when it comes back up (0 = MH pending rot)
}
```

Sources: the `ItemAnalyzer` parses KTX's demo-only stuffcmds
(`//ktx took | timer | drop` from `ktx/src/items.c`) and binds each
server-side edict number to a concrete `MapItem` on first pickup via
position snapping against the BSP-derived corpus in `items/`. MH gets
special handling — `took` opens a phase with `RespawnAt=0` (pending
rot), and the matching `timer` event later fills in the 20 s timer
when the carrier's health drops back to ≤100. Non-KTX sources
produce no item data; the field is omitted.

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

The diagnostic corpus test is opt-in — drop demos into
`qwanalytics/diagnostic/testdata/` first:

```bash
cp ~/quake/demos/*.mvd.gz qwanalytics/diagnostic/testdata/
go test -v -run TestDiagnosticParseDemos ./qwanalytics/diagnostic/
```

## Module boundary

qwanalytics depends on qwdemo (for events + Source) and the standard
library. It does not depend on qw-web — consumers like qw-web depend
on *it*, not the other way around.

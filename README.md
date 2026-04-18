# MVD Analyzer

A three-layer toolkit for QuakeWorld demo analysis. MVD bytes go in one end,
structured analysis comes out the middle, and browser/CLI/AI consumers pick
up whatever they need from the Result JSON at the far end.

## Architecture

```
  ┌─────────────┐   Event schema   ┌─────────────┐   Result schema   ┌──────────────┐
  │   Source    │ ───────────────▶ │  Analytics  │ ────────────────▶ │   Consumer   │
  │  (Layer 1)  │                  │  (Layer 2)  │                   │  (Layer 3)   │
  └─────────────┘                  └─────────────┘                   └──────────────┘
   MVD file, QTV                    Pipeline of                       Web UI, CLI,
   stream, JSON                     analyzers over                    AI review
   replayer                         event stream                      agent, bulk
                                                                      batch tool
```

The *schemas* — events and results — are the real contracts. Implementations
on either side can come and go as long as the schemas hold.

### Three Go modules in one workspace

The repo is a Go workspace (`go.work`) binding three sibling modules:

| Module | Path | Role |
|---|---|---|
| [qwdemo](qwdemo/README.md) | `qwdemo/` | Event schema + MVD source (Layer 1) |
| [qwanalytics](qwanalytics/README.md) | `qwanalytics/` | Analysis pipeline + result schema (Layer 2) |
| [qw-web](qw-web/README.md) | `qw-web/` | Browser UI + WASM glue (Layer 3) |

Each module has its own `go.mod`, is tested in isolation, and can be extracted
to its own repo later. Until that's needed, the workspace keeps
cross-layer iteration fast: one git tree, one PR per change.

### Why layered?

Splitting ingestion, analytics, and UX into three layers lets each grow on
its own timeline. Today's concrete shape:

- **Layer 1 (qwdemo)** is the only place that knows the MVD binary format.
  A future QTV live-stream source would sit beside the MVD source and emit
  the same events — downstream analytics wouldn't change.
- **Layer 2 (qwanalytics)** is the only place that knows how to compute
  match summaries, frag streaks, timeline buckets, or loc-graphs. New
  analytics (area control, advanced metrics, whatever's next) land here.
  Analytics never peeks at MVD bytes; it consumes events.
- **Layer 3 (qw-web)** is one of several possible consumers. The bundled
  example is a browser UI on WASM. The in-tree CLI `qw-analyze` is a
  second consumer. An AI review agent is a natural third — all they need
  is `qwanalytics` + a way to call it.

## Quick start

### Analyze a demo at the command line

```bash
go run ./qwanalytics/cmd/qw-analyze demo.mvd.gz                 # Result JSON to stdout
go run ./qwanalytics/cmd/qw-analyze -format md demo.mvd.gz      # human summary
go run ./qwanalytics/cmd/qw-analyze -format events demo.mvd.gz  # line-delimited events
```

### Run the web UI locally

```bash
make serve                                  # http://localhost:8080
```

### Build the WASM bundle for deploy

```bash
make build                                  # output in dist/
```

Other Makefile targets: `make test`, `make fmt`, `make clean`, `make help`.

## The contracts

### Event schema (Layer 1 → 2)

Defined in [`qwdemo/events`](qwdemo/events/events.go). A `Source` is a
pull-style iterator:

```go
type Source interface {
    Next() (Event, error)   // returns io.EOF at clean end
    Close() error
}
```

Concrete event types are plain structs: `ServerDataEvent`, `UserInfoEvent`,
`PrintEvent`, `StatUpdateEvent`, `FragUpdateEvent`, `PlayerPositionEvent`,
`DamageEvent`, `DemoInfoEvent`, `IntermissionEvent`, `StuffTextEvent`,
`CenterPrintEvent`, `ServerInfoEvent`, `DeathEvent`, `SpawnEvent`. Domain
types carried by events — `ServerData`, `PlayerInfo`, `PlayerState`,
`Stats` — are source-agnostic. `DeathEvent` / `SpawnEvent` are derived
events the parser synthesises from `StatHealth` edges so analytics never
has to reconstruct death/spawn by comparing samples across the sampling
boundary.

To write a new source: implement `events.Source`, emit the concrete event
types as you decode your wire format. That's it. See
[`qwdemo/source/mvd`](qwdemo/source/mvd/source.go) for the reference
implementation backed by MVD files.

### Result schema (Layer 2 → 3)

Defined in [`qwanalytics/result`](qwanalytics/result/result.go). `Result` is
a JSON-serializable struct with sub-results from every analyzer that ran:
match, frags, messages, demoinfo, timeline analysis, metadata, locgraph,
items (per-item pickup / respawn timeline for KTX demos).

Every breaking change bumps `CurrentSchemaVersion` (currently `2`).
Consumers can pin or feature-detect by reading `result.schemaVersion`.

### Running the pipeline

```go
import (
    "github.com/mvd-analyzer/qwanalytics/analyzer"
    mvdsource "github.com/mvd-analyzer/qwdemo/source/mvd"
)

src, err := mvdsource.Open("demo.mvd.gz")
if err != nil { ... }
defer src.Close()

reg := analyzer.NewDefaultRegistry()
res, err := reg.AnalyzeSource(src, "demo.mvd.gz")
// res is *result.Result; marshal to JSON, inspect, etc.
```

Swap the source and the rest keeps working:

```go
src := myQTVClient.Open(...)       // implements events.Source
res, err := reg.AnalyzeSource(src, "live")
```

## Repository layout

```
mvd-analyzer/
  go.work                   Workspace — names the three modules
  Makefile                  Top-level coordinator (build / serve / test / fmt)
  netlify.toml              Netlify deploy config
  README.md                 This file

  qwdemo/                   Module: ingestion layer
    events/                 Public contract — Source, Event types, domain types
    mvd/                    MVD wire decoder (internal)
    parser/                 Messages → events (internal)
    mvdfile/                Gzip-aware reader
    source/mvd/             Source implementation for MVD files

  qwanalytics/              Module: analysis pipeline
    analyzer/               Analyzer interface + Context + Registry
    result/                 JSON result schema (stable contract)
    loc/                    .loc parser + embedded corpus (466 maps)
    items/                  Per-map item corpus (positions + kinds) + loader
    mapgen/
      bsp/                  Quake 1 BSP reader (including entities lump)
      mapgeom/              Floor-face extraction
    diagnostic/             Opt-in bulk validation harness
    cmd/mapgen/             Developer tool: BSP -> per-loc JSON + per-map items JSON
    cmd/qw-analyze/         CLI: demo -> json|md|events

  qw-web/                   Module: browser UX + WASM glue
    static/                 index.html, app.js, worker.js, styles.css, maps/
    cmd/wasm/               WASM entry (exports analyzeMVD to JS)

  demos/                    Corpus for regression + manual testing (untracked)
```

## Documentation

- [qwdemo/README.md](qwdemo/README.md) — ingestion layer, how to add a source
- [qwanalytics/README.md](qwanalytics/README.md) — pipeline, how to add an analyzer, Result schema
- [qw-web/README.md](qw-web/README.md) — browser UI, build and deploy
- [qwdemo/MVD_FORMAT.md](qwdemo/MVD_FORMAT.md) — MVD binary format spec with ezQuake references

## Testing

```bash
make test                                               # all modules
go test ./qwanalytics/analyzer/                         # single package
go test -v -run TestDiagnosticParseDemos \
    ./qwanalytics/diagnostic/                           # opt-in demo corpus
```

Golden regression:

```bash
# Before a refactor
go run ./qwanalytics/cmd/qw-analyze -bulk \
    -out-dir /tmp/before -format json demos/

# After
go run ./qwanalytics/cmd/qw-analyze -bulk \
    -out-dir /tmp/after -format json demos/

# Diff
diff -r /tmp/before /tmp/after
```

Note that `locGraph` currently has documented map-iteration non-determinism
(see [CLEANUP-PLAN.md](CLEANUP-PLAN.md) item 7) — filter it with
`jq 'del(.locGraph, .schemaVersion)'` for a clean comparison.

## Known limitations

1. **Weapon switching scripts**: QW players use scripts that switch weapons
   faster than MVD stat updates, causing RL/GL shot undercounting in
   MVD-based tracking. KTX demoinfo stats (when available) are authoritative.

2. **Auth name override**: When players authenticate via mvdsv,
   `sv_forcenick` can set the userinfo name to the login. The analyzer
   resolves display names from KTX demoinfo via `*auth` login join.

## Reference sources

| Project | Description |
|---|---|
| [KTX](https://github.com/QW-Group/ktx) | Server mod — damage calc, demoinfo JSON, hidden message types |
| [mvdsv](https://github.com/QW-Group/mvdsv) | MVD server — demo recording, userinfo handling |
| [ezQuake](https://github.com/QW-Group/ezquake-source) | Client — demo parsing, character encoding |

## License

This project analyzes demo files from QuakeWorld, which uses the
GPL-licensed Quake engine.

## Acknowledgments

- [QW-Group](https://github.com/QW-Group) for KTX, mvdsv, ezQuake, and mvdparser
- The QuakeWorld community for demo format documentation

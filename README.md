# MVD Demo Analyzer

A browser-based analyzer for QuakeWorld MVD (Multi-View Demo) files. Built with Go compiled to WebAssembly — all analysis runs client-side in the browser.

## Features

- Parse MVD and gzipped MVD files (`.mvd`, `.mvd.gz`) — auto-detects gzip from suffix or magic bytes
- Match summary with per-player and per-team statistics
- Weapon stats with accuracy, kills, deaths, damage breakdowns
- Item pickup tracking (armor, health, powerups)
- Timeline visualization with weapons, health/armor, frags, and score graphs
- Frag streak detection and powerup run tracking
- 2D map with real-time player positions and configurable trails
- Region control tracking — which team holds key areas (RA, RL, LG, QUAD) over time
- Kill feed and team chat synced to the timeline
- Key moments view showing powerup runs and frag streaks
- Load demos directly from [QuakeWorld Hub](https://hub.quakeworld.nu) by game ID or URL
- Shareable URLs with game ID, tab, and time position (`?hub=123&tab=timeline&t=45`)
- Embedded KTX demoinfo JSON parsing for authoritative server-side stats
- Server metadata extraction (hostname, gamedir, game mode)
- 1v1 duel normalization — rewrites arbitrary team tags to player names for clean UI
- Zero external Go dependencies — standard library only

## Quick Start

### Load from QuakeWorld Hub

Visit the deployed site and paste a Hub game URL or ID to load and analyze a demo instantly.

### Analyze a local file

Visit the deployed site and drag-and-drop an MVD file.

### Run locally

```bash
make serve
```

Opens on http://localhost:8080.

## Building

```bash
make build    # Compile WASM + copy static files to dist/
make serve    # Build and serve locally
make test     # Run Go tests
make fmt      # Format Go code
make clean    # Remove dist/
make help     # Show all targets
```

Requires Go 1.21+.

The build embeds the git hash, tag, and build date into the WASM binary, displayed in the page header.

## Deployment

Configured for Netlify via `netlify.toml`. Every push runs `make build` and publishes `dist/`.

## Architecture

```
┌───────────────────────────────────────────────────────────────┐
│                   Browser (WASM Worker)                        │
│  cmd/wasm/main.go → analyzeMVD() JS function                  │
├───────────────────────────────────────────────────────────────┤
│                    Analyzer Registry                           │
│  ┌──────────┐ ┌──────────┐ ┌──────┐ ┌──────┐ ┌────────────┐ │
│  │ DemoInfo │ │ Metadata │ │Match │ │ Frag │ │  Messages  │ │
│  │ Analyzer │ │ Analyzer │ │ Ana. │ │ Ana. │ │  Analyzer  │ │
│  └──────────┘ └──────────┘ └──────┘ └──────┘ └────────────┘ │
│  ┌──────────┐                                                 │
│  │ Timeline │                                                 │
│  │ Analyzer │                                                 │
│  └──────────┘                                                 │
├───────────────────────────────────────────────────────────────┤
│                   Parser (Event Stream)                        │
│  UserInfo, Stats, Frags, Print, PlayerInfo, DemoInfo events   │
├───────────────────────────────────────────────────────────────┤
│                    MVD Decoder                                 │
│  Message types, hidden messages, protocol extensions          │
├───────────────────────────────────────────────────────────────┤
│                    File Handler                                │
│  .mvd and .mvd.gz support (auto-detect gzip)                  │
└───────────────────────────────────────────────────────────────┘
```

The six analyzers run in registration order on every event emitted by the parser:

| Analyzer | Purpose |
|----------|---------|
| **DemoInfo** | Parses KTX hidden-message JSON for authoritative server-side stats |
| **Metadata** | Extracts server info, hostname, gamedir, game mode |
| **Match** | Builds per-player and per-team summary statistics |
| **Frag** | Detects kills/deaths from obituary messages and maps weapons |
| **Messages** | Collects chat, obituary text, and system prints |
| **Timeline** | Buckets player state per second, tracks region control, streaks, powerups |

### Key directories

| Path | Description |
|------|-------------|
| `cmd/wasm/` | WASM entry point — exports `analyzeMVD()` to JavaScript |
| `cmd/mapgen/` | Developer tool: BSP → per-loc floor-polygon JSON for the mini-map |
| `cmd/golden/` | Developer tool: bulk-generate golden JSON outputs for regression testing |
| `internal/analyzer/` | Analysis modules, registry, shared types, and result normalization |
| `internal/parser/` | MVD event stream parser with diagnostic mode |
| `internal/mvd/` | Low-level MVD decoder, protocol types, and extensions (FTE, MVD1) |
| `internal/loc/` | Quake `.loc` file parser (data lives under `internal/web/static/locs/`) |
| `internal/bsp/` | Quake 1 BSP reader used by `mapgen` |
| `internal/mapgeom/` | Floor-face extraction + per-loc grouping for the mini-map |
| `internal/diagnostic/` | Opt-in demo validation test harness (strict parse + data quality checks) |
| `internal/web/static/` | Frontend (HTML, CSS, JS) |
| `internal/web/static/locs/` | `.loc` files served to the WASM worker on demand |
| `internal/web/static/maps/` | Pre-generated per-map floor geometry JSON (committed) |
| `pkg/mvdfile/` | MVD/gzip file reader with auto-detection |

## Region Control Customization

The Map tab tracks which team controls key areas (RA, RL, LG, QUAD). Regions are auto-detected from `.loc` files, but can be customized in two ways:

### In the browser

Each region shows an editable text field with comma-separated loc names. Add or remove names and press Enter/Tab to recompute stats instantly. No rebuild needed.

### In the code (map-specific defaults)

Edit `internal/analyzer/timeline.go` to add map-specific regions. Two things to configure:

**1. Auto-detected keywords** — the `controlKeywords` map (line ~1200):

```go
var controlKeywords = map[string]bool{
    "RA": true, "RL": true, "LG": true, "QUAD": true,
}
```

Any loc name containing one of these as a token (e.g., "high.RL", "cellar.RL") becomes a tracked region. Multiple locations with the same keyword that are far apart (>800 world units) are automatically split into separate regions.

**2. Custom named regions** — the `mapCustomRegions` map (line ~1230):

```go
var mapCustomRegions = map[string][]customRegion{
    "dm2": {
        {name: "Secret",   locNames: []string{"secret"}},
        {name: "Backroom", locNames: []string{"RA.MH", "RA.MH/rox"}},
        {name: "Tele",     locNames: []string{"tele", "tele.entry", "tele.YA", "tele.high"}},
    },
}
```

Custom region locs are excluded from auto-detection. To add a new map, add a key with the lowercase map name.

### Finding loc names

To see what loc names are available for a map:

1. **In the browser**: load a demo and check the Region Control panel — the text fields show all loc names
2. **In the source**: check `internal/web/static/locs/<map>.loc` — raw names use variables (`$loc_name_ra` → `RA`, `$.` → `.`) so `high$loc_name_separatorrl` becomes `high.RL`

## Map Geometry

The Map tab can render real walkable-floor polygons for each `.loc`-defined region instead of the convex-hull blob fallback. The polygons come from a developer-only tool that walks a Quake 1 BSP, keeps faces whose plane normal is upward-facing enough to be a floor, and assigns each face to its nearest loc point. Output is one JSON file per map under `internal/web/static/maps/<map>.json`, fetched by the viewer at map-init time. Missing files fall back silently to the hull rendering, so it is safe to ship a viewer without geometry for every map.

The tool is **not part of CI** and is not run during normal builds — only the generated JSON files are committed.

### What you need

- Go (any recent version — same as the main build).
- A `.loc` file for the map under `internal/web/static/locs/<map>.loc`. The tool needs it to assign floor faces to named locations. If no loc file exists, the map is silently skipped.
- The BSP file(s) you want to process. BSPs are **not** committed to this repo because of size — fetch them separately (see below).

### Fetching the QuakeWorld map archive

The community map mirror at <https://maps.quakeworld.nu/core/> has ~850 BSPs covering everything you are likely to want. Mirror them into a local `maps/` directory (this directory is in `.gitignore`):

```bash
mkdir -p maps
cd maps
wget --no-clobber --no-directories --no-host-directories --no-parent \
     --recursive --level=1 --accept '*.bsp' --wait=0.2 --tries=2 \
     --user-agent='mvd-analyzer/mapgen' \
     https://maps.quakeworld.nu/core/
```

`--no-clobber` makes this safe to re-run as a poor-man's incremental sync — it skips anything already on disk. Total download is roughly 1.5 GB.

### Generating geometry JSON

From the repository root:

```bash
go build ./cmd/mapgen

# Process one map
./mapgen -bsp-dir maps -map dm3 -verbose

# Process every BSP under maps/ that has a matching .loc file
./mapgen -bsp-dir maps -verbose
```

Flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `-bsp-dir` | (required) | Directory containing `.bsp` files (recursive) |
| `-map`     | (all)      | Process only the BSP whose basename matches (e.g. `dm3`) |
| `-out-dir` | `internal/web/static/maps` | Where to write `<map>.json` |
| `-loc-dir` | `internal/web/static/locs` | Where to read `.loc` files from |
| `-verbose` | off | Print per-map progress and stats |

The tool walks every BSP, parses it, looks up the matching loc finder, calls `mapgeom.Build` to extract floor faces, and writes a compact JSON with per-loc triangle lists. Maps with no matching `.loc` file are silently skipped (`mapgen: processed=N skipped=M failed=K` summary at the end).

### Adding geometry for a new map

1. Make sure `internal/web/static/locs/<map>.loc` exists (drop in a community loc file, or write one).
2. Put `<map>.bsp` in your local `maps/` directory.
3. `./mapgen -bsp-dir maps -map <map> -verbose`.
4. Inspect the new `internal/web/static/maps/<map>.json` (it should be a few tens of KB and list every loc you care about under `locs[]`).
5. Commit just the JSON file — do **not** commit the BSP.
6. Reload the viewer; the Map tab should now render real floor polygons for every region the loc file defines.

### How the viewer consumes it

`initMapView` (`internal/web/static/app.js`) fires a fetch for `maps/<basename>.json` when a demo loads. If the response is 200 and parses as a `MapRegions` payload, the renderer attaches per-loc triangle lists to the location groups and `prerenderLocationBackground` draws real polygons + thin grey outlines instead of the convex-hull blob fallback. A failed fetch is non-fatal — the page just keeps the hull fallback.

## Testing

```bash
make test                  # Run all tests
go test ./internal/parser/ # Unit tests (userinfo parsing)
```

### Unit tests

| File | Package | What it tests |
|------|---------|---------------|
| `internal/parser/userinfo_test.go` | parser | Quake character encoding → UTF-8 normalization |
| `internal/analyzer/obituary_test.go` | analyzer | Obituary message → weapon mapping |
| `internal/analyzer/metadata_test.go` | analyzer | Server info string parsing (hostname, gamedir, mode) |
| `internal/analyzer/duel_normalize_test.go` | analyzer | 1v1 team rewriting logic |
| `internal/bsp/bsp_test.go` | bsp | Quake 1 BSP lump parsing and geometry extraction |
| `internal/mapgeom/mapgeom_test.go` | mapgeom | Floor-face extraction and loc assignment |
| `internal/mapgeom/normalize_test.go` | mapgeom | Polygon winding/normalization |

### Validating the parser against real demos

The production parser is intentionally permissive — it logs nothing and recovers from many classes of error so the viewer never crashes on a weird demo. That's the right behaviour for the live page, but it's the wrong behaviour when you want to know whether *every* packet, message, and stat in a demo was actually understood. The diagnostic test is the tool for that.

It runs each demo through the parser twice:

1. **Strict parse pass** — parser is put in diagnostic mode (`SetDiagnosticMode(true)`), so warnings that production swallows (`Unknown svc_*` opcodes, unknown temp entities, unknown hidden message IDs, recoverable per-handler parse errors) are *collected* instead of dropped. They're deduplicated by `(type, message)` pair and printed once with a count and a first-occurrence timestamp.
2. **Full analysis pass + data-quality checks** — the same demo is fed through `analyzer.NewDefaultRegistry().Analyze`, then a set of coherence checks runs over the result.

#### Running it

```bash
# Run on every demo currently under internal/diagnostic/testdata/
go test -v -run TestDiagnosticParseDemos ./internal/diagnostic/
```

The test is **opt-in**: if `internal/diagnostic/testdata/` is empty it calls `t.Skip("no demos in testdata/")` and the test passes silently. That's deliberate so `make test` keeps working without forcing every contributor to ship gigabytes of demos in the repo, but it does mean *you have to actually drop demos in there* before the test does anything.

```bash
# Drop in any demos you want to validate
cp ~/quake/qw/demos/*.mvd*  internal/diagnostic/testdata/
```

`testdata/` is intentionally untracked. Common sources for test demos:

- **[QuakeWorld Hub](https://hub.quakeworld.nu)** — every game has a demo download button. Good for picking specific match types.
- **Your own demo recordings** — `record` / `easyrecord` in ezQuake.
- **Known-bad demos** — when you find a demo that fails to parse, save it to `testdata/` so it stays under coverage. The repo has a local `broken.mvd.gz` at the root used for exactly this purpose during development (also untracked).

#### Reading the output

Each demo gets its own `t.Run` subtest, and for each one you'll see lines like:

```
=== RUN   TestDiagnosticParseDemos/some-match.mvd.gz
    diagnostic_test.go:49: PARSE  [first@12.4s] svc: unknown svc command 47 (x3)
    diagnostic_test.go:49: PARSE  [first@88.1s] tempentity: unknown TE type 19 (x1)
    diagnostic_test.go:61: QUALITY  frag mismatch for "FOO": timeline events sum=23, demoInfo=22 (diff=1)
    diagnostic_test.go:64: --- summary: 4 parse warnings (2 unique), 1 quality warnings ---
```

What the prefixes mean:

- **`PARSE`** lines come from the strict parse pass — issues the production parser is silently ignoring. `[first@T]` is the demo timestamp where the warning first occurred, and `(x N)` is how many times it repeated. Only the first occurrence's timestamp is shown to keep the log scannable on demos with thousands of recurring warnings.
- **`QUALITY`** lines come from `checkDataQuality` over the analysis result. They flag *coherence* problems between independent data sources in the same demo, not parse errors.
- **`--- summary ---`** is one line per demo, suitable for grepping across a bulk run.

The test never *fails* on warnings — they're all `t.Logf`. Treat the output as a report, not a pass/fail signal. A demo that emits warnings still produces a usable analysis; the warnings tell you which corners of the parser need attention.

#### What `PARSE` warnings catch

Anything the production parser silently drops, including:

- **Unknown `svc_*` command types** — a server command opcode the decoder doesn't know about. The payload is abandoned, which usually desyncs the rest of the packet. Worth investigating.
- **Unknown temp entities** — a `TE_*` ID we don't have a handler for. Lower stakes (no desync), but a hint that some effects are invisible to the analyzer.
- **Unknown hidden message type IDs** — KTX hidden messages carry a type byte; an unknown one means we're missing a feature recently added by the mod.
- **Recoverable parse errors** in individual message handlers (a handler returned an error but the parser was able to skip past).

#### What `QUALITY` warnings catch

Coherence checks over the analysis result (`checkDataQuality` in `internal/diagnostic/diagnostic_test.go`):

- Player name coverage between `Match.Players` and `DemoInfo.Players` (each side should be a subset of the other).
- Frag totals: sum of timeline frag events vs the authoritative `DemoInfo.Stats.Frags` per player. Tolerates ±2 (race conditions around the final frag); larger gaps point at missed frag events.
- Players with frags but no team assignment (auth-name or `*team` resolution gap).
- Timeline player names not present in `DemoInfo` (orphan slot/name binding).
- Impossible stat values in 1s buckets: `health > 250`, `armor > 200`.
- Suspicious negative frags (`< -5`).
- Duplicate player names in `DemoInfo`.

Add new checks to the same file when you find another invariant worth enforcing — they're cheap and they catch regressions immediately when you bulk-run on a folder of demos.

#### Bulk validation workflow

When making changes to the parser or analyzer, the typical loop is:

```bash
# 1. Drop a representative slice of demos (a few dozen is usually enough)
cp ~/quake/demos/*.mvd.gz  internal/diagnostic/testdata/

# 2. Establish the baseline before your change
go test -v -run TestDiagnosticParseDemos ./internal/diagnostic/ 2>&1 | tee before.log

# 3. Make your change, then re-run
go test -v -run TestDiagnosticParseDemos ./internal/diagnostic/ 2>&1 | tee after.log

# 4. Diff the warning sets
diff <(grep -E 'PARSE|QUALITY' before.log | sort -u) \
     <(grep -E 'PARSE|QUALITY' after.log  | sort -u)
```

Anything that *appears* in `after.log` but not `before.log` is a regression. Anything that *disappears* is a fix.

### Golden output regression testing

`cmd/golden` is a one-off tool that runs every demo through the full analysis pipeline and writes the JSON output to a directory. Use it to snapshot analysis results before a refactor, then diff after:

```bash
# Generate golden outputs
go run ./cmd/golden ~/quake/demos/ /tmp/golden-before/

# Make your changes, then re-run
go run ./cmd/golden ~/quake/demos/ /tmp/golden-after/

# Diff the results
diff -r /tmp/golden-before/ /tmp/golden-after/
```

Each demo produces a `<filename>.json` file with the full `Result` struct. Structural diffs show exactly which fields changed and by how much.

## Additional Tools

### mapgen — Map Geometry Generator

See [Map Geometry](#map-geometry) below for full details. Developer-only tool that converts Quake 1 BSP files into floor-polygon JSON for the 2D map viewer.

```bash
go build ./cmd/mapgen
./mapgen -bsp-dir maps -verbose
```

### golden — Golden Output Generator

Bulk-generates analysis JSON for regression testing. See [Golden output regression testing](#golden-output-regression-testing) above.

```bash
go run ./cmd/golden <demos_dir> <out_dir>
```

## Documentation

- [MVD_FORMAT.md](MVD_FORMAT.md) — Binary format specification with source code references

## Reference Sources

| Project | Description |
|---------|-------------|
| [KTX](https://github.com/QW-Group/ktx) | Server mod — damage calc, demoinfo JSON, hidden message types |
| [mvdsv](https://github.com/QW-Group/mvdsv) | MVD server — demo recording, userinfo handling |
| [ezQuake](https://github.com/QW-Group/ezquake-source) | Client — demo parsing, character encoding |

## Known Limitations

1. **Weapon switching scripts**: QW players use scripts that switch weapons faster than MVD stat updates, causing RL/GL shot undercounting in MVD-based tracking. KTX demoinfo stats (when available) are authoritative.

2. **Auth name override**: When players authenticate via mvdsv, `sv_forcenick` can set the userinfo name to the login. The analyzer resolves display names from KTX demoinfo via `*auth` login join.

## License

This project analyzes demo files from QuakeWorld, which uses the GPL-licensed Quake engine.

## Acknowledgments

- [QW-Group](https://github.com/QW-Group) for KTX, mvdsv, ezQuake, and mvdparser
- The QuakeWorld community for demo format documentation

# MVD Demo Analyzer

A browser-based analyzer for QuakeWorld MVD (Multi-View Demo) files. Built with Go compiled to WebAssembly — all analysis runs client-side in the browser.

## Features

- Parse MVD and gzipped MVD files (`.mvd`, `.mvd.gz`)
- Match summary with per-player and per-team statistics
- Weapon stats with accuracy, kills, deaths, damage breakdowns
- Item pickup tracking (armor, health, powerups)
- Timeline visualization with weapons, health/armor, frags, and score graphs
- 2D map with real-time player positions and configurable trails
- Kill feed and team chat synced to the timeline
- Key moments view showing powerup runs
- Load demos directly from [QuakeWorld Hub](https://hub.quakeworld.nu) by game ID or URL
- Shareable URLs with game ID, tab, and time position (`?hub=123&tab=timeline&t=45`)
- Embedded KTX demoinfo JSON parsing for authoritative server-side stats

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
make clean    # Remove dist/
```

The build embeds the git hash, tag, and build date into the WASM binary, displayed in the page header.

## Deployment

Configured for Netlify via `netlify.toml`. Every push runs `make build` and publishes `dist/`.

## Architecture

```
┌──────────────────────────────────────────────────┐
│              Browser (WASM Worker)                │
│  cmd/wasm/main.go → analyzeMVD() JS function     │
├──────────────────────────────────────────────────┤
│                Analyzer Registry                  │
│  ┌──────────┐ ┌──────┐ ┌──────────┐ ┌─────────┐ │
│  │ DemoInfo │ │Match │ │   Frag   │ │Messages │ │
│  │ Analyzer │ │ Ana. │ │ Analyzer │ │Analyzer │ │
│  └──────────┘ └──────┘ └──────────┘ └─────────┘ │
│  ┌──────────┐                                    │
│  │Timeline  │                                    │
│  │ Analyzer │                                    │
│  └──────────┘                                    │
├──────────────────────────────────────────────────┤
│              Parser (Event Stream)                │
│  UserInfo, Stats, Frags, Print, DemoInfo events  │
├──────────────────────────────────────────────────┤
│                 MVD Decoder                       │
│  Message types, hidden messages, protocol        │
├──────────────────────────────────────────────────┤
│                 File Handler                      │
│  .mvd and .mvd.gz support                        │
└──────────────────────────────────────────────────┘
```

### Key directories

| Path | Description |
|------|-------------|
| `cmd/wasm/` | WASM entry point |
| `internal/analyzer/` | Analysis modules and shared types |
| `internal/parser/` | MVD event stream parser |
| `internal/mvd/` | Low-level MVD decoder and protocol types |
| `internal/loc/` | Quake `.loc` file parser for map locations |
| `internal/web/static/` | Frontend (HTML, CSS, JS) |
| `pkg/mvdfile/` | MVD/gzip file reader |

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
2. **In the source**: check `internal/loc/locs/<map>.loc` — raw names use variables (`$loc_name_ra` → `RA`, `$.` → `.`) so `high$loc_name_separatorrl` becomes `high.RL`

## Testing

```bash
make test                  # Run all tests
go test ./internal/parser/ # Unit tests (userinfo parsing)
```

### Diagnostic test

The diagnostic test runs demos through the parser in "strict mode", surfacing warnings that are normally silently dropped in production. It also checks data quality on the analysis output.

```bash
go test -v -run TestDiagnosticParseDemos ./internal/diagnostic/
```

To test against a larger collection, drop `.mvd` / `.mvd.gz` files into `internal/diagnostic/testdata/`.

**Parse warnings** (issues the production parser silently ignores):
- Unknown `svc_*` command types (payload abandoned)
- Unknown temp entity types
- Unknown hidden message type IDs
- Parse errors in individual message handlers

**Data quality checks** (coherence of the analysis result):
- Player names in match result vs demoInfo coverage
- Frag totals: sum of timeline frag events vs demoInfo stats
- Players with frags but no team
- Timeline player names not present in demoInfo
- Impossible stat values (health > 250, armor > 200)
- Duplicate player names in demoInfo

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

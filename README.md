# MVD Analyzer

A Go-based analyzer for QuakeWorld MVD (Multi-View Demo) files. Extracts match statistics, weapon usage, damage tracking, and player performance data from demo recordings.

## Features

- Parse MVD and gzipped MVD files (`.mvd`, `.mvd.gz`)
- Extract match metadata (map, duration, teams, scores)
- Track weapon statistics (shots, hits, damage, accuracy)
- Parse embedded KTX demoinfo JSON (authoritative server-side stats)
- Frag analysis with weapon attribution
- Support for modern KTX hidden messages (dmgdone, demoinfo)

## Installation

```bash
# Clone and build
git clone <repo>
cd mvd-analyzer
go build -o mvd-analyzer ./cmd/mvd-analyzer
```

## Usage

### Basic Analysis

```bash
# Analyze a demo file
./mvd-analyzer analyze example/match.mvd

# Analyze gzipped demo
./mvd-analyzer analyze example/match.mvd.gz
```

### Output Formats

```bash
# Human-readable output (default)
./mvd-analyzer analyze demo.mvd

# JSON output
./mvd-analyzer analyze -o json demo.mvd

# Pretty-printed JSON
./mvd-analyzer analyze -o json demo.mvd | jq .
```

### Example Output

```
=== MVD Demo Analysis ===
File: example/4on4_match.mvd
Duration: 1200.0 seconds

--- Match Summary ---
Map: dm2
Game: qw

Teams:
  red: 157 frags
  blue: 143 frags

Players:
  player1 [red]: 45 frags
  player2 [red]: 38 frags
  ...

--- Frag Analysis ---
Total frags detected: 300

Frags by weapon:
  rl: 156
  lg: 52
  sg: 48
  gl: 24
  ssg: 20
```

### JSON Output Structure

```json
{
  "filePath": "demo.mvd",
  "duration": 1200.0,
  "match": {
    "map": "dm2",
    "players": [...],
    "teams": [...]
  },
  "frags": {
    "totalFrags": 300,
    "byWeapon": {"rl": 156, "lg": 52, ...},
    "byPlayer": {...}
  },
  "weaponStats": {
    "playerStats": {
      "player1": {
        "weapons": {
          "rl": {"shots": 241, "hits": 89, "damage": 9516, "accuracy": 36.9},
          "lg": {"shots": 450, "hits": 162, "damage": 4860, "accuracy": 36.0}
        }
      }
    }
  },
  "demoInfo": {
    "version": 3,
    "map": "dm2",
    "players": [...]
  }
}
```

## Statistics Accuracy

The analyzer tracks statistics through two methods:

### MVD-Based Tracking
- **Damage**: ~100% accurate (via dmgdone hidden messages)
- **SG shots**: ~99% accurate
- **LG shots**: ~86% accurate
- **SNG/NG shots**: ~87-90% accurate
- **RL/GL shots**: ~26-37% accurate (limited by weapon switching scripts)
- **SSG shots**: ~36% accurate

### Embedded DemoInfo (Authoritative)
KTX servers embed JSON statistics in the demo file with server-side tracking. These stats are authoritative and available via the `demoInfo` field in JSON output.

## Documentation

- [MVD_FORMAT.md](MVD_FORMAT.md) - Comprehensive binary format documentation with source code references

## Reference Sources

The `reference-apps/` directory contains source code used to verify parsing correctness:

| Project | Description | Key Files |
|---------|-------------|-----------|
| [KTX](https://github.com/QW-Group/ktx) | Server mod | `src/combat.c` (damage), `include/g_consts.h` (hidden msg types) |
| [ezQuake](https://github.com/QW-Group/ezquake-source) | Client | `src/sv_demo.c` (recording), `src/cl_parse.c` (parsing) |
| [mvdparser](https://github.com/QW-Group/mvdparser) | C reference | `src/netmsg_parser.c` (shot tracking) |

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    CLI (cmd/mvd-analyzer)               │
├─────────────────────────────────────────────────────────┤
│                   Analyzer Registry                      │
│  ┌──────────┐ ┌──────────┐ ┌────────────┐ ┌──────────┐ │
│  │  Match   │ │   Frag   │ │WeaponStats │ │ DemoInfo │ │
│  │ Analyzer │ │ Analyzer │ │  Analyzer  │ │ Analyzer │ │
│  └──────────┘ └──────────┘ └────────────┘ └──────────┘ │
├─────────────────────────────────────────────────────────┤
│                    Parser (Events)                       │
│  UserInfo, StatUpdate, Damage, Frag, DemoInfo events    │
├─────────────────────────────────────────────────────────┤
│                   MVD Decoder                            │
│  Message types, hidden messages, protocol handling       │
├─────────────────────────────────────────────────────────┤
│                   File Handler                           │
│  .mvd and .mvd.gz support                               │
└─────────────────────────────────────────────────────────┘
```

## Development

### Running Tests

```bash
go test ./...
```

### Adding New Analyzers

1. Implement the `Analyzer` interface in `internal/analyzer/`
2. Register in `NewDefaultRegistry()` in `registry.go`
3. Add result handling in the registry's `Analyze()` method

### Debugging

```bash
# Check hidden message types in a demo
go run ./cmd/debug-hidden demo.mvd

# Check player stat tracking
go run ./cmd/debug-stats demo.mvd [player_name]
```

## Known Limitations

1. **Weapon switching scripts**: QW players use scripts that switch weapons faster than MVD stat updates, causing RL/GL shot undercounting

2. **Missing UserInfo**: Some demos start mid-match without player info; resolved via frag count matching with demoinfo

3. **usercmd data**: Hidden message types 0x0001 (usercmd) and 0x0009 (weapon_instruction) require server flag `sv_usercmdtrace` which is rarely enabled

## License

This project analyzes demo files from QuakeWorld, which uses the GPL-licensed Quake engine.

## Acknowledgments

- [QW-Group](https://github.com/QW-Group) for KTX, ezQuake, and mvdparser
- The QuakeWorld community for demo format documentation

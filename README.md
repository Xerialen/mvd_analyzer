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

### Five Go modules in one workspace

The repo is a Go workspace (`go.work`) binding five sibling modules:

| Module | Path | Role |
|---|---|---|
| [mvd-reader](mvd-reader/README.md)       | `mvd-reader/`       | Event schema + MVD source (Layer 1)               |
| [mvd-analytics](mvd-analytics/README.md) | `mvd-analytics/`    | Analysis pipeline + Result schema + view API (L2) |
| [mvd-api](mvd-api/README.md)             | `mvd-api/`          | HTTP REST server on top of `mvd-analytics/view`   |
| [mvd-mcp](mvd-mcp/README.md)             | `mvd-mcp/`          | Distributable stdio MCP shim that talks to mvd-api |
| [mvd-web](mvd-web/README.md)             | `mvd-web/`          | Browser UI + WASM glue (Layer 3)                  |

Each module has its own `go.mod`, is tested in isolation, and can be extracted
to its own repo later. Until that's needed, the workspace keeps
cross-layer iteration fast: one git tree, one PR per change.

### Why layered?

Splitting ingestion, analytics, and transport into separate layers lets each
grow on its own timeline. Today's concrete shape:

- **Layer 1 (`mvd-reader`)** is the only place that knows the MVD binary
  format. A future QTV live-stream source would sit beside the MVD source
  and emit the same events — downstream analytics wouldn't change.
- **Layer 2 (`mvd-analytics`)** is the only place that knows how to compute
  match summaries, frag streaks, timeline buckets, or loc-graphs. New
  analytics land here. The `view/` sub-package turns the canonical `Streams`
  into bucketed timelines, event lists, point-in-time state, and loc trails.
  Analytics never peeks at MVD bytes; it consumes events.
- **Layer 3 consumers** read `Result` or call `view/` and produce something
  user-facing. There are four today:
  - `mvd-analytics/cmd/qw-analyze` — offline CLI (one demo → JSON / md / events).
  - `mvd-api` — hosted REST API + two-tier on-disk cache.
  - `mvd-mcp` — tiny stdio MCP shim that forwards every tool call to a
    running `mvd-api`. Distributable as a small `.exe` for Claude Desktop /
    Cursor / Claude Code.
  - `mvd-web` — browser UI compiled to WASM.

## Quick start

### Analyze a demo at the command line

```bash
go run ./mvd-analytics/cmd/qw-analyze demo.mvd.gz                 # Result JSON to stdout
go run ./mvd-analytics/cmd/qw-analyze -format md demo.mvd.gz      # human summary
go run ./mvd-analytics/cmd/qw-analyze -format events demo.mvd.gz  # line-delimited events
```

### Run the web UI locally

```bash
make serve                                  # http://localhost:8080
```

### Build the WASM bundle for deploy

```bash
make bsps                                   # fetch the curated BSP set for visibility-aware loc attribution
make build                                  # output in dist/
```

`make bsps` populates a gitignored top-level `bsps/` directory with
the competitive QW map set defined in
[`scripts/fetch-bsps.sh`](scripts/fetch-bsps.sh) (id-stock from
[id-maps-gpl](https://github.com/quakeworld/id-maps-gpl), community
maps from [maps.quakeworld.nu/core](https://maps.quakeworld.nu/core/),
sha256-pinned). `make build` then copies them into `dist/bsps/` for
the WASM worker to lazy-fetch per map. The script hard-fails on any
download or sha mismatch so a flaky mirror produces a red build
rather than a silent V1-everywhere deploy.

For local dev the step is skippable — maps without a BSP fall back to
the V1 Euclidean nearest-neighbour attribution (i.e. the pre-v9
behaviour); only the wall-bleed correction is lost.

### Serve the REST API (`mvd-api`)

```bash
make build-api                              # ./dist/mvd-api
./dist/mvd-api -addr :8080                  # HTTP REST on top of mvd-analytics/view
make build-api-linux build-api-darwin build-api-windows
```

`mvd-api` hosts the analytics surface for non-Go consumers
(third-party integrations, the MCP shim, a future web frontend that
benefits from server-side caching). See
[`mvd-api/README.md`](mvd-api/README.md) for the endpoint table.

### Run MCP locally (`mvd-mcp`)

`mvd-mcp` is a thin (~7 MB) stdio MCP server that lets AI clients
(Claude Desktop, Claude Code, Cursor, anything that speaks MCP) query
the QuakeWorld demo corpus. It carries no analytics code of its own
— two of its tool calls go straight to hub.quakeworld.nu Supabase
(search), and the rest are forwarded as HTTP requests to a running
`mvd-api`. The split lets you ship a small distributable binary for
end-users without bundling the parser, and keeps the wire contract
owned by `mvd-api`.

```bash
make build-mcp                              # ./dist/mvd-mcp
./dist/mvd-api -addr :8080 &                # local API (or point at a remote one)
./dist/mvd-mcp -api http://localhost:8080   # stdio MCP shim — read from stdin, respond on stdout
make build-mcp-windows                      # dist/mvd-mcp-windows-amd64.exe for Claude Desktop
make build-all-platforms                    # cross-compile both mvd-api and mvd-mcp
```

#### Tool surface

Nineteen tools — one for discovery, two for cache control + curated
summary, the high-level Result-section pass-throughs (KTX demoinfo,
metadata, frags, loc-graph, chat, backpacks, items, map entities,
weapon-pickups), and six for the view query layer:

| Tool | Backing |
|---|---|
| **Discovery** | |
| `searchGames(players, teams, map, mode, matchtag, from, to, limit, offset)` | hub.quakeworld.nu Supabase (direct) |
| **Cache + summary** | |
| `loadDemo({gameId or sha256})` | `mvd-api` `POST /v1/demos/{id}` |
| `getOverview(demoId)` | `mvd-api` `GET /v1/demos/{id}/overview` |
| **Result section pass-throughs** | |
| `getDemoInfo(demoId)` | `mvd-api` `/demoinfo` (KTX scoreboard) |
| `getMetadata(demoId)` | `mvd-api` `/metadata` (server cvars + match settings) |
| `getFrags(demoId, players, weapon)` | `mvd-api` `/frags` (aggregates + full kill log) |
| `getLocGraph(demoId)` | `mvd-api` `/loc-graph` (per-map loc adjacency) |
| `getChat(demoId, players, from, to, types)` | `mvd-api` `/chat` |
| `getBackpacks(demoId, players, weapon)` | `mvd-api` `/backpacks` |
| `getItems(demoId, items, players, kinds)` | `mvd-api` `/items` |
| `getMapEntities(demoId, types, kinds)` | `mvd-api` `/map-entities` (static map layout) |
| `getMapEntitiesByMap(map, types, kinds)` | `mvd-api` `/v1/maps/{map}/entities` |
| `getWeaponPickups(demoId, players, weapon, source)` | `mvd-api` `/weapon-pickups` |
| **View queries** | |
| `getBuckets(demoId, windowMs, fields, reducers, layout, …)` | `mvd-api` `/buckets` (default column-major; `layout=row` for the per-bucket shape) |
| `getEvents(demoId, types, …)` | `mvd-api` `/events` |
| `getStreamSlice(demoId, from, to, fields, …)` | `mvd-api` `/stream-slice` |
| `getStateAt(demoId, time, fields, …)` | `mvd-api` `/state-at` |
| `getLocTrails(demoId, minDwellMs, …)` | `mvd-api` `/loc-trails` |
| `getRegionControl(demoId, windowMs)` | `mvd-api` `/region-control` |

**Full schemas live in three places:**

- **[`mvd-mcp/README.md`](mvd-mcp/README.md)** — per-tool *input* schemas
  (parameters, types, defaults). What the MCP SDK exposes via
  `tools/list`.
- **[`mvd-api/README.md`](mvd-api/README.md)** — REST endpoint
  responses, including the `Overview` shape that's unique to the API
  layer.
- **[`mvd-analytics/RESULT_SCHEMA.md`](mvd-analytics/RESULT_SCHEMA.md)**
  — view types (`BucketsView` / `ColumnarBuckets`, `EventsView`,
  `StreamSliceView`, `StateAtView`, `LocTrailsView`,
  `RegionControlResult`), the
  field-code vocabulary, the reducer registry, and the underlying
  `Result` / `Streams` types. View outputs are the same whether
  reached via WASM, the CLI, or MCP. The view layer is the
  parameterised-query seam — any function that takes a time / window
  knob lives there; static analyzer derivations
  (`FragResult`, `LocGraphResult`, `MetadataResult`, …) are served
  directly from result fields by the REST/MCP layer.

Tool errors come back as MCP `isError: true` results with the upstream
error message in `TextContent`. The model can read them and recover
(e.g. call `loadDemo` first when a per-demo tool says `demo_not_found`).

#### Typical session shape

```
1. searchGames({player: "bps", map: "dm6"})
     → list of recent matches with rosters, scores, dates.
       Direct hit on the hub; no mvd-api round-trip.

2. loadDemo({gameId: 12345})
     → mvd-api fetches MVD bytes, parses, caches.
       Slow only on cold demos (2–10 s); warm cache is sub-millisecond.

3. getOverview({demoId: "sha:..."}) | getBuckets | getStateAt | ...
     → analytics for the chosen demo. Fast on warm cache.
```

If the answer is already in a search-result row (e.g. "what was the
score?", "who played?"), the agent should stop there — no
`loadDemo` needed.

#### Architecture

```
                            ┌─────────────────────────────────────┐
                            │ hub.quakeworld.nu (Supabase + CDN)  │
                            └─────────────────┬───────────────────┘
                                              │
                            ┌─────────────────┴──────────────────┐
                            ▼                                    ▼
                   GET / FTS search                    GET .mvd.gz bytes
                            │                                    │
   ┌──────────┐             │                                    │
   │ mvd-web  │ ────────────┘                                    │
   └──────────┘                                                  │
                                                                 │
                                                          ┌──────┴──────┐
                                                          │   mvd-api   │
                                                          │ (parse +    │
                                                          │  cache +    │
                                                          │  view)      │
                                                          └─────┬───────┘
   ┌──────────┐                                                 │ HTTP REST
   │ mvd-mcp  │ ◀─── stdio JSON-RPC ─── Claude / Cursor / etc.  │
   │ (shim)   │ ────────────────────── searchGames ─────────────│
   │          │ ────────────────────── load/get* ───────────────┘
   └──────────┘
```

- **`searchGames`** goes directly to the hub. Discovery is the hub's
  job — `mvd-api` is narrowly responsible for "given a known
  `demoId`: fetch bytes, parse, cache, serve view analytics."
- **`loadDemo` / `get*`** go through `mvd-api`, which talks to the
  hub only to download `.mvd.gz` bytes (the rest comes from its
  two-tier on-disk cache).
- `mvd-web` (the browser UI) uses the same Supabase search path
  directly — both consumers behave identically against the hub.

#### Authentication

There is none, by design. The QW demo corpus is public, the API is
read-only, and the Supabase anon key is the same one shipped in the
web bundle. The optional `-label TAG` flag (or `Authorization: Bearer
<label>` on `mvd-api`) is **not** validated — it's a non-secret
request-source tag captured in `mvd-api`'s access log for analytics.
Common labels: `mcp-claude-desktop`, `claude-code-local`,
`web-community`.

If real auth ever becomes necessary (abuse / rate-limit enforcement),
the surface is small enough to add bearer-token validation in
`mvd-api`'s middleware without changes to the MCP shim.

#### Client setup

The shortest path for each major MCP client:

**Claude Code** — drop a `.mcp.json` in the repo root:

```json
{
  "mcpServers": {
    "mvd-mcp": {
      "command": "/path/to/mvd-mcp",
      "args": ["-api", "http://localhost:8080", "-label", "claude-code-local"]
    }
  }
}
```

Auto-approve tool calls (skip the permission prompt each time) via
`.claude/settings.local.json`:

```json
{ "permissions": { "allow": ["mcp__mvd-mcp__*"] } }
```

**Claude Desktop** — edit `claude_desktop_config.json`
(`%APPDATA%\Claude\` on Windows, `~/Library/Application Support/Claude/`
on macOS, `~/.config/Claude/` on Linux):

```json
{
  "mcpServers": {
    "mvd-mcp": {
      "command": "C:\\Tools\\mvd-mcp.exe",
      "args": ["-api", "https://mvd-api.example.com", "-label", "mcp-claude-desktop"]
    }
  }
}
```

Restart the client after editing. See
[`mvd-mcp/CLAUDE_DESKTOP.md`](mvd-mcp/CLAUDE_DESKTOP.md) for the full
matrix (proxy vs local-API, Windows SmartScreen / macOS Gatekeeper
notes, Cursor setup).

#### Distribution

Cross-compile produces unsigned binaries:

```bash
make build-all-platforms
# dist/mvd-mcp-linux-amd64
# dist/mvd-mcp-darwin-amd64
# dist/mvd-mcp-darwin-arm64
# dist/mvd-mcp-windows-amd64.exe
```

For end-users, distribute the platform-matching `mvd-mcp-*` binary
and the client config snippet. They don't need `mvd-api` locally if
you operate one publicly; otherwise the shim runs against a local
`mvd-api` on `localhost:8080`.

Windows SmartScreen / macOS Gatekeeper will warn on first run
(unsigned binaries). Documented workarounds in
[`mvd-mcp/CLAUDE_DESKTOP.md`](mvd-mcp/CLAUDE_DESKTOP.md); real
code-signing is a planned follow-up.

See [`mvd-mcp/README.md`](mvd-mcp/README.md) for the per-tool input
schemas and the rationale behind keeping the shim small.

Other Makefile targets: `make test`, `make fmt`, `make clean`, `make help`.

## The contracts

### Event schema (Layer 1 → 2)

Defined in [`mvd-reader/events`](mvd-reader/events/events.go). A `Source` is a
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
`CenterPrintEvent`, `ServerInfoEvent`, `DeathEvent`, `SpawnEvent`,
`ItemSpawnEvent`, `ItemStateEvent`, `BackpackDropHintEvent`,
`ItemPickupHintEvent`, `BackpackPickupHintEvent`,
`ItemPickupPrintEvent`, `BackpackPickupPrintEvent`. Domain types
carried by events — `ServerData`, `PlayerInfo`, `PlayerState`,
`Stats` — are source-agnostic.

`DeathEvent` / `SpawnEvent` are derived events the parser synthesises
from `StatHealth` edges so analytics never has to reconstruct
death/spawn by comparing samples across the sampling boundary.
`ItemSpawnEvent` / `ItemStateEvent` are derived from the entity-state
stream (`svc_spawnbaseline` + `svc_packetentities` /
`svc_deltapacketentities`): every item's identity and
pickup/respawn transitions come out of the wire directly — no KTX
prints, no BSP preprocessing. `ItemPickupHintEvent` /
`BackpackPickupHintEvent` / `BackpackDropHintEvent` carry KTX's
authoritative `//ktx took`, `//ktx bp`, `//ktx drop` directives — the
touch-level pickup attribution that entity-state alone can only
approximate. They only fire on KTX servers; non-KTX sources get
entity-state and stats deltas. `ItemPickupPrintEvent` /
`BackpackPickupPrintEvent` parse the per-client "You got the X"
prints that target the picking player via `dem_single`; they fill
the gap where `//ktx took` is silent (ammo boxes, H15/H25, non-RL/LG
backpacks) but only survive to the MVD for players who set `msg 0`
in their client config (see `mvd-reader/MVD_FORMAT.md` for the
server-side `messagelevel` filter that strips PRINT_LOW in most
competitive demos).

To write a new source: implement `events.Source`, emit the concrete event
types as you decode your wire format. That's it. See
[`mvd-reader/source/mvd`](mvd-reader/source/mvd/source.go) for the reference
implementation backed by MVD files.

### Result schema (Layer 2 → 3)

Defined in [`mvd-analytics/result`](mvd-analytics/result/result.go). `Result` is
a JSON-serializable struct with sub-results from every analyzer that ran:
match, frags, messages, demoinfo, timeline analysis, metadata, locgraph,
items (per-item pickup / respawn timeline — works on any MVD source),
backpacks (RL/LG drops attributed to the dropping player via KTX's
`//ktx drop` hint), and weaponPickups (every slot-weapon acquisition —
world spawners and RL/LG backpacks — with a kills-before-next-death
effectiveness metric; joins to backpacks via `backpackEnt` ==
`backpacks[].entNum`). Schema v7 introduced `streams` as the canonical
event-rate storage — every per-player field (vitals, weapons, ammo,
position) recorded at the rate it actually changed. Schema v8 stores
**every timestamped field** as `int32` milliseconds rather than float
seconds — the MVD wire format delivers ms deltas, and keeping the
unit integer end-to-end eliminates the float-precision drift that
previously broke spawn/death-boundary comparisons in locgraph and
keeps the schema consistent and sensible to extend. The view-layer
query API (`view.Buckets`, `view.Events`, `view.StreamSlice`,
`view.StateAt`) still takes and emits float64 seconds at its public
surface, so consumers querying through `view.*` (including the WASM
bridge's `getBuckets` / `getEvents` / `getStreamSlice` / `getStateAt`
exports) are unaffected. Schema v9 adds visibility-aware loc
attribution: when a per-map BSP is available, the analyzer rejects
candidate loc-points that fall outside the player's potentially-
visible-set (PVS), eliminating brief "wall-bleed" phantom loc visits
the V1 pure-Euclidean nearest-neighbour produced (see
[mvd-analytics/locvis](mvd-analytics/locvis/) and
[experiments/locattr/V2b-V6-HANDOFF.md](experiments/locattr/V2b-V6-HANDOFF.md)
for the empirical evidence). Field shapes are unchanged — only the
contents of `PlayerStream.Loc` (and everything derived: LocTrails,
LocGraph edges, RegionControl) shift for maps with BSPs. Schema v10
makes the `DF_DEAD` bit in `svc_playerinfo` the primary
DeathEvent / SpawnEvent signal, with the existing `STAT_HEALTH`
detector dedupling against it as a safety net — deaths whose
`dem_stats` block was directed at a different player slot are no
longer dropped (PlayerStream.Spawns / Deaths counts rise; LocGraph
edges, LocTrails durations, RegionControl ticks, and WeaponPickups
windows shift across the now-present boundaries). Schema v11 makes the
50 ms bucket view column-major (`view.ColumnarBuckets`) the default
across web / REST / MCP. Schema v12 adds optional `armed`,
`unarmed`, `quad` and `pent` weights to each `LocGraph` node (time) *and*
edge (transition counts) — the same breakdown restricted to samples where
the player held RL/LG, held neither, or had an active quad / pent — so
consumers can render a self-contained loc graph / heatmap per combat
posture. Schema v13 adds the `mapEntities` section — the map's static
designed layout (item spawns, player spawnpoints, teleport
sources/destinations, buttons) from the offline-generated mapents corpus
— which v14 extends with brush entities (teleport/button/door volumes
with bounds) plus the teleport source→destination link. Schema v15 adds
`timelineAnalysis.deathEvents`: a per-player death stream (`{time, player,
team}`) parallel to `fragEvents`, sourced from the authoritative protocol
DeathEvent (every death counts once), so the Timeline tab can draw
per-player frags-up / deaths-down charts and KTX-style efficiency
(`frags / (frags + deaths)`). Schema v16 adds `frags.byPlayer[].teamkills`
(KTX "tk") and recovers teamkills whose obituary names only one party, so
they re-enter `frags.frags` as complete killer↔victim pairs: killer-named
("X loses another friend") fill in the victim from the coincident
`DeathEvent`; victim-named ("X was telefragged by his teammate") fill in
the killer by combining position co-location with the teamkiller's −1
frag-delta. Across the test corpus this brings per-player teamkills to an
exact match with KTX's authoritative `tk`. Schema v18 adds
`timelineAnalysis.killEvents`: a per-player enemy-kill stream (`{time,
player, team}`) keyed on the killer, parallel to `deathEvents` and sourced
from the canonical frag log (suicides/teamkills excluded), so the Timeline
tab's per-player drill-down plots an exact cumulative kills − deaths +/-
that reconciles with `frags.byPlayer[].kills` and the kills-based
efficiency. (Team is best-effort and ungated, unlike `deathEvents`, so a
player's curve survives POV demos with an incomplete name↔team join — the
consumer groups by player name.)

Schema v19 adds `match.players[].kills`, `.deaths` and `.suicides` — the
frag-log-corrected counts, independent of the sometimes-wrong KTX demoinfo
stats. KTX credits several self / positional deaths to the wrong entity:
pentagram-deflect telefrags inflate the deflector's kills, and world-dealt
suicides (fall / lava / squish / drown) bump the world entity's counter
instead of the victim's, so demoinfo undercounts suicides. This makes
`match.players` a complete corrected scoreboard, and the API `/overview`
surfaces the same counts so non-web consumers get the correction the web
Summary already applied.

Every breaking change bumps `CurrentSchemaVersion` (currently `19`).
Consumers can pin or feature-detect by reading `result.schemaVersion`.
The full per-field reference lives in
[mvd-analytics/RESULT_SCHEMA.md](mvd-analytics/RESULT_SCHEMA.md).

### Running the pipeline

```go
import (
    "github.com/mvd-analyzer/mvd-analytics/analyzer"
    mvdsource "github.com/mvd-analyzer/mvd-reader/source/mvd"
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
  go.work                  Workspace — names the five modules
  Makefile                 Top-level coordinator (build / serve / test / fmt)
  netlify.toml             Netlify deploy config
  README.md                This file

  mvd-reader/              Module: ingestion layer (Layer 1)
    events/                Public contract — Source, Event types, domain types
    mvd/                   MVD wire decoder (internal)
    parser/                Messages → events (internal)
    mvdfile/               Gzip-aware reader
    source/mvd/            Source implementation for MVD files

  mvd-analytics/           Module: analysis pipeline (Layer 2)
    analyzer/              Analyzer interface + Context + CoreOutputs + Registry
    result/                JSON result schema (stable contract)
    view/                  Pure query API: Buckets, Events, StreamSlice, StateAt, ...
    loc/                   .loc parser + embedded corpus (466 maps)
    hubfetch/              Resolve + download from hub.quakeworld.nu (used by mvd-api)
    mapgen/                Quake 1 BSP reader + floor-face extraction
    diagnostic/            Opt-in bulk validation harness
    cmd/mapgen/            Developer tool: BSP → per-loc floor-polygon JSON
    cmd/qw-analyze/        Offline CLI: demo → json|md|events

  mvd-api/                 Module: REST host + on-disk cache (Layer 3, server)
    main.go, serve.go      HTTP entry
    handlers.go, router.go REST endpoints over mvd-analytics/view
    overview.go            Curated demo summary
    internal/democache/    Two-tier disk cache (raw MVD + parsed Result)

  mvd-mcp/                 Module: distributable stdio MCP shim
    main.go                Stdio MCP entry
    mcp_backend_proxy.go   Forwards each tool call as HTTP to a remote mvd-api
    No mvd-analytics import — outputs are opaque JSON pass-through

  mvd-web/                 Module: browser UX + WASM glue (Layer 3, frontend)
    static/                index.html, app.js, worker.js, styles.css, maps/
    cmd/wasm/              WASM entry (exports analyzeMVD to JS)

  demos/                   Corpus for regression + manual testing (untracked)
```

## Documentation

- [mvd-reader/README.md](mvd-reader/README.md) — ingestion layer, how to add a source
- [mvd-reader/MVD_FORMAT.md](mvd-reader/MVD_FORMAT.md) — MVD binary format spec with ezQuake references
- [mvd-analytics/README.md](mvd-analytics/README.md) — pipeline, how to add an analyzer, Result schema
- [mvd-analytics/RESULT_SCHEMA.md](mvd-analytics/RESULT_SCHEMA.md) — Result JSON schema reference (every field, every section)
- [mvd-api/README.md](mvd-api/README.md) — REST endpoint table, cache layout, smoke tests
- [mvd-mcp/README.md](mvd-mcp/README.md) — stdio MCP shim, distribution
- [mvd-mcp/CLAUDE_DESKTOP.md](mvd-mcp/CLAUDE_DESKTOP.md) — Claude Desktop / Claude Code config snippets
- [mvd-web/README.md](mvd-web/README.md) — browser UI, build and deploy

## Testing

```bash
make test                                               # all modules
go test ./mvd-analytics/analyzer/                         # single package
go test -v -run TestDiagnosticParseDemos \
    ./mvd-analytics/diagnostic/                           # opt-in demo corpus
```

### Golden corpus

`make test` runs `TestGoldenCorpus` (in `mvd-analytics/analyzer/golden_test.go`)
against a manifest of hub.quakeworld.nu game IDs in
[`mvd-analytics/testdata/corpus.json`](mvd-analytics/testdata/corpus.json).
On first run it downloads each demo into
`mvd-analytics/testdata/cache/<gameId>.mvd.gz` (gitignored); subsequent runs
hit the cache and stay offline. Each demo's `Result` JSON is pinned
against `mvd-analytics/testdata/golden/<label>.json`.

What is pinned: everything except `filePath`. At schema v7 the
canonical event-rate storage is `streams` (per-player change streams +
intervals + native position track) — bucketed views are produced on
demand by `mvd-analytics/view.Buckets` and not stored. Per-player time
series in `streams.players[]` are sliced to three 15 s windows
(`[0, 15]`, `[60, 75]`, last 15 s) before comparison — the native
position track alone would otherwise run ~10 MB per 4on4 demo and
swamp the git history (see [`golden_test.go`](mvd-analytics/analyzer/golden_test.go)
`sampleStreams`). The three windows are enough sampling to catch
stream-emitter / bucketer drift while keeping the committed corpus
~4 MB per 4on4. Bucketed-view behavior is exercised through the unit
tests in `mvd-analytics/view/equivalence_test.go`.

The manifest ships with nine demos (three each of 1on1, 2on2, 4on4).
Add entries by appending to the JSON array; labels follow
`mode_team1_team2_DDMMYY_map` (or player names for 1on1, where
`team_names` is null on the hub).

Workflow when an analyzer change shifts output:

```bash
make test
# TestGoldenCorpus fails with first-diff-line per demo.
# Inspect the change, then if it was intended:
go test ./mvd-analytics/analyzer/... -run TestGoldenCorpus -args -update-golden
git diff mvd-analytics/testdata/golden/   # review
git add mvd-analytics/testdata/golden/    # commit alongside the analyzer change
```

(The `-update-golden` flag is registered only in the analyzer test
package; wider scopes like `./mvd-analytics/...` fail in `mapgen` with
"flag provided but not defined".)

The pipeline also has a CLI for ad-hoc bulk diffs:

```bash
go run ./mvd-analytics/cmd/qw-analyze -bulk -out-dir /tmp/before -format json demos/
# ... change ...
go run ./mvd-analytics/cmd/qw-analyze -bulk -out-dir /tmp/after  -format json demos/
diff -r /tmp/before /tmp/after
```

## Known limitations

1. **Weapon switching scripts**: QW players use scripts that switch weapons
   faster than MVD stat updates, causing RL/GL shot undercounting in
   MVD-based tracking. KTX demoinfo stats (when available) are authoritative.

2. **Auth name override**: When players authenticate via mvdsv,
   `sv_forcenick` can set the userinfo name to the login. The analyzer
   resolves display names from KTX demoinfo via `*auth` login join.

3. **Reconnecting players**: When a player disconnects and reconnects
   mid-match they land on a new wire slot (and userid), and their old
   slot is often reused. The `identity` analyzer folds the occupancies
   back into one player — via the KTX `rejoins`/`reenters` prints, then a
   per-session demoinfo login/name join — so pickups, frags, timeline and
   the merged per-player stream stay attributed correctly (matching KTX's
   own ghost-by-netname behaviour). Residual gap: a reconnect on a
   non-KTX demo with no demoinfo *and* a different name each time has no
   signal to link the two names and will not unify. See
   [mvd-reader/MVD_FORMAT.md](mvd-reader/MVD_FORMAT.md) (search "reconnect")
   and [mvd-analytics/analyzer/identity.md](mvd-analytics/analyzer/identity.md).

4. **Same-tick item insta-regrab**: If an item respawns and is picked up
   again within a single server tick (camped spawn), the wire never
   emits a "visible" transition for that cycle. The items analyzer
   recovers these via two synthesis paths (KTX `//ktx took` hint-driven
   for armors/MH/weapons/powerups; stat-delta + position for small
   healths and ammo), so per-touch counts match KTX's authoritative
   `tooks` across the corpus. Two health boxes grabbed in one frame
   (a coalesced health jump) attribute to the gainer via per-box stat
   evidence. The one residual is two *same-magnitude* small healths
   (e.g. h15 + h15) contested in a single frame, which the health-jump
   signal can't tell apart. See
   [mvd-reader/MVD_FORMAT.md#item-tracking-via-entity-state](mvd-reader/MVD_FORMAT.md#item-tracking-via-entity-state)
   and [mvd-analytics/analyzer/items.md](mvd-analytics/analyzer/items.md#insta-regrab-synthesis).

5. **Weapon pickups from backpacks (SSG/SNG/GL/NG)**: KTX emits the
   `//ktx bp` backpack-pickup hint only for RL and LG packs, so
   `result.WeaponPickups` captures world (spawn) grabs of every weapon
   but misses super-shotgun / super-nailgun / nailgun / grenade-launcher
   taken off a dropped pack. Per-weapon totals reconcile with KTX
   `weapons.<w>.pickups.spawn-taken` but fall short of `total-taken` by
   the backpack grabs (systemic; RL/LG reconcile fully). See
   [mvd-analytics/README.md](mvd-analytics/README.md#weapon-pickups).

## Reference sources

| Project | Description |
|---|---|
| [KTX](https://github.com/QW-Group/ktx) | Server mod — damage calc, demoinfo JSON, hidden message types |
| [mvdsv](https://github.com/QW-Group/mvdsv) | MVD server — demo recording, userinfo handling |
| [ezQuake](https://github.com/QW-Group/ezquake-source) | Client — demo parsing, character encoding |

## License

mvd-analyzer is released under the MIT License — see [LICENSE](LICENSE).

It analyzes demo files from QuakeWorld, whose Quake engine is GPL-
licensed; this repo only consumes the wire format and does not
incorporate engine source.

## Acknowledgments

- [QW-Group](https://github.com/QW-Group) for KTX, mvdsv, ezQuake, and mvdparser
- The QuakeWorld community for demo format documentation

# mvd-mcp — stdio MCP shim for QuakeWorld demo analytics

`mvd-mcp` is a small (~5 MB) stdio MCP server that forwards every tool
call as an HTTP request to a running [`mvd-api`](../mvd-api/README.md).
It carries no analytics code of its own — the binary is a wire-protocol
shim, and the response shapes are owned by `mvd-api`.

Why split it from `mvd-api`?

- **Distribution.** End-users (Claude Desktop, Cursor, Claude Code)
  install one tiny binary; the heavy parser + cache lives on the
  server. The bundled-binary version was ~15 MB; this one is ~5 MB.
- **Stability.** `mvd-mcp` only depends on the REST wire contract.
  Analytics-side refactors don't force a shim release.
- **Future-extractable.** No `mvd-analytics` import — this module
  can be moved to its own repo when there's demand.

## Usage

```
mvd-mcp -api URL [-label TAG] [-timeout SECONDS]
mvd-mcp version
```

| Flag | Default | Description |
|---|---|---|
| `-api`      | (required) | Base URL of a running `mvd-api` (e.g. `https://mvd-api.example.com` or `http://localhost:8080`) |
| `-label`    | `""`        | Non-secret request-source tag forwarded as `Authorization: Bearer <label>`. Used for access-log analytics on the API side. |
| `-timeout`  | `60`        | Per-request HTTP timeout in seconds |

## Tool surface

Twenty-one tools. Inputs are typed Go structs with JSON-Schema inference
(this file); outputs are passed through as opaque JSON — see
[`../mvd-api/README.md`](../mvd-api/README.md) for the response shape
of each per-demo endpoint, and
[`../mvd-analytics/RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md)
for the view types (`BucketsView`, `EventsView`, etc.), the field-code
vocabulary, and the reducer registry.

| Tool | Backing |
|---|---|
| `searchGames` | hub.quakeworld.nu Supabase (direct) |
| `loadDemo` | `mvd-api` `POST /v1/demos/{id}` |
| `getOverview` | `mvd-api` `GET /v1/demos/{id}/overview` |
| `getDemoInfo` | `mvd-api` `GET /v1/demos/{id}/demoinfo` |
| `getMetadata` | `mvd-api` `GET /v1/demos/{id}/metadata` |
| `getFrags` | `mvd-api` `GET /v1/demos/{id}/frags` |
| `getDamage` | `mvd-api` `GET /v1/demos/{id}/damage` |
| `getLocGraph` | `mvd-api` `GET /v1/demos/{id}/loc-graph` |
| `getChat` | `mvd-api` `GET /v1/demos/{id}/chat` |
| `getBackpacks` | `mvd-api` `GET /v1/demos/{id}/backpacks` |
| `getItems` | `mvd-api` `GET /v1/demos/{id}/items` |
| `getMapEntities` | `mvd-api` `GET /v1/demos/{id}/map-entities` |
| `getMapEntitiesByMap` | `mvd-api` `GET /v1/maps/{map}/entities` |
| `getWeaponPickups` | `mvd-api` `GET /v1/demos/{id}/weapon-pickups` |
| `getBuckets` | `mvd-api` `GET /v1/demos/{id}/buckets` |
| `getEvents` | `mvd-api` `GET /v1/demos/{id}/events` |
| `getStreamSlice` | `mvd-api` `GET /v1/demos/{id}/stream-slice` |
| `getStateAt` | `mvd-api` `GET /v1/demos/{id}/state-at` |
| `getLocTrails` | `mvd-api` `GET /v1/demos/{id}/loc-trails` |
| `getRegionControl` | `mvd-api` `GET /v1/demos/{id}/region-control` |

`demoId` is the string returned by `loadDemo` (`sha:HEX`) or any
`gameId:NNNN` reference.

Tool errors come back as MCP `isError: true` results with the
upstream error message in `TextContent`. The model can read them and
recover (e.g. by calling `loadDemo` first).

### Input schemas

The Go types below are what `registerTools` declares; the MCP SDK
infers their JSON Schemas from struct tags and exposes them via
`tools/list`. Source of truth:
[`mcp_backend.go`](mcp_backend.go).

#### `searchGames(...)`

All fields optional; an empty filter returns the most recent matches.

| Param | Type | Default | Description |
|---|---|---|---|
| `players`  | `string[]` | — | Player names; FTS on `players_fts`, AND'd across multiple |
| `teams`    | `string[]` | — | Team names; `contains` on `team_names` |
| `map`      | `string`   | — | Map name, exact match (e.g. `dm6`) |
| `mode`     | `string`   | — | Game mode, exact match (`1on1`, `2on2`, `4on4`, `FFA`) |
| `matchtag` | `string`   | — | Tournament/event tag, case-insensitive substring (e.g. `qwsl`) |
| `from`     | `string`   | — | ISO date lower bound, inclusive (YYYY-MM-DD) |
| `to`       | `string`   | — | ISO date upper bound, inclusive (YYYY-MM-DD) |
| `limit`    | `int`      | 20 | Max rows; capped at 100 |
| `offset`   | `int`      | 0 | Pagination offset |

Output: `{ limit, offset, count, games: [hub_row, ...] }`. The hub
row is the Supabase `v1_games` projection
(`id, timestamp, mode, matchtag, map, teams, players, demo_sha256,
demo_source_url`).

#### `loadDemo({gameId | sha256})`

Warms `mvd-api`'s cache for the demo and returns the canonical
`demoId` (`sha:HEX`). Idempotent.

| Param | Type | Description |
|---|---|---|
| `gameId` | `int`    | hub.quakeworld.nu game id |
| `sha256` | `string` | 64-char hex of a demo already in the local cache |

Exactly one of `gameId` / `sha256` must be set.

Output: `LoadDemoOutput` —
`{ demoId, sha256, fromCache, schemaVersion }`. The `demoId` is what
every subsequent per-demo tool expects.

#### `getOverview({demoId})`

| Param | Type | Description |
|---|---|---|
| `demoId` | `string` (required) | `gameId:N` or `sha:HEX` |

Output: `Overview` —
see [`../mvd-api/README.md`](../mvd-api/README.md#getoverview).

#### `getDemoInfo({demoId})`

KTX demoinfo blob pass-through — the authoritative scoreboard.

| Param | Type | Description |
|---|---|---|
| `demoId` | `string` (required) | — |

Output: `result.DemoInfoResult`. Per-player `Weapons.<rl|lg|gl|ssg|ng>.acc`
(hit accuracy), `Weapons.<...>.kills`, `Items.<RA|YA|GA|MH|...>.count`,
`Dmg.taken/given`, `Spree.{quad,run,ring,pent}.{frags,duration}`,
RL/LG transfers, etc. Errors with `demoinfo_unavailable` (422) if the
demo has no KTX demoinfo block (rare; non-KTX or aborted matches).

#### `getMetadata({demoId})`

Server cvars + KTX match settings. Used to answer "what ruleset
was this played under".

| Param | Type | Description |
|---|---|---|
| `demoId` | `string` (required) | — |

Output: `result.MetadataResult` — `serverInfo` (map of cvar →
value), `matchSettings` (mode, timelimit, fraglimit, antilag,
spawnmodel/spawnK, midair, instagib, overtime, powerups, vwep,
noweapon, matchtag, …), `countdownText` (raw KTX
countdown centerprint).

#### `getFrags({demoId, ...})`

Frag aggregates + the full kill log. Cheaper than aggregating
`getEvents(types:["frag"])` client-side.

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string` (required) | — | — |
| `players` | `string[]` | all | Restrict aggregates + log to entries involving these (killer OR victim) |
| `weapon`  | `string[]` | all | Restrict aggregates + log to these weapon codes (`rl`, `lg`, `gl`, `ssg`, `sng`, `ng`, `axe`, `sg`, …) |

Output: `result.FragResult` —
`{ totalFrags, byPlayer: {name: {kills, deaths, byWeapon}}, byWeapon: {weapon: count}, frags: [{time, killer, victim, weapon, isSuicide, isTeamKill}, ...] }`.

#### `getDamage({demoId, ...})`

Per-hit damage aggregates + log, reconstructed from the KTX
`mvdhidden_dmgdone` stream. Cheaper than aggregating
`getEvents(types:["damage"])` client-side; use that for the raw
time-ordered per-hit log.

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string` (required) | — | — |
| `players` | `string[]` | all | Restrict aggregates + log to entries involving these (attacker OR victim) |
| `weapon`  | `string[]` | all | Restrict to these **attacker** weapon codes (`rl`, `lg`, `gl`, `ssg`, `sng`, `sg`, `tele`, …) |

Output: `result.DamageResult` — `{ totalDamage, byWeapon, byPlayer: {name:
{given, taken, givenTeam, givenSelf, takenEnv, byWeapon, enemyVsSg,
enemyVsMid, enemyVsLg, enemyVsRl, enemyVsBoth, ewep}}, matrix: [{attacker,
victim, damage, byWeapon}], events: [{time, attacker, victim, weapon,
damage, victimWep, ...}], scoreboard }`. **EWep** (= `enemyVsLg +
enemyVsRl + enemyVsBoth`) is damage dealt to enemies *holding* RL/LG,
keyed on the **victim's** inventory. Amounts are **unbound** (include
overkill; a telefrag reports 9999), so totals run higher than the KTX
scoreboard — see `scoreboard` for the cross-check.

#### `getLocGraph({demoId})`

Per-map adjacency graph of named locations.

| Param | Type | Description |
|---|---|---|
| `demoId` | `string` (required) | — |

Output: `result.LocGraphResult` —
`{ locs: [{ name, x, y, z, ... }, ...], edges: [{ from, to, weight, ... }, ...] }`.
Useful for movement-pattern reasoning ("what's adjacent to RA?",
"which paths connect quad to RL?").

#### `getChat({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`    | `string` (required) | — | — |
| `startTime` | `float64` | match start | Window start, match-relative seconds |
| `endTime`   | `float64` | match end | Window end |
| `players`   | `string[]` | all | Restrict to these speakers |
| `types`     | `string[]` | `["chat","teamsay"]` | Narrow to one of the two |

Output: `{ messages: []result.MatchEvent }` — each entry has `time`,
`type` (`chat` or `teamsay`), `player`, `team`, `message` (raw with
ezQuake markup), `messageClean` (markup stripped). Cleaner shape than
`getEvents(types:["chat"])` when you only want chat. (mvd-api returns a
bare array here; the MCP tool wraps it under `messages` because MCP
structuredContent must be a JSON object.)

#### `getBackpacks({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string` (required) | — | — |
| `players` | `string[]` | all | Restrict to drops by these dropper names |
| `weapon`  | `string`   | both | `rl` or `lg` |

Output: `{ backpacks: []result.BackpackDrop }` — each entry has `time`,
`player` (dropper), `team`, `weapon` (`rl`/`lg`), `origin` (XYZ), `loc`
(resolved name), `entNum` (server edict — joins to
`weapon-pickups[].backpackEnt`). (Wrapped under `backpacks`; see the
`getChat` note.)

#### `getItems({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string` (required) | — | — |
| `items`   | `string[]` | all | Item name or kind token, case-insensitive. A kind matches every instance of a type (`YA` → `ya_1`, `ya_2`; `RA`; `MH`; `Quad`; `Pent`; `Ring`; `RL`; `LG`; `GL`; `SSG`; `SNG`; `NG`); a suffixed name matches one instance (`ya_1`). |
| `players` | `string[]` | all | Restrict phases to those taken by these names; phases with no `takenBy` survive |
| `kinds`   | `string[]` | all | Category, case-insensitive: `armor`, `mega`, `health`, `powerup`, `weapon`, `ammo`. A raw kind token (`ra`, `quad`, …) also matches. |

Output: `result.ItemsResult` —
`{ items: [{ name, kind, entNum, x, y, z, loc, phases: [...] }, ...] }`.
`name` is unique per item (suffixed when a map has several of a kind:
`ya_1`, `ya_2`, `mh_1`); `kind` is the raw token (`ra`/`ya`/`mh`/`quad`/
`rl`/…). Each phase: `availableFrom`, `takenAt`, `takenBy`, `team`,
`respawnAt`.

#### `getWeaponPickups({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string` (required) | — | — |
| `players` | `string[]` | all | Restrict to picks by these names |
| `weapon`  | `string[]` | all | `rl`, `lg`, `gl`, `ssg`, `sng`, `ng` |
| `source`  | `string`   | both | `world` (spawner) or `backpack` (RL/LG drop) |

Output: `{ pickups: []result.WeaponPickup }` — each entry has `time`,
`player`, `team`, `weapon`, `source`, `hadBefore`, `kills` (before
picker's next death), `nextDeathTime`, plus for backpack pickups
`backpackEnt`, `dropper`, `dropperTeam`, `dropTime`. Joins to
`getBackpacks` via `backpackEnt` == `backpacks[].entNum`. (Wrapped under
`pickups`; see the `getChat` note.)

#### `getBuckets({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`      | `string` (required) | — | — |
| `windowMs`    | `int`     | **1000** (MCP default) | Bucket size in ms. The REST API itself defaults to 50 — the MCP proxy injects 1000 when caller omits, since 50 ms emits ~24K buckets per match (drowns an LLM context). Pass an explicit value to override either way. |
| `startTime`   | `float64` | match start | Window start, match-relative seconds |
| `endTime`     | `float64` | match end | Window end |
| `players`     | `string[]` | all | Restrict to these player names |
| `fields`      | `string[]` | all standard | Field codes — see RESULT_SCHEMA.md |
| `reducers`    | `{[code]: name}` | per-field defaults | Reducer-name override per field |
| `includeTeam` | `bool`    | `false` | Also emit per-team aggregates per bucket |
| `loc`         | `string`  | `name` | Ignored for `layout=column` (always raw `li` index) |
| `layout`      | `string`  | **`column`** | `column` = compact column-major `ColumnarBuckets` (one array per `(player,field)`, `time(i)=startMs+i*windowMs`, booleans 0/1); `row` = one self-describing object per bucket. Prefer column for series/trend reads; use `getStateAt` for snapshots. |

Output: `view.ColumnarBuckets` (default) or `view.BucketsView` (`layout=row`)
— see [RESULT_SCHEMA.md → Buckets](../mvd-analytics/RESULT_SCHEMA.md#buckets).

#### `getEvents({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`    | `string` (required) | — | — |
| `startTime` | `float64` | match start | — |
| `endTime`   | `float64` | match end | — |
| `players`   | `string[]` | all | — |
| `types`     | `string[]` | discrete-event default set | `frag, powerup, streak, spawn, death, weapon, item, chat` (default), opt-in: `loc, health, armor` |

Output: `view.EventsView` —
`{ events: [{ t, type, player, detail }, …] }`. Per-type `detail`
keys are in RESULT_SCHEMA.md.

#### `getStreamSlice({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`    | `string` (required) | — | — |
| `startTime` | `float64` | match start | — |
| `endTime`   | `float64` | match end | — |
| `players`   | `string[]` | all | — |
| `fields`    | `string[]` | all standard | — |

Output: `view.StreamSliceView`. Per-player change-stream entries
inside the window (carry-forward entry prepended at `startTime`;
intervals clamped to the window).

#### `getStateAt({demoId, time, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`  | `string`  (required) | — | — |
| `time`    | `float64` (required) | — | Match-relative seconds |
| `players` | `string[]` | all | — |
| `fields`  | `string[]` | all standard minus `sp`/`d` | Spawn/death timestamps are rejected — they're events, not state |

Output: `view.StateAtView` — `{ t, players: { name: {...fields} } }`.
Change streams resolve to "latest entry ≤ time" (carry-forward);
intervals to membership; position to nearest sample.

#### `getLocTrails({demoId, ...})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`     | `string` (required) | — | — |
| `players`    | `string[]` | all | — |
| `minDwellMs` | `int`     | 0 | Drop transitions shorter than this; folded into neighbour |
| `startTime`  | `float64` | match start | — |
| `endTime`    | `float64` | match end | — |

Output: `view.LocTrailsView` —
`{ players: [{ name, sequence: [{ s, e, loc }, …] }, …] }`.

#### `getRegionControl({demoId, windowMs})`

| Param | Type | Default | Description |
|---|---|---|---|
| `demoId`   | `string` (required) | — | — |
| `windowMs` | `int` | **1000** (MCP default) | Bucket size for the per-region state strings. Same MCP-vs-REST split as `getBuckets`: REST default is 50, MCP proxy injects 1000 to keep `bucketStates` lengths manageable. |

Output: `result.RegionControlResult`. Errors with
`region_control_unavailable` (HTTP 422) if the demo's map has no
region layout. See RESULT_SCHEMA.md for the encoding of
`bucketStates` (per-region one-char-per-bucket string) and `stats`
(match-aggregate percentages).

### Why search bypasses mvd-api

Discovery (finding demos by player names, teams, map, etc.) is
hub.quakeworld.nu's job — `mvd-mcp` queries its public Supabase
endpoint directly, the same way the web frontend does. `mvd-api` is
narrowly responsible for "given a known demoId, fetch the bytes,
parse, cache, and serve analytics views." We don't shadow-host hub
search.

The Supabase anon key is public (shipped in the web bundle) and the
request shape mirrors the web's exactly, so there's no second source
of truth for the search semantics.

## Local MCP

The shim has no local-cache mode. For local MCP, run `mvd-api` on
`localhost` and point the shim at it:

```bash
mvd-api -addr :8080 -cache-dir ~/.cache/mvd-api &
mvd-mcp -api http://localhost:8080 -label local-mcp
```

Two binaries, ~zero startup cost. The deliberate trade-off vs. a
bundled binary is that the shim stays tiny and the wire contract
stays clean.

## Client integration

See [`CLAUDE_DESKTOP.md`](CLAUDE_DESKTOP.md) for copy-paste config
snippets for Claude Desktop, Claude Code, and Cursor, on Windows /
macOS / Linux.

## Build

```bash
make build-mcp                              # host platform
make build-mcp-windows                      # dist/mvd-mcp-windows-amd64.exe
make build-mcp-darwin                       # dist/mvd-mcp-darwin-{amd64,arm64}
make build-mcp-linux                        # dist/mvd-mcp-linux-amd64
make build-all-platforms                    # everything above + mvd-api targets
```

## Typical session shape

1. `searchGames({player: "bps", map: "dm6"})` → list of recent
   matches with rosters, scores, dates — directly from the hub. Cheap.
   No `mvd-api` round-trip; agent can filter / rank from the rows.
2. `loadDemo({gameId: 12345})` → tells `mvd-api` to fetch + parse +
   cache. Slow only on cold demos.
3. `getOverview` / `getBuckets` / `getStateAt` / ... → analytics for
   the chosen demo. Fast on warm cache.

If the answer is in the search-result rows alone (e.g. "what was
the score?"), the agent should stop there — no need to parse.

## Module dependencies

```
github.com/modelcontextprotocol/go-sdk v1.6.0
```

That's it. No `mvd-analytics`, no `mvd-api`, no parser. Just the MCP
SDK and stdlib (`net/http`, `encoding/json`, `log/slog`, etc.).

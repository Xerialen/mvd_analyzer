# mvd-api — REST host for QuakeWorld demo analytics

`mvd-api` exposes [`mvd-analytics/view`](../mvd-analytics/view) as a
hosted HTTP REST API, backed by a two-tier on-disk cache that
resolves and downloads demos from
[hub.quakeworld.nu](https://hub.quakeworld.nu) on demand.

It's the server-side companion to [`mvd-mcp`](../mvd-mcp/README.md)
(the distributable stdio MCP shim that forwards tool calls to this
binary).

## Usage

```
mvd-api [-addr ADDR] [-cache-dir PATH] [-log-format text|json]
mvd-api version
```

| Flag | Default | Description |
|---|---|---|
| `-addr`        | `:8080`                                 | Listen address |
| `-cache-dir`   | `$XDG_CACHE_HOME/qw-mvd` or `~/.cache/qw-mvd` | On-disk cache root |
| `-maps-dir`    | _(empty)_                               | Directory of per-map geometry JSON for `/v1/maps/{map}/geometry`; empty disables that endpoint (ship `dist/maps/` next to the binary to enable) |
| `-log-format`  | `text`                                  | Access log format: `text` or `json` |

Schema bumps in `mvd-analytics` invalidate the parsed-`Result` tier
but keep the raw-MVD tier — the next access re-parses without
re-downloading from the hub.

## REST endpoints

> **Building a frontend or tool?** [`API.md`](API.md) is the detailed
> HTTP reference — per-endpoint parameters, response semantics, units
> (the seconds-vs-milliseconds gotcha), caching, and task recipes. The
> table below is just the quick index.

All paths under the base URL (default `http://localhost:8080`). The
`{id}` segment is one of:

- `gameId:NNNN` — numeric hub.quakeworld.nu game id (server fetches
  the MVD if not cached locally)
- `sha:HEX` — 64-char SHA-256 of a demo already in the local cache
  (mostly for bookmarking warm cache entries)

Successful 2xx responses set `Cache-Control: public, max-age=86400,
immutable`, `X-Schema-Version: <n>`, `X-Cache: HIT|WARM|MISS`, and
`ETag: "<sha>-v<n>"` (where `<n>` is the current `CurrentSchemaVersion`).
Send `If-None-Match` to get a cheap 304.

| Method | Path | Query params | 200 body |
|---|---|---|---|
| GET | `/healthz` | — | `{ok, schemaVersion}` |
| GET | `/v1/version` | — | `{hash, tag, buildDate}` |
| POST | `/v1/demos/{id}` | — | `{demoId, sha256, fromCache, schemaVersion}` (`loadDemo` — warms the cache) |
| GET | `/v1/demos/{id}/overview` | — | `Overview` (map, teams, top streaks, top powerups, playerUserIDs, analyzer `errors`) |
| GET | `/v1/demos/{id}/demoinfo` | — | `result.DemoInfoResult` (KTX scoreboard — per-player weapon accuracy, kills/deaths/TK, damage, sprees, item counts, RL/LG transfers) |
| GET | `/v1/demos/{id}/metadata` | — | `result.MetadataResult` (full fullserverinfo cvars + KTX match settings: timelimit, fraglimit, spawnmodel, antilag, midair, instagib, …) |
| GET | `/v1/demos/{id}/frags` | `players`, `weapon` | `result.FragResult` (totalFrags + byPlayer + byWeapon + full kill log) |
| GET | `/v1/demos/{id}/loc-graph` | — | `result.LocGraphResult` (per-map loc adjacency + edge weights) |
| GET | `/v1/demos/{id}/chat` | `from`, `to`, `players`, `types` | `[]result.MatchEvent` (chat + teamsay only; types defaults to both) |
| GET | `/v1/demos/{id}/backpacks` | `players`, `weapon` | `[]result.BackpackDrop` (RL/LG drops via `//ktx drop`) |
| GET | `/v1/demos/{id}/items` | `items`, `players`, `kinds` | `result.ItemsResult` (per-item pickup/respawn timeline) |
| GET | `/v1/demos/{id}/map-entities` | `types`, `kinds` | `result.MapEntitiesResult` (static map layout: item spawns, spawnpoints, teleporters, buttons) |
| GET | `/v1/demos/{id}/weapon-pickups` | `players`, `weapon`, `source` | `[]result.WeaponPickup` (kills-before-next-death; joins to backpacks via `backpackEnt`) |
| GET | `/v1/demos/{id}/buckets` | `windowMs`, `from`, `to`, `players`, `fields`, `reducers`, `includeTeam`, `loc`, `layout` | `view.ColumnarBuckets` (`layout=column`, default) or `view.BucketsView` (`layout=row`) |
| GET | `/v1/demos/{id}/events` | `from`, `to`, `players`, `types`, `loc` | `view.EventsView` |
| GET | `/v1/demos/{id}/stream-slice` | `from`, `to`, `players`, `fields`, `loc` | `view.StreamSliceView` |
| GET | `/v1/demos/{id}/state-at` | `time` (required), `players`, `fields`, `loc` | `view.StateAtView` |
| GET | `/v1/demos/{id}/loc-trails` | `from`, `to`, `players`, `minDwellMs`, `loc` | `view.LocTrailsView` |
| GET | `/v1/demos/{id}/loc-table` | — | `{ "locTable": []string }` (decoder for `loc=index`; index 0 = "" no-loc) |
| GET | `/v1/demos/{id}/region-control` | `windowMs` | `result.RegionControlResult` |
| GET | `/v1/maps/{map}/entities` | `types`, `kinds` | `result.MapEntitiesResult` (static layout by map name, no demo needed) |
| GET | `/v1/maps/{map}/geometry` | — | `mapgeom.MapRegions` floor-polygon JSON (needs `-maps-dir`; REST-only) |

### Details → [`API.md`](API.md)

The full HTTP reference lives in [`API.md`](API.md):

- **Query conventions** — `players`/`fields`/`types` lists, `reducers`,
  `loc=name|index`, `layout=column|row`, defaults.
- **Units** — the seconds-vs-milliseconds split (view envelopes are
  seconds; raw stream entries and the columnar grid are int32 ms).
- **Response shapes** — per-endpoint, cross-linked to
  [`mvd-analytics/RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md)
  (the authoritative source for `BucketsView`, `EventsView`,
  `StreamSliceView`, `StateAtView`, `LocTrailsView`,
  `result.RegionControlResult`, the field vocabulary, and the reducer
  registry). View shapes are produced identically via the WASM bridge,
  CLI, or this REST surface.
- **Error envelope + stable codes** — the `{ "error": { code, message } }`
  shape and every `4xx`/`5xx` code.
- **Recipes** — common frontend features → the call that backs them.

## Authentication

There is none. The data is public and read-only. The optional
`Authorization: Bearer <label>` header (or `?label=` query param) is
**not validated** — it's a non-secret request-source tag captured in
the access log for analytics. Common labels: `mcp-claude-desktop`,
`web-community`, `cli-script`.

## Cache layout

Under `-cache-dir`:

```
mvd/<sha[:2]>/<sha>.mvd.gz             # tier 1 — raw bytes from hub
results/v<N>/<sha[:2]>/<sha>.gob       # tier 2 — parsed *Result, per schema version
index/games/<gameId>.txt               # gameId → sha map
```

A 4-on-4 demo typically occupies ~3–7 MB in tier 1 and ~3–10 MB in
tier 2. There is no automatic eviction; documented as a follow-up
(see [`FOLLOWUPS.md`](../FOLLOWUPS.md)).

## Smoke tests

```bash
mvd-api -addr :8080 -cache-dir /tmp/mvd-cache &

curl -s localhost:8080/healthz
# {"ok":true,"schemaVersion":19}

curl -s -X POST localhost:8080/v1/demos/gameId:12345
# first call:  fromCache:false
# second call: fromCache:true

curl -s 'localhost:8080/v1/demos/gameId:12345/overview' | jq '.map, .duration, .teams'

# default layout is column: top-level count + per-player field arrays
curl -s 'localhost:8080/v1/demos/gameId:12345/buckets?windowMs=1000&fields=h,a' \
  | jq '.count, (.players | keys)'
# row layout (one object per bucket) is opt-in
curl -s 'localhost:8080/v1/demos/gameId:12345/buckets?windowMs=1000&fields=h,a&layout=row' \
  | jq '.buckets | length'

curl -s 'localhost:8080/v1/demos/gameId:12345/state-at?time=65&fields=h,a,rl,pos' | jq .

# Cache header sanity
curl -sI 'localhost:8080/v1/demos/gameId:12345/overview' | grep -i 'x-cache\|etag'

# Error mapping
curl -s -w 'HTTP %{http_code}\n' 'localhost:8080/v1/demos/banana/overview'    # 400 invalid_demo_id
curl -s -w 'HTTP %{http_code}\n' 'localhost:8080/v1/demos/gameId:0/overview'  # 404 demo_not_found
```

## Build

```bash
make build-api                              # ./dist/mvd-api
make build-api-{linux,darwin,windows}       # cross-compile targets
make build-all-platforms                    # everything + mvd-mcp targets
```

## Pairing with mvd-mcp

For MCP clients (Claude Desktop, Cursor, Claude Code), run `mvd-api`
either hosted or on localhost, then point
[`mvd-mcp`](../mvd-mcp/README.md) at it:

```bash
mvd-api -addr :8080 &
mvd-mcp -api http://localhost:8080
```

See [`mvd-mcp/CLAUDE_DESKTOP.md`](../mvd-mcp/CLAUDE_DESKTOP.md) for
client config snippets.

# mvd-api HTTP reference

Integration guide for building **custom web frontends and tools** on top
of `mvd-api` — the hosted REST surface over QuakeWorld demo analytics.

This document owns the **HTTP surface**: endpoints, parameters, response
*semantics*, units, caching, and task recipes. It does **not** restate
field shapes — the authoritative reference for every response *type*
(field names, vocabulary codes, reducer registry, JSON shapes) is
[`mvd-analytics/RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md).
When an endpoint returns a `view.XxxView` or `result.XxxResult`, follow
the link to that doc for the field-by-field shape. JSON snippets here are
**real captured output** (trimmed with `…`), shown for orientation, not
as a second schema.

For the operator-facing view (flags, cache layout, build, smoke tests)
see [`README.md`](README.md). For the MCP wrapper see
[`mvd-mcp/README.md`](../mvd-mcp/README.md) — it forwards the same calls.

---

## 1. Getting started

Base URL defaults to `http://localhost:8080`. A demo is addressed by an
`{id}` segment:

- **`gameId:NNNN`** — a numeric [hub.quakeworld.nu](https://hub.quakeworld.nu)
  game id. On first use the server fetches and parses the MVD; subsequent
  calls hit the cache.
- **`sha:HEX`** — the 64-char SHA-256 of a demo already in the local
  cache (returned by `loadDemo`; good for bookmarking a warm entry).

Typical frontend flow:

```
POST /v1/demos/gameId:12345              → warm the cache, get the sha id
GET  /v1/demos/gameId:12345/overview     → "what was this match" in one call
GET  /v1/demos/gameId:12345/<detail>     → drill into a specific panel
```

`loadDemo` is the only call that can be slow (cold fetch + parse).
Everything else is served from the cached `*Result`, typically
sub-millisecond.

---

## 2. Conventions (read this once)

### 2.1 Time units — the one real gotcha

The API mixes two time units **on purpose**, and frontend code must
track which is which:

| Where | Unit | Examples (real output) |
|---|---|---|
| **Query inputs** `from` / `to` / `time` | **seconds** (float) | `?from=105&to=110`, `?time=105` |
| **View envelope** times | **seconds** (float) | `events[].t=101.298`, `state-at.t=105`, `stream-slice.startTime=105`, row-bucket `.t=120`, loc-trail `s`/`e`=`1.015` |
| **Raw stream entries** embedded in `/stream-slice` | **int32 milliseconds** | `h:[{ "t":105000, "v":-7 }]`, `pos.t:[105001,…]`, `rl:[{ "s":105000,"e":105182 }]` |
| **Columnar buckets** axis | **int32 milliseconds** | `startMs`, `windowMs`, `time(i)=startMs+i*windowMs` |

Rule of thumb: anything the **view layer synthesises** (event lists,
window bounds, trail dwell spans, row-bucket timestamps) is **seconds**;
anything copied **verbatim from the stored schema** (the change-stream /
interval / position arrays, the columnar grid) is **int32 ms**. The
underlying schema is all int32 ms — see RESULT_SCHEMA.md §"Time units".
Scale ms→s by `* 0.001`.

### 2.2 Query parameters

- **`players`, `fields`, `types`** — comma-separated lists; URL-decode
  once. Omit `players` to get all; omit `fields`/`types` to get the
  endpoint's default set.
- **`reducers`** (`/buckets`) — comma-separated `field=name` pairs, e.g.
  `reducers=h=min,a=last`. Names come from the reducer registry in
  RESULT_SCHEMA.md.
- **`from` / `to`** — match-relative **seconds**. Omit for the whole
  match.
- **`time`** — match-relative **seconds**; **required** on `/state-at`.
- **`windowMs`** — integer milliseconds (`/buckets`, `/region-control`).
- **`loc`** — `name` (default) resolves loc indices to names; `index`
  returns the raw `LocTable` index for index-based math (decode via
  `/loc-table`). Honoured by `buckets`, `events`, `stream-slice`,
  `state-at`, `loc-trails`.
- **`layout`** (`/buckets` only) — `column` (default, compact) or `row`.
  See §4.10.

The valid **field codes** (`h`, `a`, `rl`, `pos`, `sp`, `d`, …) and
**reducer names** are listed once in
[RESULT_SCHEMA.md §Field vocabulary / Reducer registry](../mvd-analytics/RESULT_SCHEMA.md#field-vocabulary).

### 2.3 Caching (use it — the data is immutable)

Successful 2xx responses set:

```
Cache-Control: public, max-age=86400, immutable
ETag: "<sha>-v<schemaVersion>"
X-Schema-Version: <n>
X-Cache: HIT | WARM | MISS
```

A demo's analysis never changes for a given schema version, so frontends
should cache aggressively and send `If-None-Match: "<etag>"` for a cheap
`304`. A schema bump changes the ETag suffix and invalidates client
caches automatically.

### 2.4 Errors

Non-2xx responses use a stable envelope:

```json
{ "error": { "code": "demo_not_found", "message": "gameId 0" } }
```

| HTTP | `code` | Meaning |
|---|---|---|
| 400 | `invalid_demo_id` | malformed `{id}` |
| 400 | `invalid_param` | view-layer rejection (unknown field / bad reducer) |
| 400 | `missing_param` | required param absent (e.g. `time` on `/state-at`) |
| 404 | `demo_not_found` | hub has no row for this gameId |
| 422 | `demoinfo_unavailable` | non-KTX server or aborted match |
| 422 | `metadata_unavailable` | no fullserverinfo / countdown centerprint |
| 422 | `frags_unavailable` | no frag log |
| 422 | `damage_unavailable` | no KTX `mvdhidden_dmgdone` damage stream |
| 422 | `locgraph_unavailable` | no position track |
| 422 | `region_control_unavailable` | no region-control layout for this map |
| 502 | `hub_upstream` | network / 5xx from the hub |
| 500 | `internal` / `panic` | unexpected |

The `422`s are **expected** for some demos (a non-KTX server has no
demoinfo). Treat them as "this panel is unavailable for this demo", not
as a hard failure — `/overview` exposes `hasRegionControl` and `errors`
so you can hide panels up front.

### 2.5 Authentication

None. Data is public and read-only. The optional
`Authorization: Bearer <label>` header (or `?label=`) is **not
validated** — it's a non-secret source tag for the access log
(`web-community`, `cli-script`, …).

---

## 3. Choosing the right endpoint

For per-player state over time, four endpoints read the same underlying
streams but in different shapes. Pick by what you're drawing:

| You want… | Use | Why |
|---|---|---|
| A value **at one instant** (tooltip, scrubber readout) | **`/state-at`** | One carry-forward sample per field at `time`. |
| A **series/trend** on a fixed grid (charts, heatmaps) | **`/buckets`** | One reduced value per `windowMs` window. |
| **Every raw transition** in a window (native-rate detail, replay) | **`/stream-slice`** | Unreduced entries + carry-forward at window start. |
| A **discrete event log** (kill feed, life events, powerups) | **`/events`** | Tagged event list; authoritative for spawns/deaths. |

Concrete consequences:

- **Native-rate positions (~77 fps)** come **only** from
  `/stream-slice?fields=pos`. `/buckets` and `/state-at` down-sample
  position to one sample per window / instant.
- **Spawns & deaths**: `/events?types=spawn,death` is the authoritative
  log. `/stream-slice?fields=sp,d` gives the raw ms timestamp arrays.
  `/buckets?fields=sp,d` only yields a per-window bool (lossy — collapses
  a same-window death+respawn).

---

## 4. Endpoint reference

Headers (`X-Cache`, `ETag`, …) and the error envelope from §2 apply to
all endpoints and aren't repeated.

### 4.1 `POST /v1/demos/{id}` — loadDemo

Warm the cache and resolve the canonical id. Idempotent.

```jsonc
{ "demoId": "sha:abc…", "sha256": "abc…", "fromCache": true, "schemaVersion": 20 }
```

Use `demoId` for subsequent calls to skip the gameId→sha lookup.

### 4.2 `GET /v1/demos/{id}/overview` — getOverview

Curated "what was this match" summary, cheap enough to call first. Best
single call to populate a match header and decide which panels to show.

```jsonc
{
  "schemaVersion": 20,
  "map": "dm6", "gameDir": "qw",
  "mode": "4on4",            // omitempty
  "duration": 613.4,         // seconds
  "matchStart": 0, "matchEnd": 613.4,
  "teams":   [ { "name": "Die", "frags": 89 }, … ],          // sorted desc
  "players": [ { "name": "bps", "team": "Die", "frags": 35, "kills": 38, "deaths": 21, "suicides": 2 }, … ], // corrected scoreboard; sorted by frags desc
  "topStreaks":  [ { "player":"bps","weapon":"rl","length":7,"start":234.1,"duration":18.3 } ], // ≤5
  "topPowerups": [ { "player":"milton","type":"quad","start":412.0,"duration":29.7,"frags":5 } ], // ≤5
  "locCount": 47,
  "hasRegionControl": true,   // false ⇒ hide the region panel
  "playerUserIDs": { "bps": 123 },  // for hub.quakeworld.nu/games/<id>?track=<userId>
  "errors": [ … ]             // omitempty; non-empty ⇒ degraded analysis
}
```

`topStreaks`/`topPowerups` cap at 5; for the full lists use `/events`.
Composed in [`overview.go`](overview.go).

### 4.3 `GET /v1/demos/{id}/demoinfo`

KTX scoreboard, **verbatim** from the server — per-player weapon
accuracy, kills/deaths/TK, damage, sprees, item counts. Shape:
`result.DemoInfoResult` →
[RESULT_SCHEMA.md §DemoInfoResult](../mvd-analytics/RESULT_SCHEMA.md#demoinforesult-demoinfo).
`422 demoinfo_unavailable` on non-KTX demos.

### 4.4 `GET /v1/demos/{id}/metadata`

Full `fullserverinfo` cvars + parsed KTX match settings (timelimit,
mode, antilag, midair, instagib, …). Shape: `result.MetadataResult` →
[RESULT_SCHEMA.md §MetadataResult](../mvd-analytics/RESULT_SCHEMA.md#metadataresult-metadata).

### 4.5 `GET /v1/demos/{id}/frags`

Params: `players`, `weapon`. Total + per-player + per-weapon breakdown +
the full chronological kill log. Shape: `result.FragResult` →
[RESULT_SCHEMA.md §FragResult](../mvd-analytics/RESULT_SCHEMA.md#fragresult-frags).
For a kill feed with obituary text, prefer `/events?types=frag`.

### 4.5b `GET /v1/demos/{id}/damage`

Params: `players`, `weapon`. Per-hit damage reconstructed from the KTX
`mvdhidden_dmgdone` stream: total + per-player (`given`/`taken`/team/self,
per-weapon, and the **EWep** victim-weapon buckets
`enemyVsSg/enemyVsMid/enemyVsLg/enemyVsRl/enemyVsBoth` where
`ewep = lg+rl+both` = damage dealt to enemies *holding* RL/LG) +
attacker→victim `matrix` + the full chronological `events` log +
`telefrags` + `stomps` + a `scoreboard` cross-check against the KTX
end-of-match totals. Shape: `result.DamageResult` →
[RESULT_SCHEMA.md §DamageResult](../mvd-analytics/RESULT_SCHEMA.md#damageresult-damage).

**Units:** damage is **unbound** (includes overkill), so totals run
higher than the KTX scoreboard, which bounds each hit to the victim's
remaining health — see the `scoreboard` deltas (each pairs an `stream*`
unbound figure with the `score*` bounded KTX figure). The `weapon` filter
matches the **attacker's** weapon; the EWep buckets are keyed on the
**victim's** held weapons. `players` matches attacker OR victim. For the
raw time-ordered log alone use `/events?types=damage`.

**Positional kills** — telefrags (deathtype `tele`, the `9999` instakill
sentinel) and stomps (deathtype `stomp`, landing on a head) — are
**excluded** from every damage figure (a telefrag's 9999 would otherwise
dominate `given`/`byWeapon`/`ewep`/`totalDamage`; a stomp is a movement
kill, not a weapon). They are listed separately under `telefrags` /
`stomps`, counted per-player in `byPlayer.<name>.telefrags` / `.stomps`,
and exposed as the opt-in `telefrag` / `stomp` events (see §4.8). The
`weapon` filter treats their implicit weapon as `tele` / `stomp`. The
kill itself still appears in `/frags` and as a `frag` event.

### 4.6 `GET /v1/demos/{id}/loc-graph`

Per-map loc adjacency graph (nodes + directed transitions, with optional
combat-posture weights). Shape: `result.LocGraphResult` →
[RESULT_SCHEMA.md §LocGraphResult](../mvd-analytics/RESULT_SCHEMA.md#locgraphresult-locgraph).

### 4.7 `GET /v1/demos/{id}/backpacks`, `/items`, `/weapon-pickups`

KTX-hint-derived item analytics:

- **`/backpacks`** (`players`, `weapon`) — RL/LG drops. `[]result.BackpackDrop`.
- **`/items`** (`items`, `players`, `kinds`) — per-item pickup/respawn
  timeline. `result.ItemsResult`.
- **`/weapon-pickups`** (`players`, `weapon`, `source`) — slot-weapon
  acquisitions with kills-before-next-death; joins to backpacks via
  `backpackEnt`. `[]result.WeaponPickup`.

Shapes in
[RESULT_SCHEMA.md §Items / Backpacks / WeaponPickups](../mvd-analytics/RESULT_SCHEMA.md#itemsresult-items).

### 4.7b `GET /v1/demos/{id}/map-entities`

Params: `types`, `kinds`. The map's **static designed layout** — item
spawns, player spawnpoints, teleport destinations/sources, buttons —
with type + location. Per-map (identical for every demo on the map),
sourced from the BSP entity corpus. Shape: `result.MapEntitiesResult` →
[RESULT_SCHEMA.md §MapEntitiesResult](../mvd-analytics/RESULT_SCHEMA.md#mapentitiesresult-mapentities).
For the per-match pickup timeline use `/items` instead.

```jsonc
// ?types=item,teleportSrc,teleportDst&kinds=weapon
{ "map": "dm6", "entities": [
  { "type": "item", "class": "weapon_rocketlauncher", "kind": "rl",
    "name": "RA", "x": 1216, "y": -64, "z": 24, "loc": "RA" },
  // brush entity: anchored at bbox centre, carries the trigger volume
  { "type": "teleportSrc", "class": "trigger_teleport", "name": "GA",
    "x": 248, "y": -1784, "z": 83, "loc": "GA", "target": "t2",
    "bounds": { "min": [229,-1807,24], "max": [267,-1761,142] } },
  { "type": "teleportDst", "class": "info_teleport_destination",
    "name": "MH", "x": -512, "y": 480, "z": 24, "loc": "MH",
    "targetName": "t2" }
] }
```

`types` ∈ `item,spawn,teleportDst,teleportSrc,button,door`; `kinds` is an
item category (`armor,mega,health,powerup,weapon,ammo`) or a raw kind.
Brush entities (`teleportSrc`/`button`/`door`) carry a `bounds` volume;
link a teleporter's entrance to its exit via `teleportSrc.target` ==
`teleportDst.targetName`.

### 4.8 `GET /v1/demos/{id}/events`

Params: `from`, `to`, `players`, `types`, `loc`. A merged, time-sorted
event log. Shape: `view.EventsView`.

`types` selects event kinds; the **default set** (when `types` is empty)
is `frag,powerup,streak,spawn,death,weapon,item,chat`. High-frequency
state events `health`, `armor`, `loc`, and per-hit `damage` are
**excluded by default** — pass them explicitly to opt in. A `damage`
event carries `detail{ victim, damage, weapon, isSplash?, isEnv?,
isSelf?, isTeam?, victimWep? }`; `players` matches its attacker or
victim. For aggregates use `/damage` instead.

`telefrag` and `stomp` are also **opt-in** (the kill already appears as a
`frag` event, so they're left out of the default feed to avoid doubling
the kill count). Each carries `detail{ victim, isTeam? }` with `player` =
the killer.

```jsonc
// ?types=spawn,death&from=100&to=160
{ "events": [
  { "t": 101.298, "type": "death", "player": "diehuman" },
  { "t": 102.367, "type": "spawn", "player": "diehuman" },
  { "t": 104.199, "type": "death", "player": "sailorman" },
  …
] }
```

Envelope `t` is **seconds**. Some types carry a `detail` object (e.g. a
`loc` event's `{ "loc": "RA" }`, or `{ "li": 7 }` with `loc=index`).
This is the authoritative source for spawn/death life events.

### 4.9 `GET /v1/demos/{id}/stream-slice`

Params: `from`, `to`, `players`, `fields`, `loc`. Returns the **raw,
unreduced** change entries falling in `[from, to)`, plus a synthetic
carry-forward entry at the window start showing the value on entry;
intervals overlapping the window are clamped. Shape: `view.StreamSliceView`.

This is the faithful, native-rate view — the one to use for replay
scrubbers and detail charts.

```jsonc
// ?players=sailorman&fields=h,pos&from=105&to=106
{ "startTime": 105, "endTime": 106,          // SECONDS
  "players": [ {
    "name": "sailorman",
    "h":   [ { "t": 105000, "v": -7 }, { "t": 105182, "v": 100 } ],   // ms, value
    "pos": { "t": [105001,105014,105027,…],  // ms — 70 samples in this 1s window
             "x": [-1072,-1072,-1072,…], "y": […], "z": […] }
  } ] }
```

```jsonc
// ?players=sailorman&fields=rl&from=105&to=110   (interval field)
{ "startTime": 105, "endTime": 110,
  "players": [ { "name": "sailorman",
    "rl": [ { "s": 105000, "e": 105182 }, { "s": 106834, "e": 110000 } ] } ] }  // ms
```

⚠️ Entry `t` / `s` / `e` are **int32 ms** even though the envelope
`startTime`/`endTime` are seconds (see §2.1). With `fields=sp,d` you get
the raw spawn/death ms-timestamp arrays clipped to the window.

### 4.10 `GET /v1/demos/{id}/buckets`

Params: `windowMs`, `from`, `to`, `players`, `fields`, `reducers`,
`includeTeam`, `loc`, `layout`. One **reduced** value per `windowMs`
window per field — the shape for charts and heatmaps. Default reducer is
`first` (value at window start); override with `reducers`.

**`layout=column` (default)** → `view.ColumnarBuckets`: one dense typed
array per `(player, field)` over the player's active span, implicit time
axis `time(i) = startMs + i*windowMs` (**ms**), `0`/`1` `alive[]` mask,
booleans as `0`/`1`, loc always the raw `li` index. Compact; best for
series reads. Full shape:
[RESULT_SCHEMA.md §Columnar layout](../mvd-analytics/RESULT_SCHEMA.md#columnar-layout-viewbucketscolumnar-rest-layoutcolumn).

**`layout=row`** → `view.BucketsView`: one self-describing object per
bucket. Easier to read, larger.

```jsonc
// ?layout=row&windowMs=120000&fields=h,a&players=sailorman
{ "windowMs": 120000, "buckets": [
  { "t": 0,   "p": { "sailorman": { "h": 100, "a": 200 } } },   // bucket t = SECONDS
  { "t": 120, "p": { "sailorman": { "h": 100, "a": 0 } } }, … ] }
```

A partial trailing bucket carries `"partial": true`. For a point-in-time
read, prefer `/state-at` over indexing into buckets.

### 4.11 `GET /v1/demos/{id}/state-at`

Params: `time` (**required**, seconds), `players`, `fields`, `loc`.
Resolves each field at `time`: change streams carry-forward (latest
entry `≤ time`), intervals report `true` iff `time` ∈ an interval,
position is the nearest sample. Shape: `view.StateAtView`.

```jsonc
// ?time=105&players=sailorman&fields=h,a,rl,pos
{ "t": 105,                                   // SECONDS
  "players": { "sailorman": {
    "h": -7, "a": 0, "rl": true,              // h<0 ⇒ dead at t (died 104.199)
    "pos": { "x": -1072, "y": -348, "z": 216 } } } }
```

### 4.12 `GET /v1/demos/{id}/loc-trails`

Params: `from`, `to`, `players`, `minDwellMs`, `loc`. Per-player loc
residences with dwell spans; `minDwellMs` folds short blips into adjacent
residences. Shape: `view.LocTrailsView`.

```jsonc
// ?players=sailorman
{ "players": [ { "name": "sailorman", "sequence": [
  { "s": 0,     "e": 1.015, "loc": "tunnel" },        // s/e = SECONDS
  { "s": 1.015, "e": 2.638, "loc": "tunnel.LG" },
  { "s": 2.638, "e": 3.427, "loc": "spiral" }, … ] } ] }
```

### 4.13 `GET /v1/demos/{id}/region-control`

Params: `windowMs`. Per-region control share + per-player attribution,
re-derived at the requested resolution. Shape:
`result.RegionControlResult` →
[RESULT_SCHEMA.md §RegionControlResult](../mvd-analytics/RESULT_SCHEMA.md#regioncontrolresult-regioncontrol).
`422 region_control_unavailable` when the map has no region layout (check
`overview.hasRegionControl` first).

```jsonc
// ?windowMs=10000
{ "teamA": "blue", "teamB": "red",
  "regions": [ { "name": "QUAD", … }, … ],
  "stats": { "QUAD": {
    "teamAControl": 10, "teamBControl": 8.3, "empty": 78.3, …,   // percent
    "byPlayer": { "sailorman": { "team":"red","armed":3,"unarmed":1 }, … } } } }
```

### 4.14 `GET /v1/demos/{id}/loc-table`

The interned loc-name decoder for `loc=index` mode: `{ "locTable":
[…] }`, index 0 = `""` (no-loc). Fetch once per demo, then decode `li`
indices client-side.

### 4.15 `GET /v1/demos/{id}/chat`, `/healthz`, `/v1/version`

- **`/chat`** (`from`, `to`, `players`, `types`) — chat + teamsay only;
  `[]result.MatchEvent`.
- **`/healthz`** — `{ "ok": true, "schemaVersion": 20 }`.
- **`/v1/version`** — `{ "hash", "tag", "buildDate" }`.

### 4.16 Per-map static data — `GET /v1/maps/{map}/…`

Per-map data addressed by map name directly (no demo needed) — handy for
UIs that have a map name from `/overview` or a match listing.

- **`GET /v1/maps/{map}/entities`** (`types`, `kinds`) — the same static
  layout as `/demos/{id}/map-entities`, read from the embedded corpus.
  `{ map, entities: [...] }`. Aliases are resolved (`phantombase` →
  `phantoma`). `404 map_unavailable` when no corpus exists.
- **`GET /v1/maps/{map}/geometry`** — streams the per-map floor-polygon
  geometry JSON (`mapgeom.MapRegions`: `{ map, version, bounds, locs:[{
  name, z, tris:[…] }] }`) for renderers. Served from the server's
  `-maps-dir`; `404 map_unavailable` when unset or the map is missing.
  **REST-only — not an MCP tool** (the payload is large, up to tens of
  MB). Immutable cache + ETag; send `If-None-Match` for a 304.

---

## 5. Recipes

Common frontend features → the call that backs them.

- **Match header / scoreboard** → `GET /overview` (one call: teams,
  players, top streaks/powerups, degraded flag).
- **Kill feed with obituaries** → `GET /events?types=frag` (use
  `/frags` if you need the `isSuicide`/`isTeamKill` flags instead).
- **Score-over-time line** → `GET /events?types=frag`, accumulate
  `delta` client-side; or `/buckets?fields=sp,d` for activity density.
- **Health/armor chart for a player** → `GET /buckets?fields=h,a&windowMs=1000&players=X`
  (smooth grid) or `/stream-slice?fields=h,a&from=…&to=…` (every change).
- **Map replay / movement trails (~77 fps)** → `GET /stream-slice?fields=pos&players=X&from=…&to=…`
  — the only native-rate position source. Stitch windows for the full
  match. Remember positions are **int32 ms**.
- **Scrubber tooltip (state at playhead)** → `GET /state-at?time=T&fields=h,a,rl,pos`.
- **Life events / deaths timeline** → `GET /events?types=spawn,death`.
- **"Who controlled QUAD?"** → `GET /region-control?windowMs=10000`,
  read `stats.QUAD.byPlayer`.
- **Loc heatmap / movement graph** → `GET /loc-graph` (aggregate) or
  `/loc-trails` (per-player sequence with dwell).
- **Draw the map (items, spawns, teleporters as overlays)** → `GET /v1/maps/{map}/entities`
  (or `/demos/{id}/map-entities`); add `GET /v1/maps/{map}/geometry` for
  floor polygons to render underneath.
- **Weapon effectiveness** → `GET /demoinfo` (KTX accuracy/damage) or
  `/weapon-pickups` (kills-before-next-death).

When fetching positions or any raw stream in `index` loc mode, fetch
`/loc-table` once and decode client-side.

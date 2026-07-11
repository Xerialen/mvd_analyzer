# mvd-api HTTP reference

Integration guide for building **custom web frontends and tools** on top
of `mvd-api` — the hosted REST surface over QuakeWorld demo analytics.

This document is the **high-level guide**: getting started, the
cross-cutting conventions (units, caching, errors, auth, CORS), how to
choose the right endpoint, and task recipes. It deliberately does NOT
enumerate endpoints or restate shapes — that reference exists exactly
once, in the OpenAPI 3.1 document the server itself serves at
**`/openapi.yaml`** (browsable at **`/docs`**): every route, parameter,
full field-level response schema, and error code, pinned to the code by
drift tests and validated against golden-corpus responses. Deep
field-level *semantics* of the analysis output (the Result contract
shared with the Go library and WASM builds) live in
[`mvd-analytics/RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md).
JSON snippets here are **real captured output** (trimmed with `…`),
shown for orientation, not as a second schema.

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

A machine-readable **OpenAPI 3.1** description of the whole surface is
served at **`GET /openapi.yaml`** (embedded in the binary; drift tests pin
its routes, error codes and enums to the code, and its response schemas
are validated against golden-corpus responses), with a browsable viewer
at **`GET /docs`** (vendored RapiDoc — no external requests). Both are
reachable without an API key.

---

## 2. Conventions (read this once)

### 2.1 Time units — the one real gotcha

The API mixes two time units **on purpose**, and frontend code must
track which is which:

| Where | Unit | Examples (real output) |
|---|---|---|
| **Query inputs** `from` / `to` / `time` | **seconds** (float) | `?from=105&to=110`, `?time=105` |
| **View envelope** times | **seconds** (float) | `events[].t=101.298`, `state-at.t=105`, `stream-slice.startTime=105`, row-bucket `.t=120`, loc-trail `s`/`e`=`1.015` |
| **`/overview`** times | **int32 milliseconds** | `duration`, `matchStart`, `matchEnd`, `topStreaks[].start`/`.duration`, `topPowerups[].start`/`.duration`, `timing.*` |
| **Raw stream entries** embedded in `/stream-slice` | **int32 milliseconds** | `h:[{ "t":105000, "v":-7 }]`, `pos.t:[105001,…]`, `rl:[{ "s":105000,"e":105182 }]` |
| **Columnar buckets** axis | **int32 milliseconds** | `startMs`, `windowMs`, `time(i)=startMs+i*windowMs` |

Rule of thumb: anything the **view layer synthesises** (event lists,
window bounds, trail dwell spans, row-bucket timestamps) is **seconds**;
anything copied **verbatim from the stored schema** (the change-stream /
interval / position arrays, the columnar grid) is **int32 ms**. The
underlying schema is all int32 ms — see RESULT_SCHEMA.md §"Time units".
Scale ms→s by `* 0.001`.

### 2.2 Query parameters

Parameter **names are case-insensitive**; the canonical (documented)
spelling is camelCase — `windowMs`, `minDwellMs`, `includeTeam` — but
`windowms` / `WindowMs` resolve too. Parameter **values** are case-sensitive
for player names (QW names are case-significant) and case-insensitive for
weapon / item / kind / loc / layout tokens.

- **`players`, `fields`, `types`** — comma-separated lists; URL-decode
  once. Omit `players` to get all; omit `fields`/`types` to get the
  endpoint's default set.
- **`weapons`** — comma-separated weapon tokens (`rl,lg,…`) on `/frags`,
  `/damage`, `/backpacks`, `/weapon-pickups`. A CSV set on every one of
  them since schema v36 (`/backpacks` previously took a single value).
  The pre-16.2 singular spelling `weapon` remains an accepted legacy
  alias; `weapons` wins when both are present.
- **`reducers`** (`/buckets`) — comma-separated `field=name` pairs, e.g.
  `reducers=h=min,a=last`. Names come from the reducer registry in
  RESULT_SCHEMA.md.
- **`from` / `to`** — match-relative **seconds**. Omit for the whole
  match. Honoured by `events`, `stream-slice`, `loc-trails`, `chat`,
  `region-control`, and (schema-unchanged) `frags` / `damage`.
- **`summary`** (`/frags`, `/damage`, `/aim`, `/items`) — `1`/`true` drops
  the big per-event log / sample arrays / phase timeline and returns only
  the aggregates.
- **`time`** — match-relative **seconds**; **required** on `/state-at`.
- **`windowMs`** — integer milliseconds (`/buckets`, `/region-control`).
  ⚠️ **Defaults to 50 ms when omitted** — on a 20-minute match that is
  ~24,000 windows per field per player. Always pass an explicit
  `windowMs` sized to your question (the hosted MCP layer injects 5000).
  Note the unit split *within one call*: `windowMs` is **ms** while
  `from`/`to` are **seconds**.
- **There is deliberately no `limit`/`offset` pagination** on the
  per-demo endpoints: the data is time-series, so the size controls are
  the `from`/`to` window, `players`/`fields` scoping, and `summary`.
  (`searchGames`-style pagination exists only on the hub search, which
  is not part of this API.)
- **`loc`** — `name` (default) resolves loc indices to names; `index`
  returns the raw `LocTable` index for index-based math (decode via
  `/loc-table`). Honoured by `buckets`, `events`, `stream-slice`,
  `state-at`, `loc-trails`.
- **`layout`** (`/buckets` only) — `column` (default, compact) or `row`.
  See the `/buckets` operation in `/docs`.

The valid **field codes** (`h`, `a`, `rl`, `pos`, `view`, `hgt`, `lq`,
`vel`, `sp`, `d`, …) and **reducer names** are listed once in
[RESULT_SCHEMA.md §Field vocabulary / Reducer registry](../mvd-analytics/RESULT_SCHEMA.md#field-vocabulary).
Note (schema v31+): `pos` is **strictly x/y/z** (+ the per-sample loc
label `li`). The player's **view direction** is the opt-in `view` field
(raw `angle16` pitch/yaw state after `svc_playerinfo` delta
carry-forward, decode `deg = uint16(v)*360/65536`, pitch > 180° =
looking up); floor height is `hgt`; liquid state is `lq`;
**velocity** (vx/vy/vz, Quake units/sec, schema v32) is `vel`.
Height/liquid no longer ride along `pos` — request each by code.
Note (schema v33+): the coordinate values `pos` x/y/z, `vel` vx/vy/vz,
and `hgt` are **`float32`** Quake units (sub-unit precise — earlier
versions rounded them to whole `int32` units), so expect fractional
numbers in those arrays. In the dense outputs (`stream-slice` tracks and
`buckets` columns) they are serialized **rounded to 3 decimals**
(lossless for eighth-unit positions; trims the float tail on velocity),
so a value reads `-58.333`, not `-58.333332`. The point-in-time
`state-at` values are emitted at full float32 precision (low volume, so
not rounded). Only the **time axes** stay int32 ms (above). The `hgt`
no-floor sentinel is `-1000000000` (was `-2147483648`).

### 2.3 Caching (use it — the data is immutable)

Successful 2xx responses on the **per-demo** endpoints set:

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

Two families carry a **different ETag shape**:

- The generic artifact endpoint uses a **finer per-artifact** form
  `"<sha>-<name>@v<schemaVersion>"` (e.g. `"abc…-frag@v49"`), so a client can
  revalidate one artifact independently.
- The binary-static endpoints `/v1/artifacts` and `/v1/graph` depend
  only on the schema version, so their ETag is `"artifacts-v<n>"` /
  `"graph-v<n>"` (no sha). They set `Cache-Control` + `ETag` +
  `X-Schema-Version` but **no `X-Cache`** (nothing demo-cached to report).
- The per-map endpoints `/v1/maps/{map}/entities` and `/geometry` are
  demo-independent statics: ETags `"ents-<map>-v<corpusVersion>"` and
  `"geo-<map>-<size>"`, with **only** `Cache-Control` + `ETag` (no
  `X-Schema-Version` / `X-Cache`).

`POST /v1/demos/{id}` (the warm-up call) is a non-cacheable action: it
returns `X-Cache` / `X-Schema-Version` but **no** `Cache-Control` / `ETag`.
Error responses carry `Cache-Control: no-store` and no `ETag`.

Every response — success or error — also carries `X-Request-Id: <hex>`,
a per-request id echoed in the server access log (see §2.4).

### 2.4 Errors

Non-2xx responses use a stable envelope:

```json
{ "error": { "code": "demo_not_found", "message": "gameId 0" } }
```

| HTTP | `code` | Meaning |
|---|---|---|
| 400 | `invalid_demo_id` | malformed `{id}` |
| 400 | `invalid_param` | malformed **or rejected** query parameter — bad number, malformed `reducers` pair, unknown `loc`/`layout` token, unknown `fields` code, or unknown reducer name |
| 400 | `missing_param` | required param absent (e.g. `time` on `/state-at`) |
| 401 | `unauthorized` | **auth mode only** — missing / invalid / revoked API key on a protected route. Carries `WWW-Authenticate: Bearer`. The body is deliberately generic and never says whether the key was absent vs revoked (see §2.5). |
| 429 | `rate_limited` | **auth mode only** — per-key rate limit exceeded. Carries `Retry-After: <seconds>`; wait that long and retry (see §2.5). |
| 404 | `demo_not_found` | hub has no row for this gameId |
| 404 | `map_unavailable` | no entity corpus / geometry for this map (`/v1/maps/{map}/…`) |
| 404 | `artifact_unknown` | no servable artifact of that name (`/v1/demos/{id}/artifacts/{name}`) |
| 422 | `demoinfo_unavailable` | non-KTX server or aborted match |
| 422 | `metadata_unavailable` | no fullserverinfo / countdown centerprint |
| 422 | `frags_unavailable` | no frag log |
| 422 | `damage_unavailable` | no KTX `mvdhidden_dmgdone` damage stream |
| 422 | `shots_unavailable` | no shot data (no weapon fires decoded) |
| 422 | `aim_unavailable` | no aim data (needs shots + position/view streams) |
| 422 | `locgraph_unavailable` | no position track |
| 422 | `opening_unavailable` | no detected match start (`/v1/demos/{id}/artifacts/opening`) |
| 422 | `region_control_unavailable` | no region-control layout for this map |
| 422 | `airgibs_unavailable` | no timeline analysis (BSP-less maps return `[]`, not this) |
| 502 | `hub_upstream` | network / 5xx from the hub |
| 500 | `internal` | unexpected server error (see below) |

**5xx bodies are generic.** A `500 internal` never echoes the underlying
error text (it can embed local cache paths or upstream URLs). The body is a
fixed message plus the request id — `"internal server error (request id
<hex>)"` — and the real error is logged server-side keyed by that same
`X-Request-Id`. Quote the id when reporting a problem. `4xx` messages stay
specific and safe (they're user-actionable and path-free). The former
`panic` code is gone: a handler panic is now a plain `500 internal`.

(Schema v36 folded the former `view_error` code into `invalid_param`: a
bad query parameter is one error class regardless of whether it failed
syntactic parsing or view-layer validation.)

**Available vs unavailable — the `422` rule.** A `422 <section>_unavailable`
means the demo **structurally lacks the signal** that section needs — a
non-KTX server has no `demoinfo`/`damage`, a demo without a position track
has no `loc-graph`, a map without a region layout has no `region-control`.
These are **expected** for some demos; treat them as "this panel is
unavailable for this demo", not a hard failure, and use `/overview`
(`hasRegionControl`, `errors`) to hide panels up front. Endpoints whose data
is always computable or list-shaped — `/items`, `/backpacks`,
`/weapon-pickups`, `/chat` — instead return **`200` with an empty body**
when there's nothing, never `422`.

### 2.5 Authentication

mvd-api runs in one of two modes, chosen by the operator's `-auth-dir` flag.

**No-auth (localhost) mode — the default.** No key is required. The optional
`Authorization: Bearer <label>` header (or `?label=`) is **not validated** —
it's a non-secret source tag for the access log (`web-community`,
`cli-script`, …). This is the historical behaviour and is unchanged.

**Keyed (hosted) mode.** When the operator runs with `-auth-dir DIR`, every
route under `/v1/` — plus `POST /v1/demos/{id}` — requires an API key:

```
Authorization: Bearer qwmvd_<...>
```

- Keys look like `qwmvd_` followed by a URL-safe base64 blob. **The key is a
  secret** — treat it like a password: send it only over HTTPS, never put it
  in a URL, a query string, or a public repo. The server stores only a hash;
  a lost key cannot be recovered, only re-issued.
- Missing, malformed, or revoked keys get `401 unauthorized` with
  `WWW-Authenticate: Bearer`. The body never distinguishes those cases.
- Exempt from the key requirement: `GET /healthz`, `GET /v1/version`,
  `GET /openapi.yaml` + `GET /docs` (the API description and its viewer —
  the public contract, embedded bytes only), the `/portal/*` prefix (its
  own sign-in), and any `OPTIONS` preflight.
- **`GET /v1/auth/check`** → `204 No Content` for a live key, `401` otherwise.
  Use it to test a key without side effects:
  `curl -sSD- -o/dev/null -H "Authorization: Bearer qwmvd_…" https://host/v1/auth/check`.
- Requests are rate-limited **per key** (not per IP). Over the limit →
  `429 rate_limited` + `Retry-After: <seconds>`. Two classes exist: normal
  (portal) keys and looser `service` keys (issued to first-party apps).

**Getting a key.** On a deployment that runs with `-portal`, a user signs in
with Discord at **`https://<host>/portal`** and self-services one key (sign in
→ *Generate key* → copy it once). Regenerating revokes the old key. First-party
apps get a `service` key from the operator instead (the `keys` CLI). See
[mvd-api/README.md — "The Discord key portal"](README.md#the-discord-key-portal-getting-a-key)
for the full flow. The portal is off unless the operator enables it.

### 2.6 CORS (browser clients)

The API is CORS-enabled for any origin — it's read-only and
unauthenticated, so `*` is safe:

```
Access-Control-Allow-Origin: *
Access-Control-Expose-Headers: ETag, X-Cache, X-Schema-Version, X-Request-Id
```

`Expose-Headers` is what lets browser JS actually read those response
headers (notably `ETag`, for conditional GETs). Preflight `OPTIONS` on any
path returns `204` with `Access-Control-Allow-Methods: GET, POST, OPTIONS`,
`Access-Control-Allow-Headers: Authorization, Content-Type, If-None-Match`,
and `Access-Control-Max-Age`. Preflight needs no auth.

**CORS + auth interaction.** In keyed mode, CORS still runs *outside* the
auth check, so an `OPTIONS` preflight is answered (`204`, no key) before auth
is consulted — a browser's automatic preflight never fails on the missing
`Authorization` header. The actual `GET`/`POST` that follows still needs the
key. `Access-Control-Allow-Origin: *` and a credentialed `Authorization`
header coexist because the key travels as a plain header, not a cookie (the
CORS credentials mode that `*` forbids applies to cookies, not bearer tokens).

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

## 4. Endpoint reference — served by the API itself

The per-endpoint reference lives in the OpenAPI 3.1 document embedded in
the server, so it cannot drift from the running code:

- **`GET /openapi.yaml`** — machine-readable: all operations, reusable
  parameters, full field-level response schemas, the error-code /
  artifact / field-code enums. Drift tests pin it to the router and the
  code enums; a golden-corpus validation test checks every response
  schema against real responses.
- **`GET /docs`** — the same document rendered as a browsable reference
  (vendored RapiDoc; try-it console with Bearer auth; no external
  requests).

Both are reachable without an API key. Operation semantics that used to
be spelled out here — filtering/recompute rules, unit seams, the
artifact node↔resultKey mapping table, availability behaviour — are in
the operations' descriptions there. Field-level semantics of the
underlying Result sections stay in
[`RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md).

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
- **Aim arrows / sightlines / "who's looking at whom" (~77 fps)** →
  add `view` to the fields: `GET /stream-slice?fields=pos,view&players=X&from=…&to=…`.
  Decode `vp`/`vya` with `deg = uint16(v)*360/65536`; forward vector
  `= (cos p·cos y, cos p·sin y, −sin p)`.
- **Speed curve / bunny-hop analysis** → add `vel`:
  `GET /stream-slice?fields=vel&players=X&from=…&to=…`. Speed =
  `hypot(vx,vy,vz)`, horizontal = `hypot(vx,vy)`; expect ±1-unit
  quantization noise on the raw derivative, smooth client-side if needed.
- **Scrubber tooltip (state at playhead)** → `GET /state-at?time=T&fields=h,a,rl,pos`
  (add `view`/`hgt`/`lq` for look direction / height / liquid).
- **Life events / deaths timeline** → `GET /events?types=spawn,death`.
- **"Who controlled QUAD?"** → `GET /region-control?windowMs=10000`,
  read `stats.QUAD.byPlayer`.
- **Loc heatmap / movement graph** → `GET /loc-graph` (aggregate) or
  `/loc-trails` (per-player sequence with dwell).
- **Draw the map (items, spawns, teleporters as overlays)** → `GET /v1/maps/{map}/entities`
  (map name from `/overview`); add `GET /v1/maps/{map}/geometry` for
  floor polygons to render underneath.
- **Weapon effectiveness** → `GET /demoinfo` (KTX accuracy/damage) or
  `/weapon-pickups` (kills-before-next-death).

When fetching positions or any raw stream in `index` loc mode, fetch
`/loc-table` once and decode client-side.

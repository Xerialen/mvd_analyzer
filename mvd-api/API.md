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
| **`/overview`** times | **int32 milliseconds** | `duration`, `matchStart`, `matchEnd`, `topStreaks[].start`/`.duration`, `topPowerups[].start`/`.duration`, `timing.*` (see §4.2) |
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
- **`weapon`** — comma-separated weapon tokens (`rl,lg,…`) on `/frags`,
  `/damage`, `/backpacks`, `/weapon-pickups`. A CSV set on every one of
  them since schema v36 (`/backpacks` previously took a single value).
- **`reducers`** (`/buckets`) — comma-separated `field=name` pairs, e.g.
  `reducers=h=min,a=last`. Names come from the reducer registry in
  RESULT_SCHEMA.md.
- **`from` / `to`** — match-relative **seconds**. Omit for the whole
  match. Honoured by `events`, `stream-slice`, `loc-trails`, `chat`,
  `region-control`, and (schema-unchanged) `frags` / `damage`.
- **`summary`** (`/frags`, `/damage`) — `1`/`true` drops the big per-event
  log and returns only the aggregates.
- **`time`** — match-relative **seconds**; **required** on `/state-at`.
- **`windowMs`** — integer milliseconds (`/buckets`, `/region-control`).
- **`loc`** — `name` (default) resolves loc indices to names; `index`
  returns the raw `LocTable` index for index-based math (decode via
  `/loc-table`). Honoured by `buckets`, `events`, `stream-slice`,
  `state-at`, `loc-trails`.
- **`layout`** (`/buckets` only) — `column` (default, compact) or `row`.
  See §4.10.

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

Two families carry a **different ETag shape**:

- The generic artifact endpoint (§4.17b) uses a **finer per-artifact** form
  `"<sha>-<name>@v<schemaVersion>"` (e.g. `"abc…-frag@v49"`), so a client can
  revalidate one artifact independently.
- The binary-static endpoints `/v1/artifacts` and `/v1/graph` (§4.17) depend
  only on the schema version, so their ETag is `"artifacts-v<n>"` /
  `"graph-v<n>"` (no sha).

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
| 404 | `artifact_unknown` | no servable artifact of that name (`/v1/demos/{id}/artifacts/{name}`; see §4.17) |
| 422 | `demoinfo_unavailable` | non-KTX server or aborted match |
| 422 | `metadata_unavailable` | no fullserverinfo / countdown centerprint |
| 422 | `frags_unavailable` | no frag log |
| 422 | `damage_unavailable` | no KTX `mvdhidden_dmgdone` damage stream |
| 422 | `shots_unavailable` | no shot data (no weapon fires decoded) |
| 422 | `aim_unavailable` | no aim data (needs shots + position/view streams) |
| 422 | `locgraph_unavailable` | no position track |
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
- Exempt from the key requirement: `GET /healthz`, `GET /v1/version`, the
  `/portal/*` prefix (its own sign-in), and any `OPTIONS` preflight.
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

## 4. Endpoint reference

Headers (`X-Cache`, `ETag`, …) and the error envelope from §2 apply to
all endpoints and aren't repeated.

### 4.1 `POST /v1/demos/{id}` — loadDemo

Warm the cache and resolve the canonical id. Idempotent.

```jsonc
{ "demoId": "sha:abc…", "sha256": "abc…", "fromCache": true, "schemaVersion": 48 }
```

Use `demoId` for subsequent calls to skip the gameId→sha lookup.

### 4.2 `GET /v1/demos/{id}/overview` — getOverview

Curated "what was this match" summary, cheap enough to call first. Best
single call to populate a match header and decide which panels to show.

```jsonc
{
  "schemaVersion": 48,
  "map": "dm6", "gameDir": "qw",
  "mode": "4on4",            // omitempty
  "duration": 613400,        // int32 ms (NOT seconds — see §2.1)
  "matchStart": 0, "matchEnd": 613400,   // int32 ms
  "teams":   [ { "name": "Die", "frags": 89 }, … ],          // sorted desc
  "players": [ { "name": "bps", "team": "Die", "frags": 35, "kills": 38, "deaths": 21, "suicides": 2 }, … ], // corrected scoreboard; sorted by frags desc
  "topStreaks":  [ { "player":"bps","weapon":"rl","length":7,"start":234100,"duration":18300 } ], // ≤5; start/duration int32 ms
  "topPowerups": [ { "player":"milton","type":"quad","start":412000,"duration":29700,"frags":5 } ], // ≤5; start/duration int32 ms
  "locCount": 47,
  "hasRegionControl": true,   // false ⇒ hide the region panel
  "timing": {                 // omitempty; demo-open wall-clock anchor (from streams.global)
    "demoOffset": 10125,           // ms, demo open → match start
    "demoStartUnixMs": 1780756716100,  // server clock at demo open
    "demoStartAccuracyMs": 1,          // 1 = mvdhidden 0x000B ms block; 1000 = `epoch` cvar
    "pauses": [ { "atMs": 18340, "durationMs": 6641 } ]  // omitempty; per-pause segments
  },
  "playerUserIDs": { "bps": 123 },  // for hub.quakeworld.nu/games/<id>?track=<userId>
  "errors": [ … ]             // omitempty; non-empty ⇒ degraded analysis
}
```

`topStreaks`/`topPowerups` cap at 5; for the full lists use `/events`.
Composed in [`overview.go`](overview.go).

**Time units — `/overview` is int32 milliseconds, not seconds.** Unlike the
view-envelope endpoints (§2.1), every time field here is copied verbatim from
the stored schema and is **int32 ms**: `duration`, `matchStart`, `matchEnd`,
each `topStreaks[].start`/`.duration`, each `topPowerups[].start`/`.duration`,
and the whole `timing` block (`demoOffset`, `demoStartUnixMs`,
`demoStartAccuracyMs`, `pauses[].atMs`/`.durationMs`). Scale ms→s by `* 0.001`.

**Wall-clock mapping.** Use `timing` to convert any match-relative game
time `g` (ms) to a real-world clock — handy for syncing voice tracks or
stream overlays. The game clock freezes during a pause, so fold the pauses
in:

```
wallClockMs = demoStartUnixMs + demoOffset + g + Σ pauses[i].durationMs (atMs ≤ g)
              (±demoStartAccuracyMs)
```

`timing` is omitted when the demo carries no wall-clock source; `pauses` is
omitted when the match had none. See
[RESULT_SCHEMA.md → GlobalStream](../mvd-analytics/RESULT_SCHEMA.md#globalstream).

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

Params: `players`, `weapon`, `from`, `to`, `summary`. Total + per-player +
per-weapon breakdown + the full chronological kill log. Shape:
`result.FragResult` →
[RESULT_SCHEMA.md §FragResult](../mvd-analytics/RESULT_SCHEMA.md#fragresult-frags).
For a kill feed with obituary text, prefer `/events?types=frag`.

- **`from` / `to`** — match-relative SECONDS (float); keep only kills at
  `time ≥ from` / `time ≤ to`. `0` disables that bound (the default).
- **`summary`** — `1`/`true` drops the big per-event `frags` log and returns
  only the aggregates (avoids overflowing an LLM context). Orthogonal to the
  filters: it never by itself triggers a recompute.

**Filtering semantics (changed — bug fix).** When ANY scoping filter
(`players` OR `weapon` OR `from` OR `to`) is active, **every** aggregate
(`totalFrags`, `byPlayer`, `byWeapon`) is **recomputed from the filtered kill
log** so the response is internally consistent with the entries shown — not
just the `frags` list and `byPlayer` keys as before. With **no** scoping filter
the response is the authoritative stored totals, unchanged and byte-identical
to prior behaviour. Recomputed (filtered) aggregates are log-sourced: they
reflect exactly the shown entries and may differ from the authoritative
unfiltered totals for reconnect / unresolved-name edge cases (per-player
`deaths` in the unfiltered result come from the protocol death signal, and
top-level `byWeapon` counts some generic-killer obituaries the log omits — so a
filtered recompute cannot reproduce those exactly, by construction). `players`
matches killer OR victim; `weapon` matches the kill weapon.

### 4.5b `GET /v1/demos/{id}/damage`

Params: `players`, `weapon`, `from`, `to`, `summary`. Per-hit damage
reconstructed from the KTX
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

- **`from` / `to`** — match-relative SECONDS (float); keep only hits at
  `time ≥ from` / `time ≤ to`. `0` disables that bound (the default).
- **`summary`** — `1`/`true` drops the big per-hit `events` log and returns
  only the aggregates. Orthogonal to the filters.

**Match-only (schema v50).** The damage output — the aggregates AND the per-hit
`events` log — is match-time only. Out-of-match (warmup / post-match) hits are
dropped at the source and excluded everywhere; there is no way to see them.

**Filtering semantics.** When ANY scoping filter (`players` OR `weapon` OR
`from` OR `to`) is active, **every** aggregate (`totalDamage`, `byPlayer`
given/taken/byWeapon/EWep buckets, `byWeapon`, `matrix`) is **recomputed from
the filtered per-hit log** so it is consistent with the entries shown. This also
fixes a gap where filtered responses left `matrix` (and `events`) null. Damage
aggregates are a pure function of the per-hit `events`, and `events` is
match-gated at the source, so an all-players recompute reproduces the stored
numbers exactly (both are folds of the same in-match hit set). With **no**
scoping filter the response is the authoritative stored totals, unchanged.

**Positional kills** — telefrags (deathtype `tele`, the `9999` instakill
sentinel) and stomps (deathtype `stomp`, landing on a head) — are
**excluded** from every damage figure (a telefrag's 9999 would otherwise
dominate `given`/`byWeapon`/`ewep`/`totalDamage`; a stomp is a movement
kill, not a weapon). They are listed separately under `telefrags` /
`stomps`, counted per-player in `byPlayer.<name>.telefrags` / `.stomps`,
and exposed as the opt-in `telefrag` / `stomp` events (see §4.8). The
`weapon` filter treats their implicit weapon as `tele` / `stomp`. The
kill itself still appears in `/frags` and as a `frag` event.

### 4.5c `GET /v1/demos/{id}/aim`

No params. Per-player aim analysis (`result.Aim`): the `weapons` array
(per-weapon shots/hits, SG/SSG pellet stats + full/partial/miss, RL/GL
direct/splash/missed, the LG miss/blocked/out-of-range whiff split, plus
`enemy`/`team`/`self` per-victim-class counter slices — emitted only when a
weapon had team or self hits; see RESULT_SCHEMA.md §WeaponAimSplit for the
fallback rules), columnar `crosshair` samples for hitscan fires (signed
degrees off the enemy + a version normalized by the target's angular size,
so radius 1 ≈ the hitbox edge, with hit flag + attributed target + a `team`
flag when the target is a teammate), and the `lgRamp` series (per-LG-cell
hit vs ms since the shaft opened, with a `team` flag on teammate-only
connects). `mode` is `"duel"` (exact target) or
`"team"`; hits attribute to the server-confirmed victim, misses to the
nearest-crosshair enemy alive at the fire time (a heuristic in team games).
Shape: `result.AimResult` →
[RESULT_SCHEMA.md §AimResult](../mvd-analytics/RESULT_SCHEMA.md#aimresult-aim).

**Availability:** served from the always-full base parse. mvd-api parses every
demo with the projectile/beam/nail streams built (since phase 12 — the +3–4%
parse cost buys the enriched blocks on every request, which is what this
endpoint has effectively served since phase 5.3), so the stream-derived weapon
blocks are always present with no second parse. 422 (`aim_unavailable`) when the
demo has no shots/position data.

> **Removed in phase 12:** the `X-Shot-Streams: unavailable` degrade header and
> its `Cache-Control: no-store`. There is no longer a lazy stream rebuild that
> can fail on evicted bytes — the streams are in the base parse — so `/shots`,
> `/aim` and `/streams/*` never emit that header. Callers that sniffed it should
> stop; the normal immutable cache headers always apply.

### 4.5d `GET /v1/demos/{id}/shots`

The per-fire weapon stream (`result.Shots`): `shots` — every detected fire,
chronological, with `time` (match ms), `player`, `weapon`, `source`
(`sound`/`beam`), `hit` + `victims` where linkable (plus
`victimKinds` classifying each victim `enemy`/`team`/`self`, omitted when
all-enemy). The `shots` stream is **match-only** (warmup / prewar /
post-match fires are dropped at the source). `byPlayer` —
match-time per-weapon counts, hitscan accuracy and the
`enemyHits`/`teamHits`/`selfHits` victim-class buckets (overlapping — a
multi-victim fire counts in each); `reconciliation` — the cross-check
against KTX's authoritative `acc.attacks`. Served from the same always-full
base parse as `/aim`, so rl/gl fires carry their projectile-linked hits and
ng/sng fires their nail-linked ones.

| param | meaning |
|---|---|
| `nails` | deprecated, accepted and ignored. ng/sng fires were always in the stream; their flight-linking + accuracy (formerly gated on this param) is now always included, because a latch-dependent body under an immutable ETag broke HTTP caching. |

422 (`shots_unavailable`) when the demo has no shot data.

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
  `backpackEnt`. `[]result.WeaponPickup`. `source` is
  `world`/`backpack`/`unknown` (schema v46: weapon-stay demos carry
  synthesized `inferred` entries; `unknown` = a grant with no weapon
  pad in touch range, typically a non-RL/LG pack).

Shapes in
[RESULT_SCHEMA.md §Items / Backpacks / WeaponPickups](../mvd-analytics/RESULT_SCHEMA.md#itemsresult-items).

> The map's static designed layout is **per-map data**, served only by
> `GET /v1/maps/{map}/entities` (§4.16) — it is identical for every demo on
> a map. The demo-scoped `/v1/demos/{id}/map-entities` was **removed in
> schema v36**; a caller holding a demo id reads the map name from
> `/overview` first. For the per-match pickup timeline use `/items`.

### 4.8 `GET /v1/demos/{id}/events`

Params: `from`, `to`, `players`, `types`, `loc`. A merged, time-sorted
event log. Shape: `view.EventsView`.

`types` selects event kinds; the **default set** (when `types` is empty)
is `frag,powerup,streak,spawn,death,weapon,item,chat`. High-frequency
state events `health`, `armor`, `loc`, and per-hit `damage` are
**excluded by default** — pass them explicitly to opt in. A `damage`
event carries `detail{ victim, damage, weapon, isSplash?, isEnv?,
isSelf?, isTeam?, victimWep? }`; `players` matches its attacker or
victim. `damage` events are **match-only** — out-of-match (warmup /
post-match) hits are excluded here too (they are gated at the source, so
this feed shows the same in-match hits as `/damage`). For aggregates use
`/damage` instead.

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
             "x": [-1072,-1071.875,-1071.5,…], "y": […], "z": […] }  // x/y/z float32 units
  } ] }
```

```jsonc
// ?players=sailorman&fields=rl&from=105&to=110   (interval field)
{ "startTime": 105, "endTime": 110,
  "players": [ { "name": "sailorman",
    "rl": [ { "s": 105000, "e": 105182 }, { "s": 106834, "e": 110000 } ] } ] }  // ms
```

```jsonc
// ?players=sailorman&fields=view,hgt,lq&from=105&to=106   (position-derived)
{ "startTime": 105, "endTime": 106,
  "players": [ { "name": "sailorman",
    // each projects into its own sibling track with its own t axis
    "view": { "t": [105001,105014,…], "vp": [288,289,…], "vya": [16384,16390,…] }, // raw angle16
    "hgt":  { "t": [105001,105014,…], "h":  [0,0,40.96875,…] }, // float32 units above floor (BSP only)
    "lq":   { "t": [105001,105014,…], "lq": [0,0,5,…] },     // 0 dry, else (type<<2)|level
    "vel":  { "t": [105001,105014,…], "vx": [312.5,318.27,…], "vy": [-44.6,…], "vz": [0,…] } } ] }  // float32 units/sec
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
    "pos": { "x": -1072, "y": -348.5, "z": 216.125 } } } }  // float32 units
```

### 4.11b `GET /v1/demos/{id}/los`

No params. Per-player **line of sight** (`los`) and **potential visibility**
(`pvs`): for each ordered pair, the half-open `[s,e)` **millisecond** intervals
during which the looker could see the opponent.

- `los` — a clear geometric sightline: eye `origin+(0,0,22)` → any of the
  opponent's 8 bbox corners + midpoint, unblocked by worldspawn solids or any
  active mover posed in the way.
- `pvs` — the opponent is in the looker's **potentially-visible set**,
  reproducing the server's per-client entity cull: whether a live mvdsv would
  have sent that opponent to this player's client that frame
  (`SV_PlayerVisibleToClient` — the looker's fat PVS ∩ the opponent's
  expanded-box leaf set, or always-sent on leaf overflow). The recorded MVD
  stores every entity (`pvs = NULL`), so this is reconstructed from positions.
  This test also gates the LOS raycast, so **PVS ⊇ LOS** by construction — every
  `los` interval lies inside a `pvs` one. The gap (in PVS but never in LOS) is an
  occlusion-tolerant proximity/awareness signal: same vis region, no direct
  sightline.

Both are asymmetric — `los`/`pvs` on player A with `o = B` is A→B; B→A lives on
B. `o` indexes the `players` array. Both share shape (`{o, iv}`) and gating.

LOS/PVS are **computed lazily** by one pass: it is the heaviest position-derived
pass, so it is not in the default parse — the **first** request for a demo runs
it (a few seconds on a large 4on4) and writes the result to the tier-3 artifact
cache, so later requests — and later processes, after a restart or an LRU
eviction — splice it from disk instead of recomputing. BSP-backed maps only; on
a map with no BSP every player's `los` and `pvs` are omitted (and that empty
result is itself cached, so the pass runs at most once). View direction is not
considered (geometric visibility, not FOV).

```jsonc
{ "players": [
  { "name": "ok98",
    "los": [
      { "o": 1, "iv": [ { "s": 167696, "e": 169316 },     // ok98 saw players[1]
                        { "s": 193422, "e": 193622 } ] },  // s/e = MILLISECONDS
      { "o": 2, "iv": [ … ] } ],
    "pvs": [
      { "o": 1, "iv": [ { "s": 160000, "e": 172000 }, … ] },  // superset of los
      { "o": 2, "iv": [ … ] } ] },
  { "name": "realpit", "los": [ … ], "pvs": [ … ] }, … ] }
```

### 4.11c `GET /v1/demos/{id}/streams/{projectiles|beams|nails}`

Three endpoints serving the **spatial weapon-fire streams** for the map view
(schema v40), each read independently:

- `/streams/projectiles` → `{ "projectiles": ProjectileStreams | null }` —
  every rocket/grenade flight as a spawn→despawn segment + times.
- `/streams/beams` → `{ "beams": BeamStreams | null }` — every LG
  `TE_LIGHTNING2` bolt as a muzzle→impact segment + time.
- `/streams/nails` → `{ "nails": ProjectileStreams | null }` — ng/sng spike
  flights (`Weapon` == `"nail"`). Highest volume.

No params. All columnar (parallel arrays), times **match-relative
milliseconds**. Shapes are in
[RESULT_SCHEMA.md → ProjectileStreams / BeamStreams](../mvd-analytics/RESULT_SCHEMA.md#projectilestreams-streamsprojectiles).
The body field is `null` when the demo has none (e.g. no LG → `beams: null`).

All three are built by the **always-full base parse** (since phase 12 — mvd-api
turns on `BuildShotStreams`+`BuildNails` on every parse, at a +3–4% parse cost),
so these are plain reads off the cached Result: no re-parse, no first-request
latency, and no degrade. (Phase 12 deleted the old lazy `shot-streams` re-parse,
its tier-3 side-gob, and the `X-Shot-Streams: unavailable` header — served
bodies are unchanged because mvd-api enriched them on every request since phase
5.3.)

```jsonc
// GET /streams/projectiles
{ "projectiles": {
  "w":  ["rl", "rl", "gl"],          // weapon per flight
  "s":  [3042, 5210, 9001],          // spawn ms
  "e":  [3065, 5470, 9800],          // despawn ms
  "sx": [88, …], "sy": […], "sz": […],   // muzzle
  "ex": [88, …], "ey": […], "ez": […] }  // impact
}
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

Params: `windowMs`, `from`, `to`. Per-region control share + per-player
attribution, re-derived at the requested resolution; `from`/`to`
(match-relative seconds, omit for the whole match) clip the computation to
a sub-window — e.g. "who controlled QUAD between 4:00 and 6:00". Shape:
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

### 4.13b `GET /v1/demos/{id}/airgibs`

No params. The Key Moments airgib list (`timelineAnalysis.airgibs`): every
**direct** enemy rocket hit on an airborne victim above the qualification
height, sorted by height descending. Each entry carries `time`, `attacker` /
`victim` (+ teams, user ids), `height` (victim's feet above the floor),
`heightAboveAttacker` (the vertical gap the rocket climbed; negative =
victim below the shooter), `loc`, `damage` (unbound), and the `lethal`
heuristic. Shape: `[]result.AirgibEvent` →
[RESULT_SCHEMA.md](../mvd-analytics/RESULT_SCHEMA.md). Height needs the
map's clip hull, so the list is **empty (not an error) when the map's BSP
was not provisioned** at parse time. `422 airgibs_unavailable` only when
the demo has no timeline analysis at all.

### 4.14 `GET /v1/demos/{id}/loc-table`

The interned loc-name decoder for `loc=index` mode: `{ "locTable":
[…] }`, index 0 = `""` (no-loc). Fetch once per demo, then decode `li`
indices client-side.

### 4.15 `GET /v1/demos/{id}/chat`, `/healthz`, `/v1/version`

- **`/chat`** (`from`, `to`, `players`, `types`) — chat + teamsay only;
  `[]result.MatchEvent`.
- **`/healthz`** — `{ "ok": true, "schemaVersion": 48 }`.
- **`/v1/version`** — `{ "hash", "tag", "buildDate" }`.

### 4.16 Per-map static data — `GET /v1/maps/{map}/…`

Per-map data addressed by map name directly (no demo needed) — handy for
UIs that have a map name from `/overview` or a match listing.

- **`GET /v1/maps/{map}/entities`** (`types`, `kinds`) — the map's
  **static designed layout**: item spawns, player spawnpoints, teleport
  destinations/sources, buttons, with type + location, read from the
  embedded BSP entity corpus (identical for every demo on the map).
  Shape: `result.MapEntitiesResult` →
  [RESULT_SCHEMA.md §MapEntitiesResult](../mvd-analytics/RESULT_SCHEMA.md#mapentitiesresult-mapentities).
  `404 map_unavailable` when no corpus exists. For the per-match pickup
  timeline use `/demos/{id}/items`.

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
- **`GET /v1/maps/{map}/geometry`** — streams the per-map floor-polygon
  geometry JSON (`mapgeom.MapRegions`: `{ map, version, bounds, locs:[{
  name, z, tris:[…] }], walls?:[…], liquids?:[{ kind, tris:[…] }],
  submodels?:[{ id, tris:[…] }], pruned?:{ demos, points, facesDropped } }`)
  for renderers. `tris` is a flat float list, 9 per triangle (x,y,z per
  vertex) since `version` 2; version-1 files carried 6 (XY only, with the
  region-median `z` as the only height hint). `version` 3 adds the
  optional top-level `walls` (same 9-float triangle layout, vertical
  faces) for occluding 3D renders. `version` 4 adds optional `liquids`
  (water/slime/lava volume meshes, `kind` one of those three),
  `submodels` (brush-model lifts/doors keyed by their `*id` index, posed
  at runtime from the result's mover streams) and a `pruned` provenance
  block on usage-pruned files. All fields are presence-based, so a v4
  reader handles older files and vice-versa. Served from the server's
  `-maps-dir`; `404 map_unavailable` when unset or the map is missing.
  **REST-only — not an MCP tool** (the payload is large, up to tens of
  MB). Immutable cache + ETag; send `If-None-Match` for a 304.

> **MCP tool coverage.** `/los`, `/shots`, `/streams/{projectiles,beams,nails}`,
> and `/airgibs` are reachable over REST (and, when the MCP server runs, over
> HTTP with a `Bearer` key) but have **no dedicated MCP tool** yet — like
> `/geometry` above, they are omitted from the tool surface for now. `/los` is
> still reachable through the generic `getArtifact` tool (`name=los`). See
> `mvd-mcp/README.md` for the full MCP tool list; adding first-class tools for
> these is deferred.

### 4.17 The artifact surface — `GET /v1/artifacts`, `/v1/graph`

The analytics pipeline is an explicit DAG of **artifacts** (per-demo,
parameter-free, cacheable results — `frags`, `damage`, `los`, …). These two
endpoints expose the DAG itself; §4.17b serves any artifact generically. They
carry **no demo** and are static per binary, so their ETag is keyed only on the
schema version (`"artifacts-v<n>"` / `"graph-v<n>"`).

**`GET /v1/artifacts`** — the manifest: one entry per DAG node. `resultKey` is
the top-level Result JSON key the artifact lands under (`""` for internal
pseudo-artifacts like `clock`/`roster` that are not served); `servable` is true
when §4.17b can serve it (it has a `resultKey`, or it is the lazy `los`
artifact); `cost` is `heavy` for the lazy `los` pass and `light` otherwise. The
authoritative catalog (with descriptions) is the generated
[`mvd-analytics/ARTIFACTS.md`](../mvd-analytics/ARTIFACTS.md).

```jsonc
{ "schemaVersion": 49, "artifacts": [
  { "name": "clock", "requires": null, "provides": ["clock"],
    "mutates": false, "lazy": false, "cost": "light", "resultKey": "",
    "servable": false, "description": "Match clock — match start/end, pauses, …" },
  { "name": "demoinfo", "requires": null, "provides": ["demoinfo"],
    "mutates": false, "lazy": false, "cost": "light", "resultKey": "demoInfo",
    "servable": true, "description": "KTX demoinfo scoreboard blob: …" },
  // …
  { "name": "los", "requires": ["timeline","demoinfo"],
    "provides": ["los"], "mutates": false, "lazy": true, "cost": "heavy",
    "resultKey": "", "servable": true, "description": "Per-player line-of-sight …" }
] }
```

**`GET /v1/graph`** — the DAG as JSON: `{ nodes:[…], edges:[…] }`. Each node
carries the manifest fields (`cost`, `resultKey`, `lazy`, `mutates`) plus
`depth` (its layer in the dependency DAG — 0 for a root, deeper for nodes
with dependencies); each edge is `{ from, to, artifact }` (the provider →
consumer link for one required artifact). For a frontend "how does this
connect" panel.

```jsonc
{ "nodes": [
    { "name": "demoinfo", "requires": null, "provides": ["demoinfo"],
      "mutates": false, "lazy": false, "depth": 0, "cost": "light",
      "resultKey": "demoInfo" }, … ],
  "edges": [ { "from": "demoinfo", "to": "identity", "artifact": "demoinfo" }, … ] }
```

### 4.17b `GET /v1/demos/{id}/artifacts/{name}` — the generic accessor

Materialise and serve any **servable** artifact by name (the DAG node name from
the manifest — e.g. `frag`, `damage`, `loc-graph`, `los`). This
is a thin generic accessor, not a filtered view: the curated endpoints (§4.5 ff.)
remain the ergonomic surface with their `players`/`weapon`/window params. Use
this to reach an artifact that has no curated endpoint, or to enumerate the
surface programmatically from the manifest.

- **No query parameters.** Parameterised reads are views (§4.8–4.13); any query
  param is a `400 invalid_param`.
- **Closed registry.** `name` must be a servable artifact or you get
  `404 artifact_unknown` — this includes internal nodes (`clock`, `roster`,
  `identity`, the post-processor pseudo-artifacts). No user input reaches the
  filesystem beyond the validated name.
- **ETag** is the finer per-artifact form `"<sha>-<name>@v<n>"`.

**Eager artifacts** serve their Result section under its `resultKey`, applying
the same 422-vs-200 rule as the curated endpoints (an object-shaped section the
demo lacks → `422 <section>_unavailable`; a list-shaped / always-computable
section → `200` with a possibly-empty body):

```jsonc
// GET /v1/demos/{id}/artifacts/frag   → {"frags": FragResult}
{ "frags": { "totalFrags": 43, "byPlayer": { … },
    "frags": [ { "time": 13183, "killer": "GRID", "victim": "Evil's kid", "weapon": "rl" }, … ] } }
```

> Note: `frag` → `{"frags": …}`, `demoinfo` → `{"demoInfo": …}` — the body key is
> the artifact's `resultKey`, not the node name. The `shots`/`aim` sections here
> are already stream-enriched: the base parse is always-full (phase 12), so the
> projectile/beam/nail-derived splits are on every Result.

**Lazy artifact** `los` is materialised on demand exactly like `/los` (first
request computes, then it is cached) and serves the same body:

```jsonc
// GET /v1/demos/{id}/artifacts/los          → identical to GET …/los
{ "players": [ { "name": "Evil's kid", "los": [ … ], "pvs": [ … ] }, … ] }
```

The spatial weapon-fire streams are **not** a separate lazy artifact anymore
(phase 12 folded them into the always-full base parse); read them via
`/streams/{projectiles,beams,nails}` (§4.11c) or the enriched `/shots`, `/aim`.

Per-deployment disabling of `Heavy`-cost artifacts (plan §7) is **deferred** —
there is no config knob yet; `los` is always reachable.

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

# mvd-api — REST host for QuakeWorld demo analytics

`mvd-api` exposes [`mvd-analytics/view`](../mvd-analytics/view) as a
hosted HTTP REST API, backed by a three-tier on-disk cache that
resolves and downloads demos from
[hub.quakeworld.nu](https://hub.quakeworld.nu) on demand.

It's the server-side companion to [`mvd-mcp`](../mvd-mcp/README.md)
(the distributable stdio MCP shim that forwards tool calls to this
binary).

## Usage

```
mvd-api [serve] [-addr ADDR] [-cache-dir PATH] [-cache-max-bytes N] [-max-parses N] [-log-format text|json] [-auth-dir DIR] [-portal -portal-base-url URL]
mvd-api version
mvd-api cache stats [-cache-dir PATH]
mvd-api cache prune [-cache-dir PATH] [-max-bytes N | -older-than 30d | -all]
mvd-api keys issue  -auth-dir DIR [-service] [-note S] [-discord-id ID] [-discord-name N]
mvd-api keys revoke -auth-dir DIR (-key K | -hash H | -discord-id ID)
mvd-api keys list   -auth-dir DIR
```

| Flag | Default | Description |
|---|---|---|
| `-addr`             | `:8080`                                 | Listen address |
| `-cache-dir`        | `$XDG_CACHE_HOME/qw-mvd` or `~/.cache/qw-mvd` | On-disk cache root |
| `-cache-max-bytes`  | `21474836480` (20 GiB)                  | Disk budget for cache tiers 1–3; a background sweep evicts the oldest files (by mtime) when exceeded. `0` disables eviction (stale atomic-write temp files are still reaped) |
| `-max-parses`       | `max(1, NumCPU/2)`                      | Max concurrent heavy cold operations — a demo download+parse or an on-demand LOS raycast (both bounded by one semaphore; cache hits are unbounded) |
| `-maps-dir`         | _(empty)_                               | Directory of per-map geometry JSON for `/v1/maps/{map}/geometry`; empty disables that endpoint (ship `dist/maps/` next to the binary to enable) |
| `-log-format`       | `text`                                  | Access log format: `text` or `json` |
| `-auth-dir`         | _(empty)_                               | Directory holding `keys.json`. **Empty = no auth** (localhost mode; today's behaviour). When set, `/v1/*` and `POST /v1/demos/{id}` require an `Authorization: Bearer qwmvd_…` key; see "Running authenticated vs local" below |
| `-rate-user`        | `5`                                     | Auth mode: per-key sustained request rate (req/s) for portal (user) keys |
| `-burst-user`       | `20`                                    | Auth mode: per-key burst (token-bucket size) for user keys |
| `-rate-service`     | `50`                                    | Auth mode: per-key sustained request rate (req/s) for `service` keys (e.g. the first-party web app) |
| `-burst-service`    | `200`                                   | Auth mode: per-key burst for service keys |
| `-portal`           | `false`                                 | Enable the Discord key portal at `/portal` (users self-service their own key). **Off = no `/portal` routes at all.** Requires `-auth-dir` **and** the three env vars below; see "The Discord key portal" |
| `-portal-base-url`  | _(empty)_                               | Public origin of the deployment, e.g. `https://qw.example.com`. Used to build the OAuth `redirect_uri` (`<base>/portal/callback`) and absolute links. Required with `-portal` |

The portal's secrets are supplied via **environment variables, never
flags** (a flag value is visible in `ps`):

| Env var | Description |
|---|---|
| `DISCORD_CLIENT_ID`     | Discord application's OAuth2 client id |
| `DISCORD_CLIENT_SECRET` | Discord application's OAuth2 client secret |
| `PORTAL_COOKIE_SECRET`  | HMAC key for the session/state cookies. **≥ 16 bytes** (32 recommended); the server refuses to start if it is shorter |

With `-portal` set, the server **refuses to start** if `-auth-dir`,
`-portal-base-url`, or any of the three env vars is missing — a
misconfigured portal never runs half-open.

Running the server is the default action — bare flags (or an explicit
`serve`) start it. A positional first argument that isn't a known
subcommand (`version`, `cache`, `keys`) is rejected with a usage error
rather than silently starting a server (so `mvd-api serv` is a clear
error, not a boot).

Schema bumps in `mvd-analytics` invalidate the parsed-`Result` tier
but keep the raw-MVD tier — the next access re-parses without
re-downloading from the hub. On startup, tier-2 trees and tier-3
artifact gobs left by past schema/format versions are deleted and the
disk budget is enforced once.

### Cache ops subcommands

- `mvd-api cache stats` — per-tier file counts and bytes, plus the
  current schema tree vs any orphaned `results/*` version trees.
- `mvd-api cache prune` — reclaim disk without touching a running
  server. Exactly one of: `-max-bytes N` (evict oldest to fit `N`
  bytes, same sweep as the online GC; `N` must be `> 0` — use `-all`
  to wipe everything), `-older-than 30d` (drop tier files older than
  the given age; accepts `d`/`w`/`h`/…), `-all` (wipe all three tiers,
  keep the gameId index). Add `-dry-run` to log exactly what would be
  removed and delete nothing. Orphaned version trees and stale
  artifact gobs are always removed first.

### Running authenticated vs local

By default (`-auth-dir` empty) the server is **unauthenticated** — exactly
today's localhost behaviour, byte-for-byte. Anyone who can reach the port can
call it, and the optional `Authorization: Bearer <label>` is a non-secret
traffic tag only (see [Authentication](#authentication)). Run it this way
behind your own firewall / for local tooling.

For **public hosting**, set `-auth-dir DIR`. The server loads `DIR/keys.json`
(created empty if absent) and then requires a key on every `/v1/*` route and
on `POST /v1/demos/{id}`. Exempt: `/healthz`, `/v1/version`, `/portal/*`, and
`OPTIONS` preflight. Rate limiting is **per key**, split into a `user` class
and a looser `service` class (the four `-rate-*`/`-burst-*` flags above).

Keys are managed with the `keys` subcommands (they edit `keys.json` directly —
no running server needed):

- `mvd-api keys issue -auth-dir DIR [-service] [-note S] [-discord-id ID] [-discord-name N]`
  — mint a key. **The full key is printed once, to stdout, and is never
  recoverable** (only its SHA-256 hash is stored). `-service` marks it for the
  looser rate class. Issuing for a `-discord-id` that already has a key
  **revokes the old one** (one active key per Discord user).
- `mvd-api keys revoke -auth-dir DIR (-key K | -hash H | -discord-id ID)`
  — revoke by full key, key hash, or every active key of a Discord user.
- `mvd-api keys list -auth-dir DIR` — hash **prefix** + metadata
  (status, created, revoked, discord, note). Never prints a full key or full
  hash.

A user can self-check a key with `GET /v1/auth/check` (`204` live, `401` not).

### The Discord key portal (getting a key)

The `keys` CLI is for the operator (and for issuing `service` keys). For
end users, the optional **portal** lets them get their own key by signing
in with Discord — no operator in the loop.

Enable it with `-portal -portal-base-url https://<domain>` plus the three
env vars above; it also requires `-auth-dir` (the portal issues into the
same `keys.json` the auth middleware validates against). When `-portal` is
**not** set, none of the `/portal` routes exist (a request to `/portal`
404s) and the server is unchanged.

Flow (all under `/portal`, exempt from API-key auth — they use their own
Discord-cookie session, not a Bearer key):

- `GET /portal` — landing page describing the service, with a
  "Sign in with Discord" button.
- `GET /portal/login` → 302 to Discord's consent screen (OAuth2 scope
  **`identify` only** — no email, no guild access).
- `GET /portal/callback` — Discord redirects here; the portal verifies the
  OAuth `state` (CSRF double-submit), exchanges the code, reads the user's
  Discord id + username, and sets a **1-hour HMAC-signed session cookie**.
- `GET /portal/key` — shows the user's current key **status** (hash prefix
  + created date) and a generate / regenerate button.
- `POST /portal/key` — issues a key and shows the **full key exactly once**
  (regenerating **revokes** the previous one — one active key per user).
- `POST /portal/logout` — clears the session cookie.

The session and state cookies are `HttpOnly`, `SameSite=Lax`, `Path=/portal`.
Their `Secure` flag follows the `-portal-base-url` scheme: an **`https`** base
URL (production, the norm) ⇒ `Secure` cookies; an **`http`** base URL (local
development only) ⇒ non-`Secure` cookies, because a browser refuses to send a
`Secure` cookie over plain http, which would otherwise break the localhost dev
flow. The server logs a startup warning whenever cookies are non-`Secure`;
never run a public deployment on an `http` base URL. The full key is only ever
shown on the issue response, and only its SHA-256 hash is stored — the same
guarantee as the CLI.

**Operator prerequisite:** create a Discord application (Developer Portal →
OAuth2), note the client id + secret, and register the redirect URI
`https://<domain>/portal/callback` (add `http://localhost:8080/portal/callback`
as a second URI for local testing). See `deploy/` (phase 16b) for the
systemd `EnvironmentFile` that carries the secrets machine-side.

## REST endpoints

> **Building a frontend or tool?** [`API.md`](API.md) is the detailed
> HTTP reference — per-endpoint parameters, response semantics, units
> (the seconds-vs-milliseconds gotcha), caching, and task recipes. A
> machine-readable OpenAPI 3.1 spec is served at `/openapi.yaml` (drift
> tests pin it to the code and validate its response schemas against the
> golden corpus), browsable at `/docs`. The table below is just the
> quick index.

All paths under the base URL (default `http://localhost:8080`). The
`{id}` segment is one of:

- `gameId:NNNN` — numeric hub.quakeworld.nu game id (server fetches
  the MVD if not cached locally)
- `sha:HEX` — 64-char SHA-256 of a demo already in the local cache
  (mostly for bookmarking warm cache entries)

Successful 2xx responses set `Cache-Control: public, max-age=86400,
immutable`, `X-Schema-Version: <n>`, `X-Cache: HIT|WARM|MISS`, and
`ETag: "<sha>-v<n>"` (where `<n>` is the current `CurrentSchemaVersion`).
Send `If-None-Match` to get a cheap 304. `POST /v1/demos/{id}` (the
warm-up call) is not a cacheable resource: it carries `X-Cache` /
`X-Schema-Version` but no `Cache-Control` / `ETag`. Every response also
carries `X-Request-Id` (a per-request id echoed in the access log; a 500
body cites it instead of internal error detail) and permissive CORS
headers (see API.md §2.6). The stream endpoints (`/shots`,
`/aim`, `/streams/*`) are plain reads off the always-full base parse (phase 12
bakes the projectile/beam/nail streams into every cached Result), so they carry
the same immutable headers as everything else — the old `X-Shot-Streams:
unavailable` degrade header is gone. The generic artifact endpoint uses a finer
ETag `"<sha>-<name>@v<n>"`, and the static `/v1/artifacts` and `/v1/graph`
key their ETag on the schema version alone (`"artifacts-v<n>"` /
`"graph-v<n>"`).

| Method | Path | Query params | 200 body |
|---|---|---|---|
| GET | `/healthz` | — | `{ok, schemaVersion}` |
| GET | `/v1/version` | — | `{hash, tag, buildDate}` |
| GET | `/openapi.yaml` | — | the OpenAPI 3.1 description of this surface (embedded; content-hash ETag; auth-exempt) |
| GET | `/docs` | — | browsable API reference (vendored RapiDoc viewer over `/openapi.yaml`; auth-exempt) |
| POST | `/v1/demos/{id}` | — | `{demoId, sha256, fromCache, schemaVersion}` (`loadDemo` — warms the cache) |
| GET | `/v1/demos/{id}/overview` | — | `Overview` (map, teams, top streaks, top powerups, playerUserIDs, analyzer `errors`) |
| GET | `/v1/demos/{id}/demoinfo` | — | `result.DemoInfoResult` (KTX scoreboard — per-player weapon accuracy, kills/deaths/TK, damage, sprees, item counts, RL/LG transfers) |
| GET | `/v1/demos/{id}/metadata` | — | `result.MetadataResult` (full fullserverinfo cvars + KTX match settings: timelimit, fraglimit, spawnmodel, antilag, midair, instagib, …) |
| GET | `/v1/demos/{id}/frags` | `players`, `weapons` | `result.FragResult` (totalFrags + byPlayer + byWeapon + full kill log) |
| GET | `/v1/demos/{id}/damage` | `players`, `weapons` | `result.DamageResult` (per-hit damage log + byPlayer/byWeapon/matrix + EWep victim-weapon buckets + KTX-scoreboard cross-check; unbound/overkill amounts) |
| GET | `/v1/demos/{id}/shots` | — | `result.ShotsResult` (per-fire stream with linked hits/victims + per-player aggregates + KTX cross-check; from the always-full base parse) |
| GET | `/v1/demos/{id}/aim` | — | `result.AimResult` (per-player per-weapon effectiveness + crosshair-error samples (hitscan) + LG ramp; from the always-full base parse, so RL/GL direct/splash + the LG whiff split are always present) |
| GET | `/v1/demos/{id}/loc-graph` | — | `result.LocGraphResult` (per-map loc adjacency + edge weights) |
| GET | `/v1/demos/{id}/chat` | `from`, `to`, `players`, `types` | `[]result.MatchEvent` (chat + teamsay only; types defaults to both) |
| GET | `/v1/demos/{id}/backpacks` | `players`, `weapons` | `[]result.BackpackDrop` (RL/LG drops via `//ktx drop`) |
| GET | `/v1/demos/{id}/items` | `items`, `players`, `kinds` | `result.ItemsResult` (per-item pickup/respawn timeline) |
| GET | `/v1/demos/{id}/weapon-pickups` | `players`, `weapons`, `source` | `[]result.WeaponPickup` (kills-before-next-death; joins to backpacks via `backpackEnt`) |
| GET | `/v1/demos/{id}/buckets` | `windowMs`, `from`, `to`, `players`, `fields`, `reducers`, `includeTeam`, `loc`, `layout` | `view.ColumnarBuckets` (`layout=column`, default) or `view.BucketsView` (`layout=row`) |
| GET | `/v1/demos/{id}/events` | `from`, `to`, `players`, `types`, `loc` | `view.EventsView` |
| GET | `/v1/demos/{id}/stream-slice` | `from`, `to`, `players`, `fields`, `loc` | `view.StreamSliceView` |
| GET | `/v1/demos/{id}/state-at` | `time` (required), `players`, `fields`, `loc` | `view.StateAtView` |
| GET | `/v1/demos/{id}/los` | — | `{ "players": [{ "name", "los":[{ "o", "iv":[{ "s","e" }] }] }] }` — line of sight, **computed lazily on first request** (BSP-backed maps only) |
| GET | `/v1/demos/{id}/streams/projectiles` | — | `{ "projectiles": ProjectileStreams\|null }` — rocket/grenade flights, from the always-full base parse |
| GET | `/v1/demos/{id}/streams/beams` | — | `{ "beams": BeamStreams\|null }` — LG bolts, from the always-full base parse |
| GET | `/v1/demos/{id}/streams/nails` | — | `{ "nails": ProjectileStreams\|null }` — ng/sng spike flights, from the always-full base parse |
| GET | `/v1/demos/{id}/loc-trails` | `from`, `to`, `players`, `minDwellMs`, `loc` | `view.LocTrailsView` |
| GET | `/v1/demos/{id}/loc-table` | — | `{ "locTable": []string }` (decoder for `loc=index`; index 0 = "" no-loc) |
| GET | `/v1/demos/{id}/region-control` | `windowMs` | `result.RegionControlResult` |
| GET | `/v1/demos/{id}/airgibs` | — | `[]result.AirgibEvent` (Key Moments: direct rocket hits on airborne victims, height-sorted; empty without the map BSP) |
| GET | `/v1/maps/{map}/entities` | `types`, `kinds` | `result.MapEntitiesResult` (static layout by map name, no demo needed) |
| GET | `/v1/maps/{map}/geometry` | — | `mapgeom.MapRegions` floor-polygon JSON (needs `-maps-dir`; REST-only) |
| GET | `/v1/artifacts` | — | `{schemaVersion, artifacts:[…]}` — the DAG manifest (name, cost, lazy, requires/provides, resultKey, servable); static, ETag `"artifacts-v<n>"` |
| GET | `/v1/graph` | — | `{nodes:[…], edges:[…]}` — the analyzer DAG as JSON; static, ETag `"graph-v<n>"` |
| GET | `/v1/demos/{id}/artifacts/{name}` | — (params rejected) | the named servable artifact's section (generic accessor; closed registry, `404 artifact_unknown`; per-artifact ETag `"<sha>-<name>@v<n>"`) |

### Details → `/docs` and [`API.md`](API.md)

The per-endpoint reference (every operation, parameter, full field-level
response schema, and error code) is the OpenAPI document the server
serves at `/openapi.yaml`, browsable at `/docs` — drift-tested against
the code, so it can't go stale. [`API.md`](API.md) is the high-level
guide around it:

- **Getting started** — demo addressing, auto-load, the typical flow.
- **Query conventions + units** — the seconds-vs-milliseconds split
  (view envelopes are seconds; raw stream entries, the columnar grid,
  and all `/overview` times are int32 ms).
- **Caching, errors, auth, CORS** — the cross-cutting behaviour.
- **Choosing the right endpoint** — state-at vs buckets vs stream-slice
  vs events.
- **Recipes** — common frontend features → the call that backs them.

Deep field-level semantics stay in
[`mvd-analytics/RESULT_SCHEMA.md`](../mvd-analytics/RESULT_SCHEMA.md).

## Authentication

Two modes, chosen by `-auth-dir` (see
[Running authenticated vs local](#running-authenticated-vs-local)).

**No-auth (localhost) mode — default, `-auth-dir` empty.** There is no
authentication. The data is public and read-only. The optional
`Authorization: Bearer <label>` header (or `?label=` query param) is **not
validated** — it's a non-secret request-source tag captured in the access log
for analytics. Common labels: `mcp-claude-desktop`, `web-community`,
`cli-script`. This paragraph applies **only** to this mode.

**Keyed (hosted) mode — `-auth-dir DIR` set.** `/v1/*` and
`POST /v1/demos/{id}` require `Authorization: Bearer qwmvd_…`. Here the Bearer
value **is a secret key**, not a label — it is validated against the store and
**never logged** (the access-log identity becomes the key's note / Discord
name / hash-prefix instead). Missing/invalid/revoked → `401`; per-key rate
limit exceeded → `429 + Retry-After`. Exempt paths, key issuance, and the
`/v1/auth/check` self-test are described above and in
[`API.md` §2.5](API.md).

## Cache layout

Under `-cache-dir`:

```
mvd/<sha[:2]>/<sha>.mvd.gz                    # tier 1 — raw bytes from hub
results/v<N>f<F>/<sha[:2]>/<sha>.gob          # tier 2 — parsed *Result, per schema version + cache format
artifacts/<sha[:2]>/<sha>/<name>@v<EV>.gob    # tier 3 — lazy artifacts (los)
index/games/<gameId>.txt                      # gameId → sha map
```

Tier 2 is keyed by the schema version **and** an internal cache-format
generation `f<F>` (`resultCacheFormat` in `internal/democache/paths.go`). The
format counter, independent of the wire schema, invalidates the tier when *what*
the cache stores changes without a JSON-shape change. Phase 12 bumped it to `f2`
because the parse became **always-full** — the projectile/beam/nail streams and
the enriched shots/aim are now baked into every cached `Result` — so pre-phase-12
lean `results/v<N>/…` gobs (format 1) are simply never read and get re-parsed
once on next touch. Served bodies are byte-identical (mvd-api enriched `/shots`
and `/aim` on every request since phase 5.3), so this is a cache-locality bump,
not a schema bump: the ETag stays `"<sha>-v<n>"`.

Tier 3 holds the lazily-materialised `los` artifact (per-player LOS/PVS) as a
side-gob so its multi-second raycast survives a process restart or an LRU
eviction: after the base `Result` is served from tier 2, `/los` splices the
artifact from disk instead of recomputing (closing F8b). The effective version
`EV` is the schema version, so a schema bump invalidates tier 3 exactly like
tier 2; stale versions are simply never read. Startup cleanup deletes any
artifact gob whose `@v<EV>.gob` suffix is not current — including the
`shot-streams@*.gob` side-gobs orphaned when phase 12 retired that artifact.
Per-node effective versions arrive with the DAG manifest work if node versions
ever diverge from the schema.

A 4-on-4 demo typically occupies ~3–7 MB in tier 1 and ~3–10 MB in
tier 2. When the tier-1 + tier-2 + tier-3 total exceeds
`-cache-max-bytes`, a background sweep evicts the oldest files first
(ordered by mtime, which is bumped on every cache hit — atime is
unreliable on relatime/noatime mounts). Each file is an independent
eviction unit and every unit is reconstructible: dropping a tier-2 gob
triggers a reparse from the retained MVD; dropping a tier-1 MVD still
serves everything from its always-full gob; dropping a tier-3 artifact
recomputes on the next `/los`. The gameId index is never evicted.
Inspect and reclaim with the `cache stats` / `cache prune` subcommands
above.

## Smoke tests

```bash
mvd-api -addr :8080 -cache-dir /tmp/mvd-cache &

curl -s localhost:8080/healthz
# {"ok":true,"schemaVersion":20}

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

The binary embeds `openapi/openapi.yaml` (the OpenAPI 3.1 spec served at
`/openapi.yaml`) and the `/docs` viewer — **RapiDoc 9.3.8**, vendored as
`openapi/rapidoc-min.js` (MIT, license text committed beside it; source
URL + sha256 recorded in `openapi/docs.html`). No CDN or external
requests; updating the viewer means replacing that one file and its
header comment.

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

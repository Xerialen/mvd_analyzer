# Follow-ups

Open items across the mvd-analytics pipeline and the mvd-api / mvd-mcp
transport surface. Not an exhaustive backlog — just things a future
reader (or Claude) needs to know were deliberately left undone and why.

The branch state this captures:

- **7f26610** — Schema v7 / canonical `Streams` (Phase 1).
- **4519dd8** — `qw-mvd` REST + stdio MCP server (Phase 2).
- *(unstaged, this session)* — split into `mvd-api` / `mvd-mcp` and rename
  workspace modules (`qwdemo` → `mvd-reader`, `qwanalytics` →
  `mvd-analytics`, `qw-web` → `mvd-web`).

## Phase 1.5 — frontend panel migration

[Plan v3 §11.0](PLAN-event-streams-and-views-v3.md). Replace
`timelineState.highResBuckets` reads with direct
`getBuckets({windowMs})` calls per panel. Highest-leverage targets:

- Timeline-graph panels (currently walk ~24 K samples to render
  ~800 px) — ask for the resolution they actually render.
- Region-control heatmap when zoomed out.

Map-tab playback keeps the current data flow (it really wants every
50 ms position).

When this lands, the WASM bridge's `getDefaultBuckets` shim and the
legacy-shape types in `view/legacy.go` become dead code.

**Acceptance:** timeline-graph and region-control panels stop reading
`timelineState.highResBuckets`. Visual behaviour unchanged or better.
Initial paint becomes scoreboard + chat + frags only (sub-second on
cached WASM); bucket-dependent panels load lazily on tab open. RAM
on a 20-min match measurably drops in DevTools Memory.

## Phase 2 carryovers — mvd-api / mvd-mcp v1

Phase 2 landed in 4519dd8 (initially as a single `qw-mvd` binary
with `serve`/`mcp` subcommands) and then split (this session) into
two single-purpose binaries:

- [`mvd-api`](mvd-api/) — HTTP REST host on top of `mvd-analytics/view`,
  with a two-tier on-disk cache (raw MVD + parsed Result).
- [`mvd-mcp`](mvd-mcp/) — stdio MCP shim that forwards each tool call
  over HTTP to a running `mvd-api`. Has no `mvd-analytics` import; the
  binary stays small and the wire contract is owned by mvd-api.

For local-only MCP, run `mvd-api` on `localhost` and point
`mvd-mcp -api http://localhost:8080` at it.

`make serve` (the WASM web app) is unchanged.

### Operational gaps

- **No cache eviction.** Tier 1 (`mvd/`) and tier 2 (`results/v7/`)
  grow without bound. Per-demo footprint is ~3–7 MB raw +
  ~3–10 MB gob; a year of community traffic is fine but not
  forever. Ship a `mvd-api cache prune --older-than 30d` (or
  `--max-size 50GB`) subcommand before it becomes a real ops
  problem.
- **No cache stats / introspection.** Operators can't ask "how
  many demos cached?" or "what's the LRU hit rate?" without
  shelling out. A `mvd-api cache stats` subcommand or `/v1/debug/
  cache` endpoint would help.
- **No release pipeline.** `make build-all-platforms` produces
  binaries locally; wire a GitHub Actions workflow that builds the
  cross-compile targets on tag and attaches them to a Release.
- **Cache disk write failures are swallowed.** `democache.cache.go`
  uses `_ = writeFileAtomic(...)` for the gameId index and result
  gob; a full disk silently degrades to "parse every time." Add a
  structured warning via `log/slog`.
- **/healthz is trivially OK.** Returns `{ok:true}` unconditionally.
  A production-grade health check should at least verify the cache
  directory is writable.
- **No TLS / no reverse proxy guidance.** `mvd-api` is HTTP-
  only. Documented deployment story: stick it behind nginx / Caddy.
  Add an example config snippet to the README if more than one
  operator deploys.
- **No CORS headers.** Browser-direct REST consumption from a
  different origin would fail preflight. Not a current concern (the
  WASM web app is bundled with its own analyzer); add `Access-
  Control-Allow-Origin: *` for view-shaped endpoints if a JS client
  ever shows up.
- **No streaming responses.** A 4on4 buckets call at 50 ms windowMs
  can exceed 10 MB encoded as a single JSON document. Move to
  newline-delimited JSON or chunked transfer if a real client
  chokes.

### Parse laziness — fast vs. full level

Today the analyzer is one-shot: every cold parse runs every registered
analyzer, including the position-track build (~750 K samples per 4on4).
Position handling — wire-decode of `svc_playerinfo`, append to per-slot
position tracks, and nearest-loc resolution — is a real chunk of cold
parse but no longer the dominant slice it once was: a measured
benchmark on the corpus (`mvd-analytics/loc/bench_test.go`) puts loc
resolution at ~90 ms on dm6 and ~400 ms on the largest custom maps
(tf2k / 2fort5 with L > 2800) after the pencil-index optimization. The
remaining cost is mostly the MVD wire decode itself, which a fast
level can't help with — but the *whole* `Streams.Position` build can be
skipped, plus the analyzers that depend on it (Items, Backpacks).

A two-level parse would let "show me the score / players / map / top
streaks" (i.e. anything that doesn't need positions) skip the position
build entirely. Cold overview drops by whatever fraction position
handling occupies on the target map.

**Sketch:**

- `analyzer.Registry.WithLevel(Fast)` flag. At `Fast`:
  - Keep `DemoInfo`, `Frag`, `Match`, `Metadata`, `Messages`, `WeaponPickups`.
  - Keep `Timeline` but set `SkipPositionTracks: true` — still emits
    `FragEvents`, `PowerupEvents`, `FragStreaks`, `LocTable`, region
    region-list, and the non-position `Streams` fields (health,
    armor, weapons, powerups, ammo, spawns, deaths).
  - Skip `Items`, `Backpacks` (both attribute via nearest-player and
    need positions).
- `democache`: two gob tiers per schema — `results/v7/fast/<sha>.gob`
  and `results/v7/full/<sha>.gob`. A `full` hit satisfies any
  request (full ⊃ fast). A `fast` hit satisfies overview / score
  requests; position-needing requests transparently upgrade by
  reparsing from tier-1 MVD (no re-download).
- `mvd-api`:
  - `loadDemo` defaults to `level=fast`; accepts `?level=full` to
    prewarm.
  - `/overview`, `/events` (default types) → fast suffices.
  - `/buckets`, `/stream-slice`, `/state-at`, `/loc-trails`,
    `/region-control` → need full; trigger upgrade.
- Items / Backpacks views become an explicit endpoint that requires
  `level=full` (they're absent at fast).

When combined with the planned `searchGames` (Supabase direct from
mvd-mcp + mvd-web — no mvd-api round-trip), most agent sessions
should never pay the position-track cost: search → pick → fast
parse → overview, with full parse only when the user asks for
position-derived analytics.

Estimate: 1–2 days for the analyzer plumbing + level-aware cache +
per-endpoint mapping + tests. Not blocked on anything; pure
performance work.

### Surface gaps

- **No remote MCP transport.** Streamable HTTP MCP isn't exposed.
  Once a specific MCP client demands it, mvd-api could grow a `/mcp`
  route using the SDK's HTTP handler — open access remains
  acceptable for public read-only data, but the MCP spec is moving
  toward an OAuth protected-resource convention, so plan for
  `.well-known/oauth-protected-resource` if real auth is needed.
- **No pre-rendered view tier.** Every REST hit recomputes the view
  from the cached `*Result`. If a hot `(demoId, view, opts)` tuple
  shows up at meaningful rate in access logs, add tier 3 keyed by
  `(demoId, schemaVersion, view, optsHash)`.
- **No rate limiting.** Labels (`Authorization: Bearer <label>`)
  are recorded for analytics but not acted on. Add per-label /
  per-IP token bucket if abuse appears.
- **`loadDemo` is the only way to write to the cache.** No
  multi-demo prewarm endpoint. If you operate a public hub mirror,
  add `POST /v1/cache/warm` that takes a list of gameIds.

### Testing gaps

- **Real-demo gob round-trip not exercised.** `democache`
  unit tests use a stub parser returning a synthetic
  `result.Result`. The real `*Result` graph is much richer
  (`TimelineAnalysisResult`, `LocGraph`, `WeaponPickups`, etc.);
  gob serialization survives by Go-type-system construction, but
  a single integration test that parses one corpus demo end-to-end
  + round-trips through `encodeResult`/`decodeResult` would catch
  a silently changed field.
- **`BuildOverview` has no direct unit test.** Covered indirectly
  by `handleOverview` tests. A dedicated test fixture with edge
  cases (empty teams, missing TimelineAnalysis, no Metadata)
  belongs in `mvd-api/overview_test.go`.
- **MCP proxy equivalence not pinned.** `mcp_backend_proxy_test.go`
  exercises each tool through the proxy against an in-process
  serve, but doesn't assert the proxy returns the same shape as
  the local backend on the same demo. A side-by-side equivalence
  test would catch a regression where, say, query-param encoding
  loses a default.
- **No test that `mvd-api version` / `mvd-mcp version` / unknown-subcommand dispatch
  exits with the right code.** `main.go`'s argv handling is
  untested.

### Distribution gaps

- **Windows code-signing.** Unsigned `.exe` triggers SmartScreen.
  Either accept the warning (documented in `CLAUDE_DESKTOP.md`)
  or obtain an Authenticode cert.
- **macOS notarization.** Same story with Gatekeeper. The
  `xattr -d com.apple.quarantine` workaround is documented; real
  fix is an Apple Developer account ($99/yr).
- **`CLAUDE_DESKTOP.md` doesn't cover Claude Code.** The same
  `.mcp.json` shape works for Claude Code (in repo root) but the
  config path and discovery rules differ from Claude Desktop. Add
  a section to `mvd-mcp/CLAUDE_DESKTOP.md`.

- **Local MCP "convenience mode" removed.** The previous bundled
  binary had a `qw-mvd mcp` (no `-api`) mode that ran a local cache
  in-process. Post-split, local MCP requires running `mvd-api` on
  localhost (just two binaries, ~zero startup cost). If install
  friction matters more than binary size, an `mvd-mcp -embedded`
  mode that spawns `mvd-api` as a subprocess is conceivable.

### Toolchain note (informational)

Pulling in `github.com/modelcontextprotocol/go-sdk v1.6.0` required
Go 1.25. `go.work` and `mvd-analytics/go.mod` use the `toolchain
go1.25.0` directive so older Go installations auto-fetch via
`GOTOOLCHAIN=auto`. Workspace-internal modules now have explicit
`replace` directives in `mvd-web/go.mod` and `mvd-analytics/go.mod` so
`go mod tidy` resolves without trying to contact github.com for the
placeholder `v0.0.0` versions.

## Phase 3 — cross-demo / corpus tools

[Plan v3 §11.2](PLAN-event-streams-and-views-v3.md). Intent only.

Sits on top of `democache/results/v7/*.gob` from Phase 2 as the
corpus. Tools fetch N cached `*Result`s and run aggregation; the
per-demo `view` API composes naturally across many. Use cases TBD
by traffic: per-player season stats, per-map aggregates, free-form
corpus queries. If cache scales past a few thousand demos and
gob-load becomes slow, evaluate a column store (DuckDB over
Parquet, or SQLite extracted at cache-write time).

Concrete prerequisites (when this becomes real work):

- `mvd-api cache list` / `cache stats` (already wanted operationally;
  doubles as Phase 3 enumeration primitive).
- A streaming iterator over the corpus that doesn't load every
  `*Result` into memory at once.
- A query language or REST surface for cross-demo aggregations.
  Maintaining `mvd-analytics/view/` for per-demo + a new corpus view
  layer at `mvd-analytics/corpusview/` is the natural split.

## Pickup-attribution data quality

From `NOTES-pickup-attribution-quality.md` (working notes, not
checked in). Pre-existing issues that survived Phase 1 + Phase 2 —
neither refactor touched the analyzers themselves.

1. **`items.go` nearest-player tie-breaking** — fixed in `fd70394`
   (pre-Phase-1). Map-iteration nondeterminism replaced with
   stable slot ordering.
2. **`backpacks.go` records auth name instead of display name.**
   On auth-override demos this breaks downstream joins on player
   name. Simple fix; affects the map-tab overlay today.
3. **`items.go` reads stale positions** when attributing pickups.
   Should filter by recency or surface a weak-attribution flag.
   Worth quantifying against the KTX-authoritative pickup counts
   in the diagnostic harness first.
4. **`items.go` has no max-distance gate** on nearest-player
   selection. Degenerate when no one is near the pickup spawner
   (the "nearest" can be implausibly far).

Recommended triage order: §2 first (one-line fix, visible bug),
then §3 with a divergence harness against KTX counts, then §4.

## Schema v8 int-ms time — completed

Schema v8 migrated **every** timestamped field in the result schema
from float seconds to `int32` milliseconds. The first cut covered
`PositionTrack.T` and per-player `Spawns` / `Deaths`; the follow-up
extended the migration to `ChangeI16` / `ChangeI8` / `ChangeStr`,
`Interval`, `MatchEvent`, `GlobalStream`, `MatchResult`, all of
`TimelineAnalysisResult`, frag / powerup / streak event times,
`BackpackDrop`, `WeaponPickup`, and `ItemPhase`. JSON keys are
unchanged; external consumers reading these as seconds must scale by
`* 0.001`.

Public view-layer outputs (`view.Buckets`, `view.Events`,
`view.StreamSlice`, `view.StateAt`, `view.LocTrails`) still emit
float64 seconds, so the WASM-fed frontend panels are largely
oblivious. The few app.js sites that read raw schema fields directly
convert ms→seconds at the read site.

Design rationale and the convention to follow when adding new
timestamped fields are documented in
[`mvd-analytics/RESULT_SCHEMA.md`](mvd-analytics/RESULT_SCHEMA.md#schema-v8-all-times-are-int32-milliseconds).

## Schema v13 per-hit damage — completed (with two notes)

`Result.Damage` surfaces the long-dormant `DamageEvent` (0x0007) into the
schema (`analyzer/damage.go`, `result/damage.go`, validated by
`analyzer/damage_validation_test.go`). Per-player + per-hit, in `raw` (unbound
wire) and `eff` (KTX-scoreboard-reconstructed) flavours. Two things a future
reader should know:

- **The golden corpus was stale before this landed — and the byte-exact
  golden test is effectively Linux-CI-only.** On Windows, `core.autocrlf=true`
  checks the LF-in-repo goldens out as CRLF, so `TestGoldenCorpus` fails on a
  pure line-ending diff locally; and CI skips it without cached demos. The
  committed goldens had drifted from the v12 locgraph `armed`/`unarmed`/`quad`
  weights; regenerating for v13 (`-update-golden`) refreshed both. If you touch
  goldens, regenerate on Linux (or accept the CRLF caveat) and expect the v12
  locgraph delta to ride along until someone lands it separately. Consider a
  `.gitattributes` (`testdata/golden/** text eol=lf`) to make the test runnable
  on Windows.

- **`eff` damage reconciles to ~0.7% on team games, exact on duels.** The
  residual is wire-ambiguous: telefrag credit is `k_dmgfrags`-cvar dependent
  (the cvar isn't in the demo) and reconnect identity-folding adds noise. The
  harness allows 1.5%. Tightening would need the cvar (not recoverable) — not
  worth chasing. The enemy/team *split* is reported-not-asserted for the same
  resolution-boundary reason; the classification-independent totals are exact.

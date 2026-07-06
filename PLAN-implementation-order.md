# PLAN-implementation-order — one sequence across all six plans

> Written 2026-07-05 against main @ 05e2ed9 (schema v47), after re-verifying
> every plan finding at HEAD. This is the execution index for:
> [PLAN-analytics.md](PLAN-analytics.md) (core),
> [PLAN-analytics-maps.md](PLAN-analytics-maps.md) (maps),
> [PLAN-reader.md](PLAN-reader.md) (reader),
> [PLAN-api.md](PLAN-api.md) (api/mcp),
> [PLAN-web.md](PLAN-web.md) (web),
> [PLAN-improve-analytics.md](PLAN-improve-analytics.md) (DAG).
> Item IDs below (`analytics F1`, `reader A2`, …) refer to those documents;
> the details, line references and fix sketches live there, not here.

> **Status 2026-07-06: Phases 0–5 are DONE** — implemented on stacked
> branches `phase-0` … `phase-5` (each off the previous, all pushed),
> Opus-implemented, Fable-verified, all gates green. Commits:
> P0 `ff47a76`+`7d1a8e2` · P1 `3ebc9cd` (schema v47→v48, goldens
> regenerated once) · P2 `cda9940`+`c690ec0` · P3 `6179589`+`3316d50` ·
> P4 `f494471` · P5 `1300742`,`bcfb8ce`,`7c8cf5c`,`adc4ce5`,`56e3ab6`.
> The per-plan documents now list only still-open findings, each with a
> resolved-ledger at the bottom. **Phases 5.1–5.4 are DONE (2026-07-06)**
> — stacked branches `phase-5.1`…`phase-5.4` off `review`, all pushed:
> 5.1 `989e6e0`+`26b1c70` (reader batch + serve.go gofmt) ·
> 5.2 `4298e46` (aim/shots correctness, schema v48→v49, goldens
> regenerated once) · 5.3 `67a95ff` (one shot-stream variant +
> singleflight recover) · 5.4 `a742b0e` (aim-tab fixes; web F17 closed
> by decision) · 5.5 (post-match frag-reset fix — a regression QA on
> the phase-1 golden diff caught: analytics F25). **Next up: merge
> everything to main (single non-squash merge of the tip, or
> sequential PRs), then the Phase 6+ structural queue.**
> The phase tables that follow are kept as the record of what each
> phase covered; deviations from plan are noted inline where they
> happened (reader F1 was pulled into Phase 1 when the 0-frag fix
> exposed it; reader F17/F9 closed as documentation with mvdsv
> citations; web F13 resolved by removing the filter).

Only two plans order their own work (PLAN-reader's sequencing section,
PLAN-improve-analytics' Stage 1–4); none order work across plans. This file
does. Tick items off here as they land.

## Ordering principles

1. **Correctness before cleanup** — data-corrupting bugs first, then
   deletions, then consolidations, then architecture.
2. **Invariant tests land with the first fix in an area**, so every later
   refactor in that area is gated (analytics A1 is the model).
3. **Fix Layer 1 signal before the Layer 2 code that compensates for it**
   (reader F2 before the analytics registry stops swallowing errors).
4. **Delete before consolidating** — less code to unify.
5. **The hosted-API direction pulls mvd-api hardening forward**: the long-term
   plan is to host reader/analytics/api/mcp for other apps over the internet,
   with mvd-web eventually a client of the API. Findings that were tolerable
   for a localhost tool (unverified cache bytes, unescaped ids, global locks)
   become prerequisites.
6. **Batch schema-touching changes** so goldens regenerate once per phase,
   not once per item. Every phase ends with `make test` green and, where
   values change, a reviewed golden diff + RELEASE_NOTES entry per CLAUDE.md.

Phases are ordered; items inside a phase are independent unless a dependency
is named. Web items marked ∥ can run in parallel with any phase (different
module, no shared files). Effort: S < 1h, M = hours, L = days.

## Phase 0 — Mechanical, zero risk (one or two commits, no behavior change)

| Item | What | Effort |
|---|---|---|
| reader F14 + maps F12 | `gofmt -w` the 6 + 11 flagged files, one no-logic commit | S |
| verification follow-ups | Fix the two misleading comments found while re-verifying: `result/streams.go:34` (falsely claims the API gob-persists LOS) and the `analyzeSource` ordering comment (lists 6 of 9 post-processors; analytics nit) | S |
| doc-drift batch | reader F15 + F16, maps F4 + F14, api stale-package-doc nits, CLAUDE.md `internal/hubfetch` path | S |
| reader F7, F13 | stdlib substitutes (`strings.ToLower`, `ReadBytes`) | S |
| analytics A5 (comment half) | One canonical "add-a-column checklist" comment in coord.go, referenced from the other six sites | S |
| maps F2, F10 | `FacesDropped++` at the three skip sites; delete the `var _` filler blocks | S |

## Phase 1 — Correctness: analytics core (the headline fixes)

| Item | What | Effort | Notes |
|---|---|---|---|
| analytics F1 + A1 | Shift/rewrite `KillEvents` in both post-processors **and land the A1 structural invariant test** (time bounds + duel team labels over the golden corpus), regen goldens | M | First PR of the phase; the test is the piece that outlives everything, including DAG Stage 2 |
| analytics F2 | Print-level gate in `MatchTimingDetector.OnPrint`; tighten frag obituary level | S | Golden regen |
| analytics F3 | Reorder CRMod SSG pattern ahead of generic `" eats "` | S | Point fix now; superseded by the A2 obituary unification in Phase 5 |
| analytics F5 + F6 | Remove the 0-frag player filter and make DemoInfo authoritative in `isDuelResult` — **one PR, they interact** | M | Golden regen; likely schema-note in RELEASE_NOTES |
| analytics F13 | Single effective match-end for both powerup-close passes | S | |

## Phase 2 — Correctness: reader, then the registry seam (+ web bugs ∥)

| Item | What | Effort | Notes |
|---|---|---|---|
| reader F1 | `*spectator` key + regression test | S | Analytics gates on the flag |
| reader F2 / A3 | Map `EndOfDemo` disconnect to `io.EOF`, drain queued events in `Source.Next`, with test | M | Do before the next row |
| analytics F9 / A4 | Registry records `source.Next()` errors and `regionControlPost` errors into `Result.Errors` | S | **Depends on reader F2** — until then every healthy demo "errors" |
| reader F5, F12, F10, F11 | Propagate skip/read errors; movevar truncation; `errUnknownSvc` sentinel; diff-emit errors | M | One PR is fine |
| reader F17, F9 | Verify `dem_stats` frag path against mvdsv; decide item re-baseline doc-vs-emit | S | Verification items — may close as "document why" |
| web F1–F4 ∥ | Playhead reset, `pent`/`pe`, worker resolver race, fallback scoreboard | M | Four independent user-visible bugs |
| web F12 ∥ | Chat scroll-listener/time-line leak | S | |

## Phase 3 — mvd-api hardening (hosted-API prerequisites)

| Item | What | Effort | Notes |
|---|---|---|---|
| api F1 | SHA-256-verify downloaded demo bytes before caching | S | Worse since #97: `EnsureShotStreams` re-parses unverified tier-1 bytes |
| api F2 | `hubfetch.ErrNotFound` sentinel + `errors.Is`, one classifier | S | |
| api F5 | `url.PathEscape(in.DemoID)` (or strict id regex) — standalone one-liner now; folds into the F4 helper later | S | 18 splice sites |
| api F8 (lock half) | Per-SHA locks for `/los` **and** `streamsMu` | M | The persistence half is superseded by DAG Stage 3 — don't build a bespoke side-cache now |
| api F9, F10 | Drain `lastResolved`; make ctx semantics honest | S | |
| api docs batch | F6 (overview ms example + §2.1 units row), F7a/b (backpacks CSV, events types), error-code table for `aim_unavailable`/`shots_unavailable`, `schemaVersion` examples | M | The API is the product boundary once hosted; API.md's "real captured output" promise must hold |
| api quiet-degrade | Decide the `EnsureShotStreams` missing-tier-1 silent-lean-serve behavior (comment-grade justification or response marker) — "surface authoritative data" rule | S | |
| web A8 ∥ | Vendor the CDN deps (cytoscape, fcose, fonts) or add SRI pins | S | Deploy reliability for a hosted client |

## Phase 4 — Deletions (large negative diffs, verified by build + goldens)

| Item | What | Effort |
|---|---|---|
| reader F4 + F6 | Dead skip branches/helpers (~200 LOC after #97) + dead exported types/aliases/Kind block, README list fix | M |
| analytics F8, F16 | `tracks.go` (+ tracks.md), dead `locIndex` return | S |
| web F5, F6 ∥ | One `escapeHtml` (the null-safe one), dead panel/IDs/CSS (~190 lines) | S |
| maps F3 | Six dead exported symbols + two false doc claims | S |

## Phase 5 — Consolidations (per module, ROI order)

| Item | What | Effort | Notes |
|---|---|---|---|
| maps A3 | One-entry BSP/locvis cache keyed by map name | M | **Biggest cheap win in the plan set**; also cuts mvd-api's re-parse path and 5–6 WASM XHRs per demo |
| api F3 + F4 + F11 | Error-accumulating param reader; `demoPath` + setter helpers in mcp proxy (absorbs F5); unify on `writeUnavailable`; fold `resolveShotStreams` into `resolveDemo` | M | Makes endpoint #30 five lines instead of forty |
| analytics A2 / F4 | **Single obituary parser** consumed by frag + messages | M | Retires the F3/F4 drift class; golden regen |
| analytics A3, F7 | One `ResolveSlotAt`; one `parseInfoString` | M | |
| analytics F10, F11, F12, F15, F14 | Stream-builder dedup helpers, generic shift-filter, view dup helper, columnar builders, synthetic-respawn early-out | M | Mostly mechanical; F11/F15 make Phase 6 stream work cheaper |
| reader A2 | **Single wire-layout implementation** (parse = skip) — resolves F3 by construction | L | The one structurally interesting reader change |
| reader F8, A7 | KTX hint table helper; canonical match-phrase table in Layer 1, analytics imports it | S | |
| maps F5/A4, F8, F9, F1, F6, F7, F11, F13, A5 | Loader merges (jshost helper), qw-analyze preamble extract, in-package lump reader, load-time BSP validation, findDemos error, `-time 0`, shell-cap fallback, hubfetch error message, corpus Version check | M | Each independent and small |
| web F7, F8, F9, F15, F14 ∥ | Scanline sampler, canvas tooltip + shared layout consts, hub URL helper, small logic dedups, airgibs → `makeSortable` | M | |
| web F10, F11, F13, F16 ∥ | Region-icon churn, pan-drag rAF coalescing, chat-dedupe filter (document or scope it), worker reparse + WASM marshal helper | M | |
| analytics determinism nits | `sort.SliceStable`/tie-breaks in powerups + interval events | S | Byte-stable output before DAG goldens |

## Phase 6 — Structural (design-gated)

Order within the phase matters:

1. **DAG Stage 1** (PLAN-improve-analytics §5): explicit `NodeSpec`s, validation,
   topo sort, `-graph` export — no behavior change, golden-identical gate.
   Can start any time after Phase 1; do not let it linger unstarted — every
   merged feature keeps growing the post-processor debt Stage 2 must migrate.
2. **DAG Stage 2**: the clock/roster refactor. Deletes
   `normalizeMatchRelativeTimes` / `duelTeamNormalize` — including the Phase 1
   F1 patch, which is temporary by design; the A1 invariant test is what
   carries over. Small PRs, one producer at a time, goldens byte-identical.
3. **DAG Stage 3**: lazy materialisation + per-artifact cache. Replaces the
   `LOSComputed`/`EnsureShotStreams` special cases and delivers the
   persistence half of api F8 generically.
4. **DAG Stage 4**: artifact manifest + generic endpoints + MCP tool
   generation. This is the hosted-API payoff: the service surface stops
   being hand-maintained, which is also the durable fix for the api F7
   docs-drift class.
5. **web A1 → A2 → A3 ∥**: ES-module split along the existing section
   banners, then the `init/reset` registry (the aim tab already demonstrates
   the pattern), then the `onTimeChange` subscriber model. A2/A3 build on
   A1's seams. When the hosted API exists, the module boundary is also where
   a WASM-vs-API data source becomes swappable.
6. **reader schema batch**: A4 (value-snapshot events), A5 (TimeMs on the
   remaining event types), A6 (multi-map reset or documented single-map
   assumption) — one design pass, one schema/docs update, analytics
   consumers audited in the same PR.
7. **maps A2** (bsp/bspvis parser unification) — worthwhile, lowest urgency;
   do it whenever the next BSP-format quirk would otherwise be fixed twice.

## What this order deliberately defers

- Anything speculative in the plans (parallel Finalize, per-artifact goldens,
  202/Retry-After) stays behind the DAG stages' "measure first" gates.
- ~~The unreviewed v39–v47 surface (aim/shots analyzers, aim tab internals)
  gets its own review pass~~ — done 2026-07-06; findings live in each plan's
  "deferred review" section and feed the Phase 6+ queue.

## Phase numbering going forward

Phase 6 split into one phase per structural item so each gets its own
branch, in dependency order:

| Phase | Item |
|---|---|
| 6 | DAG Stage 1 — NodeSpecs, validation, `-graph` export (golden-identical gate) |
| 7 | DAG Stage 2 — clock/roster artifacts; delete the barrier mutators (highest risk) |
| 8 | DAG Stage 3 — lazy materialisation + per-artifact tier-3 cache (absorbs api F8's persistence half) |
| 9 | DAG Stage 4 — artifact manifest, generic endpoints, MCP tool generation |
| 10 | web A1→A2→A3 — ES module split, init/reset registry, time-change subscriber |
| 11 | reader schema batch — A4 value-snapshot events, A5 TimeMs everywhere, A6 multi-map reset |
| 12 | maps A2 — unify the mapgen/bsp and bspvis parsers |
| hosting-prep | api F14–F17 + F19 (quota/GC, throttling, capped reads, CORS, error hygiene) — before any public deployment |

The 2026-07-06 deferred reviews (aim/shots analytics, Aim Stats tab,
aim/full-data API + democache, #97 decoders) produced new findings —
see each plan's "deferred review" section. The urgent ones form a
correctness batch that runs BEFORE Phase 6, as **phases 5.1–5.4**:
four small stacked branches (`phase-5.1` … `phase-5.4`, each off the
previous, 5.1 off `review`), one PR each. **Nothing merges to main
until 5.4 is done**; then all phases go to main as sequential PRs
(phase-0 → … → phase-5 → 5.1 → … → 5.4).

- **Phase 5.1 — reader mechanical batch**: F18 (signed beam ent, with
  test), F19 (`errUnknownTE` sentinel), F21 (doc nits), F22 (parseNails
  twin + warn label), the surviving PLAN-reader nits, plus the
  `mvd-api/serve.go` gofmt fix (no-logic; decided 2026-07-06 to ride
  here). Reader F20 (handler-error contract) still batches with
  Phase 11.
- **Phase 5.2 — analytics aim/shots correctness**: F18 (Aim's RL/GL
  direct/splash block absent on every default parse — gated on a
  stream it never reads), F19 (unwindowed damage in those splits),
  F20 (duel normalize flips VictimKinds but not Damage.IsTeam), F17
  (extend the invariant test beyond timelineAnalysis to Shots/other
  event sections), F23 (doc drift per the lock-step rule); F21/F22
  (shots.go slot-resolver straggler, weaponstay parseInfoString) ride
  along as cheap same-area fixes. Golden regen expected.
- **Phase 5.3 — api correctness**: F12 (nails-latch cache consistency
  under one immutable ETag — must be settled before DAG Stage 3
  persists the latches), F13 (panicking parse → nil-deref cascade for
  singleflight waiters).
- **Phase 5.4 — web aim-tab fixes**: F18 (density-canvas hover
  misalignment), F19 (DYaw sign doc trap), F20 (LG unresolved column).
  Web F17 closed by decision (2026-07-06): **SSG crosshair samples stay
  unrendered for now** — SG + LG panels are enough; revisit if demand
  appears.

The hosted-deployment cluster (api F14/F15/F16/F17 — disk quota/GC,
cross-demo stampede + rate limiting, capped reads, CORS — plus F19
error-text hygiene) is deliberately **not** part of 5.1–5.4: hosting
is not imminent (decision 2026-07-06). It becomes its own
**hosting-prep phase**, scheduled immediately before the service goes
public — after Phase 12 in the current ordering, or pulled forward if
hosting plans firm up.

## Loose ends (small, unscheduled)

- ~~`mvd-api/serve.go` is gofmt-dirty at HEAD~~ — decided 2026-07-06:
  fixed as a no-logic commit in Phase 5.1.
- `experiments/locattr/cmd/demoeval` was already broken by the schema
  v23 DemoOffset move (untracked local tool; not a phase casualty).
- ~~The `phase-0`…`phase-5` history contains two since-removed committed
  binaries~~ — purged 2026-07-06: the `d87334b..review` chain was
  rewritten without `mvd-mcp/mvd-mcp` / `mvd-web/wasm` (final tree
  byte-identical) and `phase-5` + `review` were force-pushed. New SHAs:
  P5 API commit `7c8cf5c`, obituary/slot commit `adc4ce5`, web
  consolidation `56e3ab6`, phase-5 tip `87d0643`, gitignore commit
  `9016832`, review tip `9b3445e`. `phase-0`…`phase-4` were untouched.

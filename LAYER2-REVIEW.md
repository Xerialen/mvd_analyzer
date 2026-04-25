# Layer 2 (qwanalytics) — Critical Review

**Date:** 2026-04-21
**Scope:** `qwanalytics/` module — all analyzers and result types.
**Goal:** Identify data-quality gaps, dead/duplicate code, and architecture
smells, with concrete cleanup actions prioritised by severity.

---

## Executive summary

The module is structurally sound (clean Analyzer interface, layered
module boundary, stable JSON schema) but shows organic-growth debt in
three areas:

1. **Duplicate obituary parsing** in `frag.go` and `messages.go` — two
   parallel regex tables for the same KTX messages; drift risk.
2. **Dead/pretend-configurable code** — `ExtractTracks`,
   `SetBucketDuration`, and several half-wired tuning knobs.
3. **Timeline analyzer scope creep** — one analyzer spread over seven
   files, doing sampling, powerups, streaks, regions, and loc smoothing
   from a single shared mutable state.

Separately, a structural data-quality gap: pickup attribution for
**items** (nearest-player heuristic) and **backpacks** (no pickup
attribution at all) are both soft signals — there is almost certainly
authoritative data on the wire we're not yet using.

---

## Critical — data quality

### C1. Duplicate frag parsing in two analyzers
- `analyzer/frag.go:143-478` — `parseObituary` + `checkSuicide`,
  `checkKill`, `checkTeamKill`, `checkKillerFirstPatterns`,
  `checkGibbedBy`, `checkAtePattern`.
- `analyzer/messages.go:132-371` — `parseObituarySimple` + its own
  `killPatterns` array at `messages.go:237-281`.
- Comment in `messages.go` even says "same comprehensive pattern list
  as frag.go" — without sharing the code.

**Risk:** a new KTX message or a bug fix in one file silently diverges
from the other. `messages.go` feeds the timeline view; `frag.go` feeds
match stats. Divergence is currently possible.

**Action:** extract one `parseObituaryToFrag(msg string, time float64)
(*FragEntry, bool)` in `obituary.go`, call from both analyzers. Blocker:
write table-driven tests first.

### C2. Teamkill detection runs twice
- `frag.go:525-543` — `isTeamKill` during OnEvent uses
  `ctx.Players[slot].Name`, which can be the auth login, not display
  name.
- `frag.go:76-107` — `Finalize` re-evaluates using
  `ctx.DemoInfo`, patches `Kills`/`Deaths` in place. Comment at
  line 77 documents that the initial pass was unreliable.

**Risk:** correct only when DemoInfo is present. Non-KTX sources or
truncated demos will have wrong TK attribution.

**Action:** defer TK classification to `Finalize` uniformly. Use
DemoInfo when present, fall back to display-name from parser state.

### C3. Items `TakenBy` is heuristic nearest-player
- `analyzer/items.go:213-221` — picks the closest player origin when
  the item entity vanishes.
- README says "best-effort label". No schema flag telling consumers it
  is a guess vs. ground truth.

**Action (short-term):** add `TakenByInferred bool` to `ItemPhase` so
consumers know when to trust it.

**Action (medium-term):** replace the heuristic with authoritative
signals (see the companion investigation document).

### C4. Backpacks carry drop-side data only
- `analyzer/backpacks.go:19-25` — the entity-state stream for backpack
  edicts produces "phantom visibility cycles" indistinguishable from
  real fast pickups, so pickup attribution is intentionally omitted.
- Current schema: `BackpackDrop` has no pickup fields.

**Action:** diagnose the entity-state flutter in Layer 1; add pickup
fields to the schema once we have a reliable signal. See the companion
investigation document.

---

## High — dead code

### H1. `ExtractTracks` (tracks.go:29-170) is never called
142 lines of per-life movement extraction, never wired into the result
pipeline, no consumer, no test. Delete or integrate.

### H2. `SetBucketDuration` (timeline.go:123) is never called externally
The default (50 ms) is the only value ever used. The setter + doc
comment pretending at configurability should either plug into Config
or be removed.

### H3. Tuning knobs are scattered
- `config/config.go` — only `BlipThresholdMs`.
- `tuning.go` — `DefaultHighResBucketDuration`, `timelineBucketPrealloc`.
- Hardcoded thresholds in multiple analyzers (frag suicide patterns,
  item classification, match boundary strings).

**Action:** consolidate tunables into `config/config.go` or drop ones
that aren't actually tunable.

---

## High — architecture

### A1. Timeline analyzer has become a god object
One `TimelineAnalyzer` across seven files (~1,500 lines total):
`timeline.go`, `timeline_blipfilter.go`, `timeline_buckets.go`,
`timeline_finalize.go`, `timeline_powerups.go`, `timeline_regions.go`,
`timeline_streaks.go`. `Finalize` is 245 lines of orchestration across
disparate concerns.

**Action:** split into focused analyzers:
- `TimelineSamplerAnalyzer` — buckets + region control (they share
  bucketed state)
- `PowerupAnalyzer` — powerup event detection
- `FragStreakAnalyzer` — streak detection
- Loc blipfilter stays as a post-process utility called from registry.

### A2. Implicit Context ordering with no validation
- `registry.go` hardcodes analyzer order.
- Several analyzers read `ctx.DemoInfo` or `ctx.FragEntries` in
  `Finalize` that earlier analyzers populated.
- Registry sets `ctx.FragEntries` from the frag result *after* that
  analyzer's `Finalize` (late binding, `registry.go:134`).
- No compile-time or runtime check that dependencies are met.

**Action:** add explicit "requires: X" assertions in each analyzer's
`Init` or `Finalize`. Document the DAG at the top of `registry.go`.

---

## Medium

### M1. Thin tests on the critical paths
- `frag.go` (543 lines): no tests.
- `messages.go` (417 lines): no tests.
- `match.go` (218 lines): no tests.
- `demoinfo.go` (226 lines): no tests.
- `timeline.go` (483 lines): only blipfilter is tested.

Table-driven tests for the ~30 kill patterns are the biggest gap and
a blocker for the frag-parsing dedup (C1).

### M2. Name lookup is O(N) per chat message
`names.go` `findPlayerByName` does three sequential passes (exact,
normalised, substring) on every chat message in OnEvent. Build an
exact-match map once at `Init`. Low impact, easy win.

### M3. DemoInfo "authoritative" is underspecified
README calls it "authoritative stats" without explaining that it's
KTX's internal state machine at match end, not wire-level ground
truth. Add a docstring on `DemoInfoResult` noting the provenance and
the possibility of divergence from wire-derived stats.

### M4. Non-Analyzer post-processors are undocumented
`locgraph.go` and `duel_normalize.go` are post-passes invoked from
the registry, not full Analyzers. README lists them in "eight
analyzers" but doesn't call out the distinction. Document the
contract.

---

## Low — polish

- `state.go` (48 lines) — fold into `interface.go`.
- `tuning.go` — merge constants into `config/config.go` or inline them
  at the single use site.
- `obituary.go` weapons table — already the right shared location;
  extend it with the consolidated parser from C1.
- `tracks.go` — delete if no plan to integrate.

---

## Filters and smoothings currently applied (reference)

For the record, these are the lossy transforms that shape what a
downstream consumer actually sees:

| Transform | Site | Purpose | Risk |
|---|---|---|---|
| Blip filter | `timeline_blipfilter.go` | Suppress single-bucket loc flickers | Real quick traversals get smoothed away; only central tunable (`BlipThresholdMs`) |
| 50 ms bucket quantisation | `timeline.go` buckets | Sampling rate for timeline state | Sub-bucket events invisible |
| Nearest-player pickup attribution | `items.go:213-221` | Label who took an item | Contested pickups unpredictable |
| Obituary regex | `frag.go` + `messages.go` | Parse KTX print messages to frag events | Locale-sensitive, pattern-drift risk |
| Fuzzy name match | `names.go` | Associate chat/obituary names to clients | Substring pass can misattribute |
| Teamkill re-evaluation | `frag.go:76-107` | Fix up OnEvent TK misclassifications | Only works with DemoInfo |

---

## Priority stack

1. Table-driven frag-parsing tests — blocker for C1.
2. Dedup frag parsing (C1) into one helper.
3. Delete `ExtractTracks` (H1) and `SetBucketDuration` (H2).
4. Defer TK classification to `Finalize` uniformly (C2).
5. Add `TakenByInferred` flag on `ItemPhase` (C3, short-term).
6. Split timeline analyzer (A1).
7. Add Context-dependency documentation + `Init`-time assertions (A2).
8. **Separate track:** investigate authoritative pickup signals for
   items and backpacks — see companion doc.

# Xerialen fork → upstream `main` sync evaluation

Date: 2026-07-12

## Decision

Proceed through a reviewed pull request, then deploy in two stages (API/web
preview first, Dragonbot analyzer pin second). The sync keeps the fork's useful
extensions while moving the implementation onto upstream schema v55 and then
bumping the combined fork contract to v57.

The measured Dragonbot risk is low for headline scoring and validity gates,
but not zero for engagement-detail consumers. Upstream's match-only damage and
shot gating intentionally removes warmup/post-match evidence that schema v38
could feed into engagement segments.

## Provenance and integration strategy

- Fork base before sync: `Xerialen/mvd_analyzer` `5043249`, schema v38.
- Upstream integrated: `galfthan/mvd_analyzer` `048ee83`, schema v55.
- Integration branch starts from upstream and records `origin/main` with an
  `ours` merge only after the fork behavior was reimplemented and tested on
  the new structures. This avoids replaying obsolete schema-v38 source over
  the current analyzer DAG.

Fork-specific disposition:

| Fork capability | Disposition |
|---|---|
| Review-gate workflows and agent contracts | Preserved byte-for-byte from the fork. |
| `STAT_ACTIVEWEAPON` stream | Ported as schema v56 (`PlayerStream.w`) across analyzer, views, REST/OpenAPI, MCP, docs, and goldens. |
| KDLOG + inferred tactical decisions | Ported as schema v57 with the real server-log golden fixture, CLI flags, player-slot join, docs, and end-to-end tests. |
| `?demoUrl=` Dragonbot lab deep link | Ported and browser-tested against a real MVD plus a 404 edge case. |
| Fork Three.js full BSP shell | Preserved as an opt-in **Full shell** mode for the five committed maps. Upstream's newer Canvas 3D remains the default so its projectile/beam/nail/LOS/PVS overlays are not displaced. |

## Dragonbot A/B

Method:

- Corpus: `2026-07-12-baseline-s10b`, matches m01–m08 (8 real dm3 4on4
  MVDs plus their original ktxstats and four decision traces).
- Control analyzer: deployed `/home/xerial/dragonbot-deps/qw-analyze`, SHA-256
  `2d089a8a64f2f59a1d180ec173d48ee981cf5c044920b6f6f2bd830d53d0d145`,
  schema v38.
- Candidate: this branch's `qw-analyze`, schema v57.
- Both analyzers used `-include positions,view,height,liquid,velocity`.
- The same freshly built Dragonbot resolver consumed both outputs. No matches
  were rerun; this isolates analyzer impact from match variance.

Results:

| Measure | Schema v38 | Schema v57 | Impact |
|---|---:|---:|---:|
| Mean analyzer wall time | 1.139 s | 1.065 s | −6.5% |
| Total wall time (8) | 9.11 s | 8.52 s | −6.5% |
| Mean max RSS | 83,686 KiB | 89,260 KiB | +6.7% |
| Peak max RSS | 88,192 KiB | 95,176 KiB | +7.9% |
| Mean analysis JSON | 17.373 MB | 17.666 MB | +1.69% |
| Dragonbot scorecards equal | — | 8/8 | No headline KPI change |
| Dragonbot validity gates equal | — | 8/8 | No validity change |
| Full resolved JSON equal | — | 5/8 | Detail changes below |

The production batch runner defaults to `ANALYZE_JOBS=6`; the observed mean
RSS delta therefore adds roughly 33 MiB to the analyzer wave, small relative
to the documented multi-process allowance.

Resolved-detail differences:

- m01 loses one engagement that began at `-2786 ms` (warmup). Aggregate
  engagement count becomes 163→162, initiated 117→116, initiated-advantage
  41→40, disengaged 54→53, and `drgn-4` engagements 8→7. This is a correctness
  improvement from upstream schema v50's match-only damage/shot gating.
- m06 and m07 differ only in their final engagement detail: end time and one
  damage total move when post-match evidence is excluded. Scorecard, gates,
  and all engagement aggregates are unchanged.

Conclusion for this selected eight-match plane only: its headline scorecards
remain comparable. This does not establish equivalence for the other 40 s10b
matches, other maps/modes, or earlier Dragonbot history. Analyses that consume
individual engagement rows should record the analyzer schema/hash and must not
mix v38 and v57 rows as if they had identical window semantics.

The fixed corpus manifest, exact commands, binary/output hashes, raw comparison
summary, selection rationale, and evidence limits are committed under
[`evidence/upstream-sync/`](evidence/upstream-sync/README.md).

## API and portal impact

The public `https://mvdanalyzer.com/healthz` reported schema v53 on
2026-07-12. Deploying this branch therefore moves the hosted contract directly
from v53 to v57.

Important client effects:

- Schema/ETag changes invalidate caches as intended.
- Upstream v55 changes `/damage`'s default family from raw/unbound to bounded;
  consumers that require the old value must send `dmg=raw` explicitly.
- `w` becomes a valid view field and `timelineAnalysis.playerSlots` is an
  additive stored field.
- `decisions` is optional and currently produced by the CLI decision flags;
  the hosted API has no KDLOG-sidecar upload path in this change.
- Existing fields remain available. OpenAPI and golden response validation
  pass at schema v57.

## Verification evidence

- Exact project gate: `make test` passes across reader, analytics, API, MCP,
  and web modules.
- Analyzer corpus: all ten goldens pass; v56 changes were semantically limited
  to schema + `w`, and v57 changes to schema + `playerSlots`.
- Real KDLOG: 1,129 records resolved, 0 errors, 8 player slots (manual CLI
  end-to-end run). A durable subprocess test runs the real CLI flags against
  public golden-corpus game 212422 and verifies generated `playerSlots`, final
  JSON attachment, inferred records, and KDLOG precedence.
- Real inference: 220 records, 0 errors, same demo.
- Web: WASM build passes; direct URL loaded and rendered the real dm3 match in
  Chrome. Missing URL produced `Error: Demo fetch failed: 404 ...` and hid the
  loading overlay. No application console errors were observed (one existing
  Cytoscape wheel-sensitivity warning).
- OpenAPI drift caught and corrected during the first full test run: the
  timeline artifact schema initially lacked `playerSlots`; API tests pass
  after adding it. This correction is intentionally surfaced here.

## Rollout checklist

1. Independent ML/data-impacting review on the current full PR head SHA.
2. CI green; no merge while the PR is draft.
3. Deploy an API/web preview and smoke `/healthz`, OpenAPI docs, one upload,
   `dmg=raw`, default bounded damage, and `fields=w`.
4. After preview acceptance, replace Dragonbot's pinned `qw-analyze`, record
   the new binary hash/schema in run provenance, and resolve one canary batch.
5. Do not compare individual v38/v57 engagement rows without accounting for
   the match-gating change; headline scorecards remain comparable in the A/B.

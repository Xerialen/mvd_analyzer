# Upstream-sync evidence plane

This directory makes the Dragonbot compatibility claim in
`UPSTREAM_SYNC_EVALUATION.md` auditable. It records the selected data plane,
binary/input/output identities, exact command shape, raw comparison summary,
and the limits of the result.

## Evidence chain

```text
available data  -> 48-match 2026-07-12-baseline-s10b control batch
selected data   -> contiguous m01..m08 prefix, fixed before comparison
building blocks -> schema-v38/v57 qw-analyze + one pinned Dragonbot resolver
training run    -> none (this is a data-contract compatibility evaluation)
evaluation      -> same MVD/ktxstats/traces resolved twice; hashes + equality
next experiment -> full 48-match canary after preview deployment
```

The first eight IDs were selected as a cheap, deterministic prefix of the
latest 48-match control batch named by Dragonbot's `docs/current-stage.md`.
There was no filtering by score, validity outcome, analyzer behavior, or the
eventual comparison result. All eight were valid in the source batch.

`dragonbot-ab-manifest.json` pins every MVD and ktxstats input plus a SHA-256
of the four trace files concatenated in lexical filename order. It also pins
the binaries and source commits. `dragonbot-ab-results.json` is the raw
machine-readable comparison output retained from the run: timings, RSS,
analysis/resolved hashes, equality verdicts, and every non-equal field outside
the long engagement-detail arrays (plus the four changed detail leaves for
m06/m07).

The large MVD/analysis artifacts are not copied into this repository. A fresh
clone therefore needs access to the separately retained Dragonbot batch whose
files match the manifest hashes. The committed result artifact is sufficient
to audit the stated arithmetic and output identities, but not to reconstruct
the analyzer outputs without that corpus. This limitation is why conclusions
are scoped to these eight identified matches.

## Reproduction command shape

Run in WSL with GNU `time`, Go, and Python 3. The environment variables are
required deliberately; no machine-local path is assumed by the recipe.

```bash
set -euo pipefail
: "${CONTROL_ANALYZER:?schema-v38 qw-analyze binary}"
: "${CANDIDATE_REPO:?this mvd_analyzer checkout}"
: "${DRAGONBOT_REPO:?dragonbot checkout at the manifest commit}"
: "${CORPUS:?directory containing m01..m08}"
: "${OUT:?empty output directory}"

go build -C "$CANDIDATE_REPO" -o "$OUT/qw-analyze-v57" \
  ./mvd-analytics/cmd/qw-analyze
go build -C "$DRAGONBOT_REPO" -o "$OUT/dragonbot-resolver" ./cmd/dragonbot

for n in $(seq -w 1 8); do
  mid="m$n"
  mdir="$CORPUS/$mid"
  mvd="$mdir/team_drgn_vs_red[dm3].mvd"
  ktx="$mdir/team_drgn_vs_red[dm3].txt"
  mkdir -p "$OUT/control" "$OUT/candidate"

  /usr/bin/time -f '%e %M' -o "$OUT/control/$mid.time" \
    "$CONTROL_ANALYZER" -include positions,view,height,liquid,velocity "$mvd" \
    > "$OUT/control/$mid-analysis.json"
  /usr/bin/time -f '%e %M' -o "$OUT/candidate/$mid.time" \
    "$OUT/qw-analyze-v57" -include positions,view,height,liquid,velocity "$mvd" \
    > "$OUT/candidate/$mid-analysis.json"

  for variant in control candidate; do
    "$OUT/dragonbot-resolver" resolve \
      -analysis "$OUT/$variant/$mid-analysis.json" \
      -ktxstats "$ktx" -trace-dir "$mdir" -match "$mid" -team drgn \
      -out "$OUT/$variant/$mid-resolved.json" || test $? -eq 3
  done
done
```

Verify all input and output hashes against `dragonbot-ab-manifest.json`, then
compare parsed JSON (not formatting) for `scorecard`, `gates`, and the complete
resolved object. The comparison artifact records the expected result.

## What this does not prove

- It does not prove compatibility for the remaining 40 s10b matches, other
  maps, duel/FFA/non-KTX demos, corrupt demos, or no-match-start inputs.
- It does not prove that v38 and v57 engagement detail rows have identical
  semantics; the result demonstrates the opposite at match boundaries.
- It does not measure hosted API authentication, rate limiting, concurrent
  saturation, cache migration, or deployment rollback.
- It is not a training/model-quality experiment and says nothing about whether
  Dragonbot behavior improved. It isolates analyzer-version impact on fixed
  match artifacts.

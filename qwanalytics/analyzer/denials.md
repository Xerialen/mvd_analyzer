# denials post-processor

**Phase:** Result post-processor (registered after `locGraphPost`)
**Inputs:** `result.Items`, `result.LocGraph`, `result.TimelineAnalysis`,
            `result.DemoInfo`
**Writes to Result:** `result.Denials` (`*DenialsResult`)

## What it does

Derives two competitive-Quake metrics from the already-finalised item
timeline:

- **Denials** — a player without RL/LG took a high-value item
  (RA / YA / MH / RL / LG / Quad / Pent / Ring) while only the
  *opposing* team had a weapon-or-Quad bearer in the spawn's region.
  Tracks "value taken from under the enemy".
- **Hoovers** — a player without RL/LG took an armor or MH that a
  *same-team* weapon-or-Quad bearer in the region actually needed
  (low armor / low health). Tracks "value wasted within own team".

Both are post-processors because they need data from analysers that
finalise late: per-bucket player state from the timeline analyser,
loc connectivity from the locgraph post-processor.

## Region

Items live at a loc (`ItemTimeline.Loc`, set by the items analyser).
The "region" for a pickup at loc *A* is:

> `{A}` plus every loc *B* where the loc graph has at least 10
> traversals **in both** directions (`A → B ≥ 10` AND `B → A ≥ 10`).

The both-directions gate intentionally excludes one-way drops, rare
jumps, and route fragments where a player only ever exits once. The
threshold is `denialEdgeMinTraversals = 10`. The region map is built
once at the start of the post-processor (`buildRegionMap`).

## Weapon semantics

A player is treated as a region-control "weapon-bearer" if they
currently hold RL or LG, **or** are currently holding Quad. Quad alone
is included because a Quad player without a weapon is still a credible
threat over the area they cover — and it matches the way the metric is
discussed in the community.

The picker themselves is constrained to *not* hold RL or LG. Quad on
the picker is ignored by the picker test (Quad-only picker is still
"without weapon").

## Detection

For every closed phase in `result.Items` whose kind is in scope:

1. Look up the bucket nearest `phase.TakenAt` via `sort.Search` over
   `HighResBuckets` (sorted by `T`). Times are already match-relative
   on both sides because `normalizeMatchRelativeTimes` runs first.
2. Read the picker's state from the bucket. Skip if the picker holds
   RL or LG.
3. Walk every player present in the bucket and tally — for the item's
   region only:
   - `enemyW` = opposing-team weapon-bearers in the region.
   - `sameW` = same-team weapon-bearers in the region.
   - `sameNeedy` = first same-team weapon-bearer (alphabetical) whose
     armor or health is below the per-item hoover threshold.
4. Emit a denial when `enemyW > 0` AND `sameW == 0` (a clean steal —
   not a contested grab).
5. Emit a hoover when there is a needy same-team weapon-bearer.

Both can fire for the same pickup in principle, though it is rare.

## Hoover thresholds

| Item | Stat | Threshold |
|---|---|---|
| RA | armor | `< 75` |
| YA | armor | `< 50` |
| MH | health | `≤ 50` |

Armor type is irrelevant — the spec is "any kind". The picker constraint
is the same as the denial check (no RL/LG).

## Outputs

```go
type DenialsResult struct {
    Denials []DenialEvent
    Hoovers []HooverEvent
}

type DenialEvent struct {
    Time, Player, Team, Item, Loc string/float64
    EnemyWeapons int    // RL/LG/Quad bearers on the opposing team in region
    PlayerUserID int    // for hub.quakeworld.nu track= URLs
}

type HooverEvent struct {
    Time, Player, Team, Item, Loc string/float64
    NeedyTeammate string // teammate with weapon/Quad and below threshold
    NeedyStat     string // "armor" or "health"
    NeedyValue    int
    PlayerUserID  int
}
```

Both slices are sorted by `Time` ascending. The result is omitted
entirely (`result.Denials == nil`) when both lists are empty.

## Limitations

- **Bucket resolution.** Player state is sampled at the bucket nearest
  `TakenAt` (typical bucket spacing is 0.05 s). Edge cases where a
  player picks up a weapon and an item in the same frame as the
  bucket boundary may snap to the wrong side. In practice this is
  noise.
- **Loc dependence.** Pickups whose item has no `Loc` (no loc file
  for the map, or the item spawn is unmapped) are skipped — the
  region cannot be computed. This is a small fraction in practice.
- **Quad on picker.** Treated the same as no powerup for the
  "without weapon" check. A Quad-only picker still counts as a
  denier; this matches the spec.
- **No directionality on hoover identity.** The first same-team
  needy teammate (alphabetical) wins the `NeedyTeammate` field. The
  metric is "did any teammate need it"; the specific identity is a
  display detail.

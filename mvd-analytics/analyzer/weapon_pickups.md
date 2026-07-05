# weapon_pickups analyser

**Phase:** Derived
**Inputs:** `ItemSpawnEvent`, `ItemPickupHintEvent`,
            `BackpackPickupHintEvent`, `BackpackDropHintEvent`,
            `StatUpdateEvent` (`STAT_ITEMS`), `DeathEvent`, `SpawnEvent`,
            `PlayerPositionEvent`, `StuffTextEvent` / `ServerInfoEvent`
            (weapon-stay detection), `PrintEvent`, `IntermissionEvent`
**Reads from CoreOutputs:** `co.FragEntries`, `co.Slots` (display names)
**Writes to Result:** `result.WeaponPickups` (`[]WeaponPickup`)

## What it does

Records every weapon pickup (world spawner or backpack) along with
how many kills the picker made with that weapon before their next
death. The output is the basis for "weapons matter" analytics: which
RL pickups produced kills, who looted whom, kill-windowing per pickup.

## How it works

1. `ItemSpawnEvent` indexes entity-number → item kind so
   `ItemPickupHintEvent` (entity-keyed) can be classified later.
2. KTX hidden hints are processed pairwise:
   - `ItemPickupHintEvent` → world-spawner pickup record.
   - `BackpackPickupHintEvent` → backpack pickup, joined back to the
     drop record from `BackpackDropHintEvent`.
3. Each pickup is recorded with its slot, time, weapon, source
   ("world" / "backpack"), and a `HadBefore` flag computed from
   the pre-pickup `STAT_ITEMS` snapshot. `HadBefore=true` means the
   player already had this weapon — the pickup didn't grant anything,
   so it is excluded from kill credit.
4. `DeathEvent`s are recorded per slot for next-death lookup.
5. At Finalize, kill windows are built per `(player, weapon)` key. A
   frag from `co.FragEntries` is attributed to the most recent
   covering window (start < frag.Time ≤ end). Suicides and teamkills
   are excluded.
6. Player names are resolved at Finalize via `a.playerName(slot)` —
   prefers `co.SlotName(slot)` (demoinfo-resolved), falls back to
   `ctx.Players[slot].Name`. This is the reference pattern for
   eagerly-captured-at-OnEvent vs resolved-at-Finalize names.

## Weapon-stay synthesis

In weapon-stay modes (serverinfo `deathmatch` 2/3/5 or `coop` — KTX
`weapon_touch`'s `leave` flag, `ktx/src/items.c:835`) weapons never
emit `//ktx took`, so world pickups are synthesized from `STAT_ITEMS`
weapon-bit 0→1 transitions instead (`maybeSynthesizeFromItemsFlip`):

- **Gate**: `weaponStayDetector` latches `deathmatch`/`coop` from the
  fullserverinfo dump (first value wins). No serverinfo → no synthesis.
- **Baseline**: a `weaponFlipTracker` (shared with items.go via
  `weaponstay.go`) maintained continuously and never reset — stat
  updates fire on every value change, so the last observed value is by
  construction the current inventory, and any reset creates a window
  in which a real pickup is swallowed as a silent re-seed (measured:
  a death-reset variant lost 20-40% of grants on a 2on2). Flips are
  instead filtered by player state: flips while dead are inventory
  bookkeeping — except within 50 ms of the DeathEvent, which is a
  grab-then-die whose flip landed in the death-frame flush (KTX counts
  it) — and flips within 50 ms after a SpawnEvent are spawn loadout
  (dmm5-style; dmm3 spawns grant no tracked weapons).
- **Dedup**: a flip already explained by a record within
  `statForwardWindow` (a `//ktx bp` backpack grant, or a `//ktx took`
  if weapon-stay was mis-detected) is skipped; conversely, a hint that
  arrives *after* its flip upgrades the synthesized record in place.
- **Classification**: `source: "world"` when the picker passed within
  the pickup distance gate of a same-kind weapon spawn during the
  stat-lag window; otherwise `source: "unknown"` (almost always a
  non-RL/LG backpack grant, which has no hint in any mode).
- Synthesized records carry `Inferred: true` and `HadBefore: false`
  (the bit was observed flipping), and take part in kill-windowing
  exactly like hint-driven records.

## Limitations / known issues

- Outside weapon-stay modes, only weapons KTX hints are tracked. SG
  (starting weapon) and items without `ItemPickupHintEvent` emission
  are absent.
- Weapon-stay synthesis edge cases (all measured on the corpus and
  bounded by the pickup-invariant test thresholds):
  a grant whose bit flip never surfaces on the wire (grab + death
  inside one stat interval) is unrecoverable;
  a pad grab and a pack grab of the same weapon within one stat
  interval are wire-ambiguous (the pad touch has no hint) — the
  `//ktx bp` record claims the flip, so the grant records as
  "backpack" instead of "world" (grant totals stay correct);
  a non-RL/LG pack grabbed while standing on a matching weapon pad
  classifies as "world".
- A pickup that grants the weapon but is followed by the player
  immediately discarding it (impossible in stock QW but possible with
  some mods) would still be credited with kills made before the
  next death.
- Match-window filtering is lockstep with the rest of the analysers
  via `MatchTimingDetector`; warmup pickups are dropped.

## Reference

- KTX pickup hints: `ktx/src/items.c` (search "//ktx pickup")
- `STAT_ITEMS` bit layout: `mvd-reader/mvd/types.go` (`StatItems` constants)

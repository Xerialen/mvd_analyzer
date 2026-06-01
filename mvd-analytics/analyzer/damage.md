# damage analyser

**Phase:** Derived
**Inputs:** `DamageEvent` (KTX `mvdhidden_dmgdone` 0x0007), `StatUpdateEvent`
            (`STAT_HEALTH`, `STAT_ARMOR`, `STAT_ITEMS`), `PrintEvent`,
            `IntermissionEvent`
**Reads from CoreOutputs:** `co.SlotIdentityAt` (reconnect-unified identity),
            `co.Names` (demoInfo-authoritative team)
**Writes to Result:** `result.Damage` (`*DamageResult`)

## What it does

Surfaces the per-hit damage the reader decodes from KTX's
`mvdhidden_dmgdone` blocks — previously emitted on the event stream but
consumed by no analyzer. Produces per-player totals and a per-hit log,
each in two flavours:

- **`*Raw`** — the unbound wire value (`unbound_dmg_dealt`,
  overkill-inclusive; KTX combat.c:819). A "pressure" signal.
- **`*Eff`** — effective damage, reconstructed to match KTX's scoreboard
  (`demoInfo.dmg.*`). This is the canonical, overkill-free quantity to use
  for DDR / carry metrics.

`Σ givenRaw ≥ demoInfo.given` (gap = overkill); `Σ Eff` reconciles with
`demoInfo` — exactly for duels, ~0.7% for team games. See
`result/damage.go` for the full semantics and `damage_validation_test.go`
for the standing reconciliation against the embedded `demoInfo` oracle.

## How it works

1. Match-gate via `MatchTimingDetector` (same window as deaths/frags), so
   warmup/prewar hits are excluded — the window KTX's scoreboard covers.
2. Track each player's `health` / `armor` / `items` from `StatUpdateEvent`
   (authoritative; fire for every player in an MVD).
3. Per hit, reconstruct KTX's accounting (combat.c:634,648,655,796):
   `save = min(ceil(armortype·D), armor)` (armortype GA .3 / YA .6 / RA .8
   from the held armour bit), `take = D − save`,
   `credit = save + bound(0, take, health)` — then advance the victim's
   armour/health for same-frame later hits. The 9999 telefrag/instakill
   sentinel is reconstructed as what KTX removed (armour + health).
4. In `Finalize`, resolve attacker/victim wire slots → identities via
   `co.SlotIdentityAt` and classify enemy/team via `co.Names` (mirrors
   `frag`). Non-self accounting requires both ends resolved so the
   given/taken sides stay in lockstep.

## Known limitations

- The enemy-vs-team *split* depends on hit-time team resolution; a few hits
  near identity boundaries can land in the wrong bucket (the harness reports
  this, doesn't fail on it). The classification-independent totals are exact.
- Telefrag credit is `k_dmgfrags`-cvar dependent (the cvar isn't in the
  demo) and reconnect identity-folding adds noise — together ~0.7% residual
  on team games.

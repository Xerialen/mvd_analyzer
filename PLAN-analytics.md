# PLAN-analytics — review of mvd-analytics core

> Updated 2026-07-06 after implementation phases 0–5 (branches phase-0…phase-5). Only open items remain below; resolved findings are in the ledger at the bottom and in git history.

The three problem clusters the original review found (fields missed by the time/team post-processors, fragile string matching around timing/obituaries, and the obituary/serverinfo/helper duplication) were all addressed in phases 0–5: `TestTimelineInvariants` now guards the timelineAnalysis enumerations structurally, one obituary parser and one `ResolveSlotAt` chain exist, and the dead/duplicated code is gone. What remains below is (a) a short list of surviving nits, each re-verified against the current tree, and (b) the deferred review of the aim/shots surface (`shots.go` / `aim.go` / `weaponstay.go` and their result shapes), which had never been reviewed and turns out to carry its own instances of the known disease patterns — including one output block gated on an unrelated payload opt-in and the invariant test not reaching outside `timelineAnalysis`.

## Still open — low priority / nits

Each re-verified against the current tree (2026-07-06); line refs updated.

- view/stateat.go:263–281 `nearestPositionIndex` still linear-scans the whole time-sorted track; use `sort.Search` like `nearestSampleIndex` (analyzer/airgibs.go:171, converted in phase 5). `positionAt` (analyzer/teamkill_telefrag.go:171) remains a third "nearest sample to t" variant with its own inline binary search + gap window — consolidation is only partial.
- weapon_pickups.go:425–427 stale comment: "resolve the picker's display name from ctx.Players[slot].Name (patched by the registry to the DemoInfo name post-Finalize of DemoInfoAnalyzer)" — no such registry patching exists; the code resolves via `identityAt`/`ResolveSlotAt` (weapon_pickups.go:463, :502).
- timeline_finalize.go:279–280 comment says "top 5 longest frag streaks" but calls `detectFragStreaks(10, …)`.
- timeline_streams.go:101–116 `updateInterval`: the inner `if s.held` on the true→false branch is provably always true (the `held == s.held` early-return above guarantees it); simplify.
- `disambiguatePlayerName` suffixes *all* colliding names (timeline_streams.go:258–263), not just "the later one" as the `buildStreamsResult` doc claims (timeline_streams.go:464–465); and two same-name identities sharing the same `repSlot` (slot reuse) would still collide after suffixing — tie-break on identity key if it ever matters.
- registry.go:220–222 writes `ctx.Players[e.Player.Slot]` (a fixed `[events.MaxClients]` array) without a bounds check while identity.go:110–112 defensively guards the same slot — either trust the parser in both places or guard in both.
- Two union-find implementations: identity.go:353–375 (type) and timeline_regions.go:215–231 (inline closures); share the small type.
- items.go:1348–1349 `containsKind` is a one-line wrapper over `slices.Contains` (callers items.go:564, :666); call it directly.
- `LegacyReducerSet` (view/fields.go:171–177) is an alias with one caller (mvd-web/cmd/wasm/main.go:116) — fine to keep, but the "preserves the option to diverge later" rationale is the kind of speculative hook the codebase otherwise avoids.
- diagnostic/diagnostic_test.go:62 still only `t.Logf`s quality warnings; consider promoting a few cheap invariants (e.g. "no health > 250 in streams", the F1 time-bounds check) to hard assertions so regressions gate CI.

## Aim/shots surface (deferred review, 2026-07-06)

> Status: F17–F23 **resolved in Phase 5.2** (branch `phase-5.2`, schema
> v49) — see the ledger. Still open below: F24 (quadratic linkers) and
> the nits paragraph.

Scope: `analyzer/shots.go`, `analyzer/aim.go`, `analyzer/weaponstay.go`, `result/shots.go`, `result/aim.go`, `result/sample.go`, the `aimPost`/`airgibsPost` post-processors, and how `duel_normalize`/`normalizeMatchRelativeTimes` handle their fields. Overall shape is good: the sound/beam fire detection is well grounded in KTX sources (fireSoundWeapon's filename table, the TE_LIGHTNING2 rationale, the dm 2/3/5 weapon-stay gate matches ktx/src/items.c:835), reconciliation is diagnostic-only as policy demands, `normalizeMatchRelativeTimes` does enumerate Shots/Projectiles/Beams/Nails times (postprocess.go:107–120, :175–179), and phase-1's duel rewrite covers Shot/ByPlayer teams and VictimKinds (duel_normalize.go:265–320). The findings below are the exceptions, each verified against code and the committed goldens.

- **F24 — Quadratic scans in the linkers and beam matcher.** `linkProjectiles` scans every shot per flight (shots.go:427–438, O(flights×shots)); `linkHitscan` scans the attacker's whole damage list per fire (shots.go:495–503); `matchBeam` scans every beam per missed LG fire (aim.go:619–633). All three inputs (`a.shots` after the sort at shots.go:279, per-slot `dmgs` in event order, `Beams.T`) are time-sorted, so a `sort.Search` window / advancing cursor makes each linear. On the 4on4 goldens (≈5k shots, ≈2k beams) this is tens of millions of comparisons per demo. **(perf, cheap fix)**

- Nits: `aimHitscan` (aim.go:55) duplicates `isHitscanWeapon` (shots.go:680–682). Aim keys its tracks by *stream* names while Shots/Damage carry raw resolved names (aim.go:64–74 vs shots.go:285), so when `disambiguatePlayerName` suffixes a colliding name with `#slot` (timeline_streams.go:570) both same-named players silently lose their crosshair/LG blocks and drop out of miss attribution (`tracks[player]` misses); airgibsPost's `streamByName[d.Victim]` (airgibs.go:50–56, :98) degrades the same way — an edge case, but the failure is silent.

## Resolved (implementation phases 0–5)

| ID | What | Phase / commit |
|---|---|---|
| A1 | Structural invariant test over the goldens (`TestTimelineInvariants`, analyzer/invariants_test.go) — time bounds + duel team-label membership; scope gap closed in P5.2 (F17) | P1 3ebc9cd |
| A2 | One obituary parser (`obituary_parse.go`); frag + messages both consume `parseObituaryLine` | P5 adc4ce5 |
| A3 | Canonical `ResolveSlotAt` (core_outputs.go:146) replaces the per-analyzer copies — straggler closed in P5.2 (F21) | P5 adc4ce5 |
| A4 | Mid-demo source errors and region-control errors recorded into `Result.Errors` (with F9) | P2 cda9940 |
| A5 | Canonical add-a-column checklist in result/coord.go, referenced from every site (full abstraction deliberately not taken) | P0 7d1a8e2 |
| A6 | Observation (view/ healthiest surface), no action needed; its nits landed as F12/F15 | P5 adc4ce5 |
| F1 | `KillEvents` shifted to match clock + duel team rewrite; goldens regenerated | P1 3ebc9cd |
| F2 | Match timing ignores PRINT_CHAT; obituary parsing gated to level ≤ 2 (ktx bprints are PRINT_MEDIUM) | P1 3ebc9cd |
| F3 | CRMod `" eats 2 scoops of "` SSG pattern ordered ahead of generic `" eats "` | P1 3ebc9cd |
| F4 | Obituary table drift eliminated by the single parser (with A2) | P5 adc4ce5 |
| F5 | Duel detection demoinfo-authoritative when demoinfo lists players | P1 3ebc9cd |
| F6 | 0-frag finishers no longer dropped from `match.players` | P1 3ebc9cd |
| F7 | One `parseInfoString` for the three serverinfo walkers — recurred in weaponstay.go, folded in P5.2 (F22) | P5 adc4ce5 |
| F8 | Dead `tracks.go` (+ tracks.md) deleted | P4 f494471 |
| F9 | `source.Next()` abort → `"event stream aborted: …"`; region-control error appended | P2 cda9940 |
| F10 | `appendChangeI16/Str` helpers, shared convert loop, `strconv.Itoa` | P5 adc4ce5 |
| F11 | Generic `shiftAndFilterChanges[C]` replaces the copy-paste twins | P5 adc4ce5 |
| F12 | One `intervalContains`; `intervalsOverlapAt` deleted | P5 adc4ce5 |
| F13 | One effective match end feeds both the powerup close pass and streams finalize | P1 3ebc9cd |
| F14 | `nextDue` watermark in `processSyntheticRespawns` (no per-event sort) | P5 adc4ce5 |
| F15 | One `buildMultiCols` columnar builder behind position/view/velocity | P5 adc4ce5 |
| F16 | Dead `locIndex` return + misleading comment dropped | P4 f494471 |
| nit | Registry post-processor ordering comment lists all nine; result/streams.go position-column doc fixed | P0 7d1a8e2 |
| nit | Determinism ties: powerup events (sorted slots + stable sort), view interval events (sorted codes + (T,Type) tie-break) byte-stable | P5 adc4ce5 |
| nit | Duel rewrite gaps for WeaponPickups/Backpacks/Shots teams + victim-kind reclassification | pre-phase, #100 (v46) |
| F17 | Invariant coverage extended beyond timelineAnalysis: `TestEventSectionInvariants` walks shots/damage/messages/frags/weaponPickups/backpacks/item-phases/aim with per-section clock rules + a bespoke `crosshair.t` bounds check | P5.2 |
| F18 | Aim's rl/gl direct/splash block gates on linking evidence (any linked rl/gl fire), not the opt-in `streams.projectiles` emission — present on every default parse now; docs de-drifted | P5.2 (v49) |
| F19 | Aim's damage collection windowed to match time `[0, MatchEnd]` — warmup/post-match damage no longer inflates Direct | P5.2 (v49) |
| F20 | Duel damage classified enemy at birth (`isDuelResult` in damage.go Finalize) — events, aggregates, matrix, victimWep and EWep buckets consistent with duel-normalized VictimKinds; airgibs/aim no longer starve on shared-team duels; unit-tested (no corpus coverage) | P5.2 (v49) |
| F21 | `ShotsAnalyzer.resolveAt` delegates to the canonical `ResolveSlotAt` (team backfill parity with damage/frags) | P5.2 (v49) |
| F22 | `weaponStayDetector.OnStuffText` consumes `parseInfoString`; first-value-latch semantics preserved, duplicate-key divergence documented at the call site | P5.2 |
| F23 | Doc drift: `"ammo"` source, "left unlinked" claim, Rocket/LGReach ghosts, eye-based Dist, README/API.md/RESULT_SCHEMA.md rows all corrected to the shipped `sound`/`beam` + projectile-linking reality | P5.2 |

# Authoritative Pickup Signals — Investigation

## Update (2026-04-21 — post-implementation)

Signal 1 (per-client `svc_print`) was shipped as a complement to Signals 5/1a (KTX hints), and empirical testing exposed a major caveat that was missed in the original catalog below: `SV_ClientPrintf` in `mvdsv/src/sv_send.c:225` filters prints by the picking client's `messagelevel` cvar *before* writing to the MVD. Pickup prints are PRINT_LOW (0), which competitive QW players commonly suppress with `msg 2` — so the corresponding MVDs contain **zero** PRINT_LOW events. Out of 6 test demos, 4 had no pickup prints at all; the 2 that did gave partial coverage (only the players who had `msg 0` set).

**Signal ranking after this finding:**

1. **KTX `//ktx took` + `//ktx bp`** (Signals 5 / 1a below) — the *only* signals that bypass the `messagelevel` filter, since they're STUFFCMD_DEMOONLY directives rather than prints. **Universal on KTX servers.** Coverage limited to MH / armors / RL-LG-GL-SSG-SNG-NG / powerups + RL/LG backpacks.
2. **Per-client `svc_print`** (Signal 1) — broader item coverage when present (ammo boxes, H15/H25, all backpack classes), but *only* for players with `msg 0`. Useful as a complement, not a replacement.
3. **Player stats updates** (Signal 2) — the universal fallback for everything the KTX hints miss, on every MVD regardless of client config. Not yet implemented.

The catalog below is preserved as originally written; read it with the above caveat in mind.

---

## Summary

This investigation maps wire-level signals in the QuakeWorld MVD protocol that authoritatively identify pickup events (which player took which item at what time). The MVD wire carries three distinct, independently-sufficient attribution signals, ranked by implementation complexity and coverage. The strongest signals are:

1. **Per-client `svc_print` messages** (`dem_single` targeting specific players) — Direct, unambiguous, covers all item types including backpacks.
2. **Player stats updates** (`svc_updatestat`/`svc_updatestatlong` with per-player targeting) — Deterministic deltas in HEALTH, ARMOR, AMMO_*, and ITEMS bitfield; available for all players simultaneously.
3. **Backpack-specific KTX hints** (`//ktx bp` and `//ktx took`) — Authoritative STUFFCMD_DEMOONLY directives already partially parsed.

Beyond these, `svc_sound` with entity and origin carries the sound index and picking player edict, providing a secondary confirmation signal for items (and serving as the primary indicator for backpack pickups since the backpack entity itself exhibits entity-state flutter).

---

## Signal Catalog

### Signal 1: Per-Client `svc_print` Messages (Highest Confidence)

**Wire-level description:**
- MVD message header: `dem_single` with PlayerNum encoding the target client slot (mvd/decoder.go:78–79).
- Payload: `svc_print` (byte 8), followed by a print level (byte), then a null-terminated string.
- The string is the live pickup message sent by the server to the picking player.

**Ground-truth references:**
- **KTX pickup messages:** ktx/src/items.c:
  - Armor (all types): line 568: `G_sprint(other, PRINT_LOW, "You got the %s\n", self->netname);`
  - Health (15/25): line 337: `G_sprint(other, PRINT_LOW, "You receive %.0f health\n", self->healamount);`
  - Megahealth: line 316: `mi_print(other, IT_SUPERHEALTH, va("%s got Megahealth", getname(other)));`
  - Weapons (all): line 1288, 1542, 2049: `G_sprint(other, PRINT_LOW, "You got the %s\n", self->netname);`
  - Powerups (Quad/Pent/Ring/Suit): line 2204: `mi_print(other, self->s.v.items, va("%s got %s", getname(other), self->netname));`
  - Backpack ammo: lines 2404–2618: `G_sprint(other, PRINT_LOW, "You get ");` followed by ammo breakdowns.

- **MVDSV transmission:** mvdsv/src/sv_send.c:
  - Line 234: `MVDWrite_Begin(dem_single, cl - svs.clients, strlen(string)+3)` — sends print to specific client.
  - Lines 236–238: writes svc_print + level + string into dem_single message.

**What we currently decode in qwdemo:**
- `PrintEvent` (qwdemo/parser/print.go:7–40) captures the message string and level, but **does NOT expose the target player**.
- The message header's `PlayerNum` field is available in `msg.Header.PlayerNum` (qwdemo/mvd/decoder.go:79) and passed to `parseNetworkMessage`, but the print parser discards it (parser.go:241).

**Attribution certainty:** **AUTHORITATIVE** — The svc_print is sent directly to the picking player by the game server in response to the touch event. No ambiguity, no race conditions, no nearest-player heuristics needed.

**Coverage:**
- ✅ All armor types (GA, YA, RA)
- ✅ All health (H15, H25, MH)
- ✅ All weapons (RL, LG, GL, SSG, SNG, NG)
- ✅ All ammo boxes (Shells, Nails, Rockets, Cells)
- ✅ All powerups (Quad, Pent, Ring, Suit)
- ✅ Backpack pickups (full ammo breakdowns in the message)

**Implementation path (Phase 1):**
1. Extend `PrintEvent` to include a `TargetPlayerNum` field (int, -1 if broadcast).
2. In `parsePrint` (parser.go:241), capture `msg.Header.PlayerNum` and pass to the event.
3. Modify the PrintEvent to surface the target. Since print level already distinguishes broadcast (level=PRINT_HIGH) vs targeted, this is a straightforward 1-line change to the event struct and 1-line change to the emit.

---

### Signal 2: Player Stats Updates (Secondary, Comprehensive)

**Wire-level description:**
- MVD message headers: `dem_stats` (mvd/types.go:11) or `dem_single` (for per-player updates), each carrying `PlayerNum`.
- Payload: `svc_updatestat` (byte 3) or `svc_updatestatlong` (byte 38).
- Format:
  - Stat index (byte): 0–17 (StatHealth=0, StatArmor=4, StatShells=6, StatNails=7, StatRockets=8, StatCells=9, StatItems=15).
  - Value: single byte (svc_updatestat) or int32 (svc_updatestatlong).
- MVD_FORMAT.md:633–705 documents all stat indices and the STAT_ITEMS bitfield (IT_*).

**Ground-truth references:**
- **KTX stat setting:** ktx/src/items.c, weapon_touch (line 1000+), armor_touch (line 500+), health_touch (line 310+) all end with stat updates to `other->s.v.items` (or ammo) before or during ItemTaken.
- **MVDSV transmission:** mvdsv broadcasts per-player stats on every touch via `MSG_WriteStatUpdate` wrapped in dem_stats.

**What we currently decode in qwdemo:**
- `StatUpdateEvent` (parser/stats.go:7–16) captures PlayerNum, StatIndex, Value, and Time.
- Player stats are maintained in `p.playerStats[playerNum]` (parser.go:103–104, stats.go:136–170).
- All stat indices are decoded, and STAT_ITEMS bitfield values are available in mvd/types.go:114–138.

**Attribution certainty:** **AUTHORITATIVE** for individual pickup moments when combined with timing. A stat jump (health +25, armor shift, ammo delta, IT_* bit flip) unambiguously identifies a pickup event at that player and that time.

**Coverage:**
- ✅ Armor: IT_ARMOR1/2/3 bits flip, STAT_ARMOR value jumps.
- ✅ Health: STAT_HEALTH crosses thresholds (H15=+15, H25=+25, MH=+100 with IT_SUPERHEALTH set).
- ✅ Weapons: IT_* weapon bits flip in STAT_ITEMS.
- ✅ Ammo: STAT_SHELLS / STAT_NAILS / STAT_ROCKETS / STAT_CELLS jump.
- ✅ Powerups: IT_QUAD / IT_INVULNERABILITY / IT_INVISIBILITY / IT_SUIT bits flip.
- ✅ Backpacks: STAT_AMMO_* values jump by the backpack contents (no new item bits since weapons already owned).

**Limitation:** Stat updates are **event-driven, not frame-synchronized**. The MVD_FORMAT.md (line 14) notes: "stat updates arrive at ~3 Hz per player" vs. position updates at ~73 Hz. A contested pickup or a very fast regrab might miss a stat transition if the picking player's stats don't update in the sample window. However, in practice this is rare; the vast majority of pickups produce clear stat deltas.

**Implementation path (Phase 2):**
1. The infrastructure is already in place. Analyzers can subscribe to `StatUpdateEvent` and compare snapshots.
2. No parser changes needed; just add helper functions to items.go/backpacks.go to detect pickup-related stat patterns.
3. Example: `detectArmorPickup(prevStats, currStats, playerNum) -> (armorType, time)` by checking (prevArmor, currArmor) pairs and IT_* bit transitions.

---

### Signal 3: Entity-State Visibility (Existing, Soft Signal)

**Current implementation:**
- `ItemStateEvent` (parser/entities.go:54–66) fires on visibility transitions of item entities.
- `Taken=true` when entity disappears from the wire entity-state stream (assumed pickup).
- `Taken=false` when entity reappears (respawn).

**Ground-truth references:**
- qtx/src/items.c: Item entities are set SOLID_NOT and model="" on pickup (e.g., line 536), making them invisible to the entity-state stream.
- ezquake cl_ents.c: Visibility is driven by PF_REMOVE flag or model absence.

**What we currently decode:**
- Parser/entities.go:179–263 tracks entity spawn and state changes.
- Items.go uses ItemStateEvent to drive the "taken/available" phase model.

**Attribution certainty:** **SOFT** — The nearest-player heuristic (items.go:351–372, `attributePickup`) is unreliable for contested items.

**Backpack-specific issue:**
- Comment in backpacks.go:19–25 explains: "The wire-level ItemStateEvent stream for backpack edicts produces phantom visibility cycles in the 200 ms class...that we cannot currently distinguish from genuine fast pickups."
- This is a **real phenomenon**, not a parser bug (confirmed by the wire formats).

**Implementation path (Phase 3 — defer for now):**
1. This signal is weakest; better to rely on svc_print + stats.
2. If we must diagnose the flutter: check mvdsv/src/server.h for STAT_ITEMS and entity baseline resets mid-frame.
3. The flutter is likely caused by: multiple STAT_ITEMS updates causing the entity to toggle between "has model" and "no model" within a single frame or across frame boundaries.

---

### Signal 4: Sound Events with Entity Origin (`svc_sound`)

**Wire-level description:**
- `svc_sound` (byte 6 in payload, qwdemo/parser/parser.go:573–574).
- Format (ezquake cl_ents.c:CL_ParseSound):
  - Entity + channel (ushort): entity number in bits 0–10, channel in 11–12.
  - Optional volume (byte) if high bits set.
  - Optional attenuation (byte) if high bits set.
  - Sound index (byte).
  - Origin (3 shorts or 3 floats if FTE_PEXT_FLOATCOORDS).

**Ground-truth references:**
- **KTX item pickup sounds:** ktx/src/items.c:
  - Armor: line 570: `sound(other, CHAN_AUTO, "items/armor1.wav", 1, ATTN_NORM);`
  - Health (H15): line 248: `self->noise = "items/r_item1.wav";`
  - Health (H25): line 263: `self->noise = "items/health1.wav";`
  - Health (MH): line 255: `self->noise = "items/r_item2.wav";`
  - Weapons: line 1023–1043 (via weapon_touch, no explicit sound; absorbed into the item sound table).
  - Powerups: line 2100: `sound(other, CHAN_ITEM, self->noise, 1, ATTN_NORM);` (where self->noise is set per-powerup class).
  - **Backpacks:** line 2620: `sound(other, CHAN_ITEM, "weapons/lock4.wav", 1, ATTN_NORM);`

**What we currently decode in qwdemo:**
- `skipSound` (parser.go:691–707) reads and skips the entire svc_sound message without extracting fields.
- **No event is emitted.** The parser discards all sound information.

**Attribution certainty:** **HIGH** (secondary confirmation, not primary).
- The entity field in svc_sound is the picking player's edict (player_slot + 1).
- The sound index can be cross-referenced against the server's svc_soundlist to identify the exact sound.
- The origin is the picking player's location.
- **For backpacks specifically:** This is the **primary authoritative signal** because backpack entities exhibit entity-state flutter. The "weapons/lock4.wav" sound on the picking player's edict is unambiguous backpack pickup proof.

**Coverage:**
- ✅ All regular items via distinctive pickup sounds.
- ✅ Backpack pickups via "weapons/lock4.wav" on picking player entity.
- ⚠️ Ammo boxes (shells, nails, rockets, cells) may share sounds (e.g., all use "items/itembk2.wav" for respawn).

**Implementation path (Phase 2.5 — secondary):**
1. Extract entity + channel + sound index from svc_sound.
2. Emit a `SoundEvent` with (entity, channel, soundIndex, origin, time).
3. In items.go, filter for known pickup sound indices and cross-check against nearby entity vanishes.
4. For backpacks: use "weapons/lock4.wav" as a deterministic pickup signal (backpacks.go:go ahead and listen for the sound and validate against the KTX hint's timing).

---

### Signal 5: KTX Pickup Hints (Backpack-Specific, Authoritative)

**Wire-level description:**
- `//ktx bp <ent> <player_ent>` STUFFCMD_DEMOONLY (ktx/src/items.c:2471).
- **Delivered via:** `svc_stufftext` (byte 9) in a `dem_single` message to the picking player.
- **Related:** `//ktx took <ent> <respawn_sec> <player_ent>` for regular items (lines 355, 541, 1048, 2074, 2083).
- **Related:** `//ktx drop <ent> <item_flags> <dropper_ent>` for backpack drops (line 2740) — **already parsed** via BackpackDropHintEvent.

**Ground-truth references:**
- ktx/src/items.c:2471: `stuffcmd_flags(other, STUFFCMD_DEMOONLY, "//ktx bp %d %d\n", NUM_FOR_EDICT(self), NUM_FOR_EDICT(other));`
  - `self` = backpack entity, `other` = picking player.
- ktx/src/items.c:355, 541, 1048, 2074, 2083: `//ktx took` emitted on regular item pickups.
  - `self` = item entity, respawn time in seconds, `other` = picking player.

**What we currently decode in qwdemo:**
- `BackpackDropHintEvent` (parser/ktx_drop.go) parses only `//ktx drop`.
- **No pickup hints are parsed.** The `//ktx took` and `//ktx bp` hints are emitted but ignored.

**Attribution certainty:** **AUTHORITATIVE** — These are explicit KTX-specific signals stamped directly by the pickup code.

**Coverage:**
- ✅ Backpack pickups (via `//ktx bp`).
- ✅ Regular items (via `//ktx took` — includes respawn time hint).
- ❌ Non-KTX servers (hints only available on KTX).

**Implementation path (Phase 2):**
1. Extend ktx_drop.go to add `tryEmitBackpackPickupHint` (parse `//ktx bp`).
2. Extend ktx_drop.go to add `tryEmitItemPickupHint` (parse `//ktx took`).
3. Create new event types `BackpackPickupHintEvent` and `ItemPickupHintEvent`.
4. In backpacks.go, subscribe to `BackpackPickupHintEvent` and use it as the authoritative pickup source (replaces the absent entity-state signal).
5. In items.go, optionally subscribe to `ItemPickupHintEvent` as a secondary confirmation (but svc_print is preferred since it's server-agnostic).

---

## Backpack-Specific Findings

### The Entity-State Flutter Phenomenon

Backpack entities (model: progs/backpack.mdl, created via `TossObject` at ktx/src/items.c:2636–2849) exhibit a **real visibility flutter** on the wire.

**Root cause hypothesis** (not fully diagnosed, but supported by code inspection):
1. Backpack is spawned as SOLID_TRIGGER with a model and a touch function (BackpackTouch).
2. When touched, BackpackTouch removes the entity via `ent_remove(self)` (line 2625).
3. However, the entity's baseline may be resent mid-match if the server resets baselines or if deltapacketentities refs it.
4. Additionally, the entity may have PF_DEAD or other flags set temporarily, causing it to flicker in/out of the entity-state stream.
5. The 200 ms flutter interval aligns with the server's packet delta window (~73 Hz ~= 14 ms per frame, 200 ms = ~14 frames).

**Why this matters:**
- A contest for a backpack between two players may produce 2–3 ItemStateEvent transitions (taken, untaken, taken again) in rapid succession.
- The entity-state signal cannot distinguish a pickup contest from a fast regrab of a respawned backpack.

**Solution:** Ignore entity-state for backpacks entirely. Use **only** the authoritative signals:
- `//ktx bp` hint from backpack pickup touch code.
- Alternatively, `svc_print` (per-client) showing "You get..." messages.
- Alternatively, `svc_sound` with "weapons/lock4.wav" on the picking player.

### Backpack Contents Attribution

KTX only emits `//ktx drop` for RL and LG backpacks; other drops (SSG, NG, SNG, GL, Shotgun, empty) are not hinted.

**Current workaround** (backpacks.go:29–30):
- Non-RL/LG drops are silently dropped by the analyzer.

**Alternative approach** (Phase 3, if needed):
- Listen to `//ktx bp` hints for all backpack pickups.
- Use the ammo stat deltas to infer the backpack weapon type:
  - Only shells increased → Shotgun/SSG backpack.
  - Only nails increased → NG/SNG backpack.
  - Only rockets increased → RL/GL backpack.
  - Only cells increased → LG backpack.
  - Multiple ammo types → Mixed (e.g., leftover from a complex drop scenario).

---

## Recommendations — Implementation Plan

### Phase 1: Parse Per-Client `svc_print` Messages (Quick Win, Highest ROI)

**Changes to qwdemo/parser:**
1. Modify `PrintEvent` struct (parser/print.go:7–40):
   ```go
   type PrintEvent struct {
       Level        int     // Print level (PRINT_LOW, PRINT_MEDIUM, PRINT_HIGH, PRINT_CHAT)
       Message      string  // The print message
       TargetPlayer int     // -1 for broadcast, 0–31 for targeted (dem_single)
       Time         float64
   }
   ```

2. Update `parsePrint` to capture the target:
   ```go
   func (p *Parser) parsePrint(r *mvd.BufferReader, msg *mvd.DemoMessage, time float64) error {
       level, err := r.ReadByte()
       if err != nil { return err }
       message, err := r.ReadString()
       if err != nil { return err }
       cleanedMessage := cleanString(message)
       
       targetPlayer := -1
       if msg.Header.MessageType == mvd.DemSingle {
           targetPlayer = msg.Header.PlayerNum
       }
       
       return p.emit(&PrintEvent{
           Level:        int(level),
           Message:      cleanedMessage,
           TargetPlayer: targetPlayer,
           Time:         time,
       })
   }
   ```

3. Update `events/events.go` to re-export the new field (type aliases handle this automatically once PrintEvent is updated).

**Changes to qwanalytics/analyzer/items.go:**
1. Add a handler for `PrintEvent` with `TargetPlayer >= 0`.
2. Parse pickup messages for each known item type:
   ```
   "You got the %s" → weapon or armor
   "You receive %.0f health" → health
   "%s got Megahealth" → megahealth (extract player name from message)
   "You get " → backpack (followed by ammo details)
   ```
3. On match: pick the printing player's slot as TakenBy (deterministic, no heuristic needed).

**Changes to qwanalytics/analyzer/backpacks.go:**
1. Subscribe to `PrintEvent` with TargetPlayer >= 0 and message starting with "You get ".
2. Extract ammo counts from the message (e.g., "25 rockets" → extract 25).
3. Correlate with existing `//ktx drop` hints by time and picking player.
4. Emit pickup attribution.

**Test coverage:**
- Fixture: MVD with contested armors, weapons, and backpack pickups.
- Verify: PrintEvent.TargetPlayer is set correctly for dem_single messages.
- Verify: items.go and backpacks.go attribute correctly without nearest-player heuristic.

---

### Phase 2: Implement Stat-Based Backup Attribution

**Changes to qwdemo/parser:**
- No changes needed; `StatUpdateEvent` is already fully parsed.

**Changes to qwanalytics/analyzer/items.go:**
1. Add a fallback handler for stat changes when PrintEvent is unavailable or has a different player than expected.
2. Detect pickup patterns:
   ```
   STAT_ARMOR: prevArmor < currArmor → armor pickup
   STAT_ITEMS: (currItems & IT_ARMOR1) && !(prevItems & IT_ARMOR1) → GA pickup
   STAT_HEALTH: prevHealth + 25 == currHealth → H25
   STAT_HEALTH: prevHealth + 100 == currHealth (and IT_SUPERHEALTH set) → MH
   STAT_SHELLS/NAILS/ROCKETS/CELLS: increase → ammo box
   ```
3. Use this as a secondary attribution when Print-based attribution is unavailable (e.g., non-KTX servers, or if demo is partially corrupted).

**Changes to qwanalytics/analyzer/backpacks.go:**
1. Add a fallback handler for backpack pickups using ammo stat deltas.
2. On seeing a stat update with ammo increases and the picking player not already holding the relevant weapon, infer a backpack pickup.

**Test coverage:**
- Fixture: MVD from a non-KTX server (if available) or a synthetic MVD with svc_print stripped.
- Verify: items are attributed correctly using stat deltas alone.

---

### Phase 3: Parse Backpack-Specific KTX Hints

**Changes to qwdemo/parser/ktx_drop.go:**
1. Rename file to `ktx_hints.go` (or keep as-is and add a note).
2. Add parsing for `//ktx bp <backpack_ent> <player_ent>`:
   ```go
   type BackpackPickupHintEvent struct {
       BackpackEnt int     // server edict number of the backpack
       PlayerEnt   int     // picker's edict (player_slot + 1)
       Time        float64
   }
   ```
3. Add `tryEmitBackpackPickupHint` alongside the existing drop hint parser.
4. (Optional) Add parsing for `//ktx took <item_ent> <respawn_sec> <player_ent>` as a secondary signal:
   ```go
   type ItemPickupHintEvent struct {
       ItemEnt    int
       RespawnSec float64
       PlayerEnt  int
       Time       float64
   }
   ```

**Changes to qwanalytics/analyzer/backpacks.go:**
1. Subscribe to `BackpackPickupHintEvent`.
2. On receipt, emit the pickup (end the phantom drop, start the pickup phase).
3. Correlate with `//ktx drop` for full drop + pickup timeline.

**Test coverage:**
- Fixture: KTX MVD with multiple backpack pickups.
- Verify: `//ktx bp` hints are parsed and correlated correctly with `//ktx drop` hints.
- Verify: Backpack pickup times match the hint times exactly.

---

### Phase 4: Sound Events (Optional, Secondary Confirmation)

**Changes to qwdemo/parser:**
1. Create `events/sounds.go` with `SoundEvent`.
2. Modify `skipSound` to `parseSound` (parser.go:691–707):
   ```go
   type SoundEvent struct {
       Entity    int     // Player edict or other entity
       Channel   int     // CHAN_AUTO, CHAN_ITEM, etc.
       SoundIdx  byte    // Sound number from svc_soundlist
       Origin    [3]float32
       Time      float64
   }
   ```
3. Emit SoundEvent for all svc_sound messages.

**Changes to qwanalytics/analyzer/items.go:**
1. Optionally subscribe to SoundEvent and use to cross-check pickup attribution.
2. Example: If a svc_print arrives with targetPlayer=X, and a few ms later a pickup sound arrives with entity=X, that's strong corroboration.

**Test coverage:**
- Fixture: MVD with sound details.
- Verify: SoundEvent carries correct entity, channel, soundIdx.
- Verify: items.go can optionally use sound as a secondary signal.

---

## Open Questions / Unknowns

1. **Timing skew between signals:** How far apart can svc_print, stat updates, and sound events arrive for the same logical pickup event? We assume < 1 frame (13 ms), but haven't confirmed. If they can be > 100 ms apart, correlation becomes unreliable.

2. **MVD truncation / server crashes:** If the MVD is truncated mid-pickup, will we see partial signals (e.g., a print without a stat update, or vice versa)? How should analyzers handle incomplete events?

3. **Non-KTX servers:** The `//ktx bp` and `//ktx took` hints are KTX-specific. How common are non-KTX servers in the wild? Should we optimize for KTX or ensure stats-based fallback is equally robust?

4. **Bot/monster pickups:** KTX and mvdsv may apply special logic when bots or monsters touch items. Do they trigger the same svc_print messages? The code suggests yes, but it's worth confirming with a bot-enabled demo fixture.

5. **Powerup stacking:** When a player touches a Quad while holding a Pent, do we see two separate svc_print messages or a single combined message? The code suggests two separate touches (two calls to `touch`), so two prints expected.

6. **Entity numbers in hints:** KTX uses `NUM_FOR_EDICT(...)` to encode entity numbers. In a 32-client server, are entity numbers 1–32 (player edicts) or 0–31 (0-indexed slots)? We assume entity = slot + 1 based on backpacks.go:94, but this should be verified.

7. **Stat update ordering:** When multiple items are taken in the same frame (e.g., jumping over ammo + weapon simultaneously), do stat updates arrive in a deterministic order? Or can they be interleaved, causing the analyzer to mis-order pickups?

---

## References

### Source Code

- **KTX items pickup:** ktx/src/items.c:38–2849 (entire file; key functions: ItemTouched, health_touch, armor_touch, weapon_touch, powerup_touch, BackpackTouch).
- **KTX drop hints:** ktx/src/items.c:2355–2849 (backpack drop logic and `//ktx drop` emission).
- **KTX pickup hints:** ktx/src/items.c:355, 541, 1048, 2074, 2083, 2471 (`//ktx took` and `//ktx bp` emissions).
- **MVDSV transmission:** mvdsv/src/sv_send.c:230–319 (svc_print + dem_single/dem_all logic).
- **MVDSV sound:** mvdsv/src/sv_send.c:700+ (svc_sound emission to clients).
- **Current parser:** qwdemo/parser/print.go, parser.go:240–245, parser/stats.go, parser/entities.go.
- **Current items analyzer:** qwanalytics/analyzer/items.go:200–263 (ItemStateEvent handling).
- **Current backpacks analyzer:** qwanalytics/analyzer/backpacks.go (BackpackDropHintEvent handling).
- **KTX hint decoder (pattern to extend):** qwdemo/parser/ktx_drop.go.

### Wire Format Documentation

- **MVD message types:** qwdemo/mvd/types.go:4–13 (dem_single, dem_multiple, dem_stats, dem_all).
- **MVD decoder:** qwdemo/mvd/decoder.go:58–171 (message header extraction, including PlayerNum).
- **Print parsing:** qwdemo/MVD_FORMAT.md (search "svc_print").
- **Stats indices:** qwdemo/MVD_FORMAT.md:633–705 (svc_updatestat, STAT_* constants, IT_* bitfield).
- **Entity state:** qwdemo/parser/entities.go:20–120 (modelPathToKind classification).

### Published Standards

- **QW Protocol:** https://github.com/QW-Group/qwprot (reference: protocol.h, for svc_* constants and message layouts).
- **ezquake client:** ezquake-source/src/cl_parse.c (reference implementation for parsing svc_print, svc_updatestat, svc_sound).
- **MVDSV:** mvdsv/src/sv_send.c (reference implementation for demo recording).


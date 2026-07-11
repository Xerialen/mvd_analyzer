# identity analyser

**Phase:** Core (registered right after `demoinfo`)
**Inputs:** `UserInfoEvent`, `PrintEvent`
**Writes to Result:** nothing
**Writes to CoreOutputs:** `co.Sessions` + the `co.SlotIdentityAt(slot, tMs)` resolver it backs

## What it does

Reconstructs player identity across reconnects so per-player outputs
aren't mislabelled. A player who disconnects and reconnects mid-match
gets a *new* wire slot (and a new userid); the slot they vacated is often
reused by someone else or stamped with a late userinfo name. The old
slot→final-name resolution (`co.Slots`) then relabels the player's
pre-reconnect events with the wrong name. KTX itself unifies the player
via its ghost mechanism (restore-stats-by-netname on reconnect,
`ktx/src/client.c:1513-1538`); this analyser reproduces that unification.

## How it works

1. **Sessions (during the event pass).** Each `UserInfoEvent` opens /
   continues / rotates a per-slot *session* (one contiguous occupancy by
   a single userid). A new session opens when a slot's userid changes; a
   plain name change with the same userid stays one session (a rename,
   which final-name resolution already handled correctly). Scalars are
   copied off `e.Player` — the parser mutates that struct in place on the
   next occupancy (`mvd-reader/parser/userinfo.go:47-54`).
2. **Reconnect prints.** `rejoins the game with …` / `reenters the game
   without stats` broadcasts are recorded (already Q-normalised to
   ASCII, so the `[team]` brackets and redtext fold to plain text).
3. **Unification (`PopulateCore`).** Sessions are folded into canonical
   identities via union-find over four signals, in priority order:
   (1) shared nonzero `*auth` login; (2) same demoinfo player (login or
   normalized-name join, reusing the `demoinfo` index); (3) a KTX
   `rejoins`/`reenters` print for that netname; (4) bare-demo fallback —
   unify by normalized netname, *only* when there is no demoinfo, no auth
   and no reconnect print (so modern demos never over-merge two distinct
   same-name players).
4. **Output.** `co.Sessions[slot]` is the time-sorted, identity-resolved
   occupancy list (first session extends to -inf, last to +inf so edge
   events still resolve). `co.SlotIdentityAt(slot, tMs)` returns the
   identity that held the slot at `tMs`.

### Diagnostic identity isolation

The CLI's closed `diagnostic-buckets` mode opts this analyser into a stricter
non-match policy. Every observed slot occupancy remains a separate identity:
auth, demoinfo, reconnect-print, and bare-name unions are disabled. A
userid-ambiguous userinfo update with a different normalized name rotates the
slot instead of adopting the later identity. Timeline output also guarantees
unique display keys when separate occupancies used the same name and slot.
This policy prevents standby history from being attributed to the candidate;
ordinary match analysis retains the four reconnect-union sources above.

## Who consumes it

- **items**, **weapon_pickups**, **timeline** (frag events, powerups,
  streaks) resolve each event by its own timestamp via
  `co.SlotIdentityAt`, so pre-reconnect events stay with the right player.
- **timeline streams** group per-slot builders by
  `ResolvedSession.IdentityKey`, stitching a player's two slots into one
  `PlayerStream` (and carving a slot shared by two players at the
  handover). Phantom sessions with no recorded play are dropped.

## Limitations / known issues

- A reconnect with **no demoinfo, no auth, no KTX print** and a
  **different netname** each time cannot be unified — there is no signal
  linking the two names. (The bare-demo fallback only joins identical
  normalized names.)
- The bare-demo name fallback could merge two genuinely distinct players
  who share a name on an old non-KTX demo; this matches the pre-existing
  risk of the name-join in `Context.ResolveSlotDemoInfo`.

## Reference

- KTX ghost restore + rejoin/reenter prints: `ktx/src/client.c:1490-1556`
- KTX leave print: `ktx/src/client.c:2948`, `ktx/src/bot_commands.c:401`
- Wire format: [`mvd-reader/MVD_FORMAT.md`](../../mvd-reader/MVD_FORMAT.md) (search "reconnect")

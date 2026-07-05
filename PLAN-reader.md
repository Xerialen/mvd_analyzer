# PLAN-reader — review of mvd-reader

> Updated 2026-07-05 against main @ 05e2ed9 (schema v47). Originally written pre-#97; the sound/tempentity/nails decoders added since materially change the skip-path inventory — re-verified throughout.

Scope: `mvd-reader/` (events/, mvd/, parser/, mvdfile/, source/mvd/), ~7.7k LOC
including tests. Review targets: correctness, quality, LOC bloat, readability,
extensibility, maintainability.

**Overall assessment.** The module is in good shape: the layering (wire decoder
→ push parser → pull `events.Source` façade) is clean and honestly enforced,
the doc comments are exemplary (nearly every non-obvious decision cites
ktx/mvdsv/ezquake source lines), the integer-millisecond time discipline is
carried through consistently, and the tests exercise real wire encodings
rather than mocks. The problems are concentrated in three areas: (1) a
**duplicated wire-layout implementation** — every svc/entity-delta format
exists once in "parse" form and once in "skip" form, and the two copies have
already diverged; (2) an **end-of-demo signalling bug** where the normal
termination path of most demos surfaces as an error instead of `io.EOF`,
which downstream code compensates for by swallowing *all* errors; and (3) a
layer of **dead code and dead exported API** (unused domain types, unreachable
skip branches, reimplemented stdlib helpers) that adds ~300 LOC of maintenance
surface for zero value. None of these require architectural change — the plan
below is incremental.

---

## Big picture

**A1. Keep the architecture; it is right.** Wire framing in `mvd/`, payload →
event translation in `parser/`, type-alias façade in `events/`, push→pull
adaptation in `source/mvd/`. Logic lands at the right altitude (e.g. the
three-source death detection lives in the parser because it is
protocol-level; obituary *attribution* stays in analytics). No structural
refactor is warranted. The suggestions below are consolidation, not redesign.

**A2. One wire format, two implementations — consolidate the parse and skip
paths.** Every layout the parser understands is written twice: once to decode
(`entities.go` `applyDeltaFields`/`parseSpawnBaseline`, `position.go`
`parsePlayerInfo`, and since #97 `sound.go` `parseSound`, `tempentity.go`
`parseTempEntity`, `nails.go` `parseNails`, `serverdata.go` `parseSoundList`)
and once to skip (`parser.go` `skipEntityDelta`, `skipSpawnBaseline`,
`skipPlayerInfo`, `skipPacketEntities`, `skipSound`, `skipTempEntity`,
`skipNails`, `skipModelList`/`skipSoundList`). #97 gave svc_sound /
svc_temp_entity / svc_nails real parse paths in the main switch but kept
their skip twins, so the duplicated surface *grew*. Duplicate layout code
*will* drift, and it already has (see F3). Worse, most of the skip copies
are unreachable (see F4) because the main dispatch switch handles those
commands, so the dead copy silently rots. Suggested end state:
- `skipCommand` handles only commands the main switch does **not** decode.
- The entity-delta layout has exactly one implementation
  (`applyDeltaFields` + a flag-word reader shared with `parsePacketEntities`
  and `readDelta`); "skip" is just decode-and-discard. The per-field
  byte cost of decoding into a stack `EntityState` is negligible relative to
  drift risk.
- Delete `skipPlayerInfo`, `skipSpawnBaseline`-as-duplicate,
  `skipPacketEntities`/`skipDeltaPacketEntities`, and the newly-dead
  `skipSound`/`skipTempEntity`/`skipNails`/`skipModelList`/`skipSoundList`
  (#97 already removed `skipTempEntityDiag`, folding its diagnostics into
  `parseTempEntity`).
Impact: correctness + bloat (~200 LOC net deletion) + extensibility (new FTE
flag handled in one place).

**A3. Clean end-of-demo is reported as an error.** `events/events.go:23-25`
promises "Next returns io.EOF at a clean end of stream", but the standard MVD
termination — `svc_disconnect` with text `"EndOfDemo"` — returns
`mvd.ErrEndOfDemo` from `parseNetworkMessage` (parser.go:346-350), which
`ParseOne` (parser.go:224-233) passes through raw (it only maps the
*decoder*-level `ErrEndOfDemo` to `io.EOF`), so `Source.Next`
(source/mvd/source.go:87-93) returns it as a hard error. Evidence this bites
in practice: the untracked debug tools special-case `err != mvd.ErrEndOfDemo`
(cmd/debug-frags/main.go:52, cmd/debug-stats/main.go:86),
`qw-analyze dumpEvents` (mvd-analytics/cmd/qw-analyze/main.go:442-457) returns
an error for every healthy demo that ends with the disconnect, and
`registry.go:201-208` copes by treating **every** Next error as end-of-stream
("Log and stop" — and it doesn't even log), which means real corruption is
now indistinguishable from a clean end. Fix in Layer 1:
1. In `ParseOne` (or at the `SvcDisconnect` site), map the disconnect-based
   end to `io.EOF` too.
2. In `Source.Next`, when `ParseOne` fails, first **drain events already
   queued** by that call before surfacing the error — today events emitted
   earlier in the final message are silently dropped.
3. Then tighten `registry.go` to propagate non-EOF errors (Layer-2 follow-up;
   noted in PLAN-analytics).
Impact: correctness.

**A4. Events are not immutable — two events share pointers with live parser
state.** `UserInfoEvent.Player` (userinfo.go:57, 110) and
`ServerDataEvent.Data` (serverdata.go:120) hand consumers pointers into
parser-owned structs that the parser keeps mutating. This is load-bearing
spookiness: `parseModelList` (serverdata.go:161-164) fills in
`ServerData.MapFile` *after* `ServerDataEvent` was emitted, and the analytics
registry stores `e.Player` pointers (`registry.go:213-215`) that retroactively
change on every subsequent userinfo update. A consumer that value-copies at
event time gets a different answer than one that holds the pointer — nothing
in the `events` package documents which is intended. Suggested fix: emit
value snapshots (`Player PlayerInfo`, not `*PlayerInfo`), and make the
MapFile discovery its own small event (or re-emit ServerDataEvent when it is
known) rather than in-place mutation. This is a schema-touching change —
sequence it deliberately, with the analytics consumers audited in the same
PR. Impact: correctness (latent) + extensibility.

**A5. Inconsistent event time representation.** The project learned the
float-drift lesson and added canonical `TimeMs` — now on ten event types
(`PlayerPositionEvent`, `DeathEvent`, `SpawnEvent`, `MoverSpawnEvent`,
`MoverStateEvent`, plus #97's `SoundEvent`, `BeamEvent`, `NailsFrameEvent`,
`ProjectileSpawnEvent`, `ProjectileDespawnEvent` — every new event adopted
it, so the convention is winning). Everything else still carries only float
seconds, and `entities.go:250-255` (`wireMs`) round-trips float→ms to recover
what the decoder had exactly. Suggestion: pass `TimeMs` alongside `Time`
through the parse call-chain (it is already threaded to most parsers) and put
it on every event; consider an `EventTimeMs() int32` companion on the `Event`
interface so consumers stop re-deriving it. Impact: correctness-adjacent
consistency + extensibility.

**A6. Parser state never resets on a second `svc_serverdata`.**
`parseServerData` replaces `p.serverData` but leaves `modelList`, `soundList`,
`baselines`, `currentEntities`, `spawnedItems`, `spawnedMovers`,
`spawnedProjectiles`, `playerDead*`, and `matchStarted` from the previous
map. Irrelevant for single-match MVD files,
but the package doc sells the event schema as QTV-stream-ready
(events/events.go:2-14), and a live QTV stream crosses map changes. Either
reset the per-map state in `parseServerData` or document the single-map
assumption where the QTV claim is made. Impact: extensibility.

**A7. Match-start phrase list is duplicated across layers in the wrong
direction.** `print.go:96-109` keeps a copy of the analyzer's
`matchStartPatterns` with a "should be mirrored here" comment — a drift
landmine only the parser side even acknowledges (matchtiming.go:32-39 carries
no back-pointer, so an addition there silently strands the parser's gate).
The dependency arrow allows the clean fix: analytics imports mvd-reader, so
the **canonical table can live in Layer 1** (parser or events) and
`mvd-analytics/analyzer/matchtiming.go` can import it. Delete the mirror
comment. Impact: maintainability.

---

## Findings

**F1. Bulk userinfo parses the wrong spectator key (`spectator` vs
`*spectator`).** — userinfo.go:145 vs userinfo.go:103. Ground truth: mvdsv
*removes* the client-sent `spectator` key and sets the star key
(`mvdsv/src/sv_main.c:1065-1066`, `sv_user.c:466-467`), and ezquake reads
`*spectator` from broadcast userinfo (`ezquake-source/src/cl_parse.c:2118`).
So `parseUserInfoString` — the path that handles the initial
`svc_updateuserinfo` for every player — can never set
`PlayerInfo.Spectator`, while `parseSetInfo` checks the correct `*spectator`.
Analytics gates on this flag (`mvd-analytics/analyzer/match.go:79`). Fix by
unifying the two duplicated key-switches (userinfo.go:88-108 and 130-147)
into one `applyUserInfoKey(player, key, value) bool` helper that accepts
`*spectator` (and keep `spectator` as a fallback if QWD-era demos need it —
verify against the golden corpus). Add a regression test with a real
`\*spectator\1` userinfo string. *(correctness)*

**F2. `ErrEndOfDemo` escapes the Source contract; queued events dropped.**
Details and fix in A3. Concrete edits: parser.go:346-350 (or 224-233),
source/mvd/source.go:87-93 (drain-then-error), plus a test asserting a demo
ending in `svc_disconnect "EndOfDemo"` yields the tail events then `io.EOF`.
*(correctness)*

**F3. The skip-path entity delta has diverged from the parse-path.** Three
concrete divergences between `skipEntityDelta` (parser.go:1079-1176) and
`applyDeltaFields`/`parsePacketEntities` (entities.go:412-643), all
re-verified on current HEAD:
- `skipEntityDelta` does not skip `uFTEScale` / `uFTEFatness` payload bytes
  that `applyDeltaFields` handles (entities.go:614-623) — a demo using those
  would misalign only on the (dead-ish) skip path.
- Extension gating differs: skip gates `uFTETrans`/`uFTEColourMod` on the
  negotiated `fteExt` bits (parser.go:1166-1173), parse skips them
  unconditionally on `morebits` (entities.go:609-634). ezquake gates on the
  negotiated extension (cl_ents.c:580, 586), so the parse path is the
  nonconforming one.
- `parsePacketEntities` reads the FTE morebits byte only when `fteExt != 0`
  (entities.go:463) but `readDelta` reads it unconditionally
  (entities.go:660).
Resolving A2 (single implementation) makes all three impossible by
construction; otherwise fix each individually. *(correctness)*

**F4. ~200 LOC of unreachable/duplicate skip code — grown since #97.**
`skipCommand` is only invoked from the `default:` arm of the main dispatch
switch (parser.go:450-455), so its branches for commands the main switch
already handles are dead. #97 moved svc_sound / svc_temp_entity /
svc_nails / svc_soundlist into the main switch without deleting their skip
branches, so the dead set grew. Dead branches now: `SvcSound` (738-739),
`SvcSpawnBaseline` (757-763), `SvcTempEntity` (767-768), `SvcPlayerInfo`
(794-795), `SvcNails`/`SvcNails2` (796-797), `SvcModelList` (800-801),
`SvcSoundList` (802-803), `SvcPacketEntities` (804-805),
`SvcDeltaPacketEntities` (806-807), `SvcSetInfo` (812-823, even
self-documented as "unused"), `SvcFTESpawnBaseline2` (833-839),
`SvcFTEModelListShort` (847-848). Helper functions with no live caller die
with them: `skipSound` (856-872), `skipTempEntity` (892-939 — its own doc
now admits it is a "skip-only fallback retained on skipCommand's path", but
that path is unreachable), `skipPlayerInfo` (953-993, a byte-for-byte
duplicate of position.go's `skipPlayerInfoRemainder`), `skipNails`
(995-1005), `skipModelList`/`skipSoundList` (1007-1033, identical twins —
both now deletable outright), `skipPacketEntities` (1035-1049),
`skipDeltaPacketEntities` (1051-1054). Still reachable and staying:
`skipSpawnBaseline` (via `SvcSpawnStatic`), `skipDownload`, and
`skipEntityDelta` (via `SvcFTESpawnStatic2`) — until A2 replaces them.
The pre-#97 `skipTempEntityDiag`/`skipTempEntity` fold suggestion is
resolved: #97 removed `skipTempEntityDiag`. *(bloat)*

**F5. Skip helpers ignore `Skip`/read errors → silent cursor misalignment.**
`BufferReader.Skip` returns `io.EOF` *without advancing* (mvd/reader.go:266-272),
so an ignored error leaves the cursor in place and the command loop
reinterprets the remaining payload bytes as fresh svc commands — exactly the
"silent drift" failure mode the project's own comments call the worst case
(parser.go:892-916). Ignored returns at: `skipSound` (parser.go:861-867),
`skipSpawnBaseline` (874-886), `skipDownload` (946), `parseHiddenDamage`
(596), `skipDeltaPacketEntities` (1052), `skipPlayerInfoRemainder`
(position.go:161-190), and the `SvcDisconnect` `ReadString` (parser.go:347).
All of these can trivially propagate — though note the skipSound and
skipDeltaPacketEntities sites are now dead code per F4, so deletion also
cures them. (#97's new `parseSound`/`parseTempEntity`/`parseNails` all check
their Skip errors — the new code got this right.) *(correctness)*

**F6. Dead exported types and aliases.** Verified zero references anywhere in
the workspace (reader, analytics, api, web, mcp):
- `mvd.Demo` (types.go:408-419) — legacy batch-mode aggregate.
- `mvd.PrintMessage` (272-277), `mvd.FragEvent` (279-287), `mvd.PlayerState`
  (218-232), `mvd.DamageEvent` (289-297 — a stale duplicate of
  `parser.DamageEvent`, differing in field types).
- Their `events` aliases `PlayerState`, `Stats`, `PrintMessage`, `FragEvent`,
  `Vec3`, `Angle3` (events/events.go:110-119) and `EntityState`
  (events/events.go:105) — none used by any consumer (position/mover events
  and #97's sound/beam/projectile events all use raw `[3]float32`).
- The entire `Kind` alias + `Kind*` constants block (events/events.go:35-70)
  — zero consumer references; dispatch is by type switch as the docs
  recommend. #97 dutifully extended it to 30 constants (KindSound…KindNails),
  growing the untested surface.
Delete them (and fix the README "domain types carried on those events" list,
which names `PlayerState`/`Stats` — mvd-reader/README.md:9-13). If the Kind
constants are meant as public API for future consumers, document that intent;
otherwise they are 30 lines of untested surface. *(bloat)*

**F7. Hand-rolled stdlib substitutes with a false justification.**
`lowercaseASCII` + `containsASCII` (print.go:126-157) exist "so the import
surface stays minimal" — but `obituary.go:3` in the same package already
imports `strings`. Replace with `strings.Contains(strings.ToLower(msg), …)`
(ASCII-only behaviour is preserved for these all-ASCII needles). −30 LOC.
*(bloat)*

**F8. KTX stuffcmd hint parsing is the same function three times.**
`tryEmitItemPickupHint` (ktx_pickup.go:69-96), `tryEmitBackpackPickupHint`
(101-123), `tryEmitBackpackDropHint` (ktx_drop.go:34-62) are structurally
identical: match prefix, `strings.Fields`, N × `strconv.Atoi`, emit. Extract
`parseKtxHintInts(cmd, prefix string, n int) ([]int, bool)` and reduce each
to ~8 lines; the next `//ktx` directive becomes a table entry. Also consider
having the `SvcStuffText` case (parser.go:364-384) check the `"//ktx "`
prefix once before fanning out to the three matchers. *(bloat / extensibility)*

**F9. `ItemStateEvent` doc contradicts re-baseline behaviour.** The doc
promises `Taken=false` when "a fresh baseline replaced a taken entity"
(entities.go:57-69), but `registerBaseline` overwrites `currentEntities`
without emitting an item state event (entities.go:337-374; only movers get
the re-baseline state emission at 386-397) — and because the overwrite makes
the entity already-visible, the next frame diff sees no transition either.
Either emit the transition for items on re-baseline (mirroring the mover
branch) or fix the doc to say re-baselines reset silently. Decide against a
demo that actually resends baselines if one exists. *(correctness / docs)*

**F10. Diagnostics mislabel truncated known commands as "unknown_svc".**
`skipCommand` returns `io.EOF` both for "command not in the table"
(parser.go:849-852) and for a genuine truncated read inside a known command's
skip; the caller then warns `unknown_svc` for both (parser.go:450-455). Use a
distinct sentinel (`errUnknownSvc`) for the not-in-table case and report
truncation as `parse_error` with the command name. (#97 fixed the temp-entity
instance of this by giving it its own `unknown_te` warning at parser.go:334-338;
the general skipCommand conflation remains.) Cheap, and makes the diagnostic
corpus runs trustworthy. *(readability / diagnostics)*

**F11. Entity-diff emit errors are discarded.** `diffItemEntity` /
`diffMoverEntity` / #97's `diffProjectileEntity` use `_ = p.emit(...)`
(entities.go:754, 770, 773, 797, 816, 850, 872 — the pattern spread to the
new projectile events) because `diffEntityTransitions` returns void —
breaking the handler contract (a handler error should abort parsing; see
`emit`, parser.go:197-205). Today's only production handler never errors,
but the contract exists for sources/tools that do. Make the diff functions
return error and propagate from `parsePacketEntities`. *(correctness / API
contract)*

**F12. `parseServerData` silently zeroes the ten movevars on truncation.**
serverdata.go:87-115 ignores every error (`gravity, _ := …`). A truncated
serverdata yields `MaxSpeed=0`-style physics silently — inconsistent with the
careful error handling five lines earlier. Propagate the first error.
*(correctness, low likelihood)*

**F13. `parseHiddenDemoInfo` reads the JSON body byte-by-byte.**
parser.go:653-660 loops `ReadByte` into a pre-sized slice; `r.ReadBytes(contentLen)`
is one line (note it returns a sub-slice of the message payload — fine here
because each `DemoMessage.Payload` is freshly allocated, but copy if the
ReadBytes aliasing nit below ever changes). *(readability)*

**F14. gofmt failures.** `gofmt -l mvd-reader/` flags 6 files:
`mvd/reader.go`, `mvd/types.go`, `parser/entities_mover_test.go`,
`parser/ktx_drop.go`, `parser/ktx_pickup.go`, `parser/obituary.go`
(`parser/entities.go` came clean via #97's rewrite). For a repo whose
CLAUDE.md demands style discipline, run gofmt once and keep it clean
(consider a CI check). *(readability)*

**F15. Doc drift, various.** Fix while touching the files:
- events/events.go:4 references `qwdemo/mvd` / `qwdemo/parser` — stale module
  name.
- README event table + prose (mvd-reader/README.md:69-70, 88-96) describe
  `DeathEvent`/`SpawnEvent` as StatHealth-derived only; the implementation now
  has three deduplicated sources (StatHealth edges, DF_DEAD bit, obituary
  corroboration via `forceEmitDeath`) — the README should sell that design,
  it's one of the best parts of the parser.
- diagnostic.go:8 lists warning categories `invalid_slot` and
  `payload_abandoned` that nothing emits.
- entities.go:150 says "see classifyArmor below" — no such function (the
  logic is inline in `classifyItem`).
- source/mvd/source.go:22 calls the queue a "ring"; it's a reset-and-reuse
  slice. *(docs)*

**F16. `mvdfile.Open` gzip detection is convoluted and half-dead.**
file.go:40-42: `isGzip` ORs in a filename-suffix check that the following
`if isGzip && magic…` immediately neutralizes — the suffix can never matter.
Reduce to the magic-byte check (correct behaviour, current behaviour), and
extract the shared peek-and-detect used by both `Open` and `NewReader`.
*(readability)*

**F17. Frag updates via `dem_stats` don't emit `FragUpdateEvent`.**
`updateStat`'s `StatFrags` arm (stats.go:228-231) updates `players[].Frags`
but emits only the generic `StatUpdateEvent`, while `svc_updatefrags`
(stats.go:170-195) emits `FragUpdateEvent`. The analytics registry builds
`ctx.FragsBySlot` from `FragUpdateEvent` only (registry.go:216-218). If any
demo carries a frag change only through `STAT_FRAGS`, that consumer misses
it. Either emit `FragUpdateEvent` (deduplicated on value change) from the
stat path, or record why `svc_updatefrags` is guaranteed to always accompany
it (mvdsv reference). *(correctness, needs verification)*

---

## Low priority / nits

- `parseMessage` (parser.go:237): `msg.Payload == nil || len(msg.Payload) == 0`
  — the second test covers the first.
- `decoder.go:140-168`: the `DemSingle, DemStats` and `DemAll, DemRead` cases
  are identical; merge into one `case` list. The "unknown message type" error
  (176) should include the type byte and stream offset for diagnosability.
- `decoder.go:101`: `Time: float64(d.timeMs) * 0.001` duplicates
  `CurrentTime()`; call the method.
- `registerBaseline` (entities.go:351): local variable named `copy` shadows
  the builtin.
- parser.go:1171: `morebits&int(uFTEColourMod)` — the cast is redundant.
- `ktx_pickup_print.go:178`: the `!strings.HasPrefix(trimmed, ktxGotThePrefix)`
  guard is unreachable ("You get " and "You got the " already differ at byte
  5) and the check inconsistently uses `msg` while the others use `trimmed`.
- `BufferReader.ReadBytes` returns an aliased sub-slice while
  `BinaryReader.ReadBytes` returns a fresh copy (mvd/reader.go:202-209 vs
  101-109) — document the aliasing on the interface-like pair so a future
  caller doesn't retain a view into a reused buffer.
- `position.go:111-123` drops position events when the origin is exactly
  (0,0,0) as "likely uninitialized" — a heuristic filter under the project's
  "surface, don't filter" policy. Probably harmless (players are never at the
  exact world origin on real maps), but the comment should cite that
  reasoning, not just "likely uninitialized".
- `Parser.Players()` / `Parser.PlayerStats()` (parser.go:187-195) have no
  non-test callers in the workspace outside untracked debug tools — candidates
  for pruning when A4 (snapshot events) is done.
- `decodeULEB128` (parser.go:717-728) silently accepts a truncated varint
  (all-continuation bytes); given F15's note that some demos carry garbage in
  this block anyway, fine — but a comment would help.
- Naming: the constant `KindPlayerInfo` maps to `PlayerPositionEvent`
  (events.go:45/79) — a rename-in-place trap for readers; if F6 keeps the
  Kind constants, align the names.

## Suggested sequencing

1. **Mechanical, zero-risk:** F14 (gofmt), F7, F13, F15, nits — one PR.
2. **Dead-code deletion:** F4 + F6 (verify with `go build ./...` +
   golden corpus) — one PR, large negative diff.
3. **Correctness:** F1 (with test), F2/A3 (with test), F5, F12, F10, F11 —
   can be split, each with a golden-corpus run.
4. **Consolidation:** A2 (single delta implementation, resolves F3), F8, F9,
   A7 — the only structurally interesting work.
5. **Schema-touching (design first):** A4 (event immutability), A5 (TimeMs
   everywhere), A6 (multi-map reset), F17 — these change observable
   behaviour; bump schema/docs per CLAUDE.md rules.

## New since this review (#97–#102, not covered above)

Only #97 materially touched this module (#100 edited `MVD_FORMAT.md` docs
only — `ktx_pickup.go`/`ktx_drop.go` are unchanged, F8 stands as written).
Not deep-reviewed; pattern flags only:

- `parser/sound.go` — svc_sound decoded into `SoundEvent` (emitting entity
  unpacked from the channel word, precache name resolved via the new
  `parseSoundList` table in serverdata.go). Value-typed, carries `TimeMs`,
  checks every `Skip` error — clean against the A4/A5/F5 patterns.
- `parser/tempentity.go` — `parseTempEntity` surfaces `TE_LIGHTNING1/2/3`
  as `BeamEvent` and consumes point effects by known length; it absorbed
  `skipTempEntityDiag` and the main-switch route, but its TE-type case list
  duplicates the layout table in the now-unreachable `skipTempEntity`
  (parser.go:917-939) — the parse/skip duplication pattern recurring
  (folded into the A2/F4 inventories above).
- `parser/nails.go` — opt-in svc_nails/svc_nails2 decode
  (`Parser.SetDecodeNails`) into `NailsFrameEvent`; its internal
  `!p.decodeNails` consume-without-emitting branch restates the dead
  `skipNails` layout — one more A2 instance, though at least inside a
  single function.
- entities.go projectile tracking (`ProjectileSpawnEvent` /
  `ProjectileDespawnEvent`, `diffProjectileEntity`) — adopts the
  `_ = p.emit(...)` discard (extends F11) and adds `spawnedProjectiles`
  to the per-map state that never resets (extends A6).
- events/events.go dutifully grew five new Kind constants + aliases for the
  new events — extending the dead surface F6 wants deleted; none of the new
  code uses `Vec3`/`Kind` either.
- All five new event types carry `TimeMs` and share no pointers with parser
  state — the new code follows the A5 convention and avoids the A4 trap.
- New tests (`sound_test.go`, `tempentity_test.go`, `nails_test.go`,
  `entities_projectile_test.go`) exercise real wire encodings, matching the
  module's existing test style.

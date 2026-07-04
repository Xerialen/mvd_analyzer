# PLAN-reader — review of mvd-reader

Scope: `mvd-reader/` (events/, mvd/, parser/, mvdfile/, source/mvd/), ~6.8k LOC
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
`parsePlayerInfo`) and once to skip (`parser.go` `skipEntityDelta`,
`skipSpawnBaseline`, `skipPlayerInfo`, `skipPacketEntities`). Duplicate
layout code *will* drift, and it already has (see F3). Worse, most of the
skip copies are unreachable (see F4) because the main dispatch switch handles
those commands, so the dead copy silently rots. Suggested end state:
- `skipCommand` handles only commands the main switch does **not** decode.
- The entity-delta layout has exactly one implementation
  (`applyDeltaFields` + a flag-word reader shared with `parsePacketEntities`
  and `readDelta`); "skip" is just decode-and-discard. The per-field
  byte cost of decoding into a stack `EntityState` is negligible relative to
  drift risk.
- Delete `skipPlayerInfo`, `skipSpawnBaseline`-as-duplicate,
  `skipPacketEntities`/`skipDeltaPacketEntities`, and fold
  `skipTempEntityDiag`/`skipTempEntity` and `skipModelList`/`skipSoundList`
  into single functions.
Impact: correctness + bloat (~150 LOC net deletion) + extensibility (new FTE
flag handled in one place).

**A3. Clean end-of-demo is reported as an error.** `events/events.go:23-25`
promises "Next returns io.EOF at a clean end of stream", but the standard MVD
termination — `svc_disconnect` with text `"EndOfDemo"` — returns
`mvd.ErrEndOfDemo` from `parseNetworkMessage` (parser.go:317), which
`ParseOne` (parser.go:216-225) passes through raw (it only maps the
*decoder*-level `ErrEndOfDemo` to `io.EOF`), so `Source.Next`
(source/mvd/source.go:87-93) returns it as a hard error. Evidence this bites
in practice: the untracked debug tools special-case `err != mvd.ErrEndOfDemo`,
`qw-analyze dumpEvents` (mvd-analytics/cmd/qw-analyze/main.go:438-443) returns
an error for every healthy demo that ends with the disconnect, and
`registry.go:185-192` copes by treating **every** Next error as end-of-stream
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
registry stores `e.Player` pointers (`registry.go:196-198`) that retroactively
change on every subsequent userinfo update. A consumer that value-copies at
event time gets a different answer than one that holds the pointer — nothing
in the `events` package documents which is intended. Suggested fix: emit
value snapshots (`Player PlayerInfo`, not `*PlayerInfo`), and make the
MapFile discovery its own small event (or re-emit ServerDataEvent when it is
known) rather than in-place mutation. This is a schema-touching change —
sequence it deliberately, with the analytics consumers audited in the same
PR. Impact: correctness (latent) + extensibility.

**A5. Inconsistent event time representation.** The project learned the
float-drift lesson and added canonical `TimeMs` — but only to five event
types (`PlayerPositionEvent`, `DeathEvent`, `SpawnEvent`, `MoverSpawnEvent`,
`MoverStateEvent`). Everything else carries only float seconds, and
`entities.go:183-185` (`wireMs`) round-trips float→ms to recover what the
decoder had exactly. Suggestion: pass `TimeMs` alongside `Time` through the
parse call-chain (it is already threaded to several parsers) and put it on
every event; consider an `EventTimeMs() int32` companion on the `Event`
interface so consumers stop re-deriving it. Impact: correctness-adjacent
consistency + extensibility.

**A6. Parser state never resets on a second `svc_serverdata`.**
`parseServerData` replaces `p.serverData` but leaves `modelList`, `baselines`,
`currentEntities`, `spawnedItems`, `spawnedMovers`, `playerDead*`, and
`matchStarted` from the previous map. Irrelevant for single-match MVD files,
but the package doc sells the event schema as QTV-stream-ready
(events/events.go:2-14), and a live QTV stream crosses map changes. Either
reset the per-map state in `parseServerData` or document the single-map
assumption where the QTV claim is made. Impact: extensibility.

**A7. Match-start phrase list is duplicated across layers in the wrong
direction.** `print.go:96-109` keeps a copy of the analyzer's
`matchStartPatterns` with a "keep in sync" comment — a drift landmine both
files acknowledge. The dependency arrow allows the clean fix: analytics
imports mvd-reader, so the **canonical table can live in Layer 1** (parser or
events) and `mvd-analytics/analyzer/matchtiming.go` can import it. Delete the
mirror comment pair. Impact: maintainability.

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
Details and fix in A3. Concrete edits: parser.go:317 (or 216-225),
source/mvd/source.go:87-93 (drain-then-error), plus a test asserting a demo
ending in `svc_disconnect "EndOfDemo"` yields the tail events then `io.EOF`.
*(correctness)*

**F3. The skip-path entity delta has diverged from the parse-path.** Three
concrete divergences between `skipEntityDelta` (parser.go:1074-1169) and
`applyDeltaFields`/`parsePacketEntities` (entities.go:446-616):
- `skipEntityDelta` does not skip `uFTEScale` / `uFTEFatness` payload bytes
  that `applyDeltaFields` handles (entities.go:544-553) — a demo using those
  would misalign only on the (dead-ish) skip path.
- Extension gating differs: skip gates `uFTETrans`/`uFTEColourMod` on the
  negotiated `fteExt` bits (parser.go:1159-1166), parse skips them
  unconditionally on `morebits` (entities.go:539-564). ezquake gates on the
  negotiated extension, so the parse path is the nonconforming one.
- `parsePacketEntities` reads the FTE morebits byte only when `fteExt != 0`
  (entities.go:393) but `readDelta` reads it unconditionally
  (entities.go:590).
Resolving A2 (single implementation) makes all three impossible by
construction; otherwise fix each individually. *(correctness)*

**F4. ~150 LOC of unreachable/duplicate skip code.** `skipCommand` is only
invoked from the `default:` arm of the main dispatch switch
(parser.go:418-429), so its branches for commands the main switch already
handles are dead: `SvcSpawnBaseline` (731-737), `SvcPlayerInfo` (768-769),
`SvcModelList` (774-775), `SvcPacketEntities` (778-779),
`SvcDeltaPacketEntities` (780-781), `SvcSetInfo` (786-797, even
self-documented as "unused"), `SvcFTESpawnBaseline2` (807-813),
`SvcFTEModelListShort` (821-822). Their helper functions `skipPlayerInfo`
(946-986, a byte-for-byte duplicate of position.go's
`skipPlayerInfoRemainder`), `skipPacketEntities` (1028-1042),
`skipDeltaPacketEntities` (1044-1047) die with them (`skipEntityDelta` stays
— still reachable via `SvcFTESpawnStatic2`, until A2 replaces it). Also fold:
`skipTempEntityDiag` and `skipTempEntity` (parser.go:889-932) are identical
except for returning the TE type — implement one as a one-line wrapper of the
other; `skipModelList`/`skipSoundList` (1000-1026) are identical — keep one.
*(bloat)*

**F5. Skip helpers ignore `Skip`/read errors → silent cursor misalignment.**
`BufferReader.Skip` returns `io.EOF` *without advancing* (mvd/reader.go:266-272),
so an ignored error leaves the cursor in place and the command loop
reinterprets the remaining payload bytes as fresh svc commands — exactly the
"silent drift" failure mode the project's own comments call the worst case
(parser.go:869-887). Ignored returns at: `skipSound` (parser.go:836-845),
`skipSpawnBaseline` (848-860), `skipDownload` (939), `parseHiddenDamage`
(570), `skipDeltaPacketEntities` (1045), `skipPlayerInfoRemainder`
(position.go:161-190), and the `SvcDisconnect` `ReadString` (parser.go:315).
All of these can trivially propagate. *(correctness)*

**F6. Dead exported types and aliases.** Verified zero references anywhere in
the workspace (reader, analytics, api, web, mcp):
- `mvd.Demo` (types.go:408-419) — legacy batch-mode aggregate.
- `mvd.PrintMessage` (272-277), `mvd.FragEvent` (279-287), `mvd.PlayerState`
  (218-232), `mvd.DamageEvent` (289-297 — a stale duplicate of
  `parser.DamageEvent`, differing in field types).
- Their `events` aliases `PlayerState`, `Stats`, `PrintMessage`, `FragEvent`,
  `Vec3`, `Angle3`, `EntityState` (events/events.go:94-108) — none used by
  any consumer (position/mover events use raw `[3]float32`).
- The entire `Kind` alias + 25 `Kind*` constants block (events/events.go:35-65)
  — zero consumer references; dispatch is by type switch as the docs
  recommend.
Delete them (and fix the README "domain types carried on those events" list,
which names `PlayerState`/`Stats` — mvd-reader/README.md:10-12). If the Kind
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
having the `SvcStuffText` case (parser.go:344-351) check the `"//ktx "`
prefix once before fanning out to the three matchers. *(bloat / extensibility)*

**F9. `ItemStateEvent` doc contradicts re-baseline behaviour.** The doc
promises `Taken=false` when "a fresh baseline replaced a taken entity"
(entities.go:61-66), but `registerBaseline` overwrites `currentEntities`
without emitting an item state event (entities.go:280-304; only movers get
the re-baseline state emission at 317-327) — and because the overwrite makes
the entity already-visible, the next frame diff sees no transition either.
Either emit the transition for items on re-baseline (mirroring the mover
branch) or fix the doc to say re-baselines reset silently. Decide against a
demo that actually resends baselines if one exists. *(correctness / docs)*

**F10. Diagnostics mislabel truncated known commands as "unknown_svc".**
`skipCommand` returns `io.EOF` both for "command not in the table"
(parser.go:823-825) and for a genuine truncated read inside a known command's
skip; the caller then warns `unknown_svc` for both (parser.go:425-428). Use a
distinct sentinel (`errUnknownSvc`) for the not-in-table case and report
truncation as `parse_error` with the command name. Cheap, and makes the
diagnostic corpus runs trustworthy. *(readability / diagnostics)*

**F11. Entity-diff emit errors are discarded.** `diffItemEntity` /
`diffMoverEntity` use `_ = p.emit(...)` (entities.go:677, 696, 730, 752)
because `diffEntityTransitions` returns void — breaking the documented
handler contract (a handler error should abort parsing; parser.go:190-197).
Today's only production handler never errors, but the contract exists for
sources/tools that do. Make the diff functions return error and propagate
from `parsePacketEntities`. *(correctness / API contract)*

**F12. `parseServerData` silently zeroes the ten movevars on truncation.**
serverdata.go:87-115 ignores every error (`gravity, _ := …`). A truncated
serverdata yields `MaxSpeed=0`-style physics silently — inconsistent with the
careful error handling five lines earlier. Propagate the first error.
*(correctness, low likelihood)*

**F13. `parseHiddenDemoInfo` reads the JSON body byte-by-byte.**
parser.go:627-634 loops `ReadByte` into a pre-sized slice; `r.ReadBytes(contentLen)`
is one line (note it returns a sub-slice of the message payload — fine here
because each `DemoMessage.Payload` is freshly allocated, but copy if F6's
aliasing note ever changes). *(readability)*

**F14. gofmt failures.** `gofmt -l mvd-reader/` flags 7 files:
`mvd/reader.go`, `mvd/types.go`, `parser/entities.go`,
`parser/entities_mover_test.go`, `parser/ktx_drop.go`, `parser/ktx_pickup.go`,
`parser/obituary.go`. For a repo whose CLAUDE.md demands style discipline,
run gofmt once and keep it clean (consider a CI check). *(readability)*

**F15. Doc drift, various.** Fix while touching the files:
- events/events.go:4 references `qwdemo/mvd` / `qwdemo/parser` — stale module
  name.
- README event table + prose (mvd-reader/README.md:69-70, 83-91) describe
  `DeathEvent`/`SpawnEvent` as StatHealth-derived only; the implementation now
  has three deduplicated sources (StatHealth edges, DF_DEAD bit, obituary
  corroboration via `forceEmitDeath`) — the README should sell that design,
  it's one of the best parts of the parser.
- diagnostic.go:8 lists warning categories `invalid_slot` and
  `payload_abandoned` that nothing emits.
- entities.go:116 says "see classifyArmor below" — no such function (the
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
`ctx.FragsBySlot` from `FragUpdateEvent` only (registry.go:199-201). If any
demo carries a frag change only through `STAT_FRAGS`, that consumer misses
it. Either emit `FragUpdateEvent` (deduplicated on value change) from the
stat path, or record why `svc_updatefrags` is guaranteed to always accompany
it (mvdsv reference). *(correctness, needs verification)*

---

## Low priority / nits

- `parseMessage` (parser.go:229): `msg.Payload == nil || len(msg.Payload) == 0`
  — the second test covers the first.
- `decoder.go:105-177`: the `DemSingle, DemStats` and `DemAll, DemRead` cases
  are identical; merge into one `case` list. The "unknown message type" error
  (176) should include the type byte and stream offset for diagnosability.
- `decoder.go:101`: `Time: float64(d.timeMs) * 0.001` duplicates
  `CurrentTime()`; call the method.
- `registerBaseline` (entities.go:281): local variable named `copy` shadows
  the builtin.
- entities.go:1164 (`parser.go` actually): `morebits&int(uFTEColourMod)` — the
  cast is redundant.
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
- `Parser.Players()` / `Parser.PlayerStats()` (parser.go:179-187) have no
  non-test callers in the workspace outside untracked debug tools — candidates
  for pruning when A4 (snapshot events) is done.
- `decodeULEB128` (parser.go:691-702) silently accepts a truncated varint
  (all-continuation bytes); given F15's note that some demos carry garbage in
  this block anyway, fine — but a comment would help.
- Naming: the constant `KindPlayerInfo` maps to `PlayerPositionEvent`
  (events.go:45/74) — a rename-in-place trap for readers; if F6 keeps the
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

# PLAN-reader — review of mvd-reader

> Updated 2026-07-06 after implementation phases 0–5 (branches phase-0…phase-5). Only open items remain below; resolved findings are in the ledger at the bottom and in git history.

Scope: `mvd-reader/` (events/, mvd/, parser/, mvdfile/, source/mvd/). Review
targets: correctness, quality, LOC bloat, readability, extensibility,
maintainability.

**Where things stand.** The three problem areas of the original review are
gone: the duplicated parse/skip wire layouts were consolidated to one reader
per layout (P4+P5), the end-of-demo signalling bug was fixed with a
drain-then-error Source contract (P2), and the dead exported surface was
deleted (P4, part of a ~700 LOC cut). `gofmt -l mvd-reader/` is clean. What
remains open: the schema-touching batch (A4–A6), a handful of nits, and the
new findings from the deferred #97-decoder review below.

---

## Big picture (open)

**A1.** The architecture is right — wire framing in `mvd/`, payload → event
translation in `parser/`, type aliases in `events/`, push→pull adaptation in
`source/mvd/` — and everything below is incremental, not structural.

**A4. Events are not immutable — two events share pointers with live parser
state.** Unchanged by phases 0–5 (deliberately deferred as schema-touching).
`UserInfoEvent.Player` (userinfo.go:11-14, emitted at :57 and :110) and
`ServerDataEvent.Data` (serverdata.go:8-11, emitted at :104) hand consumers
pointers into parser-owned structs that the parser keeps mutating. The
load-bearing spookiness is still live: `parseModelList` fills in
`ServerData.MapFile` *after* the `ServerDataEvent` was emitted
(serverdata.go:145-148), and the analytics registry stores both pointers
(mvd-analytics/analyzer/registry.go:217-221), which retroactively change on
every later userinfo update. A consumer that value-copies at event time gets
a different answer than one that holds the pointer; nothing in `events`
documents which is intended. Fix as before: emit value snapshots
(`Player PlayerInfo`, not `*PlayerInfo`), make the MapFile discovery its own
small event (or re-emit ServerDataEvent), and audit the analytics consumers
in the same PR. All five #97 event types got this right (value fields only),
so the problem is contained to these two legacy events. Impact: correctness
(latent) + extensibility.

**A5. Inconsistent event time representation — re-verified: 10 of 30 event
types carry `TimeMs`** (PlayerPosition, Death, Spawn, MoverSpawn, MoverState,
Sound, Beam, NailsFrame, ProjectileSpawn, ProjectileDespawn; the EventType
inventory is parser.go:27-58). Partially improved since the original review:
the ms clock is now threaded into nearly every parse path (`parsePrint`, the
stat parsers, playerinfo, sound/TE/nails all receive `msg.TimeMs`), so for
most of the remaining 20 types the work is just adding the field. The seams
that remain:
- `diffItemEntity` is the one diff sibling *not* passed `timeMs`
  (entities.go:838 vs :788/:888), so `ItemSpawnEvent` / `ItemStateEvent`
  stay float-only;
- `registerBaseline` isn't passed `timeMs` and recovers it via the `wireMs`
  float→ms round-trip (entities.go:283-287, used at :430/:441);
- `PrintEvent`'s parser receives `timeMs` (parser.go:295) but the event
  doesn't carry it.
Suggestion stands: put `TimeMs` on every event and consider an
`EventTimeMs() int32` companion on the `Event` interface so consumers stop
re-deriving it. Schema-touching. Impact: correctness-adjacent consistency +
extensibility.

**A6. Parser state never resets on a second `svc_serverdata`.** Re-verified
after P5 — the stale-state inventory *grew* with #97. `parseServerData`
(serverdata.go:17-105) replaces `serverData` / `floatCoords` /
`fteExtensions` but leaves `modelList`, `soundList`, `baselines`,
`currentEntities`, `spawnedItems`, `spawnedMovers`, `spawnedProjectiles`,
`playerPositions`, `playerAngles`, `playerDead` / `playerDeadKnown` /
`playerSeenInfo`, `matchStarted`, `players`, and `playerStats` from the
previous map (field inventory: parser.go:120-171). Irrelevant for
single-match MVD files, but the package doc still sells the event schema as
QTV-stream-ready (events/events.go:1-15), and a live QTV stream crosses map
changes. The item-tracking half is now at least *documented* as single-map
(ItemStateEvent doc, entities.go:83-92, added with the F9 resolution), but
the reset itself remains unimplemented. Either reset the per-map state in
`parseServerData` or scope the QTV claim where it is made. Impact:
extensibility.

---

## Low priority / nits (open; each re-verified on current HEAD)

- `decoder.go:140-168`: the `DemSingle, DemStats` and `DemAll, DemRead`
  cases are still byte-identical — merge into one case list. The "unknown
  message type" error (:176) still omits the type byte and stream offset.
- `decoder.go:101`: `Time: float64(d.timeMs) * 0.001` still duplicates
  `CurrentTime()` (:43-45); call the method.
- `registerBaseline` (entities.go:397): local variable named `copy` still
  shadows the builtin.
- `ktx_pickup_print.go:178`: the `!strings.HasPrefix(trimmed, ktxGotThePrefix)`
  guard is still unreachable ("You get " and "You got the " already differ at
  byte 5) and the check still inconsistently uses `msg` where the others use
  `trimmed`.
- `BufferReader.ReadBytes` still returns an aliased sub-slice while
  `BinaryReader.ReadBytes` returns a fresh copy (mvd/reader.go:202-209 vs
  :101-109). P0 documented the aliasing at the one retaining call site
  (parser.go:676-678), but the method pair itself still carries no warning —
  document it where the two implementations live.
- `position.go:111-113` still drops position events for an exact-(0,0,0)
  origin with only "likely uninitialized" as justification — under the
  project's "surface, don't filter" policy the comment should cite the
  reasoning (players are never at the exact world origin on real maps).
- `Parser.Players()` / `Parser.PlayerStats()` (parser.go:195-203): now
  **zero** callers anywhere in the workspace, tests included (the untracked
  debug tools that used them are gone). Delete outright, or fold into A4's
  snapshot work.
- `decodeULEB128` (parser.go:737-749) still silently accepts a truncated
  varint (all-continuation bytes); fine given the garbage some demos carry in
  this block, but a one-line comment would help.
- Residual naming trap (the deleted Kind-constant nit, one layer down): the
  parser-side constant `EventPlayerInfo` (parser.go:33) names
  `PlayerPositionEvent` (position.go:27) — align if the constants are ever
  touched.

---

## Sound/tempentity/nails/projectile decoders (deferred review, 2026-07-06)

The #97 decoders (`parser/sound.go`, `parser/tempentity.go`,
`parser/nails.go`, and the projectile diffing in `parser/entities.go`) were
pattern-flagged in the original review but never deep-reviewed. Wire layouts
now verified against the vendored ground truth:

- **svc_sound matches** ezquake `CL_ParseStartSoundPacket`
  (cl_parse.c:1948-1990) and the mvdsv writer `SV_StartSound`
  (sv_send.c:597-668) field-for-field: volume byte before attenuation byte,
  attenuation encoded ×64, `(ent<<3)|channel` packing with `ent = (w>>3) &
  1023` / `channel &= 7`, byte-sized sound_num (mvdsv `MAX_SOUNDS` is 256 —
  bothdefs.h:56 — so no FTE short-form soundlist can appear in an MVD),
  float-coord gating on the negotiated extension.
- **The TE payload table matches** ezquake `CL_ParseTEnt`
  (cl_tent.c:625-729) for all fourteen QW types: 0,1,3,4,7,8,10,11,13 =
  3 coords; 2 (TE_GUNSHOT, cl_tent.c:532-541) and 12 (TE_BLOOD) = QW count
  byte + 3 coords; 5/6/9 = short entity + 6 coords (`CL_ParseBeam`,
  cl_tent.c:151-166). Type ids match ktx/include/g_consts.h:215-228, and a
  sweep of every `WriteByte(…, TE_…)` in ktx/src confirms KTX writes nothing
  outside 0–13.
- **BeamEvent's KTX claims hold**: `W_FireLightning` writes exactly one
  TE_LIGHTNING2 per fire tick in the same function that increments
  `wpn[wpLG].attacks` (ktx/src/weapons.c:1233, 1261-1270), and discharge /
  out-of-cells paths return before the write — so "beam count ==
  acc.attacks" is real.
- **The nails unpack matches** ezquake `CL_ParseProjectiles`
  (cl_ents.c:1197-1240) and the mvdsv encoder `SV_EmitNailUpdate`
  (sv_ents.c:61-108) bit-for-bit (12-bit x/y/z, `<<1 − 4096`). The
  svc_nails2 id byte is the entity's colormap, assigned once per nail and
  wrapping 1..255 (never 0) — the "stable while the nail lives" doc claim is
  true.
- **Demo completeness**: `SV_MulticastEx` copies every non-reliable
  multicast into `demo.datagram` unconditionally, *after* the per-client PHS
  filtering loop (mvdsv sv_send.c:505-525) — sounds and beams are never
  PHS-dropped from an MVD, so SoundEvent / BeamEvent are complete records of
  what the server emitted.
- **Event contracts**: all five event types are value snapshots (no
  parser-owned pointers; `NailsFrameEvent.Nails` is freshly allocated per
  frame), all carry `TimeMs` plus the derived float `Time`, and every
  read/Skip error propagates — the new code stays on the right side of
  A4/A5 and the old F5. The tests (`sound_test.go`, `tempentity_test.go`,
  `nails_test.go`, `entities_projectile_test.go`) exercise real wire
  encodings, matching the module's style.
- **Docs**: MVD_FORMAT.md covers all of it (svc_soundlist / svc_sound
  sections with the weapon-sound table that calls out the sgun1/rocket1i
  naming mismatch, svc_temp_entity, projectile tracking, svc_nails), and the
  README event table lists all five events. No structural doc drift.

What the deeper pass did find:

**F18. BeamEvent decodes the beam entity as unsigned.** `parseTempEntity`
reads the beam entity with `r.ReadUint16()` and surfaces `Ent: int(entRaw)`
(tempentity.go:63, :78). ezquake's `CL_ParseBeam` reads a **signed** short
(cl_tent.c:158) and assigns protocol meaning to negative values —
TE_LIGHTNING1 with ent in −512..−1 is the rail-trail extension
(cl_tent.c:175-183). On such a stream the event reports Ent 65024–65535,
`Ent-1` slot arithmetic silently lands out of range, and a consumer cannot
recover the intended value. KTX always writes a real edict (`WriteEntity`),
so the current corpus is unaffected — but the faithful decode is
`int(int16(entRaw))`, with a test. *(correctness, low likelihood)*

**F19. The `unknown_te` warning conflates three failure modes.**
`parseTempEntity` returns bare `io.EOF` both for "TE type not in the table"
(tempentity.go:90-91) and for a genuinely truncated read inside a *known*
type (any short/coord read), and the single dispatch arm labels both — plus
any emit-handler error — `unknown_te` (parser.go:342-346). A truncated
TE_LIGHTNING therefore warns "unknown_te: temp entity type 6: EOF …". This
is exactly the conflation F10 fixed for `skipCommand` with `errUnknownSvc`
(parser.go:11-16); mirror it with an `errUnknownTE` sentinel and report
truncation of a known type as `parse_error` naming the TE type.
*(diagnostics)*

**F20. Emit-handler errors are swallowed by the warn-and-continue arms.**
The new decoders correctly return `p.emit(...)` errors, but the sound / TE /
nails dispatch arms (parser.go:336-352) — like the older sub-parser arms
they were modelled on (print, stats, playerinfo, packetentities, …) —
convert *any* returned error into a diagnostic warning and `return nil`. A
handler error therefore neither aborts parsing (the documented `emit`
contract, parser.go:205-213) nor reaches the caller, and is mislabelled as a
wire problem. Latent: the only production handler never errors
(source/mvd/source.go:72-77). A proper fix separates wire errors from
handler errors across all sub-parser arms (wrap or sentinel), which is the
same altitude as A4's contract work — batch them together. *(API contract,
latent)*

**F21. Doc nits, sound-adjacent.**
- sound.go:18-19 and serverdata.go:158 both pick `weapons/rocket1i.wav` as
  their example weapon-fire sound without the caveat that it is the
  *nailgun* sound (ktx/src/weapons.c:52, :1707) — a reader will assume
  rocket. MVD_FORMAT.md's table gets this right; either switch the example
  to `sgun1.wav` (rl, weapons.c:1044) or name the weapon.
- The MVD_FORMAT.md svc_sound layout table's offset column contradicts
  itself: channel occupies offsets 1–2, but `sound_num` is listed at offset
  1 and origin at 6/12 (MVD_FORMAT.md:597-605); after the optional
  volume/attenuation bytes every later offset is variable — mark them `var`
  as other tables do.
- sound.go discards volume/attenuation (:60-69, "not retained") — a
  deliberate, documented choice, but note them as candidate fields if a
  consumer ever needs to separate local vs. attenuated sounds. *(docs)*

**F22. The last parse/skip twin, and a warn label.**
- `parseNails`' `!p.decodeNails` branch (nails.go:49-55) restates the
  per-nail layout as `count × 6/7` skip arithmetic beside the decode loop
  (:57-84) that expresses the same layout — the one skip twin P5 left
  standing, albeit inside a single function where drift would be visible.
  Either decode-and-discard through the same loop or add a one-line comment
  tying the two.
- The nails dispatch arm warns `"svc_nails: %v"` for both svc_nails and
  svc_nails2 (parser.go:348-351) — use `SvcName(cmd)`. *(bloat /
  diagnostics, nit)*

---

## Suggested sequencing

Steps 1–4 of the original sequencing (mechanical fixes, dead-code deletion,
correctness batch, consolidation) shipped as phases 0–5 — see the ledger
below. What remains:

1. **Schema-touching batch (design first)** — the original step 5, now the
   Phase 11 task in PLAN-implementation-order.md: A4 (event immutability),
   A5 (TimeMs everywhere), A6 (multi-map reset), designed together, with the
   analytics consumers audited in the same PR and schema/docs bumped per
   CLAUDE.md. F20 (handler-error separation) belongs in this batch — it
   touches the same contract text as A4.
2. **Small mechanical PR — scheduled as Phase 5.1** (branch `phase-5.1`
   off `review`): F18 (with a signed-ent test), F19, F21, F22, and the
   surviving nits above, plus the `mvd-api/serve.go` gofmt fix riding
   along as a no-logic commit. Golden corpus is unaffected by all of
   them except F18 on exotic non-KTX streams.

---

## Resolved (implementation phases 0–5)

F17 and F9 were closed by *documenting the mvdsv guarantee* rather than
changing behaviour — see their rows.

| ID | What | Phase / commit |
|---|---|---|
| F7 | Hand-rolled `lowercaseASCII`/`containsASCII` replaced with `strings.ToLower`/`Contains` (print.go:122-128) | P0 ff47a76+7d1a8e2 |
| F13 | Byte-by-byte hidden-JSON read replaced with `ReadBytes`, aliasing documented at the call site (parser.go:676-687) | P0 7d1a8e2 |
| F14 | `gofmt` clean across mvd-reader (kept clean since) | P0 ff47a76 |
| F15 | Doc drift fixed: stale `qwdemo` module name, README three-source death derivation, diagnostic warning categories, `classifyArmor` ghost, source queue "ring" comment | P0 7d1a8e2 |
| F16 | Gzip detection reduced to the shared `peekGzip` magic-byte check used by both `Open` and `NewReader` (mvdfile/file.go:52-59) | P0 7d1a8e2 |
| nits | `parseMessage` payload check simplified (parser.go:245); the `morebits` cast and Kind-naming nits were mooted by the P4/P5 deletions | P0 7d1a8e2 (rest P4/P5) |
| F1 | Bulk userinfo now parses `*spectator` (with documented mvdsv ground truth and per-update spectator reset; regression-tested) — userinfo.go:124-164 | P1 3ebc9cd |
| F2/A3 | Clean EOF contract: `svc_disconnect "EndOfDemo"` → `io.EOF` (parser.go:354-372); `Source.Next` drains queued events before surfacing a stashed error (source/mvd/source.go:85-117); Layer-2 registry records stream errors | P2 cda9940 |
| F5 | Remaining live Skip/read error drops now propagate; dead sites deleted with F4 | P2 cda9940 |
| F10 | `errUnknownSvc` sentinel separates "unknown svc" from truncation inside a known command's skip (parser.go:11-16, :466-476) | P2 cda9940 |
| F11 | Entity-diff emit errors propagate: `diffEntityTransitions` and all three diff siblings return error (entities.go:749-781) | P2 cda9940 |
| F12 | `parseServerData` propagates the first movevar read error instead of zeroing physics (serverdata.go:86-99) | P2 cda9940 |
| F17 | Resolved as documented: mvdsv never transports frags via STAT_FRAGS (neither `MVD_WriteStats` nor `SV_UpdateClientStats` assigns it; index left commented out at bothdefs.h:66) — guarantee cited at stats.go:228-244; `svc_updatefrags` remains the sole `FragUpdateEvent` source | P2 cda9940 (doc) |
| F9 | Resolved as documented: items are never re-baselined mid-game in a single-map MVD (baselines only in the initial gamestate flush, mvdsv sv_demo.c:1418-1453; zero re-baselines across the golden corpus) — ItemStateEvent doc now states re-baselines reseed silently and points multi-map sources at the mover branch (entities.go:83-92) | P2 cda9940 (doc) |
| F4 | All unreachable skip branches and their helper functions deleted; `skipCommand` now handles only commands the main switch does not decode (parser.go:751-844) | P4 f494471 |
| F6 | Dead exported types (`mvd.Demo`, `PrintMessage`, `FragEvent`, `PlayerState`, stale `DamageEvent`), the unused `events` aliases, and the whole Kind-constants block deleted; README list fixed | P4 f494471 |
| A2/F3 | One reader per wire layout: `readDeltaBits` + `readEntityDelta` + `applyDeltaFields` + `readBaselineBody` shared by parse and decode-and-discard paths (entities.go:299-360, :544-734); FTE evenmorebits/trans/colourmod now gated on the negotiated extension per mvdsv sv_ents.c and ezquake cl_ents.c — all three F3 divergences impossible by construction | P5 1300742 |
| F8 | KTX stuffcmd hints parse through one `parseKtxHintInts` helper behind a single `//ktx ` prefix gate (ktx_pickup.go:59-110) | P5 1300742 |
| A7 | `MatchStartPatterns` is canonical in Layer 1 (print.go:98-114), re-exported via `events` (events.go:91-95), imported by `mvd-analytics/analyzer/matchtiming.go:32-36`; mirror comment gone | P5 1300742 |

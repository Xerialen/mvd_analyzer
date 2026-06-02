# messages analyser

**Phase:** Derived
**Inputs:** `PrintEvent` (only)
**Reads from CoreOutputs:** `co.Names` (post-Finalize team backfill)
**Writes to Result:** `result.Messages` (`*MessagesResult`)

## What it does

Captures chat messages and obituaries as a unified `[]MatchEvent`
ordered timeline for the frontend's chat/kill panels. This is the
human-readable transcript layer — it preserves the original print
text in `MatchEvent.Message` so the UI can render the exact server
output, and (since v6) ships a plain-text twin in `MatchEvent.MessageClean`
for consumers that don't want to deal with ezQuake markup.

`MessageClean` is filled via `events.StripChatMarkup` (defined in
`mvd-reader/parser/userinfo.go`), which strips `&cRGB` color codes,
`&r` resets, `!K`/`!H`/`!G`/`!C` sound triggers, `{` `}` `[` `]`
macro delimiters, and a leading `\r`. It is elided via `omitempty`
when the cleaned text equals the raw `Message` (frag descriptions are
already plain), so consumers should treat a missing `messageClean` as
"use `message`".

## How it works

1. Chat is parsed by `parseChatMessage`: `(name): message`,
   `(name) message`, and `name: message` shapes are recognised. Server
   announcements are filtered (the join/leave/ready prefix list).
   Identical chat/teamsay copies are then deduped by `seenChat` on
   `(time, type, player, message)` — see the dedup note below.
2. Obituaries are parsed by `parseObituarySimple` — a separate parser
   from frag.go that produces `MatchEvent`s carrying the raw print
   text. Pattern coverage mirrors `frag.go` for consistency, including
   the infix Satan's-power-deflect self-telefrag (`satanDeflectVictim`,
   shared with frag.go) so every death has a matching frag event.
3. Live team lookup during OnEvent uses `findPlayerByName` (3-pass
   exact → normalized → substring match against `ctx.Players`).
4. Finalize backfills missing teams using `co.Names.TeamForName(name)`
   for events whose live lookup returned empty. Handles the
   auth-override case where userinfo `Name` differs from the displayed
   netname.

## Chat dedup (per-recipient `svc_print`)

KTX handles `say`/`say_team` in QC (`ClientSay`, `ktx/src/g_cmd.c`) and
sprints the line to each eligible recipient individually. Every
`G_sprint` becomes a `dem_single` `svc_print` in the MVD
(`SV_ClientPrintf`, `mvdsv/src/sv_send.c`), so the parser faithfully
emits one `PrintEvent` per recipient. A single public `say` line would
otherwise appear N times — public `say` reaches every client and so
duplicates the most, `say_team` only teammates. `seenChat` collapses
these to one event keyed on `(time, type, player, message)`. All copies
share an identical wire-ms, so the exact-match key is safe: a human
cannot send the same line twice in the same millisecond, and a same-text
line at a *different* time is preserved. This is the CLAUDE.md "filter
only when a consumer cannot itself disambiguate" exception.

Edge case: KTX sends the colored text to colour-capable clients and a
markup-stripped copy to the rest (`g_cmd.c:558`), so a *mixed* lobby can
leave one colored + one stripped survivor. This is rare on modern
ezquake (everyone is colour-capable → byte-identical copies → collapses
to one) and never drops a real message, so we accept it rather than key
on stripped text and risk losing the colored variant.

The obituary/frag path is **not** deduped: obituaries arrive as a single
broadcast copy and must pass through verbatim.

## Limitations / known issues

- Obituary parsing is duplicated with frag.go (see PR audit §2b).
  Each parser produces a different downstream shape (`FragEntry`
  vs `MatchEvent` with `Message`); merging them would either drop the
  print text from `FragEntry` or require teaching frag to retain it.
  Deferred.
- Generic player names ("teammate") are skipped in chat detection but
  emitted in obituary events with `Player: "teammate"` so the chat
  panel can render the friendly-fire line verbatim.
- Substring fuzzy matching in `findPlayerByName` can misroute a chat
  line if one connected player's name is a prefix of another's.

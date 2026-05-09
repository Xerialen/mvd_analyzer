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
`qwdemo/parser/userinfo.go`), which strips `&cRGB` color codes,
`&r` resets, `!K`/`!H`/`!G`/`!C` sound triggers, `{` `}` `[` `]`
macro delimiters, and a leading `\r`. It is elided via `omitempty`
when the cleaned text equals the raw `Message` (frag descriptions are
already plain), so consumers should treat a missing `messageClean` as
"use `message`".

## How it works

1. Chat is parsed by `parseChatMessage`: `(name): message`,
   `(name) message`, and `name: message` shapes are recognised. Server
   announcements are filtered (the join/leave/ready prefix list).
2. Obituaries are parsed by `parseObituarySimple` — a separate parser
   from frag.go that produces `MatchEvent`s carrying the raw print
   text. Pattern coverage mirrors `frag.go` for consistency.
3. Live team lookup during OnEvent uses `findPlayerByName` (3-pass
   exact → normalized → substring match against `ctx.Players`).
4. Finalize backfills missing teams using `co.Names.TeamForName(name)`
   for events whose live lookup returned empty. Handles the
   auth-override case where userinfo `Name` differs from the displayed
   netname.

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

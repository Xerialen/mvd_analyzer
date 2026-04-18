# qwdemo

Layer 1 of the mvd-analyzer workspace: turn QuakeWorld demo data — today an
MVD file, tomorrow a QTV stream — into a canonical event stream that
analytics can consume without caring about the on-the-wire format.

## What's in the box

- `events/` — the **public API**. Defines the `Source` iterator interface,
  every concrete `Event` type, and the source-agnostic domain types carried
  on those events (`ServerData`, `PlayerInfo`, `PlayerState`, `Stats`,
  `Vec3`, `Angle3`). Import this package and nothing else if you're writing
  downstream analytics.
- `mvd/` — MVD wire-format decoder: message headers, svc_* command opcodes,
  FTE and MVD protocol extensions, hidden-message framing.
- `parser/` — message → event translation. Takes `mvd.DemoMessage`s and
  emits the concrete event types from `events/`.
- `mvdfile/` — gzip-aware file reader. Detects `.mvd.gz` by magic bytes and
  wraps the stream.
- `source/mvd/` — the reference **Source implementation** backed by an MVD
  file or in-memory byte stream. Exposes `Open(path)` and
  `NewFromReader(io.Reader)`; both return a value that satisfies
  `events.Source`.

## Using qwdemo

```go
import (
    "io"

    "github.com/mvd-analyzer/qwdemo/events"
    mvdsource "github.com/mvd-analyzer/qwdemo/source/mvd"
)

src, err := mvdsource.Open("demo.mvd.gz")
if err != nil { panic(err) }
defer src.Close()

for {
    ev, err := src.Next()
    if err == io.EOF { break }
    if err != nil { panic(err) }

    switch e := ev.(type) {
    case *events.ServerDataEvent:
        // ...
    case *events.FragUpdateEvent:
        // ...
    }
}
```

The concrete event list, in stable order:

| Kind | Type | Purpose |
|---|---|---|
| `KindServerData` | `ServerDataEvent` | Connection-time server data block |
| `KindUserInfo` | `UserInfoEvent` | Player slot userinfo bind / rebind |
| `KindPrint` | `PrintEvent` | Text messages (chat, obituaries, system) |
| `KindStatUpdate` | `StatUpdateEvent` | Per-player stat delta (health, armor, weapons, ...) |
| `KindFragUpdate` | `FragUpdateEvent` | Frag count changes (server-authoritative) |
| `KindPlayerInfo` | `PlayerPositionEvent` | Per-player position / angle sample |
| `KindDamage` | `DamageEvent` | Damage dealt (from KTX hidden messages) |
| `KindDemoInfo` | `DemoInfoEvent` | KTX `*demoinfo` JSON dump |
| `KindIntermission` | `IntermissionEvent` | Scoreboard-camera takeover (match ended) |
| `KindStuffText` | `StuffTextEvent` | Server-pushed console command |
| `KindCenterPrint` | `CenterPrintEvent` | HUD center text (match settings countdown) |
| `KindServerInfo` | `ServerInfoEvent` | Mid-game serverinfo key/value update |
| `KindDeath` | `DeathEvent` | Player died — `StatHealth` crossed from >0 to ≤0 |
| `KindSpawn` | `SpawnEvent` | Player spawned — `StatHealth` crossed from ≤0 to >0 |

`DeathEvent` and `SpawnEvent` are derived events synthesised by the
parser from protocol-level `StatHealth` transitions. They fire at the
exact event time, so analytics don't have to reconstruct death/spawn
by comparing health samples across the sampling boundary (including
the instant-respawn case where a gib and respawn land in the same
50 ms window). See `parser/stats.go` for the emission logic;
consumers that want killer / weapon attribution still go to the
analyzer-layer obituary parser (that's KTX-mod-specific text, not a
protocol signal).

## Writing a new Source

To add a new input format (QTV live, a JSON event replay, something else),
implement `events.Source`:

```go
type Source interface {
    Next() (Event, error)
    Close() error
}
```

Emit the concrete event types from `qwdemo/events`. The same analytics
pipeline that runs over an MVD file will now run over your new source with
no changes.

See `source/mvd/source.go` for a worked example: it registers a handler on
the parser that appends events to an internal queue, then `Next()` drains
the queue and pumps `parser.ParseOne()` when the queue runs dry.

## Pure parser access (no Source wrapper)

For tools that need to drive the parser directly — the diagnostic harness
flips it into warning-collection mode — `qwdemo/parser` exposes `Parser`,
`NewParser(decoder)`, `OnEvent(handler)`, `Parse()`, `ParseOne()`, and
`SetDiagnosticMode(true)`.

## Running tests

```bash
go test ./qwdemo/...
```

## Module boundary

qwdemo has no dependency on qwanalytics or qw-web. It depends only on the
Go standard library. This is intentional: the event schema has to stay
stable across consumer changes, and independent test/release cadence is
the forcing function that keeps that invariant true.

## Reference

- [MVD_FORMAT.md](MVD_FORMAT.md) — the MVD binary format specification
  with ezQuake source references. The authority for anything the wire
  decoder in `mvd/` does.

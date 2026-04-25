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
| `KindItemSpawn` | `ItemSpawnEvent` | Item entity observed — baseline known (kind, position) |
| `KindItemState` | `ItemStateEvent` | Item became taken or respawned — from entity modelindex transitions |
| `KindBackpackDropHint` | `BackpackDropHintEvent` | KTX `//ktx drop` stuffcmd: `(BackpackEnt, ItemFlags, PlayerEnt)` for RL/LG drops only |
| `KindItemPickupHint` | `ItemPickupHintEvent` | KTX `//ktx took` stuffcmd: `(ItemEnt, RespawnSec, PlayerEnt)` — authoritative pickup attribution for every MH / armor / weapon / powerup touch |
| `KindBackpackPickupHint` | `BackpackPickupHintEvent` | KTX `//ktx bp` stuffcmd: `(BackpackEnt, PlayerEnt)` — symmetric to `//ktx drop`, fires only for RL/LG packs |
| `KindItemPickupPrint` | `ItemPickupPrintEvent` | Per-client `svc_print` "You got the X" / "You receive N health" — covers ammo boxes and H15/H25 that `//ktx took` misses. **Subject to per-client `msg` cvar filter; frequently absent in competitive demos.** |
| `KindBackpackPickupPrint` | `BackpackPickupPrintEvent` | Per-client `svc_print` "You get " backpack opener — covers all backpack classes, including the SSG/NG/GL packs that `//ktx bp` skips. Same server-side-filter caveat as `ItemPickupPrintEvent`. |

`DeathEvent` and `SpawnEvent` are derived events synthesised by the
parser from protocol-level `StatHealth` transitions. They fire at the
exact event time, so analytics don't have to reconstruct death/spawn
by comparing health samples across the sampling boundary (including
the instant-respawn case where a gib and respawn land in the same
50 ms window). See `parser/stats.go` for the emission logic;
consumers that want killer / weapon attribution still go to the
analyzer-layer obituary parser (that's KTX-mod-specific text, not a
protocol signal).

`ItemSpawnEvent` and `ItemStateEvent` are derived events synthesised
from the entity-state stream (`svc_spawnbaseline`,
`svc_packetentities`, `svc_deltapacketentities` — see
`parser/entities.go`). `ItemSpawnEvent` fires once per item entity
when the demo first makes it observable, carrying the classified kind
(`ra`, `mh`, `rl`, ...) and world origin. `ItemStateEvent` fires on
every visibility transition: `Taken=true` when the entity's
modelindex drops to 0 (server set `self->model = ""` on pickup),
`Taken=false` when it reappears (`SUB_regen` restored the model).
Classification uses standard Quake 1 item model paths (armor.mdl +
skin for GA/YA/RA; maps/b_bh*.bsp for health; progs/g_*.mdl for
weapons; progs/{quaddama,invulner,invisibl}.mdl for powerups) —
protocol-level, not KTX-specific.

`ItemPickupHintEvent` and `BackpackPickupHintEvent` are the
authoritative KTX counterparts to `ItemStateEvent`: they pin each
pickup to a concrete player edict, replacing the nearest-origin
heuristic that `ItemStateEvent` alone requires for attribution.
`//ktx took` (`ktx/src/items.c:355, 541, 1048, 2074, 2083`) fires on
every competitive item touch; `//ktx bp`
(`ktx/src/items.c:2471`) fires on every RL/LG backpack pickup —
symmetric to the existing `//ktx drop` hint. Both are
**KTX-specific**: a non-KTX server (ktpro, CustomTF, or vanilla)
will not emit them, in which case consumers fall back to
`ItemStateEvent` + heuristics or to per-player stats deltas.

`ItemPickupPrintEvent` and `BackpackPickupPrintEvent` complement
the hints by parsing KTX's per-client pickup prints
(`"You got the Red Armor"`, `"You receive 25 health"`,
`"You get "` backpack opener). They cover categories `//ktx took`
misses — ammo boxes (`ammo_touch` has no hint call), H15/H25, and
all backpack classes including SSG/NG/GL packs. `PrintEvent.TargetPlayerNum`
carries the `dem_single` slot the server addressed. **Caveat:** mvdsv's
`SV_ClientPrintf` (`mvdsv/src/sv_send.c:225`) drops prints where
`level < cl->messagelevel` before recording, so players with `msg 1`
or higher contribute *no* pickup prints to the MVD. Competitive
demos where everyone sets `msg 2` will have zero print-based pickup
events; always inspect the Level=0 count on a given demo before
leaning on this signal.

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

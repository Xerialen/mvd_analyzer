# Result JSON schema reference

This is the field-level reference for the JSON shape produced by
`mvd-analytics`. The Go source of truth lives in `mvd-analytics/result/`;
this document mirrors that shape so consumers (web UI, CLIs, AI
agents, future MCP servers) can navigate it without reading Go.

For tutorial-grade narrative on Items, Backpacks, and WeaponPickups
— including signal-attribution mechanics — see
[`README.md`](README.md). Pipeline architecture and how to add an
analyzer are also covered there.

## Top-level shape

`result.Result` (defined in `result/result.go`):

| Field | JSON key | Type | Intent |
|---|---|---|---|
| SchemaVersion | `schemaVersion` | int | Identifies JSON schema shape; bump on every breaking change. Currently **10**. |
| FilePath | `filePath` | string | Source path / display label of the analyzed demo. |
| Match | `match` | *MatchResult | Match summary: map, game dir, duration, players, teams. |
| Frags | `frags` | *FragResult | Total / per-player / per-weapon frag breakdown plus chronological frag list. |
| Messages | `messages` | *MessagesResult | Frag and chat events for timeline display. |
| DemoInfo | `demoInfo` | *DemoInfoResult | Verbatim KTX STUFFCMD demoinfo JSON; authoritative weapon / damage / pickup stats. **Untransformed by design.** |
| TimelineAnalysis | `timelineAnalysis` | *TimelineAnalysisResult | High-res state buckets, key-moment events, region control. |
| Metadata | `metadata` | *MetadataResult | Server cvars (fullserverinfo) + parsed match-settings centerprint. |
| LocGraph | `locGraph` | *LocGraphResult | Loc-to-loc movement graph (nodes + transitions). |
| Items | `items` | *ItemsResult | Per-entity pickup / respawn timeline. |
| Backpacks | `backpacks` | []BackpackDrop | RL/LG backpack drops from KTX `//ktx drop` hint. |
| WeaponPickups | `weaponPickups` | []WeaponPickup | Slot-weapon acquisitions with kills-before-next-death effectiveness. |
| Errors | `errors` | []string | Non-fatal parse / analysis errors (omitted when empty). |

All sub-result fields are pointers and use `omitempty`, so a missing
key means "the analyzer didn't produce this section for this demo"
(usually because the source lacked the necessary signals — e.g. no
KTX hints means no Items / Backpacks).

## MatchResult (`match`)

Defined in `result/match.go`.

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Map | `map` | string | Map basename (e.g., `dm2`, `schloss`). |
| GameDir | `gameDir` | string | Game directory (`qw`, `fortress`, custom). |
| Duration | `duration` | float64 | Match length in seconds (parser-derived). Read this for "how long was the match". |
| StartTime | `startTime` | float64 | Match-relative start (always 0 after the time-normalisation post-process). |
| EndTime | `endTime` | float64 | Match-relative end (equal to Duration in match-relative coords). |
| Players | `players` | []PlayerStat | Lightweight scoreboard view. |
| Teams | `teams` | []TeamStat | Team standings (omitted in FFA). |

### PlayerStat

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | Display name. |
| Team | `team` | string | Team name. |
| Frags | `frags` | int | Canonical QW frag count (`kills − teamkills − suicides`). |

`MatchResult` is the non-KTX-fallback view: it works on any MVD source.
For richer per-player stats (kills/deaths/accuracy/damage) read
`Frags.ByPlayer` (parser-derived) or `DemoInfo.Players[].Stats`
(KTX-authoritative).

### TeamStat

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | Team name. |
| Frags | `frags` | int | Team total. |

## FragResult (`frags`)

Defined in `result/frag.go`.

| Field | JSON key | Type |
|---|---|---|
| TotalFrags | `totalFrags` | int |
| Frags | `frags` | []FragEntry |
| ByWeapon | `byWeapon` | map[string]int |
| ByPlayer | `byPlayer` | map[string]*PlayerFrags |

### FragEntry

| Field | JSON key | Type |
|---|---|---|
| Time | `time` | float64 |
| Killer | `killer` | string |
| Victim | `victim` | string |
| Weapon | `weapon` | string (`rl`, `lg`, `gl`, `ssg`, `sng`, `ng`, `sg`, `ax`) |
| IsSuicide | `isSuicide` | bool (omitempty) |
| IsTeamKill | `isTeamKill` | bool (omitempty) |

### PlayerFrags

| Field | JSON key | Type |
|---|---|---|
| Kills | `kills` | int |
| Deaths | `deaths` | int |
| ByWeapon | `byWeapon` | map[string]int |

## MessagesResult (`messages`)

Defined in `result/messages.go`.

| Field | JSON key | Type |
|---|---|---|
| Events | `events` | []MatchEvent |

### MatchEvent

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Time | `time` | float64 | Demo time. |
| Type | `type` | string | `"frag"`, `"chat"`, `"teamsay"`. |
| Player | `player` | string | Sender / killer. |
| Team | `team` | string | Sender's team. |
| Message | `message` | string | Q-normalised text **with** ezQuake markup intact (color codes `&cRGB`, sound triggers `!K`, macro delimiters `{}` `[]`). |
| MessageClean | `messageClean` | string (omitempty) | Same text with markup stripped (plain ASCII). Elided when identical to `message`. |
| Victim | `victim` | string (omitempty) | Frag-only. |
| Weapon | `weapon` | string (omitempty) | Frag-only. |

Frag entries here overlap with `FragResult.Frags[]` — same time / killer
/ victim / weapon, plus the obit text. Pick the one whose shape matches
your consumer's needs; see "Layered views" below.

## DemoInfoResult (`demoInfo`)

Defined in `result/demoinfo.go`. **Verbatim from KTX's STUFFCMD
demoinfo JSON; never transformed.** Treat this as authoritative for
accuracy, damage breakdown, item pickups, bot info.

Top-level fields (`version`, `date`, `map`, `hostname`, `ip`, `port`,
`mode`, `timelimit`, `fraglimit`, `duration`, `demo`, `teams`,
`players`, `rawJson`) plus per-player nested objects:

- `Stats` — `frags`, `deaths`, `tk`, `spawn-frags`, `kills`, `suicides`
- `Dmg` — `taken`, `given`, `team`, `self`, `team-weapons`, `enemy-weapons`, `taken-to-die`
- `Spree` — `max`, `quad`
- `Speed` — `max`, `avg`
- `Bot` — `skill`, `customised` (when player is a frogbot)
- `Weapons[k]` — per-weapon `Acc`, `Kills`, `Deaths`, `Pickups`, `Damage`
- `Items[k]` — `Took`, `Time`

For the full nested table, see `result/demoinfo.go` directly — every
field is documented inline.

## TimelineAnalysisResult (`timelineAnalysis`)

Defined in `result/timeline.go`.

At schema v7 the parse-time `HighResBuckets` and `HighResDuration`
fields are gone. Bucketed data is produced on demand by
`mvd-analytics/view.Buckets` (any window size, any reducer set; see
[Streams](#streams-streams) and [Query API](#query-api)). The wire
format here only carries the event-shaped derived results.

| Field | JSON key | Type |
|---|---|---|
| MatchStartTime | `matchStartTime` | float64 (always 0 after post-process) |
| DemoOffset | `demoOffset` | float64 (warmup seconds before match start) |
| FragEvents | `fragEvents` | []TimelineFragEvent |
| PowerupEvents | `powerupEvents` | []PowerupEvent |
| FragStreaks | `fragStreaks` | []FragStreakEvent |
| LocationData | `locationData` | []MapLocation (loc anchor points) |
| LocTable | `locTable` | []string (interned loc names; index 0 = ""). `Streams.Players[].Loc[].V` indexes into this. |
| PlayerUserIDs | `playerUserIDs` | map[string]int (name → Hub viewer UserID) |
| RegionControl | `regionControl` | *RegionControlResult |

The legacy `HighResBucket` / `HighResPlayerData` / `HighResTeamData`
shim shapes (and `view.ToLegacyHighResBuckets`) were removed once the
web frontend moved to the columnar layout. Bucketed data is now served
only as `view.BucketsView` (row) or `view.ColumnarBuckets` (column) —
see [Query API → Buckets](#buckets). Each player's per-bucket data is a
`map[string]any` keyed by the [field vocabulary](#field-vocabulary)
(row) or one dense array per field (column).

### TimelineFragEvent

`{ time, player, team, delta }`. Score-delta channel (`+1` enemy kill,
`-1` suicide / teamkill, `+2` for the rare gib double-frag KTX edge).
Reconstruct the killer ↔ victim relationship from `FragResult.Frags[]`
or `MessagesResult.Events[type=frag]` by matching `time`.

### PowerupEvent

`{ time, endTime, playerName, playerSlot, playerUserID, team,
powerupType, duration, frags }`. One record per powerup run. Carries
both `playerSlot` and `playerUserID` (TimelineFragEvent doesn't —
intentional: that channel is lean by design).

### FragStreakEvent

`{ time, endTime, playerName, playerUserID, team, frags, duration,
ewep }`. `ewep` = effective weapon = the weapon that scored the most
kills during the streak.

### MapLocation

`{ x, y, z, name }`. Used by `LocationData` (loc anchor points) and
`ControlRegion.Points` (rendering anchors).

### RegionControlResult (`regionControl`)

At schema v7 the parse-time `bucketStates` field is no longer baked
into the result. Stats remain (match-aggregate percentages). For
per-bucket region states at any resolution, call
`view.RegionControl(opts)` (Go) or `recomputeRegionControl(regionsJSON)`
(WASM bridge); both derive the bucket states on demand from
`result.Streams`.

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Regions | `regions` | []ControlRegion | Region definitions. |
| TeamA | `teamA` | string (omitempty) | Team name encoded as `A` in BucketStates. Picked alphabetically. |
| TeamB | `teamB` | string (omitempty) | Team name encoded as `B`. |
| BucketStates | `bucketStates` | map[string]string (omitempty) | Populated only by query-time results (`view.RegionControl` / `recomputeRegionControl`). Region name → string of length `n_buckets`, one ASCII char per bucket. |
| Stats | `stats` | map[string]RegionStats (omitempty) | Region name → match-aggregate share of each control state (percent, one decimal). |

`BucketStates` codes (one byte per bucket):

| Char | State |
|---|---|
| `_` | empty |
| `A` | teamAControl |
| `a` | teamAWeakControl |
| `C` | contested |
| `c` | weakContested |
| `B` | teamBControl |
| `b` | teamBWeakControl |

Control rule (faithful port of `mvd-web/static/app.js:classifyRegionState`):
"armed" = carrying RL or LG. Strong control = the dominant team has at
least one armed player; weak = present but unarmed; contested = both
present and armed. Dead players (`D=true` or `H<=0`) are skipped.

`view.RegionControl` (Go pure function in `view/region_control.go`)
is callable post-analysis with edited regions, custom team labels,
or a custom `teamOf` closure via `RegionControlOptions`. WASM
exports `recomputeRegionControl(regionsJSON)` for the web UI's
in-page region editing; the REST/MCP `/v1/demos/{id}/region-control`
endpoint exposes the same function with a `windowMs` query
parameter. The CLI's `-regions <path>` flag overrides the embedded
per-map regions at analysis time, before the result is cached.

### ControlRegion

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | |
| Locs | `locs` | []string | **Authoritative logical membership.** A player is "in" the region iff their resolved loc name is here. (added in v6) |
| Points | `points` | []MapLocation | Rendering anchors. Geometry only — the classifier ignores them. |
| CentroidX | `centroidX` | float32 | Label placement anchor. |
| CentroidY | `centroidY` | float32 | |

### RegionStats

```
RegionStats = {
  // Seven aggregate control-state percentages (0..100, one decimal,
  // sum to 100 within rounding).
  "teamAControl":     float,
  "teamAWeakControl": float,
  "contested":        float,
  "weakContested":    float,
  "empty":            float,
  "teamBWeakControl": float,
  "teamBControl":     float,
  // Per-player attribution. Map: player name → counts of buckets this
  // player was present in the region. Multiply by the bucket WindowMs
  // to convert to milliseconds of presence.
  "byPlayer": {
    "<player>": {
      "team":    "<team>",
      "armed":   <int>,  // buckets present carrying RL or LG
      "unarmed": <int>   // buckets present without RL/LG
    }, ...
  }
}
```

`byPlayer` answers "who was responsible for keeping <region>?" Sort
its entries by `armed + unarmed` for total presence, or by `armed`
alone for armed-presence share. Total per team in the region equals
the team-aggregate state count, so you can also compute "what
fraction of team A's presence in QUAD came from sailorman".

## Streams (`streams`)

Added in v7. Defined in `result/streams.go`. Streams is the canonical
event-rate storage for every per-player field. Each
`PlayerStream` records every change to a tracked field at the rate it
actually changed; aggregated views (50 ms / 1 s buckets, point-in-time
state, loc trails) are computed on demand from this storage by the
`mvd-analytics/view` package.

### Top-level shape

| Field | JSON key | Type |
|---|---|---|
| Players | `players` | []PlayerStream |
| Global | `global` | GlobalStream |

### GlobalStream

| Field | JSON key | Type | Notes |
|---|---|---|---|
| MatchStart | `matchStart` | int32 | Match window start in milliseconds (always 0 after post-process). |
| MatchEnd | `matchEnd` | int32 | Match window end in milliseconds. |

### PlayerStream

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | Canonical player name (D12: collisions in same match get a `#slotIndex` suffix). |
| Team | `team` | string (omitempty) | Team label (post-duel-normalise: per-player synthetic team). |
| Position | `pos` | *PositionTrack (omitempty) | Native-rate position track. Omitted from default JSON unless `-include positions` (CLI) or equivalent is set. |
| Health / Armor | `h` / `a` | []ChangeI16 | Vital change streams. Health caps at 250, Armor at 200; v7 uses int16 since v6's int8 was too narrow. |
| ArmorType | `at` | []ChangeStr | `"ga"` / `"ya"` / `"ra"` / `""` transitions. |
| Loc | `li` | []ChangeI16 | Index into `TimelineAnalysisResult.LocTable`. Smoothed by the same blip filter v6 used. |
| RL / LG / GL / SSG / SNG | `rl` / `lg` / `gl` / `ssg` / `sng` | []Interval | Half-open `[Start, End)` periods the weapon was held. |
| Quad / Pent / Ring | `q` / `pe` / `r` | []Interval | Same shape as weapons. |
| Shells / Nails / Rockets / Cells | `sh` / `nl` / `rk` / `cl` | []ChangeI16 | Ammo change streams. |
| Spawns / Deaths | `sp` / `d` | []int32 | Discrete event timestamps in milliseconds (schema v8). |

### ChangeI16 / ChangeStr / Interval

```
ChangeI16 = { "t": int32, "v": int16 }
ChangeStr = { "t": int32, "v": string }
Interval  = { "s": int32, "e": int32 }   // half-open [s, e)
```

`t` / `s` / `e` are **integer milliseconds** since the stream's time
origin (schema v8 — changed from `float64` seconds; see PositionTrack
for the unit rationale).

### PositionTrack

Columnar to compress JSON. Indices align across the four arrays.

```
PositionTrack = { "t": [int32...], "x": [int32...], "y": [int32...], "z": [int32...] }
```

`t` is **integer milliseconds** since the stream's time origin
(schema v8 — changed from `float32` seconds). The MVD wire format
delivers a 1-byte ms delta per message; storing the cumulative value
as `int32` keeps it exact across the persistence boundary. Consumers
reading the JSON as seconds must scale by `* 0.001`. Range is ±24.8
days, ample for matches that run minutes to hours; values can go
negative for pre-match warmup samples after time normalisation.

`x` / `y` / `z` are `int32` (not `int16`) because Quake maps can
exceed ±32 768 in any axis.

### Schema v8: all times are int32 milliseconds

Every timestamped field in this schema — `PositionTrack.T`,
`PlayerStream.Spawns/Deaths`, `ChangeI16.T` / `ChangeStr.T`,
`Interval.Start/End`, `GlobalStream.MatchStart/End`,
`MatchResult.Duration/StartTime/EndTime`,
`TimelineAnalysisResult.MatchStartTime/DemoOffset`,
`TimelineFragEvent.Time`, `PowerupEvent.Time/EndTime/Duration`,
`FragStreakEvent.Time/EndTime/Duration`, `MatchEvent.Time`,
`FragEntry.Time`, `BackpackDrop.Time`,
`WeaponPickup.Time/NextDeathTime/DropTime`,
`ItemPhase.AvailableFrom/TakenAt/RespawnAt` —
is stored as `int32` integer milliseconds. JSON keys are unchanged;
external consumers reading these as seconds must scale by `* 0.001`.
The view-layer query API (`view.Buckets`, `view.Events`,
`view.StreamSlice.StartTime/EndTime`, `view.StateAt.Time`) still
takes and returns `float64` seconds at its public surface, so any
consumer querying through `view.*` is unaffected.

#### Why integer ms

The MVD wire format carries time as a 1-byte millisecond delta per
message; the decoder accumulator (`mvd.Decoder.timeMs`) keeps this
integer end-to-end. Float seconds is a derived view, not a source of
truth. Integer storage:

- Eliminates float-precision drift across boundary comparisons. The
  motivating bug was a gib-respawn case where a spawn-boundary at
  wire-exact `658.279` compared against a position sample narrowed to
  `658.278992` produced a spurious `MH.low → start` teleport edge.
- Keeps comparison cost flat — `int32 <= int32` is exact.
- Removes float-noise artefacts (`5.499999999999972`) from JSON,
  making goldens stable and JSON human-readable.
- `int32` ms = ±24.8 days, comfortably more than any match.

#### Adding a new timestamped field

1. **Storage**: `int32` ms in the result schema. Same JSON-key shape
   as adjacent fields.
2. **Producer** (`mvd-analytics/analyzer/`): if the source event has
   a `TimeMs int32` field, use that directly. Otherwise convert at
   the write site via `msTime(e.Time)` (defined in
   [`analyzer/timeline_streams.go`](analyzer/timeline_streams.go);
   `int32(math.Round(t*1000))` — well-conditioned because the
   float64-seconds view derives once from the decoder's int32-ms
   accumulator).
3. **Postprocess** (`normalizeMatchRelativeTimes` in
   `analyzer/postprocess.go`): if the field shifts with match start,
   add it there. Everything works in int32 ms;
   `matchStartMs` comes from `res.TimelineAnalysis.MatchStartTime`
   directly.
4. **View layer** (`mvd-analytics/view/`): if the field is queryable
   via `view.Buckets` / `view.Events` / `view.StreamSlice` /
   `view.StateAt`, follow the existing pattern — accept window
   bounds in float64 seconds, convert to int32 ms once at entry, do
   comparisons in int32 ms, emit float64 seconds at the public
   output. Don't push ms through the view's public surface without a
   deliberate decision.
5. **Tests**: write fixtures with int32-ms literals (`Time: 5000`,
   not `Time: 5.0`).
6. **Frontend** (`mvd-web/static/app.js`): if the new field is read
   from the raw schema (not via the view layer), add a `* 0.001` at
   the read site. View-layer consumers (most panels) need no change.

### Append rules (the dedup invariant)

- **Change streams** (Health, Armor, ArmorType, Loc, ammo): every entry
  is a transition. `appendChange(t, v)` appends only if `v` differs
  from the previous entry's value. Consecutive identical samples are
  dropped.
- **Position**: every native sample is appended without dedup.
  Positions almost always differ; checking is overhead with no payoff.
- **Intervals** (weapons, powerups): one entry per period the field
  was true. Anchor opens on `false→true`, closes on `true→false` or at
  match end.
- **Spawn / Death timestamps**: discrete events, just appended.

### Identity / disambiguation (D12)

`PlayerStream.Name` is the canonical demoinfo-resolved name. If two
slots resolve to the same canonical name within one match (rare —
typical in pickup games where two players both pick "Player"), the
later slot's stream is suffixed `name#slotIndex`. Mid-match name
changes are folded into the same stream by the analyser's existing
canonicalisation.

## Query API

Provided by `mvd-analytics/view`. All functions are pure: no I/O, no
shared mutable state, no mutation of the input `*Result`.

### Field vocabulary

These codes are used identically in JSON wire keys, view-API
parameters, CLI `-fields` values, and (future) MCP tool inputs.

All default reducers use **first-sample-of-bucket** semantics: bucket
N's value represents player state at time `t = N × bucketDur`.
Bucket 0 is match-start state, consistent with the timeline-playback
mental model where each bucket is a snapshot at its own T. Override
per-call via `BucketsOptions.Reducers` if you want analytics-style
aggregation (`min`, `max`, `mean`, `dominant`, etc.).

| Code | Field | Stream form | Default reducer |
|------|-------|-------------|-----------------|
| `h` | Health | `[]ChangeI16` | `first` |
| `a` | Armor | `[]ChangeI16` | `first` |
| `at` | Armor type | `[]ChangeStr` | `first` |
| `li` | Loc index | `[]ChangeI16` | `first` |
| `pos` | Position xyz | `*PositionTrack` | `first` |
| `rl` | Rocket Launcher held | `[]Interval` | `first` |
| `lg` | Lightning Gun held | `[]Interval` | `first` |
| `gl` | Grenade Launcher held | `[]Interval` | `first` |
| `ssg` | Super Shotgun held | `[]Interval` | `first` |
| `sng` | Super Nailgun held | `[]Interval` | `first` |
| `q` | Quad | `[]Interval` | `first` |
| `pe` | Pentagram | `[]Interval` | `first` |
| `r` | Ring of Shadows | `[]Interval` | `first` |
| `sh` | Shells | `[]ChangeI16` | `first` |
| `nl` | Nails | `[]ChangeI16` | `first` |
| `rk` | Rockets | `[]ChangeI16` | `first` |
| `cl` | Cells | `[]ChangeI16` | `first` |
| `sp` | Spawn timestamps | `[]float64` | `any` |
| `d` | Death timestamps | `[]float64` | `any` |

`sp` / `d` stay on `any` because they need a bool ("did this event
happen during the bucket?"); `first` would return a timestamp.

### Reducer registry

| Name | Behavior | Applies to |
|------|----------|------------|
| `last` | Value at end of window (carry-forward if no change). | Numeric / categorical. |
| `first` | Value at start of window. | Numeric / categorical. |
| `mean` | Arithmetic mean over samples. | Numeric. |
| `min` / `max` | Extrema over samples. | Numeric. |
| `dominant` | Mode (most common value); ties broken by `last`. | Categorical. |
| `held-any` | OR over a bool stream — true if any sample is true. | Bool / interval. |
| `majority` | True if held ≥ 50 % of window samples. | Bool / interval. |
| `any` | True if at least one event is in the window. | Event lists (spawn/death). |

Override per call via `BucketsOptions.Reducers`:

```json
{ "windowMs": 1000, "reducers": { "h": "min", "rl": "majority" } }
```

Unknown reducer name → explicit error from `view.Buckets`. Unknown
field codes also error.

### View functions

#### Buckets

```go
view.Buckets(r, view.BucketsOptions{
    WindowMs: 1000,
    Fields:   []string{"h", "a", "rl"},
    Players:  []string{"bps", "griffin"},
    Reducers: map[string]string{"h": "mean"},
    IncludeTeam: true,
})
// → *BucketsView { WindowMs, Buckets: []ViewBucket }
```

Partial last bucket carries `Partial: true` when the window doesn't
divide evenly into `EndTime - StartTime`.

Loc rendering follows `BucketsOptions.LocIndex` (REST `?loc=`): by
default each bucket's player map carries a resolved `loc` name; in
index mode (`loc=index`) it carries the raw `li` integer instead, which
you decode against the demo's loc-table (`GET /loc-table`).

##### Columnar layout (`view.BucketsColumnar`, REST `?layout=column`)

The same per-bucket values in a column-major shape — for each
`(player, field)` one dense typed array instead of a map per bucket.
Far smaller and allocation-light for series/trend reads; use
`StateAt` for point-in-time snapshots rather than aligning indices
across arrays.

```go
view.BucketsColumnar(r, view.BucketsOptions{WindowMs: 50, IncludeTeam: true})
// → *ColumnarBuckets {
//     windowMs, startMs, count, partialLastMs?,
//     players: { name: {
//        first, n,                       // active span [first, first+n)
//        alive: [0/1 …],                 // liveness per bucket in the span
//        validFrom: { field: idx },      // sparse; field valid from idx (omitted when == first)
//        h|a|li|sh|nl|rk|cl: [int16 …],  // dense, carry-forward
//        x|y|z: [int32 …],               // position split
//        at: [string …],
//        rl|lg|gl|ssg|sng|q|pe|r|sp|d: [0/1 …],
//     } },
//     teams: { name: { rl|lg|rllg|w|gl|q|pe|r|pw|th|ta: [int …],
//                      abt: { ra|ya|ga: [int …] } } },
//   }
```

Conventions: `time(i) = startMs + i*windowMs` (int32 ms); booleans and
the `alive` mask are `0`/`1`; a field array is omitted when the player
never has it; values carry forward through dead buckets (the `alive`
mask, not the arrays, marks liveness — row-major omits dead players, so
treat `alive[i]==0` as "absent"); loc is always the raw `li` index
(`LocIndex` does not apply). Team arrays span the full `count` grid.

There is no per-life table: it would be a bucket-resolution approximation
that undercounts a death+respawn falling in one window. A same-window
death+respawn surfaces as that bucket carrying both `d=1` and `sp=1`
while `alive` stays `1`; for authoritative life counts/durations read the
per-player spawn/death event streams (`/events`, or the raw
`Streams.Players[].sp`/`.d`).

#### Events

```go
view.Events(r, view.EventsFilter{
    StartTime: 60.0, EndTime: 120.0,
    Types: []string{"frag", "powerup"},
})
// → *EventsView { Events: []TaggedEvent }
```

Default Types omits high-frequency change events (`health`, `armor`,
`loc`); pass them explicitly to opt back in. A `loc` event's `detail`
holds the resolved name (`{"loc":"RA"}`) by default, or the raw index
(`{"li":7}`) with `loc=index` — decode via `GET /loc-table`.

#### StreamSlice

```go
view.StreamSlice(r, view.StreamSliceOptions{
    StartTime: 432.0, EndTime: 442.0,
    Players:   []string{"bps"},
    Fields:    []string{"h", "a", "rl", "pe"},
})
// → *StreamSliceView { Players: []PlayerSlice }
```

Raw, unreduced change entries falling in `[StartTime, EndTime)`. For
each requested field, a synthetic carry-forward entry is prepended at
`StartTime` showing the value at window entry; intervals overlapping
the window are clamped.

The loc field is resolved to loc **names** by default (JSON key `loc`,
`[]ChangeStr`) so consumers never need the table. Pass `loc=index` to
get the raw `li` index stream (`[]ChangeI16`) instead — decode it via
`GET /loc-table`.

#### StateAt

```go
view.StateAt(r, view.StateAtOptions{
    Time:    432.5,
    Players: []string{"bps"},
    Fields:  []string{"h", "a", "rl", "pos"},
})
// → *StateAtView { Time, Players: map[string]PlayerStateAt }
```

Resolves each requested field at `Time`. Change streams use latest
entry with `T <= Time` (carry-forward). Intervals: `true` iff `Time` ∈
some interval. Position: nearest sample by `T`. The loc field comes
back as a resolved name by default (JSON key `loc`, string); pass
`loc=index` for the raw `li` index — decode via `GET /loc-table`.

#### LocTrails

Per-player loc residences with dwell durations. `MinDwellMs` folds
short blips into adjacent stable residences (defaults to 0 = no
filter; the analyser's pre-existing blip filter has already smoothed
the underlying loc stream). Each residence carries the loc **name**
(`loc`) by default, or the raw index (`li`) with `loc=index` — decode
via `GET /loc-table`.

##### Loc representation (shared)

Every loc-bearing view (Buckets, Events, StreamSlice, StateAt,
LocTrails) renders loc as a resolved **name** by default. Pass
`loc=index` (REST query param; `LocIndex: true` on the Go options) to
get the raw `LocTable` index instead — useful for index-based
computation (transition matrices, clustering). Fetch the decoder once
from `GET /v1/demos/{id}/loc-table` → `{ "locTable": [...] }` (index 0
is the `""` no-loc sentinel). RegionControl is unaffected — it reports
region names, not single loc indices.

#### RegionControl

Re-derives per-bucket region state strings + per-region per-player
attribution (`RegionStats.byPlayer`) at the requested `WindowMs`,
optionally clipped to a `[StartTime, EndTime)` sub-window. Options
(`RegionControlOptions`) optionally override the regions (caller-
edited region defs from the web UI), `TeamA`/`TeamB` labels, and
the `teamOf` lookup; defaults pull from
`TimelineAnalysisResult.RegionControl.Regions` (set at parse time)
and `r.Match.Players` (post-normalize team mapping). No `Players`
filter — region control is by team; filtering individuals would
skew the team tallies. To attribute control to specific players,
read the `byPlayer` field on each `RegionStats`.

The function's view-layer return type is aliased as
`RegionControlView = result.RegionControlResult` so the
`XxxView` naming is symmetric with the other five views;
the aliased type is the canonical one because the same shape is
baked into parse-time Result.

## MetadataResult (`metadata`)

Defined in `result/metadata.go`.

| Field | JSON key | Type | Notes |
|---|---|---|---|
| ServerInfo | `serverInfo` | map[string]string | Last-write-wins union of fullserverinfo stufftext + per-key svc_serverinfo updates. |
| MatchSettings | `matchSettings` | *MatchSettings | Parsed KTX countdown centerprint. |
| CountdownText | `countdownText` | string | Raw multi-line centerprint (color-stripped). |

`MatchSettings` covers `mode`, `deathmatch`, `teamplay`, `timelimit`,
`fraglimit`, `spawnmodel`, `spawnK`, `antilag`, `overtime`, `powerups`,
`dmgfrags`, `noItems`, `midair`, `instagib`, `yawnmode`, `airstep`,
`vwep`, `noweapon`, `matchtag`, `socdv2`. See `result/metadata.go` for
the per-field intent.

## LocGraphResult (`locGraph`)

Defined in `result/locgraph.go`.

`{ locs: []LocNode, edges: []LocEdge }`.

### LocNode

`{ name, x, y, z, total, byPlayer, byTeam, armed?, unarmed?, quad?, pent? }`
— total seconds spent at each named location, aggregated all-players +
per-player + per-team. `armed`, `unarmed`, `quad` and `pent` are optional
`LocWeights` (`{ total, byPlayer, byTeam }`, same shape) carrying that
breakdown restricted to samples where the player held RL or LG (`armed`),
held neither (`unarmed`, the complement of `armed`), or had an active
quad / pent powerup; omitted when no observed sample met the condition.
They let consumers re-weight the graph by combat posture without
re-walking streams (schema v12).

### LocEdge

`{ from, to, kind, total, byPlayer, byTeam, armed?, unarmed?, quad?, pent? }`
— directed transitions between locs. `kind` = `normal` / `teleport`.
`armed`, `unarmed`, `quad` and `pent` are optional `LocEdgeWeights`
(`{ total, byPlayer, byTeam }`, int counts) carrying the subset of
transitions made while the player held RL or LG (`armed`), held neither
(`unarmed`), or had an active quad / pent at the destination sample, so
the loc graph can be drawn as a self-contained movement graph per combat
posture (schema v12). Omitted when no transition met the condition.

## ItemsResult (`items`)

Defined in `result/items.go`. KTX-only (uses `//ktx took|timer|drop`
hints).

`{ items: []ItemTimeline }`. Each `ItemTimeline` has
`{ name, kind, entNum, x, y, z, loc, phases: []ItemPhase }`.
`ItemPhase` is `{ availableFrom, takenAt, takenBy, team, respawnAt }`.

## Backpacks (`backpacks`)

Defined in `result/backpacks.go`. Each `BackpackDrop` is
`{ time, player, team, weapon ("rl"|"lg"), origin, loc, entNum }`.
`entNum` is the join key with `WeaponPickup.BackpackEnt`.

## WeaponPickups (`weaponPickups`)

Defined in `result/weapon_pickups.go`. Each entry is a slot-weapon
acquisition: `{ time, player, team, weapon, source ("world"|"backpack"),
hadBefore, kills, nextDeathTime, backpackEnt, dropper, dropperTeam,
dropTime }`. `kills` is the kills-before-next-death effectiveness
metric (only non-zero on first acquisition in a life — redundant grabs
stay listed as zero-kill entries so denial labelling still works).

## Cross-references / join keys

- `weaponPickups[i].backpackEnt` ↔ `backpacks[j].entNum` —
  drop-to-pickup join, `source=="backpack"` only.
- `streams.players[].li[].v` → `timelineAnalysis.locTable[i]` —
  resolve player loc name. (Same key joins were on `highResBuckets[].p[name].li`
  in v6.)
- `controlRegion.locs[]` ↔ `locTable[]` — region membership.
- `playerUserIDs[name]` → Hub viewer track parameter.
- `match.players[].name` ↔ `frags.byPlayer[]` ↔
  `demoInfo.players[].name` ↔ `streams.players[].name` — same name
  resolves through every layer (canonicalised by the demoinfo
  resolver). Mid-match name collisions get `#slot` suffix on the
  streams entry.

## Layered views (intentional overlap)

Several pieces of data appear in more than one section by design.
Pick the shape that matches your consumer:

| Data | Lean source | Rich source | Pick lean when… |
|---|---|---|---|
| Frag list | `frags.frags[]` | `messages.events[type=frag]` | …you want kill-classification flags (`isSuicide`, `isTeamKill`). |
| Frag list | `messages.events[type=frag]` | `frags.frags[]` | …you want the obit text for display. |
| Score timeline | `timelineAnalysis.fragEvents` | `frags.frags[]` | …you only need delta over time (no killer/victim). |
| Per-player stats | `match.players[]` | `demoInfo.players[]` | …you only need name/team/frags. |
| Per-player stats | `demoInfo.players[]` | `match.players[]` | …you need accuracy / damage / pickups (KTX demos only). |
| Match length | `match.duration` | `demoInfo.duration` | …you want the parser-derived float. |
| Match length | `demoInfo.duration` | `match.duration` | …you want the KTX integer. |
| Loc names | `timelineAnalysis.locTable` | `locationData[].name` | …you need integer indexing from `Li`. |
| Loc names | `locationData[]` | `locTable[]` | …you need the world coordinates. |

`demoInfo` is **verbatim from KTX** and never transformed; if a
duplication exists, the canonical fix lives on the other side.

## Schema versioning history

| Version | Changes |
|---|---|
| v11 | Bucket views gain a **column-major layout** (`view.ColumnarBuckets`): one dense typed array per `(player, field)` over the player's active span, implicit time axis (`time(i) = startMs + i*windowMs`), a `0`/`1` `alive[]` liveness mask, sparse per-field `validFrom`, booleans/alive as `0`/`1`, loc always the raw `li` index. It is the **default** for the web (`getDefaultBuckets`), REST `/buckets`, and MCP `getBuckets`; the row-major `BucketsView` stays available via `layout=row`. The legacy `HighResBucket`/`HighResPlayerData`/`HighResTeamData` shim and `view.ToLegacyHighResBuckets` are removed. The `Result` **structure is unchanged** — this bump versions the outward *view/query* wire surface so API/MCP/web consumers can feature-detect the new default shape and cached view responses (ETag/`X-Schema-Version`) are invalidated. |
| v10 | DeathEvent / SpawnEvent now derive primarily from the `DF_DEAD` bit in `svc_playerinfo` (broadcast every frame for every player) instead of relying solely on `STAT_HEALTH` crossings (directed at the active POV via `dem_stats`). The stat-based detector still runs and is deduplicated against the new signal — whichever fires first wins. Deaths whose `dem_stats` block was addressed to a different player slot are now captured; `PlayerStream.Spawns`/`Deaths` counts go up for affected demos. Downstream `LocGraph` edges (some spurious `teleport` edges across previously-missed deaths disappear), `LocTrails`, `RegionControl`, `WeaponPickups` (kills-before-next-death windows), and streak boundaries shift accordingly. Field shapes are unchanged. |
| v9 | Loc attribution gains visibility awareness via `mvd-analytics/locvis` (V6: Euclidean primary + PVS-veto). When a per-map BSP is available the analyzer rejects loc-points outside the player's potentially-visible-set, eliminating the brief "wall-bleed" phantom visits V1 produced. Field shapes unchanged: only the contents of `PlayerStream.Loc` (`li`) and everything derived (LocTrails, LocGraph edges, RegionControl) shift for maps with a BSP. Maps without a BSP fall back to V1 — bit-identical to v8 for those. Background: [`experiments/locattr/V2b-V6-HANDOFF.md`](../experiments/locattr/V2b-V6-HANDOFF.md). |
| v8 | All timestamped result fields migrate from `float64` seconds to `int32` milliseconds — `PositionTrack.T`, `PlayerStream.Spawns`/`Deaths`. JSON keys unchanged; consumers reading as seconds must scale by 1/1000. Eliminates the float-precision drift that produced spurious teleport edges in locgraph when a respawn boundary and a position sample shared the same wire timestamp. Other timestamped fields (ChangeI16.T, Interval.Start/End, MatchEvent.Time, frag/powerup event times) stay float64 seconds. |
| v7 | `Streams` added as the canonical event-rate storage (per-player change streams + intervals + native-rate position track with parallel `Li` column). `TimelineAnalysisResult.HighResBuckets` and `HighResDuration` removed; bucketed views are now produced on demand by `mvd-analytics/view.Buckets`. `RegionControlResult.BucketStates` removed from the parse-time output (still produced by `view.RegionControl` at the requested resolution). Health / Armor change streams use int16 (Quake values reach 250). `BuildLocGraph` and the region-control classifier (then `analyzer.ComputeRegionControl`, since folded into `view.RegionControl` as the sixth view function) walk `Streams` natively — no bucket intermediate. Default reducer policy is "first-sample-of-bucket" (point-sampling at bucket start; bucket N == state at t = N × windowMs). Bucket grid is anchored at match-relative t = 0; v6 anchored at the wall-clock 50 ms grid post-shifted by `−matchStart`, so the new grid is offset by up to one sample-interval from main's. Discrete event analytics (frags, items, weapon pickups, scoreboard) are byte-identical with v6; locgraph and region-control percentages drift slightly because of the native-rate sampling cadence (~13 ms between position samples vs v6's 50 ms grid). |
| v6 | HighResPlayerData adds `gl`, `sh`, `nl`. HighResTeamData adds `gl`. MatchEvent adds `messageClean`. ControlRegion adds `locs`. RegionControlResult adds `teamA`/`teamB`/`bucketStates`/`stats` + new `RegionStats`. Top-level `duration` removed (use `match.duration`). MatchResult.PlayerStat drops dead `kills`/`deaths`. |
| v5 | WeaponPickups added — slot-weapon acquisitions with kills-before-next-death effectiveness. Backpack pickups carry `backpackEnt` joining to `backpacks[].entNum`. |
| v4 | Backpacks added — RL/LG backpack drops sourced from KTX `//ktx drop` STUFFCMD_DEMOONLY directive. |

`CurrentSchemaVersion` lives at `result/result.go:CurrentSchemaVersion`;
bump when a change breaks consumers of the outward data — either the
`Result` structure **or** the on-demand view/query wire surface
(`/buckets`, `/events`, `/state-at`, …) served identically via
WASM/CLI/API/MCP — and update this table in the same commit.

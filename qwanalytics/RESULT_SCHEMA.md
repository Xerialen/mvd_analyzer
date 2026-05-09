# Result JSON schema reference

This is the field-level reference for the JSON shape produced by
`qwanalytics`. The Go source of truth lives in `qwanalytics/result/`;
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
| SchemaVersion | `schemaVersion` | int | Identifies JSON schema shape; bump on every breaking change. Currently **6**. |
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

| Field | JSON key | Type |
|---|---|---|
| HighResDuration | `highResDuration` | float64 (seconds per bucket; default 0.05 = 20 Hz) |
| MatchStartTime | `matchStartTime` | float64 (always 0 after post-process) |
| DemoOffset | `demoOffset` | float64 (warmup seconds before match start) |
| HighResBuckets | `highResBuckets` | []HighResBucket |
| FragEvents | `fragEvents` | []TimelineFragEvent |
| PowerupEvents | `powerupEvents` | []PowerupEvent |
| FragStreaks | `fragStreaks` | []FragStreakEvent |
| LocationData | `locationData` | []MapLocation (loc anchor points) |
| LocTable | `locTable` | []string (interned loc names; index 0 = ""). `HighResPlayerData.Li` indexes into this. |
| PlayerUserIDs | `playerUserIDs` | map[string]int (name → Hub viewer UserID) |
| RegionControl | `regionControl` | *RegionControlResult |

### HighResBucket

| Field | JSON key | Type |
|---|---|---|
| T | `t` | float64 (bucket start time) |
| P | `p` | map[string]*HighResPlayerData |
| TD | `td` | map[string]*HighResTeamData |

### HighResPlayerData (compact keys)

| Field | JSON key | Type | Notes |
|---|---|---|---|
| X | `x` | float32 | World position from svc_playerinfo origin. |
| Y | `y` | float32 | |
| Z | `z` | float32 | |
| H | `h` | int | Health. |
| A | `a` | int | Armor. |
| AT | `at` | string (omitempty) | Armor type: `ga` / `ya` / `ra`. |
| RL | `rl` | bool (omitempty) | |
| LG | `lg` | bool (omitempty) | |
| GL | `gl` | bool (omitempty) | (added in v6) |
| SSG | `ssg` | bool (omitempty) | |
| SNG | `sng` | bool (omitempty) | |
| Q | `q` | bool (omitempty) | Quad. |
| Pent | `pe` | bool (omitempty) | |
| R | `r` | bool (omitempty) | Ring. |
| Shells | `sh` | int (omitempty) | (added in v6) |
| Nails | `nl` | int (omitempty) | (added in v6) |
| Rockets | `rk` | int (omitempty) | |
| Cells | `cl` | int (omitempty) | |
| D | `d` | bool (omitempty) | Death-frame marker. |
| Sp | `sp` | bool (omitempty) | Spawn-frame marker. |
| Li | `li` | int (omitempty) | Loc-table index (0 = no loc). |

Shotgun (baseline) and NG (functionally useless in modern QW) are
intentionally not tracked. Dead players are absent from `p` between
`d:true` and the next `sp:true` (consult `D` only on the death-frame).

### HighResTeamData

| Field | JSON key | Type | Notes |
|---|---|---|---|
| RL | `rl` | int (omitempty) | Players with RL only. |
| LG | `lg` | int (omitempty) | Players with LG only. |
| RLLG | `rllg` | int (omitempty) | Players with both. |
| W | `w` | int (omitempty) | Total players with RL or LG. |
| GL | `gl` | int (omitempty) | Independent GL count (added in v6). |
| Q / Pe / R | `q` / `pe` / `r` | int (omitempty) | Per-powerup counts. |
| Pw | `pw` | int (omitempty) | Total players with any powerup. |
| TH | `th` | int (omitempty) | Sum of team health. |
| TA | `ta` | int (omitempty) | Sum of team armor. |
| ABT | `abt` | map[string]int (omitempty) | Armor by type → count. |

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

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Regions | `regions` | []ControlRegion | Region definitions. |
| TeamA | `teamA` | string (omitempty) | Team name encoded as `A` in BucketStates. Picked alphabetically. |
| TeamB | `teamB` | string (omitempty) | Team name encoded as `B`. |
| BucketStates | `bucketStates` | map[string]string (omitempty) | Region name → string of length `len(highResBuckets)`, one ASCII char per bucket. |
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

Control rule (faithful port of `qw-web/static/app.js:classifyRegionState`):
"armed" = carrying RL or LG. Strong control = the dominant team has at
least one armed player; weak = present but unarmed; contested = both
present and armed. Dead players (`D=true` or `H<=0`) are skipped.

`ComputeRegionControl` (Go pure function in
`analyzer/region_control.go`) is callable post-analysis with edited
regions: WASM exports `recomputeRegionControl(regionsJSON)` for the
web UI; a future MCP wrapper imports the same function. The CLI's
`-regions <path>` flag overrides the embedded per-map regions at
analysis time.

### ControlRegion

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | |
| Locs | `locs` | []string | **Authoritative logical membership.** A player is "in" the region iff their resolved loc name is here. (added in v6) |
| Points | `points` | []MapLocation | Rendering anchors. Geometry only — the classifier ignores them. |
| CentroidX | `centroidX` | float32 | Label placement anchor. |
| CentroidY | `centroidY` | float32 | |

### RegionStats

`{ teamAControl, teamAWeakControl, contested, weakContested, empty,
teamBWeakControl, teamBControl }`. Each value is a percentage 0..100
with one decimal place; the seven sum to 100 within rounding.

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

`{ name, x, y, z, total, byPlayer, byTeam }` — total seconds spent at
each named location, aggregated all-players + per-player + per-team.

### LocEdge

`{ from, to, kind, total, byPlayer, byTeam }` — directed transitions
between locs. `kind` = `walk` / `jump` / `telefrag` / `teleport`.

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
- `highResBuckets[].p[name].li` → `timelineAnalysis.locTable[i]` —
  resolve player loc name.
- `controlRegion.locs[]` ↔ `locTable[]` — region membership.
- `playerUserIDs[name]` → Hub viewer track parameter.
- `match.players[].name` ↔ `frags.byPlayer[]` ↔
  `demoInfo.players[].name` ↔ `highResBuckets[].p` keys — same name
  resolves through every layer (canonicalised by the demoinfo
  resolver).

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
| v6 | HighResPlayerData adds `gl`, `sh`, `nl`. HighResTeamData adds `gl`. MatchEvent adds `messageClean`. ControlRegion adds `locs`. RegionControlResult adds `teamA`/`teamB`/`bucketStates`/`stats` + new `RegionStats`. Top-level `duration` removed (use `match.duration`). MatchResult.PlayerStat drops dead `kills`/`deaths`. |
| v5 | WeaponPickups added — slot-weapon acquisitions with kills-before-next-death effectiveness. Backpack pickups carry `backpackEnt` joining to `backpacks[].entNum`. |
| v4 | Backpacks added — RL/LG backpack drops sourced from KTX `//ktx drop` STUFFCMD_DEMOONLY directive. |

`CurrentSchemaVersion` lives at `result/result.go:CurrentSchemaVersion`;
bump when changes break consumers and update this table in the same
commit.

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
| SchemaVersion | `schemaVersion` | int | Identifies JSON schema shape; bump on every breaking change. Current value lives at `result/result.go:CurrentSchemaVersion`. |
| FilePath | `filePath` | string | Source path / display label of the analyzed demo. |
| Match | `match` | *MatchResult | Match summary: map, game dir, duration, players, teams. |
| Frags | `frags` | *FragResult | Total / per-player / per-weapon frag breakdown plus chronological frag list. |
| Messages | `messages` | *MessagesResult | Frag and chat events for timeline display. |
| DemoInfo | `demoInfo` | *DemoInfoResult | Verbatim KTX STUFFCMD demoinfo JSON; authoritative weapon / damage / pickup stats. **Untransformed by design.** |
| TimelineAnalysis | `timelineAnalysis` | *TimelineAnalysisResult | High-res state buckets, key-moment events, region control. |
| Metadata | `metadata` | *MetadataResult | Server cvars (fullserverinfo) + parsed match-settings centerprint. |
| LocGraph | `locGraph` | *LocGraphResult | Loc-to-loc movement graph (nodes + transitions). |
| Items | `items` | *ItemsResult | Per-entity pickup / respawn timeline (per match). |
| Damage | `damage` | *DamageResult | Per-hit damage log + aggregates (matrix, per-weapon, given/taken, EWep victim-weapon buckets) from the KTX `mvdhidden_dmgdone` stream, with a KTX-scoreboard cross-check. |
| MapEntities | `mapEntities` | *MapEntitiesResult | Static designed map layout (item spawns, spawnpoints, teleporters, buttons) from the BSP entity corpus. |
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
| Duration | `duration` | int32 | Match length in milliseconds (parser-derived). Read this for "how long was the match". |
| Players | `players` | []PlayerStat | Lightweight scoreboard view. |
| Teams | `teams` | []TeamStat | Team standings (omitted in FFA). |

The `startTime` / `endTime` fields were **removed in schema v36** — after time
normalisation `startTime` was always 0 and `endTime` always equalled
`duration`, and both duplicated [`streams.global.matchStart` /
`matchEnd`](#globalstream). Read `duration` for match length, or
`streams.global` for the match window.

### PlayerStat

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | Display name. |
| Team | `team` | string | Team name. |
| Frags | `frags` | int | Canonical QW net score (from the `svc_updatefrags` scoreboard). |
| Kills | `kills` | int | Gross kills, frag-log-corrected (v19). Supersedes KTX demoinfo `stats.kills` (which over-counts pentagram-deflect telefrags); `0` when the demo had no frag log. |
| Deaths | `deaths` | int | Deaths, frag-log-corrected (v19). `0` when the demo had no frag log. |
| Suicides | `suicides` | int | Self-inflicted deaths, frag-log-corrected (v19). Counts every `IsSuicide` frag entry (incl. fall / lava / squish / drown), which KTX demoinfo `stats.suicides` undercounts — world-dealt deaths bump the world entity's counter, not the victim's (`ktx/src/client.c:5132`). `0` when the demo had no frag log. |

`MatchResult` is the non-KTX-fallback view: it works on any MVD source.
`Frags`/`Kills`/`Deaths` are the **corrected scoreboard** — net frags from
the scoreboard stream, kills/deaths from the frag log, both independent of
the sometimes-wrong KTX demoinfo (which over-counts pentagram-deflect
telefrags and resets after a reconnect). For per-weapon kills, accuracy, or
damage read `Frags.ByPlayer` (parser-derived) or `DemoInfo.Players[].Stats`
(KTX-authoritative, verbatim).

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
| ByWeapon | `byWeapon` | map[string]int — **enemy kills only** (v15; suicides/teamkills excluded) |
| ByPlayer | `byPlayer` | map[string]*PlayerFrags |

### FragEntry

| Field | JSON key | Type |
|---|---|---|
| Time | `time` | int32 (match-relative ms) |
| Killer | `killer` | string |
| Victim | `victim` | string |
| Weapon | `weapon` | string (`rl`, `lg`, `gl`, `ssg`, `sng`, `ng`, `sg`, `ax`, `tele`, env: `lava`/`fall`/`water`/`slime`/`world`/`squish`) |
| IsSuicide | `isSuicide` | bool (omitempty) |
| IsTeamKill | `isTeamKill` | bool (omitempty) |

At schema v17, a self-kill carries the **weapon/cause that produced it**
(`rl`/`gl`/`lg` for weapon self-detonations, env labels for lava/fall/etc.)
with `isSuicide` set; only the `/kill` console command (KTX "X suicides",
−2 frags) keeps weapon `suicide`. So a real `/kill` is distinguishable
from a weapon self-detonation, and recovered teamkills never carry a stale
`isSuicide` (killer ≠ victim).

Includes **teamkills** recovered at schema v16, both kinds whose obituary
names only one party. *Killer-named* ("X loses another friend") fill in
the victim by matching the coincident authoritative `DeathEvent` on the
killer's team. *Victim-named* ("X was telefragged by his teammate") fill
in the killer by combining position co-location with the teamkiller's −1
frag-delta (the two signals must agree, so a rare alias can't
misattribute) — these recover only when the position/score evidence is
unambiguous; a few may stay unattributed (readable from
`MessagesResult.Events[type=frag]`).

### PlayerFrags

| Field | JSON key | Type |
|---|---|---|
| Kills | `kills` | int |
| Deaths | `deaths` | int |
| TeamKills | `teamkills` | int (omitempty) — KTX "tk"; killer-named teamkills only (v14) |
| ByWeapon | `byWeapon` | map[string]int |

## DamageResult (`damage`)

Defined in `result/damage.go`. Reconstructed from the KTX
`mvdhidden_dmgdone` stream (see `mvd-reader/MVD_FORMAT.md`). Present only
when the demo carries that stream (KTX with MVD-hidden extensions).

**Unbound vs bounded.** All amounts are **unbound** — the full hit
including overkill, capped only at 9999 (a telefrag reports 9999). KTX's
end-of-match scoreboard (`demoInfo.players[].dmg`) instead bounds each
hit to the victim's remaining health. So these figures run higher than
the scoreboard, most on killing blows and telefrags. The `scoreboard`
sub-object surfaces both side by side; the divergence is expected.

Per-player and matrix aggregates count **match-time** hits only (KTX
scoreboard parity); the `events` log keeps every hit (incl. warmup), each
with the match-relative `time` so consumers can window it.

**Positional kills (telefrag, stomp) are excluded from every damage
figure.** A telefrag (deathtype `tele`) is an instant kill reported on
the wire as the 9999 sentinel; a stomp (deathtype `stomp`, landing on a
head) is a movement kill, not a weapon. Left in, a telefrag's 9999 would
dominate the attacker's `given` / `byWeapon` / `ewep` and the totals.
Both are pulled out into `telefrags` / `stomps` (and the opt-in
`telefrag` / `stomp` events) and counted per-player in
`PlayerDamage.telefrags` / `.stomps`. The kill still appears in
`FragResult` and as a `frag` event. (KTX's scoreboard `dmg.given` does
fold in a *bounded* ~victim-health telefrag/stomp amount, so a player's
`streamGiven` may sit slightly under `scoreGiven` for that reason —
separate from the overkill effect.)

| Field | JSON key | Type |
|---|---|---|
| TotalDamage | `totalDamage` | int (match-time, all sources; excl. telefrags + stomps) |
| Events | `events` | []DamageEntry (chronological; excl. telefrags + stomps) |
| ByWeapon | `byWeapon` | map[string]int (enemy damage by attacker weapon) |
| ByPlayer | `byPlayer` | map[string]*PlayerDamage |
| Matrix | `matrix` | []DamagePair (attacker→victim totals) |
| Telefrags | `telefrags` | []PositionalKill (omitempty — instant kills, separate from damage) |
| Stomps | `stomps` | []PositionalKill (omitempty — head-stomp kills, separate from damage) |
| Scoreboard | `scoreboard` | *DamageReconciliation (omitempty) |

### PositionalKill

A telefrag (`telefrags`, deathtype `tele`) or stomp (`stomps`, deathtype
`stomp`) — an instant kill from occupying a player's space rather than a
weapon. No damage amount (a telefrag is the 9999 instakill sentinel; a
stomp is a movement kill).

| Field | JSON key | Type |
|---|---|---|
| Time | `time` | int32 (match-relative ms) |
| Attacker | `attacker` | string (killer) |
| Victim | `victim` | string |
| IsTeam | `isTeam` | bool (omitempty — same team) |

### DamageEntry

| Field | JSON key | Type |
|---|---|---|
| Time | `time` | int32 (match-relative ms) |
| Attacker | `attacker` | string (`world` for environmental / non-player inflictor) |
| Victim | `victim` | string |
| Weapon | `weapon` | string (attacker weapon `rl`/`lg`/…, or env category `fall`/`lava`/…) |
| Damage | `damage` | int (unbound) |
| IsSplash | `isSplash` | bool (omitempty) |
| IsEnv | `isEnv` | bool (omitempty — world/environmental) |
| IsSelf | `isSelf` | bool (omitempty — attacker == victim) |
| IsTeam | `isTeam` | bool (omitempty — same team, not self) |
| VictimWep | `victimWep` | string (omitempty — victim's class at hit: `sg`/`mid`/`lg`/`rl`/`both`; set only on enemy hits) |

### PlayerDamage

`Given` counts enemy damage only; `Taken` counts **all** sources (enemy +
team + self + env), so it runs above the KTX `dmg.taken` (which is
enemy-player damage only — `dmg_t`, `combat.c:1083`; it excludes team,
self, and environmental). The `EnemyVs*` buckets partition `Given` by the **victim's**
held weapons at hit time — KTX "ewep" semantics, keyed on the *target's*
inventory, not the attacker's weapon. Mutually exclusive, priority
RL+LG > RL > LG > mid > sg (NG counts as shotgun-tier). `ewep` is the
sum of the LG/RL/both buckets = damage dealt to enemies holding RL or LG.

| Field | JSON key | Type |
|---|---|---|
| Given | `given` | int (to enemies) |
| Taken | `taken` | int (all sources) |
| GivenTeam | `givenTeam` | int |
| GivenSelf | `givenSelf` | int |
| TakenEnv | `takenEnv` | int |
| ByWeapon | `byWeapon` | map[string]int (enemy given, by attacker weapon) |
| EnemyVsSG | `enemyVsSg` | int (victim held shotgun-tier only) |
| EnemyVsMid | `enemyVsMid` | int (victim held ssg/sng/gl) |
| EnemyVsLG | `enemyVsLg` | int (victim held LG, not RL) |
| EnemyVsRL | `enemyVsRl` | int (victim held RL, not LG) |
| EnemyVsBoth | `enemyVsBoth` | int (victim held both RL and LG) |
| EWep | `ewep` | int (= enemyVsLg + enemyVsRl + enemyVsBoth) |
| Telefrags | `telefrags` | int (omitempty — instant-kill telefrags DEALT; not damage, excluded from `given`) |
| Stomps | `stomps` | int (omitempty — head-stomp kills DEALT; not damage, excluded from `given`) |

### DamagePair

| Field | JSON key | Type |
|---|---|---|
| Attacker | `attacker` | string |
| Victim | `victim` | string |
| Damage | `damage` | int |
| ByWeapon | `byWeapon` | map[string]int (attacker weapon → damage to this victim) |

### DamageReconciliation / DamageDelta

Diagnostic cross-check vs the KTX scoreboard (`demoInfo.players[].dmg`).
Keyed by player name. The `stream*` fields are **this pipeline's
unbound** figures (overkill-inclusive, from the `mvdhidden_dmgdone`
stream); the `score*` fields are **KTX's bounded** figures (capped to
victim health, from the scoreboard JSON). `score*` ≤ `stream*` by the
overkill; `scoreEwep` is the KTX `enemy-weapons` field.

| Field (DamageDelta) | JSON key | Type |
|---|---|---|
| StreamGiven | `streamGiven` | int (unbound, this pipeline) |
| ScoreGiven | `scoreGiven` | int (bounded, KTX scoreboard) |
| StreamTaken | `streamTaken` | int (unbound, this pipeline) |
| ScoreTaken | `scoreTaken` | int (bounded, KTX scoreboard) |
| StreamEWep | `streamEwep` | int (unbound, this pipeline) |
| ScoreEWep | `scoreEwep` | int (bounded, KTX scoreboard) |

## MessagesResult (`messages`)

Defined in `result/messages.go`.

| Field | JSON key | Type |
|---|---|---|
| Events | `events` | []MatchEvent |

### MatchEvent

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Time | `time` | int32 | Match-relative ms. |
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

This section carries only the event-shaped derived results. Bucketed
data is produced on demand by `mvd-analytics/view.Buckets` (any window
size, any reducer set; see [Streams](#streams-streams) and
[Query API](#query-api)), not baked into the parse-time result.

The match window and the wall-clock anchor (`matchStart`/`matchEnd`,
`demoOffset`, `demoStartUnixMs`, `demoStartAccuracyMs`, `pauses`) live in
[`streams.global`](#globalstream) as of schema v23 — they describe how to
read the streams' times, so they sit next to them.

| Field | JSON key | Type |
|---|---|---|
| FragEvents | `fragEvents` | []TimelineFragEvent |
| DeathEvents | `deathEvents` | []TimelineDeathEvent |
| KillEvents | `killEvents` | []TimelineKillEvent |
| PowerupEvents | `powerupEvents` | []PowerupEvent |
| FragStreaks | `fragStreaks` | []FragStreakEvent |
| Airgibs | `airgibs` | []AirgibEvent (top airborne rocket hits) |
| LocationData | `locationData` | []MapLocation — one anchor point per loc name (the medoid of that name's `.loc` points; since v31) |
| LocTable | `locTable` | []string (interned loc names; index 0 = ""). `Streams.Players[].Loc[].V` indexes into this. |
| PlayerUserIDs | `playerUserIDs` | map[string]int (name → Hub viewer UserID) |
| RegionControl | `regionControl` | *RegionControlResult |

Bucketed data is served as `view.BucketsView` (row) or
`view.ColumnarBuckets` (column) — see
[Query API → Buckets](#buckets). Each player's per-bucket data is a
`map[string]any` keyed by the [field vocabulary](#field-vocabulary)
(row) or one dense array per field (column).

### TimelineFragEvent

`{ time, player, team, delta }`. Score-delta channel (`+1` enemy kill,
`-1` suicide / teamkill, `+2` for the rare gib double-frag KTX edge).
Reconstruct the killer ↔ victim relationship from `FragResult.Frags[]`
or `MessagesResult.Events[type=frag]` by matching `time`.

### TimelineDeathEvent

`{ time, player, team }`. One record per death, sourced from the
authoritative protocol DeathEvent and gated to match time exactly like
`fragEvents` — every death counts once (enemy kill, suicide, world, or
being teamkilled), so a player's death count here matches their
scoreboard deaths and KTX efficiency `frags / (frags + deaths)`
(`ktx/src/statsTables.c` `calculateEfficiency`). Unlike `frags.frags`,
this does not drop teamkill victims whose obituary names only the
attacker. Pairs with `killEvents` for the Timeline tab's per-player
+/- (cumulative kills − deaths) drill-down.

### TimelineKillEvent

`{ time, player, team }`. One record per enemy kill, keyed on the
**killer**, sourced from the canonical frag log (`FragResult.Frags[]` /
`CoreOutputs.FragEntries`) filtered to real enemy kills (suicides and
teamkills excluded). A player's cumulative `killEvents` reconciles
exactly with `frags.byPlayer[].kills` and thus the kills-based
efficiency `kills / (kills + deaths)`. Parallel to `deathEvents`; the
Timeline per-player drill-down plots `killEvents − deathEvents` as a
windowed +/- area. `team` is best-effort via the name table and — unlike
`deathEvents` — is **not** gated to non-empty: `byPlayer.kills` isn't
either, so gating would silently drop a player's whole kill curve in POV
demos with an incomplete name↔team join (the consumer groups by player
name and ignores `team`).

### PowerupEvent

`{ time, endTime, playerName, playerSlot, playerUserID, team,
powerupType, duration, frags }`. One record per powerup run. Carries
both `playerSlot` and `playerUserID` (TimelineFragEvent doesn't —
intentional: that channel is lean by design).

### FragStreakEvent

`{ time, endTime, playerName, playerUserID, team, frags, duration,
ewep }`. `ewep` = effective weapon = the weapon that scored the most
kills during the streak.

### AirgibEvent

`{ time, attacker, attackerTeam, attackerUserID, victim, victimTeam,
victimUserID, height, heightAboveAttacker, loc, damage, lethal }`. One
record per direct
enemy rocket hit landed on an airborne victim (an "airgib"). `height` is
the victim's feet above the floor at the hit (`PositionTrack.H` units);
`heightAboveAttacker` (schema v29) is the victim's origin minus the
shooter's at the hit — the vertical gap the rocket climbed, negative
when the victim was below the shooter, `0`/absent when the shooter had
no position sample near the hit (a genuine dead-level hit also reads
0); `loc` is the victim's loc there. `lethal` is whether the hit
killed (a
matching rocket frag near the hit — a highlight heuristic, see below).
`attackerUserID` is the one to track for the Hub viewer link (shooter
perspective).

Derived by a post-processor (schema v25) from `Damage.Events` (the
per-hit log), the streams' `PositionTrack.H` column, the frag log, and
the loc table. A hit qualifies when `weapon == "rl"` and it is a **direct
hit** (`isSplash` false), the attacker is an enemy (not self / teammate /
world), and the victim's height at the hit is ≥ 96 units (≈ two player
models). Every qualifying hit is emitted (uncapped since schema v30 —
the ≥ 96 threshold already bounds the list), ordered by `height`
descending; the web
view re-sorts client-side. **Empty when the map has no clip hull** (no
`PositionTrack.H` to read — same BSP provisioning as the
visibility-aware loc filter). The `lethal` window can over-attribute on a
rare back-to-back double-rocket exchange (two rockets, same
attacker→victim, within the window) — fine for a highlight, not an exact
killing-blow flag.

### MapLocation

`{ x, y, z, name }`. Used by `LocationData` (one anchor point per loc
name — see below) and `ControlRegion.Points` (rendering anchors).

Since schema v31 `LocationData` holds one `MapLocation` per loc name
instead of every raw `.loc` corpus point. The point chosen is the
**medoid** of that name's corpus points — the actual point minimizing
summed 3D distance to its same-name siblings, never an averaged mid-air
centroid — so the map view draws one label per name instead of a cluster
of duplicates. `locGraph` node coordinates (resolved from this list by
name) move to the medoid accordingly.

### RegionControlResult (`regionControl`)

The parse-time output carries only `stats` (match-aggregate
percentages); `bucketStates` is not baked in. For per-bucket region
states at any resolution, call
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
| Locs | `locs` | []string | **Authoritative logical membership.** A player is "in" the region iff their resolved loc name is here. |
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

Defined in `result/streams.go`. Streams is the canonical
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
| Movers | `movers` | []MoverStream (brush-model lifts/doors/plats/trains; since v32, `omitempty`) |

### GlobalStream

The match window plus the demo/wall-clock anchor (moved here from
`timelineAnalysis` at schema v23).

| Field | JSON key | Type | Notes |
|---|---|---|---|
| MatchStart | `matchStart` | int32 | Match window start in milliseconds (always 0 after post-process — it *is* the time origin). |
| MatchEnd | `matchEnd` | int32 | Match window end in milliseconds. |
| DemoOffset | `demoOffset` | int32, omitempty | Ms from demo open (≈ countdown start) to match start. |
| DemoStartUnixMs | `demoStartUnixMs` | int64, omitempty | Server wall clock (Unix epoch ms) at demo open. |
| DemoStartAccuracyMs | `demoStartAccuracyMs` | int32, omitempty | Resolution of `demoStartUnixMs`: `1` or `1000`. |
| Pauses | `pauses` | []TimelinePause, omitempty | Per-pause wall-clock segments; see below. |

**Wall-clock anchor.** All other times in the result are match-relative
(`t=0` is match start). The anchor lets a consumer project any
match-relative game time `g` (ms) onto a real-world wall clock for
syncing external data (voice tracks, stream overlays):

```
wallClockMs = demoStartUnixMs + demoOffset + g + P(g)   (±demoStartAccuracyMs)
P(g)        = Σ pauses[i].durationMs  for  pauses[i].atMs <= g
```

`demoStartUnixMs` is the server's clock at **demo open** (demo `t=0`, ≈
countdown start — not match start; `demoOffset` bridges the two).
`demoStartAccuracyMs` is its resolution: `1` from the millisecond
[mvdhidden 0x000B block](../mvd-reader/MVD_FORMAT.md#hidden-message-types),
`1000` from the whole-second serverinfo `epoch` cvar. The anchor fields
are omitted when the demo carries no usable wall-clock source; implausible
0x000B payloads (some demos emit a non-timestamp block here) fall back to
`epoch`. The REST `/overview` endpoint mirrors this anchor in its `timing`
block.

The `P(g)` term accounts for **pauses**: the game clock freezes during a
pause while wall-clock time keeps running, so without it the mapping
drifts by the total pause time on any paused demo. `P(g)` is `0` (and
`pauses` may be absent) otherwise.

Each `pauses[]` entry is a **TimelinePause** `{ atMs, durationMs }`: `atMs`
is the match-relative game time the clock froze at (negative if the pause
happened during the countdown), `durationMs` the real wall-clock time the
pause consumed. Recovered from the [mvdhidden 0x000A `paused_duration`
blocks](../mvd-reader/MVD_FORMAT.md#hidden-message-types) mvdsv embeds once
per idle frame while paused (summed per pause), in `atMs` order. Absent
when the demo has no pauses or the server does not embed the block.

### PlayerStream

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Name | `name` | string | Canonical player name (D12: collisions in same match get a `#slotIndex` suffix). |
| Team | `team` | string (omitempty) | Team label (post-duel-normalise: per-player synthetic team). |
| Position | `pos` | *PositionTrack (omitempty) | Native-rate position track: x/y/z plus optional per-sample `li`/`h`/`lq` and (schema v31) view-direction `vp`/`vya` columns. Omitted from default JSON unless `-include positions` (CLI) or equivalent is set; `-include view`/`height`/`liquid` keep the respective extra columns. |
| Health / Armor | `h` / `a` | []ChangeI16 | Vital change streams. Health caps at 250, Armor at 200; int16 holds the range. |
| ArmorType | `at` | []ChangeStr | `"ga"` / `"ya"` / `"ra"` / `""` transitions. |
| Loc | `li` | []ChangeI16 | Index into `TimelineAnalysisResult.LocTable`. Smoothed by the blip filter. |
| RL / LG / GL / SSG / SNG | `rl` / `lg` / `gl` / `ssg` / `sng` | []Interval | Half-open `[Start, End)` periods the weapon was held. |
| Quad / Pent / Ring | `q` / `pe` / `r` | []Interval | Same shape as weapons. |
| Shells / Nails / Rockets / Cells | `sh` / `nl` / `rk` / `cl` | []ChangeI16 | Ammo change streams. |
| Spawns / Deaths | `sp` / `d` | []int32 | Discrete event timestamps in milliseconds. |

### ChangeI16 / ChangeStr / Interval

```
ChangeI16 = { "t": int32, "v": int16 }
ChangeStr = { "t": int32, "v": string }
Interval  = { "s": int32, "e": int32 }   // half-open [s, e)
```

`t` / `s` / `e` are **integer milliseconds** since the stream's time
origin (see PositionTrack for the unit rationale).

### PositionTrack

Columnar to compress JSON. Indices align across all arrays. `t`/`x`/`y`/`z`
are always present; `li`, `h`, `lq`, `vp`, `vya`, `vx`, `vy`, `vz` are
optional (`omitempty`) per-sample columns populated during analysis when
their inputs are available.

```
PositionTrack = {
  "t": [int32...], "x": [float32...], "y": [float32...], "z": [float32...],
  "li":  [int16...],   // optional: loc index per sample
  "h":   [float32...], // optional: height above floor per sample
  "lq":  [int8...],    // optional: liquid state per sample
  "vp":  [int16...],   // optional: view pitch per sample (raw angle16)
  "vya": [int16...],   // optional: view yaw per sample (raw angle16)
  "vx":  [float32...], // optional: velocity X per sample (units/sec)
  "vy":  [float32...], // optional: velocity Y per sample (units/sec)
  "vz":  [float32...]  // optional: velocity Z per sample (units/sec)
}
```

`t` is **integer milliseconds** since the stream's time origin. The
MVD wire format delivers a 1-byte ms delta per message; storing the
cumulative value
as `int32` keeps it exact across the persistence boundary. Consumers
reading the JSON as seconds must scale by `* 0.001`. Range is ±24.8
days, ample for matches that run minutes to hours; values can go
negative for pre-match warmup samples after time normalisation.

`x` / `y` / `z` are `float32` — the wire-native sub-unit origin
(mvd-reader decodes coordinates as float32; the MVD wire carries them as
eighth-unit fixed point, or as true floats under the float-coords
extension). **Since schema v33** they are no longer rounded to whole
units: schema v32 and earlier stored `int32`, silently truncating up to
~1 unit per axis, which also coarsened the derived velocity. Quake maps
exceed ±32 768 in any axis, so `int16` was never an option. The values
are kept at **native float32 resolution in memory**; only the JSON text
is rounded — to 3 decimals, which is lossless for eighth-unit coordinates
(`.125`/`.25`/… round-trip exactly) and just sheds float artifacts.

`li` (when present) is the resolved loc-name index for each sample —
indexes into `TimelineAnalysisResult.LocTable`, `0` = "no loc",
smoothed by the blip filter. Same length as `t`. Absent when no `.loc`
corpus is loaded for the map. (Distinct from `PlayerStream.Loc`, which
is the *sparse* change-stream view of the same data.)

`h` (when present, schema v24) is the **player's height above the floor
beneath them** at each sample — how far the feet are above the highest
solid surface at or below the player, from straight-down traces through
the map's player clip hulls (parsed from the map's BSP `CLIPNODES` at
analyze time; see `mvd-analytics/mapclip`). Same length as `t`. **Since
schema v26** it is measured over the player's bounding-box footprint,
not just the origin column: the highest floor under a 3×3 grid of
columns sampled ±8 around the origin wins (an effective ~48-wide
footprint on the already-±16-box-inflated hull), so a player skimming a
ledge / well rim — origin momentarily over the pit while the box overhangs
the rim — reads the **near** floor, not the distant one far below (this is
what removed bogus high airgibs at spots like anwalked RA's well rim).
**Since schema v27** the trace scene also includes every moving
brush-model entity (lift, door, train) posed at its demo-streamed origin
for the sample's time, so a player riding the dm2 RA lift stands on the
lift, not the shaft floor beneath it. It
reads **~0 when grounded** and grows positive during a jump or airborne
hit (airgib), so
a consumer flags those directly with no coordinate arithmetic — test
`|h|` small rather than `== 0`, since slopes and the trace epsilon leave
a unit or two of slack. (The absolute floor surface, if needed, is
`z[i] - 24 - h[i]` — the player origin rides 24 units above the floor.)
**Since schema v28** liquids participate: a sample in liquid (`lq`
level ≥ 1) reads `h = 0` by definition — the liquid surface is the
support, so swimmers don't read as airborne over the pool bottom — and
a dry sample airborne above water/slime/lava measures down to the
**liquid surface** when it is the highest support beneath the player.
The sentinel `-1000000000` (`result.NoFloor`, schema v33; was
`-2147483648` while `h` was `int32`) marks a sample with **no floor to
measure from** — over a void / bottomless pit, an embedded origin, or
the zero origin. Absent entirely when no BSP is
provisioned for the map (same best-effort BSP source as the
visibility-aware loc filter), so floor height and PVS-veto loc
attribution light up together.

`lq` (when present, schema v28) is the **per-sample liquid state**,
computed by mirroring the engine's `PM_CategorizePosition` waterlevel
probes (feet z−23, waist z+4, eyes z+22) against the map's render BSP:
`0` = dry, otherwise `(type << 2) | level` with level 1–3
(feet/waist/eyes submerged) and type 1 water / 2 slime / 3 lava — so
water reads 5/6/7, slime 9/10/11, lava 13/14/15. Decode with `lq & 3`
(level) and `lq >> 2` (type); Go consumers use `result.LqLevel` /
`result.LqType` and the `result.LqWater/LqSlime/LqLava` constants.
Same length as `t`; absent when no BSP is provisioned. (One deliberate
deviation from the engine predicate: `CONTENTS_SKY` does **not** count
as liquid — the physics treats sky like water for drag, but a
void-faller reported as swimming would mislead consumers.)

`vp` / `vya` (when present, schema v31) are the **player's view
direction** — pitch and yaw — at each sample, stored as the **raw
`angle16` state** (the exact 2-byte values, kept losslessly after
`svc_playerinfo` delta carry-forward; see MVD_FORMAT.md "View-angle
semantics"). Decode to degrees with `deg = uint16(v) * 360/65536`;
values land in `[0,360)`, so a pitch
**> 180° means looking up**. Roll is not stored (the server forces it to
0). A forward unit vector is one trig call away — with `p`, `y` the
decoded pitch/yaw in radians,
`forward = (cos p·cos y, cos p·sin y, −sin p)`. Same length as `t`;
populated whenever the track is (the angles ride the same
`svc_playerinfo` samples as x/y/z, so unlike `h`/`lq` they need no BSP).

`vx` / `vy` / `vz` (when present, schema v32) are the player's
**velocity** in Quake units/sec at each sample — **derived**, not a wire
field, from the position columns by a central-difference estimator
(second-order accurate). The estimator does not differentiate across a
respawn teleport, a map-teleporter relocation, or an abnormal time gap
(death / pause / reconnect): such a step reads ~0 rather than a
tens-of-thousands-ups spike, and an isolated sample reads 0. **Since
schema v33** the source `x`/`y`/`z` are float32 (no longer rounded to
whole units), so the derivative is sub-unit precise — the ±1-unit
position quantization that used to add a few tens of ups of noise is
gone. Like positions, velocity is native float32 in memory and the JSON
text is rounded to 3 decimals (the float32 division tail, e.g.
`-58.333332`, is false precision below the estimator's noise floor);
smooth client-side only if a softer curve is wanted. Speed is `hypot(vx,vy,vz)`;
horizontal speed (the usual movement metric) is `hypot(vx,vy)`. Same
length as `t`; populated whenever the track is (no BSP needed).

### MoverStream (`streams.movers[]`, schema v35)

The pose timeline of one brush-model entity — a lift, door, plat or
train. Columnar like PositionTrack; indices align across `t`/`x`/`y`/`z`/`vis`.

```
MoverStream = {
  "ent": int,            // MVD entity number
  "sub": int,            // brush-model index ("*sub"); matches the corpus SubModelMesh id
  "t":   [int32...],     // match-relative milliseconds
  "x": [float32...], "y": [float32...], "z": [float32...],  // origin per sample
  "vis": [bool...]       // whether the mover is drawn at that sample
}
```

At `t[i]` ms the mover sits at `(x,y,z)[i]` and is rendered when
`vis[i]`. A renderer offsets the map-geometry `SubModelMesh` whose `id`
equals `sub` by `(x,y,z)` to place it. Origins are `float32` (wire
values are exact ⅛-unit multiples; `int32` would quantize the pose
stepping). Tracks are **short**: MVD delta compression only re-sends an
origin when it changes, so a parked mover is a single entry and a
travelling one re-sends per frame only while in motion. The first entry
is clamped to `t = 0` carrying the **match-start pose**, so a parked
mover whose only wire state predates the match still has a pose to draw;
earlier pre-match states are dropped as superseded. Absent (`omitempty`)
when the demo has no movers. The same internal mover tracks already feed
the v27 floor-height pass (players ride lifts).

### Time units: all times are int32 milliseconds

Every timestamped field in this schema — `PositionTrack.T`,
`PlayerStream.Spawns/Deaths`, `ChangeI16.T` / `ChangeStr.T`,
`Interval.Start/End`, `GlobalStream.MatchStart/End/DemoOffset`,
`GlobalStream.Pauses[].AtMs/DurationMs`,
`MatchResult.Duration`,
`TimelineFragEvent.Time`, `PowerupEvent.Time/EndTime/Duration`,
`FragStreakEvent.Time/EndTime/Duration`, `MatchEvent.Time`,
`FragEntry.Time`, `BackpackDrop.Time`,
`WeaponPickup.Time/NextDeathTime/DropTime`,
`ItemPhase.AvailableFrom/TakenAt/RespawnAt` —
is stored as `int32` integer milliseconds. External consumers that
want seconds must scale by `* 0.001`.
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
   `matchStartMs` comes from `res.Streams.Global.MatchStart`
   (pre-normalize, the demo-relative match start) directly.
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
| `view` | View direction (pitch/yaw, raw angle16) | `*PositionTrack` (vp/vya) | `first` |
| `hgt` | Height above floor | `*PositionTrack` (h) | `first` |
| `lq` | Liquid state | `*PositionTrack` (lq) | `first` |
| `vel` | Velocity (vx/vy/vz, units/sec) | `*PositionTrack` (vx/vy/vz) | `first` |
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
| `sp` | Spawn timestamps | `[]int32` | `any` |
| `d` | Death timestamps | `[]int32` | `any` |

`sp` / `d` stay on `any` because they need a bool ("did this event
happen during the bucket?"); `first` would return a timestamp.

**`view` / `hgt` / `lq` / `vel` are opt-in** — they are *not* in the
default field set (`AllStandardFields`), so a query that omits `fields`
keeps the pre-v31 shape and a consumer only pays for view direction,
floor height, liquid state, or velocity when it asks for the code
explicitly. They all read from the player's `*PositionTrack` but project
disjoint columns: `view` → `vp`/`vya`, `hgt` → `h`, `lq` → `lq`,
`vel` → `vx`/`vy`/`vz`. **Clean break (schema v31):** `pos` now returns
**strictly** `x`/`y`/`z` (plus the per-sample loc label `li`); height and
liquid no longer ride along it — request `hgt` / `lq` for those. In
`view.StreamSlice` each projects into its own sibling track
(`pos`/`view`/`hgt`/`lq`/`vel`); in `view.Buckets` `view` and `vel`
reduce to a vector (`[vp, vya]` / `[vx, vy, vz]`, split to columns in the
columnar layout), `hgt` to a scalar height (so `mean`/`min`/`max` give a
jump apex / average), `lq` to a scalar liquid code; in `view.StateAt`
they surface as `view` (`{vp, vya}`), `hgt`, `lq`, and `vel`
(`{vx, vy, vz}`). (The stored `result.PositionTrack` still carries every
column — the split is purely in the query projection; the WASM frontend
reads the track directly and is unaffected.)

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
//        x|y|z: [float32 …],             // position split
//        vx|vy|vz: [float32 …],          // velocity split; hgt: [float32 …]
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
re-walking streams.

### LocEdge

`{ from, to, kind, total, byPlayer, byTeam, armed?, unarmed?, quad?, pent? }`
— directed transitions between locs. `kind` = `normal` / `teleport`.
`armed`, `unarmed`, `quad` and `pent` are optional `LocEdgeWeights`
(`{ total, byPlayer, byTeam }`, int counts) carrying the subset of
transitions made while the player held RL or LG (`armed`), held neither
(`unarmed`), or had an active quad / pent at the destination sample, so
the loc graph can be drawn as a self-contained movement graph per combat
posture. Omitted when no transition met the condition.

## ItemsResult (`items`)

Defined in `result/items.go`. The item **spawn list, kind, and
location** are derived from the MVD entity stream (model-classified
baselines) and are present on **any** demo — KTX or not. The pickup
`phases` (taken/respawn transitions) come from entity-visibility
changes; KTX `//ktx took|timer|drop` hints only refine **attribution**
(`takenBy`/`team`) and MH respawn timing. Non-KTX demos still get the
full item layout and pickup timeline, just without picker names.

`{ items: []ItemTimeline }`. Each `ItemTimeline` has
`{ name, kind, entNum, x, y, z, loc, phases: []ItemPhase }`.
`ItemPhase` is `{ availableFrom, takenAt, takenBy, team, respawnAt }`.

For the map's **designed** static layout (all spawns + teleporters /
spawnpoints / buttons, independent of what happened this match), see
[MapEntitiesResult](#mapentitiesresult-mapentities).

## MapEntitiesResult (`mapEntities`)

Defined in `result/map_entities.go`. The map's **static, designed
layout** — item spawns, player spawnpoints, teleport
destinations/sources, and buttons — each with a type and a location.
Sourced from the offline-generated **mapents corpus** (BSP entity lumps,
`mvd-analytics/mapents/data/<map>.json`, produced by `cmd/mapgen`),
keyed by map name. It is therefore **identical for every demo on a
given map** and independent of what happened in the match. Absent when
no corpus exists for the map.

This is the map's *designed* layout. For the per-match pickup timeline —
which items actually spawned, who took each, when it respawned — see
[ItemsResult](#itemsresult-items). The two can be joined by `kind` +
nearest origin.

`{ map, entities: []MapEntity }`. Each `MapEntity`:

| Field | JSON key | Type | Notes |
|---|---|---|---|
| Type | `type` | string | `item` / `spawn` / `teleportDst` / `teleportSrc` / `button` / `door`. |
| Class | `class` | string | Raw BSP classname (`weapon_rocketlauncher`, `info_player_deathmatch`, …). |
| Kind | `kind` | string (omitempty) | Items only; same vocabulary as `ItemTimeline.Kind` (`rl`,`lg`,`ra`,`mh`,`quad`,…). |
| Name | `name` | string | Loc-based label, disambiguated with `-1`/`-2` within each `(type, kind, loc)` group; falls back to kind/type when no loc file exists. |
| X / Y / Z | `x`/`y`/`z` | float32 | Position. Entity origin for point entities; bmodel bbox centre for brush entities (teleportSrc/button/door). |
| Loc | `loc` | string (omitempty) | Nearest named loc, when a loc file exists for the map. |
| Target | `target` | string (omitempty) | `teleportSrc` → the destination's targetname. |
| TargetName | `targetName` | string (omitempty) | `teleportDst` → its own targetname (join key for teleport pairs). |
| Spawnflags | `spawnflags` | int (omitempty) | Raw BSP spawnflags. |
| Bounds | `bounds` | object (omitempty) | Brush entities only: `{ min:[x,y,z], max:[x,y,z] }` — the trigger/door volume in world coords. |

Classification is grounded in `ktx/src/items.c` spawn functions
(`item_health` spawnflags `H_ROTTEN=1`/`H_MEGA=2` select h15/mh).

**Point vs brush.** Point entities (items, spawns, `teleportDst`) sit at
their entity origin. Brush entities (`teleportSrc`, `button`, `door`)
have no origin — they are placed at their BSP submodel's bbox centre
(`X/Y/Z`) and carry the box as `bounds` (the trigger/door volume).

**Teleport pairs.** A `teleportSrc` (the trigger you walk into) links to
its `teleportDst` (where you arrive) by `teleportSrc.target` ==
`teleportDst.targetName`. Multiple sources may share one destination.

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
  resolve player loc name.
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
| Per-player deaths | `timelineAnalysis.deathEvents` | `frags.byPlayer[].deaths` | …you need per-death timing (not just totals); counts every death, no teamkill-victim drops. |
| Per-player kills | `timelineAnalysis.killEvents` | `frags.byPlayer[].kills` | …you need per-kill timing keyed on the killer (enemy kills only); cumulative count reconciles with `byPlayer.kills`. |
| Per-player stats | `match.players[]` | `demoInfo.players[]` | …you only need name/team/frags. |
| Per-player stats | `demoInfo.players[]` | `match.players[]` | …you need accuracy / damage / pickups (KTX demos only). |
| Match length | `match.duration` | `demoInfo.duration` | …you want the parser-derived float. |
| Match length | `demoInfo.duration` | `match.duration` | …you want the KTX integer. |
| Loc names | `timelineAnalysis.locTable` | `locationData[].name` | …you need integer indexing from `Li`. |
| Loc names | `locationData[]` | `locTable[]` | …you need the world coordinates. |

`demoInfo` is **verbatim from KTX** and never transformed; if a
duplication exists, the canonical fix lives on the other side.

## Schema versioning history

The field tables above describe the **current** schema. This table
records what each bump changed, for consumers migrating across versions.

| Version | Changes |
|---|---|
| v36 | `MatchResult` drops the dead `startTime` / `endTime` fields. After the match-relative time normalization `startTime` was always 0 (already `omitempty`, so absent from JSON) and `endTime` always equalled `duration`; both duplicated `streams.global.matchStart` / `matchEnd`. The `endTime` key disappears from the `match` object — read `duration` for match length, or `streams.global` for the match window. Breaking removal (not additive). |
| v35 | `streams` gains `movers[]` (`MoverStream`): the pose timeline of every tracked brush-model entity (lift, door, plat, train). Each carries `ent` (entity number), `sub` (the `*N` brush-model index, matching the corpus `SubModelMesh` id), and index-aligned `t`/`x`/`y`/`z`/`vis` columns — the mover sits at `(x,y,z)[i]` at `t[i]` ms and is drawn when `vis[i]`. Origins are `float32` (exact ⅛-unit wire values). The first entry is clamped to `t = 0` carrying the match-start pose so a parked mover (only wire state predates the match) still has one. Additive (`omitempty`); absent when the demo has no movers. The same internal tracks already drive the v27 floor-height pass. |
| v34 | `timelineAnalysis.locationData` now carries **one `MapLocation` per loc name** — the medoid of that name's `.loc` corpus points — instead of every raw point. The corpus often repeats a name across several nearby points, which drew duplicate map labels; the medoid is the actual point minimizing summed distance to its same-name siblings (never an averaged mid-air centroid). `locGraph` node coordinates (resolved from this list by name) move to the medoid. Same field name and `MapLocation` shape; the list is just shorter. |
| v33 | `PositionTrack` `x` / `y` / `z`, `vx` / `vy` / `vz`, and `h` change from `int32` to **`float32`** — the pipeline stops truncating the wire-native sub-unit origin (mvd-reader decodes coordinates as float32; the wire carries eighth-unit fixed point, or true floats under the float-coords extension). v32 and earlier rounded each axis to whole units (losing up to ~1 unit) and derived velocity from those rounded positions; velocity is now sub-unit precise, so the old ±1-unit quantization noise is gone. Values are kept at **native float32 in memory**; only the JSON text is rounded — to 3 decimals, applied by `PositionTrack.MarshalJSON` (lossless for eighth-unit coordinates; it just sheds the float division/epsilon tail on derived velocity & height). The `PositionTrack.H` `NoFloor` sentinel changes from `-2147483648` (`math.MinInt32`, which a float32 cannot represent exactly and serializes as `-2147483600`) to **`-1000000000`** (`-1e9`, exact in float32 and float64). The `buckets` x/y/z/vx/vy/vz/hgt columns get the same 3-decimal rounding; the point-in-time `state-at` `pos`/`vel`/`hgt` and the `AirgibEvent` heights are float32 too but emitted at full precision (low volume). Time axes stay `int32` ms; view angles stay `int16` raw `angle16`; loc/liquid columns unchanged. JSON keys unchanged; values now carry fractional digits where the wire delivered sub-unit positions. |
| v32 | `PositionTrack` gains `vx` / `vy` / `vz` columns: the player's **velocity** per sample in Quake units/sec, derived from the position columns by a central-difference estimator (it does not differentiate across a respawn teleport, a map-teleporter relocation, or an abnormal time gap — those read ~0 instead of spiking). Additive (`omitempty`), populated whenever the track is (no BSP needed). New opt-in view-layer field code `vel` (vx/vy/vz) and CLI `-include velocity`. Expect ±1-unit quantization noise on the raw derivative (integer-rounded source positions); smooth client-side for a clean speed curve. |
| v31 | `PositionTrack` gains `vp` / `vya` columns: the player's **view direction** (pitch, yaw) per sample as the raw `angle16` state, kept losslessly after `svc_playerinfo` delta carry-forward (decode `deg = uint16(v) * 360/65536`; values `[0,360)`, pitch > 180° = looking up; roll not stored). Additive (`omitempty`), populated whenever the track is — no BSP needed (the angles ride the same `svc_playerinfo` samples as x/y/z). New opt-in view-layer field codes expose per-channel selection: `view` (vp/vya), `hgt` (h), `lq` (lq). **Clean break:** the `view`-API `pos` code now returns strictly x/y/z (+`li`); height/liquid no longer ride along it — request `hgt` / `lq`. CLI `-include` becomes column-aware (`positions` / `view` / `height` / `liquid`). |
| v30 | `timelineAnalysis.airgibs` is no longer capped at the top 20: every qualifying hit (direct enemy rocket, victim ≥ 96 units above the floor) is emitted, still ordered by `height` descending. The qualification threshold already bounds the list to a handful per match, and a cap keyed on floor height could drop the hits a consumer sorting by `heightAboveAttacker` cares about most. |
| v29 | `AirgibEvent` gains `heightAboveAttacker`: the victim's origin minus the shooter's at the hit (units; negative = victim below the shooter) — the vertical gap the rocket climbed, often the more impressive number for a highlight than the floor height. Computed from the two players' nearest position samples to the hit; `0`/absent when the shooter had no sample within the gap window. Ranking and the ≥ 96 qualification still use the floor height. Additive (`omitempty`). |
| v28 | `PositionTrack` gains an `lq` column: per-sample **liquid state**, packed `(type << 2) \| level` — level 1–3 (feet/waist/eyes submerged, mirroring the engine's `PM_CategorizePosition` probes at z−23 / z+4 / z+22 against the map's render BSP), type 1 water / 2 slime / 3 lava (water 5/6/7, slime 9/10/11, lava 13/14/15; `0` = dry). Decode with `lq & 3` (level) and `lq >> 2` (type). `h` interacts with liquids: a sample in liquid (level ≥ 1) reads `h = 0` by definition, and a dry sample airborne above liquid measures down to the **liquid surface** when it is the highest support beneath the player. Additive (`omitempty`); absent when no BSP is provisioned for the map. |
| v27 | `PositionTrack.H` now stands players on **moving brush-model entities** (lifts, doors, trains): the parser surfaces `"*N"` submodel entities as `MoverSpawn`/`MoverState` events and the floor trace runs over the worldspawn hull **plus** each mover's submodel clip hull posed at its demo-streamed origin for the sample's time (`mapclip.HeightAboveFloorBoxScene`) — the highest floor wins. A player riding the dm2 RA lift reads ~0 instead of the height to the shaft floor, which also removes the false airgib entries rocket hits on lift riders produced (dm2 `path.lift`/`Quad.button`). `NoFloor` narrows accordingly: "on a moving brush model" disappears as a cause, leaving void/pit, embedded and zero origins. Same shape and units; only values over movers change. |
| v26 | `PositionTrack.H` is now measured over the player's **bounding-box footprint** instead of the single origin column: the height is taken to the highest floor found under a 3×3 grid of columns sampled ±8 around the origin (`mapclip.HeightAboveFloorBox`) — an effective ~48-wide footprint on the already-±16-box-inflated hull. A player skimming a ledge / well rim — origin momentarily over the pit while the box overhangs the rim — now reads the near floor (small `h`) rather than plunging to the distant floor far below. Same shape and units; only values near ledges change, which also removes the bogus high airgibs those samples produced (e.g. anwalked RA's well rim logged a 553-unit airgib that was really a rim skim). |
| v25 | `TimelineAnalysis` gains `airgibs[]` (`AirgibEvent`): the top airborne rocket hits for Key Moments — each direct enemy rocket hit (splash excluded) whose victim was ≥ 96 units above the floor, annotated with attacker/victim (name, team, userid), hit time, victim loc and height, raw damage, and lethality (a matching rocket frag near the hit). Derived by a post-processor from `Damage.Events` + the streams' `PositionTrack.H` column + the frag log; capped at top 20 sorted by height descending. Additive (`omitempty`); empty when the map has no clip hull (no `H` column). |
| v24 | `PositionTrack` gains an `h` column: the player's height above the floor directly beneath them at each native-rate sample (feet above the nearest solid surface below), from a straight-down trace through the map's worldspawn player clip hull (parsed from BSP `CLIPNODES` at analyze time by the new `mvd-analytics/mapclip` package; BSPs come from the same best-effort source as the visibility-aware loc filter via the shared `mvd-analytics/mapbsp` loader). Reads ~0 grounded and grows during a jump / airborne hit (airgib); absolute floor is `z − 24 − h` if needed. Sentinel `-2147483648` (`result.NoFloor`) marks samples with no floor to measure from (void/pit, or a moving brush model such as the dm2 lift, which the worldspawn-only hull excludes). Additive (`omitempty`); absent when no BSP is provisioned for the map. |
| v20 | New `Damage` section: per-hit damage log + aggregates (attacker→victim `matrix`, per-weapon, given/taken, and the **EWep** victim-weapon buckets `enemyVsSg/Mid/Lg/Rl/Both` where `ewep=lg+rl+both`) reconstructed from the KTX `mvdhidden_dmgdone` stream, plus a `scoreboard` cross-check vs `demoInfo.players[].dmg`. Amounts are unbound (include overkill). **Positional kills** — telefrags (deathtype `tele`, the 9999 instakill sentinel) and stomps (deathtype `stomp`) — are excluded from all damage figures and surfaced separately as `damage.telefrags`/`damage.stomps` + `PlayerDamage.telefrags`/`.stomps` + the opt-in `telefrag`/`stomp` events. Also a Layer-1 change: world/environmental damage-taken (lava/fall/trigger) is now emitted with an `Attacker == -1` "world" sentinel rather than dropped. Additive (`omitempty`); absent when the demo lacks the KTX hidden-damage stream. |
| v19 | `MatchResult.PlayerStat` gains `kills`, `deaths` and `suicides` — the frag-log-corrected counts, making `match.players` a complete corrected scoreboard rather than just the net frag tally. They supersede the KTX demoinfo `stats`, which credit several self / positional deaths to the wrong entity: pentagram-deflect telefrags (`dtTELE2`) inflate the deflector's kills, and world-dealt suicides (fall / lava / squish / drown) bump the world entity's counter instead of the victim's (`ktx/src/client.c:5132`), so demoinfo undercounts suicides. `0` when the demo carried no frag log. Filled by the `scoreboardStatsPost` post-processor (kills/deaths from `Frags.ByPlayer` joined on the final display name; suicides counted from the `IsSuicide` frag entries). The API `/overview` player rows surface the same `kills`/`deaths`/`suicides`, so non-web consumers get the correction the web Summary already applied. Field additions only. |
| v18 | `TimelineAnalysis` gains `KillEvents`: a per-player enemy-kill stream (`{time, player, team}`) keyed on the killer, parallel to `DeathEvents`, from the canonical frag log filtered to real enemy kills (suicides/teamkills excluded). Cumulative `killEvents` per player reconciles with `frags.byPlayer[].kills` and the kills-based efficiency; the Timeline per-player drill-down plots `killEvents − deathEvents` as a windowed +/-. `team` is best-effort and, unlike `deathEvents`, ungated. Additive (`omitempty`). |
| v17 | Self-kill weapon labels in `Frags.Frags` are no longer flattened to `suicide`: only the `/kill` console command (KTX "X suicides", −2 frags) keeps weapon `suicide`; weapon self-detonations carry their real weapon (`rl`/`gl`/`lg`) with `isSuicide` set. `Frags.ByWeapon` is now enemy kills only (suicides/teamkills excluded). Recovered teamkills no longer carry a stale `isSuicide`. |
| v16 | `PlayerFrags` gains `teamkills` (KTX "tk"), and teamkills whose obituary names only one party re-enter `Frags.Frags` as complete killer↔victim pairs (killer-named recover the victim from the coincident `DeathEvent`; victim-named recover the killer via position co-location + the teamkiller's −1 frag-delta). Brings per-player teamkills to an exact match with KTX's `tk`. |
| v15 | `TimelineAnalysis` gains `DeathEvents`: a per-player death stream (`{time, player, team}`) parallel to `FragEvents`, from the authoritative protocol `DeathEvent` (every death counts once), for the Timeline per-player frags/deaths drill-down and KTX-style efficiency `frags / (frags + deaths)`. Additive (`omitempty`). |
| v14 | `MapEntities` gains **brush entities** — `teleportSrc` (`trigger_teleport`), `button` (`func_button`), `door` (`func_door`) — placed at their BSP submodel bbox centre with a `bounds` (trigger/door volume), plus the teleport source→destination link (`teleportSrc.target` == `teleportDst.targetName`). v13 carried point entities (items, spawns, teleport destinations) only. |
| v13 | New `MapEntities` section: the map's static designed layout (item spawns, player spawnpoints, teleport destinations/sources, buttons) with type + location, from the offline-generated mapents corpus (BSP entity lumps) keyed by map name. Additive (`omitempty`); absent when no corpus exists for the map. |
| v12 | `LocNode` and `LocEdge` gain optional combat-posture weights — `armed` / `unarmed` / `quad` / `pent` breakdowns (`LocWeights` on nodes, `LocEdgeWeights` on edges) restricted to samples where the player held RL or LG, held neither, or had an active quad / pent. Lets consumers re-weight the loc graph by combat posture without re-walking streams. Field additions only; each weight is omitted when no observed sample met its condition. |
| v11 | Bucket views gain a **column-major layout** (`view.ColumnarBuckets`): one dense typed array per `(player, field)` over the player's active span, implicit time axis (`time(i) = startMs + i*windowMs`), a `0`/`1` `alive[]` liveness mask, sparse per-field `validFrom`, booleans/alive as `0`/`1`, loc always the raw `li` index. It is the **default** for the web (`getDefaultBuckets`), REST `/buckets`, and MCP `getBuckets`; the row-major `BucketsView` stays available via `layout=row`. The legacy `HighResBucket`/`HighResPlayerData`/`HighResTeamData` shim and `view.ToLegacyHighResBuckets` are removed. The `Result` **structure is unchanged** — this bump versions the outward *view/query* wire surface so API/MCP/web consumers can feature-detect the new default shape and cached view responses (ETag/`X-Schema-Version`) are invalidated. |
| v10 | DeathEvent / SpawnEvent now derive primarily from the `DF_DEAD` bit in `svc_playerinfo` (broadcast every frame for every player) instead of relying solely on `STAT_HEALTH` crossings (directed at the active POV via `dem_stats`). The stat-based detector still runs and is deduplicated against the new signal — whichever fires first wins. Deaths whose `dem_stats` block was addressed to a different player slot are now captured; `PlayerStream.Spawns`/`Deaths` counts go up for affected demos. Downstream `LocGraph` edges (some spurious `teleport` edges across previously-missed deaths disappear), `LocTrails`, `RegionControl`, `WeaponPickups` (kills-before-next-death windows), and streak boundaries shift accordingly. Field shapes are unchanged. |
| v9 | Loc attribution gains visibility awareness via `mvd-analytics/locvis` (V6: Euclidean primary + PVS-veto). When a per-map BSP is available the analyzer rejects loc-points outside the player's potentially-visible-set, eliminating the brief "wall-bleed" phantom visits V1 produced. Field shapes unchanged: only the contents of `PlayerStream.Loc` (`li`) and everything derived (LocTrails, LocGraph edges, RegionControl) shift for maps with a BSP. Maps without a BSP fall back to V1 — bit-identical to v8 for those. Background: [`experiments/locattr/V2b-V6-HANDOFF.md`](../experiments/locattr/V2b-V6-HANDOFF.md). |
| v8 | All timestamped result fields migrate from `float64` seconds / `float32` seconds to `int32` milliseconds — every `T`/`Time`/`Start`/`End`/`Duration`/`AvailableFrom`/`TakenAt`/`RespawnAt`/`NextDeathTime`/`DropTime` field across the schema (`PositionTrack.T`, `PlayerStream.Spawns`/`Deaths`, `ChangeI16.T`/`ChangeStr.T`, `Interval.Start`/`End`, `GlobalStream.MatchStart`/`End`, `MatchResult.Duration`/`StartTime`/`EndTime`, `TimelineAnalysisResult.MatchStartTime`/`DemoOffset`, frag/powerup/streak/message/frag-entry/backpack/weapon-pickup/item-phase times). JSON keys unchanged; consumers reading as seconds must scale by 1/1000. The view-layer query API still takes and returns `float64` seconds at its public surface. Eliminates the float-precision drift that produced spurious teleport edges in locgraph when a respawn boundary and a position sample shared the same wire timestamp. |
| v7 | `Streams` added as the canonical event-rate storage (per-player change streams + intervals + native-rate position track with parallel `Li` column). `TimelineAnalysisResult.HighResBuckets` and `HighResDuration` removed; bucketed views are now produced on demand by `mvd-analytics/view.Buckets`. `RegionControlResult.BucketStates` removed from the parse-time output (still produced by `view.RegionControl` at the requested resolution). Health / Armor change streams use int16 (Quake values reach 250). `BuildLocGraph` and the region-control classifier (then `analyzer.ComputeRegionControl`, since folded into `view.RegionControl` as the sixth view function) walk `Streams` natively — no bucket intermediate. Default reducer policy is "first-sample-of-bucket" (point-sampling at bucket start; bucket N == state at t = N × windowMs). Bucket grid is anchored at match-relative t = 0; v6 anchored at the wall-clock 50 ms grid post-shifted by `−matchStart`, so the new grid is offset by up to one sample-interval from main's. Discrete event analytics (frags, items, weapon pickups, scoreboard) are byte-identical with v6; locgraph and region-control percentages drift slightly because of the native-rate sampling cadence (~13 ms between position samples vs v6's 50 ms grid). |
| v6 | HighResPlayerData adds `gl`, `sh`, `nl`. HighResTeamData adds `gl`. MatchEvent adds `messageClean`. ControlRegion adds `locs`. RegionControlResult adds `teamA`/`teamB`/`bucketStates`/`stats` + new `RegionStats`. Top-level `duration` removed (use `match.duration`). MatchResult.PlayerStat drops dead `kills`/`deaths`. |
| v5 | WeaponPickups added — slot-weapon acquisitions with kills-before-next-death effectiveness. Backpack pickups carry `backpackEnt` joining to `backpacks[].entNum`. |
| v4 | Backpacks added — RL/LG backpack drops sourced from KTX `//ktx drop` STUFFCMD_DEMOONLY directive. |

`CurrentSchemaVersion` lives at `result/result.go:CurrentSchemaVersion`;
bump when a change breaks consumers of the outward data — either the
`Result` structure **or** the on-demand view/query wire surface
(`/buckets`, `/events`, `/state-at`, …) served identically via
WASM/CLI/API/MCP — and add a row above in the same commit.

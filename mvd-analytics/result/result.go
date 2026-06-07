// Package result defines the stable JSON contract produced by running a
// qwanalytics pipeline over a qwdemo.events.Source. Every analyzer's
// Finalize output is a value defined in this package; the top-level
// Result struct is the aggregate returned by the pipeline.
//
// Consumers of qwanalytics (web UI, CLIs, AI agents) should depend on
// this package directly and stay indifferent to where each sub-result
// is computed. The JSON schema is versioned via SchemaVersion so JS
// callers can feature-detect breaking changes.
package result

// CurrentSchemaVersion identifies the JSON schema shape. Bump on any
// breaking change to the outward data the pipeline serves — both the
// Result structure / its sub-types AND the on-demand view/query wire
// surface (Buckets, Events, StreamSlice, StateAt, LocTrails,
// RegionControl), which is served identically via WASM, CLI, the REST
// API, and MCP. Consumers pin or switch on this value to feature-detect
// breaking changes; it is also the REST API's ETag / X-Schema-Version,
// so a bump invalidates cached view responses.
//
// v4 adds Backpacks: a list of RL/LG backpack drops sourced from
// KTX's //ktx drop STUFFCMD_DEMOONLY directive. Pickup tracking is
// intentionally deferred until the wire-flutter reliability issue
// is resolved — see qwanalytics/analyzer/backpacks.go for the full
// reasoning.
//
// v5 adds WeaponPickups: a list of slot-weapon acquisition events
// (world spawners via //ktx took, RL/LG backpacks via //ktx bp)
// with an effectiveness metric — kills with the weapon before the
// picker's next death. Backpack pickups carry BackpackEnt which pairs
// with Backpacks[i].EntNum so frontends can join drop ↔ pickup.
//
// v6:
//   - HighResPlayerData adds GL, Shells, Nails (sh/nl/gl JSON keys);
//     HighResTeamData adds GL.
//   - MatchEvent adds MessageClean (markup-stripped chat text); raw
//     Message preserved.
//   - RegionControlResult adds explicit Locs[] on each region plus
//     TeamA/TeamB labels, BucketStates (compact one-char-per-bucket)
//     and Stats (match-aggregate percentages) — region control is now
//     computed in Go and re-callable via WASM.
//   - Top-level Result.Duration removed (use Match.Duration or
//     DemoInfo.Duration).
//   - MatchResult.PlayerStat drops dead Kills/Deaths fields (always
//     0; consumers read FragResult.ByPlayer or DemoInfoResult).
//
// v7:
//   - Adds Result.Streams: the canonical event-rate storage for every
//     per-player field (vitals, weapons, powerups, ammo, position,
//     loc, spawns, deaths). Sparse change streams + half-open
//     intervals + columnar position track. See qwanalytics/result/streams.go.
//   - Removes TimelineAnalysisResult.HighResBuckets and
//     TimelineAnalysisResult.HighResDuration. Bucketed data is now
//     produced on demand by qwanalytics/view.Buckets at any window
//     resolution, with per-field reducers selected by the caller.
//   - Removes RegionControlResult.BucketStates from the parse-time
//     output. View-time callers (CLI -view region-control, WASM
//     recomputeRegionControl) get it on demand at the requested
//     resolution.
//   - Health/Armor change streams use int16 (Quake values reach 250,
//     above int8 range).
//
// v8:
//   - PositionTrack.T changes from []float32 seconds to []int32
//     milliseconds. PlayerStream.Spawns / Deaths change from []float64
//     seconds to []int32 milliseconds. JSON keys unchanged; consumers
//     reading these as seconds must scale by 1/1000. The integer-ms
//     unit is what the MVD wire format already carries (1-byte ms
//     delta per message); keeping it integer eliminates the float-
//     precision drift that caused spurious teleport edges in locgraph
//     when a respawn boundary and a position sample shared the same
//     wire timestamp but disagreed by ~1e-6 after float roundtrip.
//   - Other timestamped result fields (ChangeI16.T, Interval.Start/End,
//     MatchEvent.Time, frag/powerup event times) remain float64
//     seconds — they don't participate in the boundary comparison
//     that motivated this change.
//
// v9:
//   - Loc attribution gains visibility awareness (V6 algorithm in
//     mvd-analytics/locvis). When a BSP is available for the demo's
//     map the analyzer rejects candidate loc-points that fall outside
//     the player's potentially-visible-set, eliminating the brief
//     "wall-bleed" phantom loc visits V1's pure-Euclidean nearest-
//     neighbour produced. Maps without a BSP fall back to V1 unchanged.
//     Affected fields: PlayerStream.Loc (li), Backpacks[i].Loc,
//     ItemTimeline[i].Loc, plus everything derived from those
//     (LocTrails, LocGraph edges, RegionControl). Field shapes are
//     unchanged — only the contents shift for maps with BSPs.
//
// v10:
//   - DeathEvent / SpawnEvent gain two new signal sources beyond the
//     v9 StatHealth-crossing detector:
//     1. The DF_DEAD bit in svc_playerinfo (broadcast every frame
//     for every player), captured in mvd-reader/parser/position.go.
//     2. Victim-prefix and infix obituary prints (rocketed by,
//     telefragged by, "Satan's power deflects X's telefrag", the
//     CRMod-added "disembowled" / "shish-kebabed" / etc. set,
//     KTX's k_spawnicide variants) matched in
//     mvd-reader/parser/obituary.go and consumed in parsePrint,
//     gated on a parser-internal match-started flag so warmup
//     obits cannot pre-seed dedup state.
//     The first two sources flow through maybeEmitDeath /
//     maybeEmitSpawn which dedupe against each other. The obit path
//     uses forceEmitDeath instead, bypassing dedup, because KTX's
//     own deathcount (logfrag) can increment without any visible
//     DF_DEAD / stat transition on the wire — the most common case
//     being a Satan-pent deflection (dtTELE2) that fires against a
//     player whose entity state never visibly leaves the previous
//     dead interval. Cross-validated end-to-end against KTX's
//     authoritative demoinfo `stats.deaths` scoreboard. Field shapes
//     are unchanged; PlayerStream.Spawns / Deaths counts rise for
//     affected demos and downstream LocGraph, LocTrails,
//     RegionControl, WeaponPickups, and streak boundaries shift
//     accordingly.
//
// v11:
//   - Bucket views gain a column-major layout (view.ColumnarBuckets):
//     for each (player, field) one dense typed array over the player's
//     active span, with an implicit time axis (time(i) =
//     startMs + i*windowMs), a 0/1 alive[] liveness mask, a sparse
//     per-field validFrom, and booleans/alive emitted as 0/1. It becomes
//     the default for the web (getDefaultBuckets), the REST /buckets
//     endpoint, and MCP getBuckets; the row-major view.BucketsView stays
//     available via layout=row. Columnar always emits the raw loc index.
//   - Removes the legacy HighResBucket / HighResPlayerData /
//     HighResTeamData shim and view.ToLegacyHighResBuckets (the v6 WASM
//     bridge shape). The Result *structure* is unchanged; this bump
//     versions the outward view/query wire surface so API / MCP / web
//     consumers can feature-detect the new default bucket shape and
//     cached view responses are invalidated.
//
// v12:
//   - LocGraph nodes and edges gain optional Armed / Unarmed / Quad / Pent
//     weights: the same Total / ByPlayer / ByTeam time (node) and
//     transition-count (edge) breakdown restricted to samples where the
//     player held RL or LG (Armed), held neither (Unarmed), or had an
//     active Quad / Pent powerup. Additive and backward-compatible (all
//     omitempty), but the bump invalidates cached loc-graph responses so
//     consumers pick them up.
//
// v13:
//   - New MapEntities section: the map's static designed layout (item
//     spawns, player spawnpoints, teleport destinations/sources,
//     buttons) with type + location, sourced from the offline-generated
//     mapents corpus (BSP entity lumps) keyed by map name. Additive
//     (omitempty); absent when no corpus exists for the map.
//
// v14:
//   - MapEntities gains brush entities — teleportSrc (trigger_teleport),
//     button (func_button), door (func_door) — placed at their BSP
//     submodel bbox centre with a Bounds (trigger/door volume), plus the
//     teleport source→destination link via MapEntity.Target ==
//     teleportDst.TargetName. v13 carried point entities only.
//
// v15:
//   - TimelineAnalysis gains DeathEvents: a per-player death stream
//     ({time, player, team}) parallel to FragEvents, sourced from the
//     authoritative protocol DeathEvent (every death counts once), for
//     the Timeline tab's per-player frags/deaths drill-down and KTX-style
//     efficiency = frags/(frags+deaths). Additive and omitempty, but the
//     bump invalidates cached timeline responses so consumers pick it up.
//
// v16:
//   - PlayerFrags gains TeamKills (KTX "tk") and teamkills re-enter
//     Frags.Frags as complete killer↔victim pairs (previously dropped
//     because the obituary names only one party). Killer-named teamkills
//     ("X loses another friend") recover the victim by matching the
//     coincident authoritative DeathEvent on the killer's team;
//     victim-named ones ("X was telefragged by his teammate") recover the
//     killer by combining position co-location with the teamkiller's −1
//     frag-delta. The messages stream also now tags Satan's-power-deflect
//     self-telefrags as frag events (one frag event per death). Additive,
//     but the bump invalidates cached frag responses so consumers pick it
//     up.
//
// v17:
//   - Self-kill weapon labels in Frags.Frags are no longer flattened to
//     "suicide": only the /kill console command ("X suicides", −2 frags)
//     keeps weapon "suicide"; weapon self-detonations now carry their real
//     weapon (rl/gl/lg) with IsSuicide set, matching the messages stream.
//   - Frags.ByWeapon is now enemy kills only (suicides/teamkills excluded),
//     so self-detonations under their real weapon don't inflate kills.
//   - Recovered teamkills no longer carry a stale IsSuicide flag (the "X
//     gets a frag for the other team" case). Bump invalidates cached frag
//     responses so consumers pick up the relabeled weapons.
//
// v18:
//   - TimelineAnalysis gains KillEvents: a per-player enemy-kill stream
//     ({time, player, team}) keyed on the killer, parallel to DeathEvents,
//     sourced from the canonical frag log (FragEntries) and filtered to
//     real enemy kills (suicides/teamkills excluded). Lets the Timeline
//     tab's per-player drill-down plot an exact cumulative kills−deaths
//     +/- that reconciles with byPlayer.kills and kills-based efficiency.
//     Additive and omitempty, but the bump invalidates cached timeline
//     responses so consumers pick it up.
//
// v19:
//   - MatchResult.PlayerStat gains Kills, Deaths and Suicides: the
//     frag-log-corrected counts, so MatchResult.Players is a complete
//     corrected scoreboard rather than just the net frag tally. They
//     supersede the KTX demoinfo stats, which credit several self/positional
//     deaths to the wrong entity — pentagram-deflect telefrags (dtTELE2)
//     inflate the deflector's kills, and world-dealt suicides (fall/lava/
//     squish/drown) bump the world entity's counter, not the victim's
//     (ktx/src/client.c:5132), so demoinfo undercounts suicides. 0 when the
//     demo carried no frag log. The API /overview player rows surface the
//     same Kills/Deaths/Suicides so non-web consumers get the correction the
//     web UI already applied. Field additions only.
//
// v20:
//   - New Damage (DamageResult) section: per-hit damage log + aggregates
//     (attacker→victim matrix, per-weapon, given/taken, and the EWep
//     victim-weapon buckets enemyVsSg/Mid/Lg/Rl/Both where ewep=lg+rl+both)
//     reconstructed from the KTX mvdhidden_dmgdone stream, plus a scoreboard
//     cross-check vs demoInfo.players[].dmg. Positional kills (telefrag,
//     stomp) are excluded from all damage figures and surfaced separately
//     (Damage.Telefrags/Stomps, opt-in telefrag/stomp events). Also a
//     Layer-1 change: world/environmental damage-taken is emitted with an
//     Attacker == -1 "world" sentinel rather than dropped. Additive
//     (omitempty); absent when the demo lacks the KTX hidden-damage stream.
//
// v21:
//   - TimelineAnalysis gains a wall-clock anchor for the demo timeline:
//     demoStartUnixMs (server clock, Unix epoch ms, at demo open / t=0)
//     plus demoStartAccuracyMs (its resolution: 1 from the mvdhidden
//     0x000B millisecond block, 1000 from the whole-second serverinfo
//     `epoch` cvar). With the existing demoOffset, a consumer maps any
//     match-relative game time to wall clock for syncing external data
//     (voice, stream overlays). Additive (omitempty); absent when the
//     demo carries no wall-clock source.
//
// v22:
//   - TimelineAnalysis gains pauses[]: per-pause {atMs, durationMs} segments
//     recovered from the mvdhidden 0x000A (paused_duration) blocks mvdsv
//     embeds once per idle frame while paused. The game clock freezes during
//     a pause, so the v21 wall-clock formula drifted by the total pause time
//     on paused demos; folding Σ durationMs for atMs <= g into the mapping
//     fixes it. The parser now decodes 0x000A (it omits the standard hidden
//     block-length header — a bare type_id+byte — so it is read via a
//     dedicated path). Additive (omitempty); absent when the demo has no
//     pauses or was recorded by a server that does not embed the block.
//
// v23:
//   - Move the wall-clock/timing anchor from timelineAnalysis to
//     streams.global (breaking move, not additive): demoOffset,
//     demoStartUnixMs, demoStartAccuracyMs, pauses now live there next to
//     matchStart/matchEnd (the match window they time). The redundant
//     timelineAnalysis.matchStartTime (always 0, duplicated by
//     streams.global.matchStart) is dropped. timelineAnalysis keeps its
//     event-shaped data + map metadata, including locTable. The /overview
//     REST endpoint gains a `timing` block exposing the wall-clock anchor to
//     REST/MCP consumers (previously only the in-process WASM build could
//     read it).
const CurrentSchemaVersion = 23

// Result is the aggregate output of a qwanalytics pipeline run. Each
// top-level field is produced by one or more analyzers; omitted fields
// mean no analyzer contributed that section (for example, because the
// source lacked the necessary events).
//
// Match length: read MatchResult.Duration (int32 milliseconds,
// parser-derived) or DemoInfoResult.Duration (integer seconds,
// KTX-authoritative).
type Result struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	FilePath         string                  `json:"filePath"`
	Match            *MatchResult            `json:"match,omitempty"`
	Frags            *FragResult             `json:"frags,omitempty"`
	Messages         *MessagesResult         `json:"messages,omitempty"`
	DemoInfo         *DemoInfoResult         `json:"demoInfo,omitempty"`
	TimelineAnalysis *TimelineAnalysisResult `json:"timelineAnalysis,omitempty"`
	Metadata         *MetadataResult         `json:"metadata,omitempty"`
	LocGraph         *LocGraphResult         `json:"locGraph,omitempty"`
	Items            *ItemsResult            `json:"items,omitempty"`
	Damage           *DamageResult           `json:"damage,omitempty"`
	MapEntities      *MapEntitiesResult      `json:"mapEntities,omitempty"`
	Backpacks        []BackpackDrop          `json:"backpacks,omitempty"`
	WeaponPickups    []WeaponPickup          `json:"weaponPickups,omitempty"`
	Streams          *Streams                `json:"streams,omitempty"`
	Errors           []string                `json:"errors,omitempty"`
}

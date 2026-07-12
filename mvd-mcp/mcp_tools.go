package main

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools wires every MCP tool onto the given server. Tools
// that act on a single demo go through `b` (the mvd-api proxy);
// `searchGames` goes through `sr` (a direct hub.quakeworld.nu
// Supabase client — discovery is the hub's responsibility, not
// mvd-api's).
//
// All view-shaped tool outputs are opaque JSON pass-through; only
// LoadDemo is typed, because consumers need to extract `demoId`
// from its result.
func registerTools(s *mcp.Server, b MCPBackend, sr searcher) {
	addTool(s, &mcp.Tool{
		Name:        "searchGames",
		Description: "Search hub.quakeworld.nu for matches by player names, teams, map, mode, matchtag, or date range. Returns {limit, offset, count, total?, games}: count = rows in THIS page, total = all matching rows (when the hub reports it) — page with limit/offset until offset+count >= total. Rows are compact by default (players projected to {name, team, frags}); roster:true returns the verbatim hub rows (ping, colors). Analysis tools accept gameId:N directly and auto-load on first use — loadDemo is an optional warm-up, not a required step.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchGamesInput) (*mcp.CallToolResult, any, error) {
		out, err := sr.Search(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "loadDemo",
		Description: "Resolve, fetch, parse, and cache a demo on the mvd-api. Returns the demoId (sha:HEX) to use with subsequent tool calls. Idempotent — cheap on warm cache.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in LoadDemoInput) (*mcp.CallToolResult, *LoadDemoOutput, error) {
		out, err := b.LoadDemo(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getOverview",
		Description: "Return a curated summary of the demo (map, teams, top streaks, top powerups). Who won: `teams` and `players` are sorted by frags descending, so teams[0] (team modes) / players[0] (duel/FFA) is the winner — frags is the canonical net score. Also carries `errors`: the analyzer's non-fatal errors — if non-empty the result is degraded (some sections may be missing/partial), so check it before trusting detail views. Use this first to decide which detailed view to query next. UNITS SEAM: overview times (duration, matchStart/End, topStreaks/topPowerups start+duration) are integer MILLISECONDS, while every tool's startTime/endTime/time input is match-relative SECONDS — divide by 1000 when carrying an overview time into a filter. Response shape: see mvd-api /v1/demos/{id}/overview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetOverviewInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetOverview(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getDemoInfo",
		Description: "KTX demoinfo blob — per-player weapon accuracy (hits/fires), kills/deaths/TK, damage dealt/taken, sprees, control time, item pickup counts, RL/LG transfers. Authoritative KTX scoreboard. Errors if the demo has no KTX demoinfo (rare; non-KTX servers or aborted matches).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetDemoInfoInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetDemoInfo(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getMetadata",
		Description: "Server cvars + KTX match settings: mode, timelimit, fraglimit, antilag, spawnmodel (k_spw), midair, instagib, overtime, powerups, noitems, vwep, noweapon, matchtag. Plus the full fullserverinfo cvar dump (hostname, version, watervis, dmgfrags, etc.). Used to answer 'what ruleset was this played under'.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMetadataInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetMetadata(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getFrags",
		Description: "Frag aggregates + full kill log. totalFrags + byPlayer (kills/deaths/byWeapon per player) + byWeapon (kills per weapon) + frags (every kill with time/killer/victim/weapon/isSuicide/isTeamKill). NOTE: unlike getDamage/getAim/getItems, summary defaults FALSE here — the kill log is small (one row per frag) and usually the point; pass summary:true to drop it. Optional players= / weapons= filters narrow both aggregates and log. Empty-log convention: frags is null when dropped by summary=true, [] when included but the filter matched nothing. Use this instead of aggregating getEvents(types:['frag']) yourself.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetFragsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetFrags(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getDamage",
		Description: "Per-hit damage aggregates + log, reconstructed from the KTX damage stream. TWO damage families: 'raw' = the unbound wire values (overkill included, so totals run HIGHER than the KTX scoreboard, which caps damage to victim health); 'bounded' = KTX-scoreboard semantics reconstructed per hit (armor absorbed + health capped to the victim's remaining health), so per-player given/taken land near-equal to demoInfo dmg. Pick a family with dmg=raw|bounded|both. Default: an unset dmg resolves to BOUNDED for BOTH the summary (this layer's default) and the full log — the KTX-scoreboard number a reader usually wants. raw (the lean unbound v53 shape) and both (raw fields PLUS a per-player `bounded` nest and a per-event `bounded`) stay explicit opt-ins; a defaulted summary response carries a hint — pass summary:false for the per-hit events log (same bounded family). On a bounded/both SUMMARY the per-player bounded figures are sourced from the EXACT KTX end-of-match scoreboard when the demo carries demoInfo (boundedSource echoes 'ktx' vs 'reconstructed'); `taken` and the enemyVs* buckets stay reconstructed (KTX has no matching split), so on a KTX-sourced summary they may not sum exactly to given. An EXPLICIT dmg=bounded errors (bounded_unavailable) on midair / instagib / dmgfrags demos, whose server mode rewrites the take in ways the wire doesn't expose; a DEFAULTED bounded there falls back to raw instead. boundedMode echoes 'standard' or 'skipped:midair|instagib|dmgfrags', and every bounded field is absent when skipped. The per-event `bounded` is OMITTED when it equals the raw damage (the common no-overkill case; note 0 is a real value — a fully-nullified pent/teamplay hit). Optional players= / weapons= / startTime/endTime filters; any filter recomputes every aggregate from the filtered log. Positional instant kills — telefrags (the 9999 instakill sentinel) and stomps (head-stomps) — are EXCLUDED from byWeapon/matrix/totalDamage and listed separately (telefrags/stomps lists + per-player counts), BUT their damage DOES fold into given/givenTeam/taken — and, for enemy kills, the EWep victim-weapon buckets — in both families, matching KTX; each telefrags[]/stomps[] entry carries that folded value as `bounded` (and `damage` when the raw fold differs). On skipped:* demos NO fold happens at all — the raw family reverts to pure exclusion. Shape vocabulary: totalDamage + byWeapon (enemy damage per attacker weapon) + byPlayer (given (to enemies), taken (all sources), givenTeam, givenSelf, takenEnv, byWeapon, and the EWep victim-weapon buckets enemyVsSg/enemyVsMid/enemyVsLg/enemyVsRl/enemyVsBoth where ewep=lg+rl+both = damage to enemies holding RL/LG) + matrix (attacker->victim totals) + scoreboard (stream-vs-KTX cross-check; carries a bounded side for the like-for-like compare). Empty-log convention: events is null when dropped by summary, [] when included but the filter matched nothing. Related: getEvents(types:['damage']) is the raw hit feed; getEvents(types:['telefrag','stomp']) the positional-kill events.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetDamageInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetDamage(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getAim",
		Description: "Per-player aim analysis. summary defaults TRUE at this layer (the weapons aggregates only; the response carries a hint) — pass summary:false for the large per-fire crosshair + lgRamp columnar blocks. players scopes to named shooters; startTime/endTime (match-relative seconds) recompute aim over the shots in that window so every figure scopes to it (no window = the stored match-wide aim). First call on a demo may be slow (builds the projectile/beam streams). Shape vocabulary: players[].weapons is a deliberately ORDERED array of per-weapon entries keyed by each entry's weapon field (not a {weapon: …} object like byWeapon on getFrags/getDamage) — per-weapon shots/hits plus SG/SSG pellet stats (pellets, pelletHits, full/partial/miss fires), RL/GL direct/splash/missed, and the LG miss split (miss = aim error, no enemy on the beam's line; blocked = the beam would have hit an enemy in range but an object stopped it short; outOfRange = the enemy was on the beam's line but beyond its ~600u reach). hits count ALL victims (team + self hits included — server parity); the enemy/team/self objects slice the hit counters by victim class and appear only when a weapon had team or self hits (enemy absent = every hit was an enemy hit; a rocket jump is a self hit). mode notes attribution quality: 'duel' (exact, one enemy) or 'team' (nearest-crosshair-enemy heuristic); only enemies alive at fire time are miss candidates. Full detail (summary:false): crosshair = per-hitscan-fire angular error (signed degrees + normalized so ±1 = the hitbox edge, hit flag, attributed target, team flag on teammate targets); lgRamp = per-LG-cell hit vs ms since the shaft opened. Response shape: see mvd-api /v1/demos/{id}/aim.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetAimInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetAim(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getLocGraph",
		Description: "Per-map adjacency graph of named locations: which locs are reachable from which, with edge weights derived from per-player loc-to-loc transitions. Useful for movement-pattern reasoning ('what's adjacent to RA?').",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocGraphInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocGraph(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getChat",
		Description: "All-chat and team-chat messages within an optional time window. Returns {messages:[...]} where each message has time, type ('chat' or 'teamsay'), player, team, message (raw with ezQuake markup), messageClean (markup stripped). Cheaper and shape-cleaner than getEvents(types:['chat']) when you only want chat.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetChatInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetChat(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getBackpacks",
		Description: "RL/LG backpack drops emitted by KTX's //ktx drop hint. Returns {backpacks:[...]} where each entry carries time, dropper, weapon ('rl'/'lg'), origin XYZ, resolved loc, and the server ent number that joins to getWeaponPickups (the grab appears there as source:'backpack' with the same ent). Division of labour across the item trio: getItems = every world spawner's availability/take timeline; getWeaponPickups = who acquired slot weapons and their effectiveness; getBackpacks = the RL/LG drops that feed backpack grabs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetBackpacksInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetBackpacks(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getItems",
		Description: "Per-item pickup/respawn timeline, returned as {items:[...]}. Each item has a unique name (suffixed when a map has several of a type: ya_1, ya_2, mh_1), a kind token (ra/ya/ga/mh/quad/pent/ring/rl/lg/ssg/sng/ng/gl/h15/h25/nails/shells/rockets/cells), world position + nearest loc, and a phases list — when it became available, when it was taken (if at all), by whom, when it respawned. Filters (case-insensitive): items= matches a name or kind ('ya' → both yellow armors, 'ya_1' → one, 'RA'/'MH'/'Quad' all work); kinds= matches a category (armor, mega, health, powerup, weapon, ammo); players= keeps only phases taken by those players. summary defaults TRUE at this layer (per-item take counts + first take; the response carries a hint) — pass summary:false for the full phase timeline. Division of labour across the item trio: this tool covers world spawners (armors, megas, powerups, weapons — which YA was taken, when, by whom); getWeaponPickups adds the acquirer's outcome and backpack grabs (which never flip a world spawner's state, so they are NOT phases here); getBackpacks lists the RL/LG drops themselves.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetItemsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetItems(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getMapEntitiesByMap",
		Description: "The map's static designed layout (NOT this match) — every item spawn, player spawnpoint, teleport destination/source, and button — addressed by map name directly (e.g. 'dm6'), no demo needed. Returns {map, entities:[...]}: each entity has a type (item/spawn/teleportDst/teleportSrc/button/door), raw BSP class, a loc-based name, world position XYZ, nearest loc, and (for items) a kind token. Sourced from the BSP entity corpus, identical for every demo on the map. Get the map name from getOverview; use getItems for the per-match pickup timeline. Filters (case-insensitive): types=, kinds= (category or raw kind).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMapEntitiesByMapInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetMapEntitiesByMap(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getWeaponPickups",
		Description: "Slot-weapon acquisitions (world spawners + RL/LG backpacks) with kills-before-next-death effectiveness. Returns {pickups:[...]} where each pickup carries time, player, weapon, source ('world'/'backpack'), kills earned, next death time. Backpack pickups also carry the dropper and the ent number joining to getBackpacks. Division of labour across the item trio: getItems has the spawner-side timeline (which spawner, when available/taken); this tool has the acquirer's outcome incl. backpack grabs; getBackpacks the RL/LG drops.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetWeaponPickupsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetWeaponPickups(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getBuckets",
		Description: "Bucketed per-player time series over the match (health, armor, weapons, powerups, ammo, position, loc). Choose a windowMs that matches your query resolution. layout='column' (the default — compact, far fewer tokens) returns the column-major shape: top-level windowMs/startMs/count, then players[name] with first/n, an alive 0/1 mask over [first,first+n), and one array per field where the value at index i is bucket i and time(i)=startMs+i*windowMs (booleans are 0/1; loc is the raw 'li' index, decoded via the response's own locTable legend — no getLocTable call needed); teams[name] has per-field count arrays. layout='row' returns one self-describing object per bucket (handy for a single snapshot). For point-in-time snapshots (\"who had quad at 5:00\") use getStateAt instead — don't align indices across columnar arrays. For life counts / death timing use getEvents (the bucketed d/sp markers are per-window booleans, not exact). Keep payloads small with fields/players/from/to. Response shape: see mvd-api /v1/demos/{id}/buckets.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetBucketsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetBuckets(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getEvents",
		Description: "Time-ordered discrete events (frags, powerups, streaks, spawns, deaths, pickups, chat). Default types exclude high-frequency change events (health/armor/loc). pickup events carry full identity in two shapes: world-spawner takes detail{item (per-spawner name, ya_1 vs ya_2), kind, entNum, loc?, source:'world'}; backpack/unknown weapon grants detail{item, kind, source, entNum? (backpack edict), dropper?} (no loc) — no cross-referencing needed to learn which spawner was taken. spawn events include the match-start spawn (t=0) and carry detail{loc} — types:['spawn'] with endTime:1 answers \"where did everyone start\"; for the pre-joined opening summary use getArtifact('opening'). Response shape: see mvd-api /v1/demos/{id}/events.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetEventsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetEvents(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getStreamSlice",
		Description: "Raw native-rate change entries for each requested field inside a REQUIRED time window (startTime and/or endTime — an unwindowed slice would be the biggest payload this service can emit; keep windows tens of seconds). Right shape for inspecting a short event in detail (carry-forward at window start; intervals clamped to window). Health entries are the authoritative wire values: a negative value is the killing blow's overkill remainder (the player died at that entry) — for death events themselves prefer the d/sp streams or getEvents.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetStreamSliceInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetStreamSlice(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getStateAt",
		Description: "Point-in-time state per player at a given match-relative time. Carry-forward for change streams; nearest-sample for position; interval membership for held items/powerups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetStateAtInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetStateAt(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getLocTrails",
		Description: "Per-player sequence of loc residences with dwell durations. minDwellMs filters nearest-loc flicker (MCP default 250; pass 0 for raw). Each residence is a resolved loc name by default; pass loc='index' for raw LocTable indices (decode via getLocTable).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocTrailsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocTrails(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getLocTable",
		Description: "The demo's interned loc-name table: a string array where index i is the loc name (index 0 = '' no-loc sentinel). Only needed when you've requested loc='index' on another tool and want to decode the raw `li` integers back to names. In the default 'name' mode you never need this.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocTableInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocTable(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getRegionControl",
		Description: "Per-region territorial control over the match. Returns per-bucket state strings + match-aggregate percentages per region.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetRegionControlInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetRegionControl(ctx, in)
		return toolResult(out, err)
	})

	// Generic DAG-artifact tools (Stage 4). The curated tools above stay the
	// product surface — their hand-written descriptions and filter params give
	// each section rich ergonomics. These two add a generic escape hatch: any
	// NEW servable artifact becomes reachable via getArtifact with ZERO new
	// hand-written tools. Discover names + shapes with listArtifacts first.
	addTool(s, &mcp.Tool{
		Name:        "listArtifacts",
		Description: "List every FETCHABLE analytics artifact (name, resultKey, cost light/heavy, lazy flag, description) — the discovery layer for getArtifact. Trimmed at the MCP layer to servable artifacts and routing-relevant fields; the full DAG (requires/provides edges, internal nodes) lives in REST /v1/artifacts and /v1/graph. Static per schema version. For the common sections prefer the dedicated tools (getFrags, getDamage, getAim, ...) which offer filters; heed per-artifact size notes in descriptions (e.g. timeline is LARGE).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListArtifactsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.ListArtifacts(ctx, in)
		return toolResult(out, err)
	})

	addTool(s, &mcp.Tool{
		Name:        "getArtifact",
		Description: "Fetch one servable analytics artifact for a demo by its manifest name (from listArtifacts), e.g. frag, damage, loc-graph, opening, los. opening is the match-opening summary: each player's match-start spawn loc + the first take of every contested spawner (armors, mega, powerups, RL/LG) — the one-call answer to opening-race questions. Returns the artifact's Result section under its resultKey (los is materialised on demand; the first call may be slow). Takes no filters — for filtered/parameterised reads use the curated tools (getFrags players=..., getBuckets windowMs=..., etc.). Unknown or non-servable names error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetArtifactInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetArtifact(ctx, in)
		return toolResult(out, err)
	})
}

// addTool wraps mcp.AddTool, supplying an input schema with the spurious
// "null" that jsonschema-go adds to every nilable Go slice/map stripped out.
// Left in, an array field like `players` reflects to
// {"type":["null","array"]}; several MCP clients treat that union as "no
// type" and coerce the value to a string, silently breaking every array
// filter (players/fields/types/weapon/items/kinds and the reducers map). A
// plain {"type":"array"} is accepted. The handler still decodes into the
// concrete In struct, so the Go side is unchanged.
func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if t.InputSchema == nil {
		t.InputSchema = inputSchema[In]()
	}
	if t.Annotations == nil {
		// Every mvd tool reads/analyzes demos; none mutate any user-facing
		// state (loadDemo only warms a transparent, reconstructible cache). The
		// readOnlyHint lets clients that honor it cut the per-call approval
		// prompt — the analytics surface is safe to run unattended.
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	mcp.AddTool(s, t, h)
}

// inputSchema reflects In's JSON schema and strips the null unions (see
// addTool). An `any` input has no object schema to reflect, so it gets a bare
// object, matching mcp.AddTool's own any-handling (and its object-type
// requirement).
func inputSchema[In any]() *jsonschema.Schema {
	if reflect.TypeFor[In]() == reflect.TypeFor[any]() {
		return &jsonschema.Schema{Type: "object"}
	}
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("inputSchema: reflect %v: %v", reflect.TypeFor[In](), err))
	}
	stripNullTypes(s)
	return s
}

// stripNullTypes collapses every ["null", …] type union in the schema tree by
// dropping the "null" alternative. jsonschema-go emits it for nilable kinds
// (slices, maps, pointers); for a tool input an omitted optional field is
// already fine, so the null is noise that some clients mishandle.
func stripNullTypes(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) > 0 {
		kept := make([]string, 0, len(s.Types))
		for _, t := range s.Types {
			if t != "null" {
				kept = append(kept, t)
			}
		}
		switch {
		case len(kept) == 1:
			s.Type, s.Types = kept[0], nil
		case len(kept) != len(s.Types):
			s.Types = kept
		}
	}
	for _, p := range s.Properties {
		stripNullTypes(p)
	}
	stripNullTypes(s.Items)
	stripNullTypes(s.AdditionalProperties)
	for _, p := range s.PrefixItems {
		stripNullTypes(p)
	}
}

// toolResult adapts (Out, error) to the SDK's tool-handler return
// triplet. On error, returns an isError tool result with the error
// text in TextContent. On success, returns the typed output — the SDK
// serialises it into structuredContent.
func toolResult[Out any](out Out, err error) (*mcp.CallToolResult, Out, error) {
	if err != nil {
		var zero Out
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%v", err)}},
		}, zero, nil
	}
	return nil, out, nil
}

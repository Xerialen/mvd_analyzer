package main

import (
	"context"
	"fmt"

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
	mcp.AddTool(s, &mcp.Tool{
		Name:        "searchGames",
		Description: "Search hub.quakeworld.nu for matches by player names, teams, map, mode, matchtag, or date range. Returns identity + lightweight match summary (gameId, sha256, map, teams w/ scores + rosters, timestamp). Use this first to find demos, then call loadDemo for any you want analyzed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchGamesInput) (*mcp.CallToolResult, any, error) {
		out, err := sr.Search(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "loadDemo",
		Description: "Resolve, fetch, parse, and cache a demo on the mvd-api. Returns the demoId (sha:HEX) to use with subsequent tool calls. Idempotent — cheap on warm cache.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in LoadDemoInput) (*mcp.CallToolResult, *LoadDemoOutput, error) {
		out, err := b.LoadDemo(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getOverview",
		Description: "Return a curated summary of the demo (map, teams, top streaks, top powerups). Also carries `errors`: the analyzer's non-fatal errors — if non-empty the result is degraded (some sections may be missing/partial), so check it before trusting detail views. Use this first to decide which detailed view to query next. Response shape: see mvd-api /v1/demos/{id}/overview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetOverviewInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetOverview(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getDemoInfo",
		Description: "KTX demoinfo blob — per-player weapon accuracy (hits/fires), kills/deaths/TK, damage dealt/taken, sprees, control time, item pickup counts, RL/LG transfers. Authoritative KTX scoreboard. Errors if the demo has no KTX demoinfo (rare; non-KTX servers or aborted matches).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetDemoInfoInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetDemoInfo(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getMetadata",
		Description: "Server cvars + KTX match settings: mode, timelimit, fraglimit, antilag, spawnmodel (k_spw), midair, instagib, overtime, powerups, noitems, vwep, noweapon, matchtag. Plus the full fullserverinfo cvar dump (hostname, version, watervis, dmgfrags, etc.). Used to answer 'what ruleset was this played under'.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMetadataInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetMetadata(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getFrags",
		Description: "Frag aggregates + full kill log. totalFrags + byPlayer (kills/deaths/byWeapon per player) + byWeapon (kills per weapon) + frags (every kill with time/killer/victim/weapon/isSuicide/isTeamKill). Optional players= / weapon= filters narrow both aggregates and log. Use this instead of aggregating getEvents(types:['frag']) yourself.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetFragsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetFrags(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getDamage",
		Description: "Per-hit damage aggregates + log, reconstructed from the KTX damage stream. totalDamage + byWeapon (enemy damage per attacker weapon) + byPlayer (per player: given (to enemies), taken (all sources), givenTeam, givenSelf, takenEnv, byWeapon, the EWep victim-weapon buckets enemyVsSg/enemyVsMid/enemyVsLg/enemyVsRl/enemyVsBoth where ewep=lg+rl+both = damage to enemies holding RL/LG, plus telefrags + stomps dealt) + matrix (attacker->victim totals) + telefrags + stomps (positional instant kills, separate from damage) + scoreboard (stream vs KTX-scoreboard cross-check). NOTE: damage is UNBOUND (includes overkill), so totals run higher than the KTX scoreboard which is bounded to victim health. Positional kills — telefrags (the 9999 instakill sentinel) and stomps (head-stomps) — are EXCLUDED from all damage figures and listed separately. Optional players= / weapon= filters. Use this instead of aggregating getEvents(types:['damage']); use getEvents(types:['damage']) for the raw per-hit log and getEvents(types:['telefrag','stomp']) for those kill events.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetDamageInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetDamage(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getLocGraph",
		Description: "Per-map adjacency graph of named locations: which locs are reachable from which, with edge weights derived from per-player loc-to-loc transitions. Useful for movement-pattern reasoning ('what's adjacent to RA?').",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocGraphInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocGraph(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getChat",
		Description: "All-chat and team-chat messages within an optional time window. Returns {messages:[...]} where each message has time, type ('chat' or 'teamsay'), player, team, message (raw with ezQuake markup), messageClean (markup stripped). Cheaper and shape-cleaner than getEvents(types:['chat']) when you only want chat.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetChatInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetChat(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getBackpacks",
		Description: "RL/LG backpack drops emitted by KTX's //ktx drop hint. Returns {backpacks:[...]} where each entry carries time, dropper, weapon ('rl'/'lg'), origin XYZ, resolved loc, and the server ent number that joins to weapon-pickups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetBackpacksInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetBackpacks(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getItems",
		Description: "Per-item pickup/respawn timeline, returned as {items:[...]}. Each item has a unique name (suffixed when a map has several of a type: ya_1, ya_2, mh_1), a kind token (ra/ya/ga/mh/quad/pent/ring/rl/lg/ssg/sng/ng/gl/h15/h25/nails/shells/rockets/cells), world position + nearest loc, and a phases list — when it became available, when it was taken (if at all), by whom, when it respawned. Filters (case-insensitive): items= matches a name or kind ('ya' → both yellow armors, 'ya_1' → one, 'RA'/'MH'/'Quad' all work); kinds= matches a category (armor, mega, health, powerup, weapon, ammo); players= keeps only phases taken by those players.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetItemsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetItems(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getMapEntitiesByMap",
		Description: "The map's static designed layout (NOT this match) — every item spawn, player spawnpoint, teleport destination/source, and button — addressed by map name directly (e.g. 'dm6'), no demo needed. Returns {map, entities:[...]}: each entity has a type (item/spawn/teleportDst/teleportSrc/button/door), raw BSP class, a loc-based name, world position XYZ, nearest loc, and (for items) a kind token. Sourced from the BSP entity corpus, identical for every demo on the map. Get the map name from getOverview; use getItems for the per-match pickup timeline. Filters (case-insensitive): types=, kinds= (category or raw kind).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMapEntitiesByMapInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetMapEntitiesByMap(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getWeaponPickups",
		Description: "Slot-weapon acquisitions (world spawners + RL/LG backpacks) with kills-before-next-death effectiveness. Returns {pickups:[...]} where each pickup carries time, player, weapon, source ('world'/'backpack'), kills earned, next death time. Backpack pickups also carry the dropper and the joining ent number.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetWeaponPickupsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetWeaponPickups(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getBuckets",
		Description: "Bucketed per-player time series over the match (health, armor, weapons, powerups, ammo, position, loc). Choose a windowMs that matches your query resolution. layout='column' (the default — compact, far fewer tokens) returns the column-major shape: top-level windowMs/startMs/count, then players[name] with first/n, an alive 0/1 mask over [first,first+n), and one array per field where the value at index i is bucket i and time(i)=startMs+i*windowMs (booleans are 0/1, loc is the raw 'li' index); teams[name] has per-field count arrays. layout='row' returns one self-describing object per bucket (handy for a single snapshot). For point-in-time snapshots (\"who had quad at 5:00\") use getStateAt instead — don't align indices across columnar arrays. For life counts / death timing use getEvents (the bucketed d/sp markers are per-window booleans, not exact). Keep payloads small with fields/players/from/to. Response shape: see mvd-api /v1/demos/{id}/buckets.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetBucketsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetBuckets(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getEvents",
		Description: "Time-ordered discrete events (frags, powerups, streaks, spawns, deaths, chat). Default types exclude high-frequency change events (health/armor/loc). Response shape: see mvd-api /v1/demos/{id}/events.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetEventsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetEvents(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getStreamSlice",
		Description: "Raw native-rate change entries for each requested field inside a time window. Right shape for inspecting a short event in detail (carry-forward at window start; intervals clamped to window).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetStreamSliceInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetStreamSlice(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getStateAt",
		Description: "Point-in-time state per player at a given match-relative time. Carry-forward for change streams; nearest-sample for position; interval membership for held items/powerups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetStateAtInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetStateAt(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getLocTrails",
		Description: "Per-player sequence of loc residences with dwell durations. Use minDwellMs to filter nearest-loc flicker. Each residence is a resolved loc name by default; pass loc='index' for raw LocTable indices (decode via getLocTable).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocTrailsInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocTrails(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getLocTable",
		Description: "The demo's interned loc-name table: a string array where index i is the loc name (index 0 = '' no-loc sentinel). Only needed when you've requested loc='index' on another tool and want to decode the raw `li` integers back to names. In the default 'name' mode you never need this.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLocTableInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetLocTable(ctx, in)
		return toolResult(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "getRegionControl",
		Description: "Per-region territorial control over the match. Returns per-bucket state strings + match-aggregate percentages per region.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetRegionControlInput) (*mcp.CallToolResult, any, error) {
		out, err := b.GetRegionControl(ctx, in)
		return toolResult(out, err)
	})
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

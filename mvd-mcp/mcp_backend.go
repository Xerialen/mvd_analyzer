package main

import "context"

// MCPBackend is the contract every MCP tool handler depends on. The
// proxy backend below implements it by forwarding HTTP calls to a
// running mvd-api. By design the shim is independent of mvd-analytics
// — view-shaped outputs are passed through as opaque JSON (`any`) so
// the binary stays small and the wire contract is owned by mvd-api.
type MCPBackend interface {
	LoadDemo(ctx context.Context, in LoadDemoInput) (*LoadDemoOutput, error)
	GetOverview(ctx context.Context, in GetOverviewInput) (any, error)
	GetDemoInfo(ctx context.Context, in GetDemoInfoInput) (any, error)
	GetMetadata(ctx context.Context, in GetMetadataInput) (any, error)
	GetFrags(ctx context.Context, in GetFragsInput) (any, error)
	GetDamage(ctx context.Context, in GetDamageInput) (any, error)
	GetAim(ctx context.Context, in GetAimInput) (any, error)
	GetLocGraph(ctx context.Context, in GetLocGraphInput) (any, error)
	GetChat(ctx context.Context, in GetChatInput) (any, error)
	GetBackpacks(ctx context.Context, in GetBackpacksInput) (any, error)
	GetItems(ctx context.Context, in GetItemsInput) (any, error)
	GetMapEntitiesByMap(ctx context.Context, in GetMapEntitiesByMapInput) (any, error)
	GetWeaponPickups(ctx context.Context, in GetWeaponPickupsInput) (any, error)
	GetBuckets(ctx context.Context, in GetBucketsInput) (any, error)
	GetEvents(ctx context.Context, in GetEventsInput) (any, error)
	GetStreamSlice(ctx context.Context, in GetStreamSliceInput) (any, error)
	GetStateAt(ctx context.Context, in GetStateAtInput) (any, error)
	GetLocTrails(ctx context.Context, in GetLocTrailsInput) (any, error)
	GetLocTable(ctx context.Context, in GetLocTableInput) (any, error)
	GetRegionControl(ctx context.Context, in GetRegionControlInput) (any, error)
	ListArtifacts(ctx context.Context, in ListArtifactsInput) (any, error)
	GetArtifact(ctx context.Context, in GetArtifactInput) (any, error)
}

// --- Tool input/output structs ---
//
// Each Input mirrors the corresponding REST query params (the API is
// the source of truth for parameter names and defaults). LoadDemoOutput
// is the one structured output kept here, since the model needs the
// resolved demoId from it to drive subsequent tool calls.

// LoadDemoInput identifies a demo by exactly one of GameID or SHA256.
type LoadDemoInput struct {
	GameID int    `json:"gameId,omitempty" jsonschema:"hub.quakeworld.nu game id (exactly one of gameId or sha256 required)"`
	SHA256 string `json:"sha256,omitempty" jsonschema:"SHA-256 of a previously-resolved demo (mostly for bookmarking warm cache entries)"`
}

// LoadDemoOutput mirrors POST /v1/demos/{id} on mvd-api.
type LoadDemoOutput struct {
	DemoID        string `json:"demoId"`
	SHA256        string `json:"sha256"`
	FromCache     bool   `json:"fromCache"`
	SchemaVersion int    `json:"schemaVersion"`
}

// GetOverviewInput is just a demoId reference (gameId:N or sha:HEX).
type GetOverviewInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id from loadDemo: 'gameId:NNNN' or 'sha:HEX'"`
}

// GetBucketsInput mirrors /v1/demos/{id}/buckets query params.
type GetBucketsInput struct {
	DemoID      string            `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	WindowMs    int               `json:"windowMs,omitempty" jsonschema:"bucket size in MILLISECONDS (startTime/endTime are seconds!); default 5000 (5 s) — right for trends/control questions; pass 1000 or finer only when the question needs it (50 ms produces tens of thousands of buckets per match)"`
	StartTime   float64           `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds"`
	EndTime     float64           `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
	Players     []string          `json:"players,omitempty"`
	Fields      []string          `json:"fields,omitempty" jsonschema:"field codes: h=health, a=armor, at=armorType, li=location (NOTE: the selector code is li; the loc= param picks how it renders — name under output key 'loc' (default) or raw index under 'li'), pos, view, hgt, lq, vel, rl/lg/gl/ssg/sng (held weapons), q/pe/r (powerups), sh/nl/rk/cl (ammo), sp/d (spawn/death events). Empty = all standard fields; an unknown code errors with the full list"`
	Reducers    map[string]string `json:"reducers,omitempty" jsonschema:"per-field reducer override, e.g. {\"h\":\"min\"}"`
	IncludeTeam bool              `json:"includeTeam,omitempty"`
	Loc         string            `json:"loc,omitempty" jsonschema:"loc representation: 'name' (default, resolved loc names) or 'index' (raw LocTable indices; decode via getLocTable). Ignored for layout=column, which always returns raw 'li' indices"`
	Layout      string            `json:"layout,omitempty" jsonschema:"'column' (default) returns the compact column-major shape: per (player,field) one array indexed by bucket, where time(i)=startMs+i*windowMs — best for time-series/trend questions (far fewer tokens). 'row' returns one self-describing object per bucket. For point-in-time snapshots use getStateAt instead"`
}

// GetEventsInput mirrors /v1/demos/{id}/events query params.
type GetEventsInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	StartTime float64  `json:"startTime,omitempty"`
	EndTime   float64  `json:"endTime,omitempty"`
	Players   []string `json:"players,omitempty"`
	Types     []string `json:"types,omitempty" jsonschema:"event types. Default set (when empty): frag, powerup, streak, spawn, death, weapon, item, chat, pickup. Opt-in (pass explicitly): loc, health, armor, damage, telefrag, stomp. pickup = identity-rich takes: world takes detail{item, kind, entNum, loc?, source:'world'}, backpack/unknown grants detail{item, kind, source, entNum?, dropper?} (no loc); weapon/item = held-interval gain/lose (the holding story). spawn carries detail{loc} and includes the synthesized match-start spawn at t=0. A damage event carries detail{victim, damage, weapon, isSplash?, ...}; telefrag/stomp carry detail{victim, isTeam?} with player = the killer (the kill is already in the frag feed, hence opt-in)"`
	Loc       string   `json:"loc,omitempty" jsonschema:"loc-event representation: 'name' (default) or 'index' (raw LocTable index; decode via getLocTable)"`
}

// GetStreamSliceInput mirrors /v1/demos/{id}/stream-slice query params.
type GetStreamSliceInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	StartTime float64  `json:"startTime,omitempty" jsonschema:"window start, match-relative seconds. The MCP layer REQUIRES at least one of startTime/endTime: an unwindowed slice is native-rate change entries for the whole match (the biggest payload this service can emit). REST /stream-slice stays unwindowed for programs."`
	EndTime   float64  `json:"endTime,omitempty" jsonschema:"window end, match-relative seconds (see startTime: at least one bound is required at the MCP layer). Keep windows tens of seconds, not minutes."`
	Players   []string `json:"players,omitempty"`
	Fields    []string `json:"fields,omitempty" jsonschema:"field codes: h=health, a=armor, at=armorType, li=location (NOTE: the selector code is li; the loc= param picks how it renders — name under output key 'loc' (default) or raw index under 'li'), pos, view, hgt, lq, vel, rl/lg/gl/ssg/sng (held weapons), q/pe/r (powerups), sh/nl/rk/cl (ammo), sp/d (spawn/death events). Empty = all standard fields; an unknown code errors with the full list"`
	Loc       string   `json:"loc,omitempty" jsonschema:"loc representation: 'name' (default) or 'index' (raw LocTable index stream; decode via getLocTable)"`
}

// GetStateAtInput mirrors /v1/demos/{id}/state-at query params.
type GetStateAtInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Time    float64  `json:"time" jsonschema:"required; match-relative seconds"`
	Players []string `json:"players,omitempty"`
	Fields  []string `json:"fields,omitempty" jsonschema:"field codes: h=health, a=armor, at=armorType, li=location (NOTE: the selector code is li; the loc= param picks how it renders — name under output key 'loc' (default) or raw index under 'li'), pos, view, hgt, lq, vel, rl/lg/gl/ssg/sng (held weapons), q/pe/r (powerups), sh/nl/rk/cl (ammo), sp/d rejected here (no point-in-time meaning). Empty = all standard fields; an unknown code errors with the full list"`
	Loc     string   `json:"loc,omitempty" jsonschema:"loc representation: 'name' (default) or 'index' (raw LocTable index; decode via getLocTable)"`
}

// GetLocTrailsInput mirrors /v1/demos/{id}/loc-trails query params.
type GetLocTrailsInput struct {
	DemoID     string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players    []string `json:"players,omitempty"`
	MinDwellMs *int     `json:"minDwellMs,omitempty" jsonschema:"drop residences shorter than this (ms). MCP default 250 (REST differs: 0 = raw) — nearest-loc flicker at loc boundaries otherwise dominates the list. Pass 0 explicitly for the raw unfiltered residences."`
	StartTime  float64  `json:"startTime,omitempty"`
	EndTime    float64  `json:"endTime,omitempty"`
	Loc        string   `json:"loc,omitempty" jsonschema:"residence representation: 'name' (default) or 'index' (raw LocTable index; decode via getLocTable)"`
}

// GetLocTableInput identifies a demo for its interned loc-name table —
// the decoder for li indices returned by the loc views in index mode.
type GetLocTableInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
}

// GetRegionControlInput mirrors /v1/demos/{id}/region-control query params.
type GetRegionControlInput struct {
	DemoID    string  `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	WindowMs  int     `json:"windowMs,omitempty" jsonschema:"bucket size in MILLISECONDS (startTime/endTime are seconds!); default 5000 (5 s) for the per-region state strings — finer resolution multiplies the bucketStates string length"`
	StartTime float64 `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds"`
	EndTime   float64 `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
}

// GetDemoInfoInput identifies a demo for the KTX demoinfo blob.
type GetDemoInfoInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
}

// GetMetadataInput identifies a demo for its server cvars + KTX
// match settings.
type GetMetadataInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
}

// GetFragsInput filters /v1/demos/{id}/frags. When any scoping filter
// (players / weapon / startTime / endTime) is set, every aggregate is
// recomputed from the filtered kill log; with none set the authoritative
// stored totals are returned.
type GetFragsInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players   []string `json:"players,omitempty" jsonschema:"restrict aggregates + kill log to entries involving these players (killer OR victim)"`
	Weapons   []string `json:"weapons,omitempty" jsonschema:"restrict aggregates + kill log to these weapon codes (rl, lg, gl, ssg, sng, ng, axe, sg, ...)"`
	StartTime float64  `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds (frags at or after this time)"`
	EndTime   float64  `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds (frags at or before this time)"`
	Summary   bool     `json:"summary,omitempty" jsonschema:"return only aggregates, dropping the big per-event kill log (avoids overflowing context)"`
}

// GetDamageInput mirrors /v1/demos/{id}/damage query params. When any scoping
// filter (players / weapon / startTime / endTime) is set, every aggregate is
// recomputed from the filtered per-hit log; with none set the authoritative
// stored totals are returned.
type GetDamageInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players   []string `json:"players,omitempty" jsonschema:"restrict aggregates + damage log to entries involving these players (attacker OR victim)"`
	Weapons   []string `json:"weapons,omitempty" jsonschema:"restrict aggregates + damage log to these attacker weapon codes (rl, lg, gl, ssg, sng, sg, tele, ...)"`
	StartTime float64  `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds (hits at or after this time)"`
	EndTime   float64  `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds (hits at or before this time)"`
	Summary   *bool    `json:"summary,omitempty" jsonschema:"MCP default TRUE (REST differs): aggregates only, the big per-hit damage log dropped. Pass false for the full log."`
}

// GetAimInput identifies a demo for its per-player aim analysis, with optional
// player / time-window scoping and a summary switch. With no time window the
// stored aim is served (players= selects named shooters' match-wide aim); a
// startTime/endTime window recomputes aim over the shots in that window.
type GetAimInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players   []string `json:"players,omitempty" jsonschema:"scope to these shooters (players[].player); with no time window this selects their match-wide aim, with a window it restricts the recompute"`
	StartTime float64  `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds; setting a window recomputes aim over the shots in it so every figure (weapons, crosshair, lgRamp) scopes to the window"`
	EndTime   float64  `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
	Summary   *bool    `json:"summary,omitempty" jsonschema:"MCP default TRUE (REST differs): only the compact per-player weapons aggregates, the large per-fire crosshair + lgRamp sample arrays dropped. Pass false for the full arrays."`
}

// GetLocGraphInput identifies a demo for its per-loc adjacency graph.
type GetLocGraphInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
}

// GetChatInput filters /v1/demos/{id}/chat by player, time window,
// and chat kind (`chat` / `teamsay`).
type GetChatInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	StartTime float64  `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds"`
	EndTime   float64  `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
	Players   []string `json:"players,omitempty" jsonschema:"restrict to these speaker names"`
	Types     []string `json:"types,omitempty" jsonschema:"chat-event types: 'chat' (public say), 'teamsay'. Empty = both."`
}

// GetBackpacksInput filters /v1/demos/{id}/backpacks.
type GetBackpacksInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players []string `json:"players,omitempty" jsonschema:"restrict to drops by these dropper names"`
	Weapons []string `json:"weapons,omitempty" jsonschema:"restrict to these dropped-weapon codes (rl, lg); empty = both. Forwarded as a CSV set, matching REST /backpacks"`
	StartTime float64 `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds (drops at or after this time)"`
	EndTime   float64 `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
}

// GetItemsInput filters /v1/demos/{id}/items.
type GetItemsInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Items   []string `json:"items,omitempty" jsonschema:"item name or kind token (case-insensitive). A kind matches every instance of a type (YA → ya_1, ya_2; RA; MH; Quad; Pent; Ring; RL; LG; GL; SSG; SNG; NG); a suffixed name matches one instance (ya_1)."`
	Players []string `json:"players,omitempty" jsonschema:"restrict phases to those taken by these player names (phases with no TakenBy survive)"`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"item category (case-insensitive): armor, mega, health, powerup, weapon, ammo. A raw kind token (ra, quad, rl, ...) is also accepted."`
	StartTime float64 `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds. Timeline mode keeps phases OVERLAPPING the window; summary mode counts takes INSIDE it"`
	EndTime   float64 `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds (e.g. endTime:60 = the opening minute)"`
	Summary   *bool   `json:"summary,omitempty" jsonschema:"MCP default TRUE (REST differs): per-item take aggregates {takenCount, byPlayer, firstTake} instead of the full phase timeline. Pass false for every phase (available/taken/respawn cycles)."`
}

// GetMapEntitiesByMapInput addresses the static layout by map name
// directly (no demo): /v1/maps/{map}/entities.
type GetMapEntitiesByMapInput struct {
	Map   string   `json:"map" jsonschema:"map name (e.g. dm6); aliases are resolved"`
	Types []string `json:"types,omitempty" jsonschema:"restrict to entity types: item, spawn, teleportDst, teleportSrc, button, door"`
	Kinds []string `json:"kinds,omitempty" jsonschema:"restrict items by category or raw kind token"`
}

// GetWeaponPickupsInput filters /v1/demos/{id}/weapon-pickups.
type GetWeaponPickupsInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players []string `json:"players,omitempty" jsonschema:"restrict to picks by these names"`
	Weapons []string `json:"weapons,omitempty" jsonschema:"weapon codes: rl, lg, gl, ssg, sng, ng"`
	Source  string   `json:"source,omitempty" jsonschema:"'world' (spawner) or 'backpack' (RL/LG drop)"`
	StartTime float64 `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds (pickups at or after this time)"`
	EndTime   float64 `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
}

// ListArtifactsInput has no parameters — the artifact manifest is static
// per binary. It exists so the tool has a (empty) input schema.
type ListArtifactsInput struct{}

// GetArtifactInput addresses one servable artifact by DAG node name on a
// demo. The generic endpoint takes no other parameters (parameterised reads
// are the curated view tools).
type GetArtifactInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Name   string `json:"name" jsonschema:"artifact name from listArtifacts (e.g. frag, damage, loc-graph, los). Only 'servable' artifacts are reachable"`
}

// SearchGamesInput hits hub.quakeworld.nu's Supabase directly — not
// mvd-api. Discovery is the hub's job; mvd-api only handles parse +
// cache for demos chosen from a search. All fields optional; an
// empty filter returns the most recent matches.
type SearchGamesInput struct {
	Players  []string `json:"players,omitempty"  jsonschema:"player names to match (FTS on players_fts, AND'd across multiple)"`
	Teams    []string `json:"teams,omitempty"    jsonschema:"team names that must appear in team_names (contains)"`
	Map      string   `json:"map,omitempty"      jsonschema:"map name, exact match (e.g. dm6)"`
	Mode     string   `json:"mode,omitempty"     jsonschema:"game mode, exact match (e.g. 1on1, 2on2, 4on4, FFA)"`
	Matchtag string   `json:"matchtag,omitempty" jsonschema:"tournament/event tag, case-insensitive substring (e.g. qwsl)"`
	From     string   `json:"from,omitempty"     jsonschema:"ISO date lower bound, inclusive (YYYY-MM-DD)"`
	To       string   `json:"to,omitempty"       jsonschema:"ISO date upper bound, inclusive (YYYY-MM-DD)"`
	Limit    int      `json:"limit,omitempty"    jsonschema:"max rows (default 20, capped at 100)"`
	Offset   int      `json:"offset,omitempty"   jsonschema:"pagination offset"`
	Roster   bool     `json:"roster,omitempty"   jsonschema:"true = verbatim hub rows with full roster detail (per-player ping, color arrays, name_color). Default = compact rows: players projected to {name, team, frags}"`
}

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
	GetLocGraph(ctx context.Context, in GetLocGraphInput) (any, error)
	GetChat(ctx context.Context, in GetChatInput) (any, error)
	GetBackpacks(ctx context.Context, in GetBackpacksInput) (any, error)
	GetItems(ctx context.Context, in GetItemsInput) (any, error)
	GetMapEntities(ctx context.Context, in GetMapEntitiesInput) (any, error)
	GetMapEntitiesByMap(ctx context.Context, in GetMapEntitiesByMapInput) (any, error)
	GetWeaponPickups(ctx context.Context, in GetWeaponPickupsInput) (any, error)
	GetBuckets(ctx context.Context, in GetBucketsInput) (any, error)
	GetEvents(ctx context.Context, in GetEventsInput) (any, error)
	GetStreamSlice(ctx context.Context, in GetStreamSliceInput) (any, error)
	GetStateAt(ctx context.Context, in GetStateAtInput) (any, error)
	GetLocTrails(ctx context.Context, in GetLocTrailsInput) (any, error)
	GetLocTable(ctx context.Context, in GetLocTableInput) (any, error)
	GetRegionControl(ctx context.Context, in GetRegionControlInput) (any, error)
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
	WindowMs    int               `json:"windowMs,omitempty" jsonschema:"bucket size in ms; default 1000 (1 s) — finer resolution like 50 ms or 100 ms produces tens of thousands of buckets per match, override only when needed"`
	StartTime   float64           `json:"startTime,omitempty" jsonschema:"window start in match-relative seconds"`
	EndTime     float64           `json:"endTime,omitempty" jsonschema:"window end in match-relative seconds"`
	Players     []string          `json:"players,omitempty"`
	Fields      []string          `json:"fields,omitempty" jsonschema:"field codes (h, a, rl, lg, ...). Empty = all standard fields"`
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
	Types     []string `json:"types,omitempty" jsonschema:"event types: frag, powerup, streak, spawn, death, weapon, item, chat, loc, health, armor"`
	Loc       string   `json:"loc,omitempty" jsonschema:"loc-event representation: 'name' (default) or 'index' (raw LocTable index; decode via getLocTable)"`
}

// GetStreamSliceInput mirrors /v1/demos/{id}/stream-slice query params.
type GetStreamSliceInput struct {
	DemoID    string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	StartTime float64  `json:"startTime,omitempty"`
	EndTime   float64  `json:"endTime,omitempty"`
	Players   []string `json:"players,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	Loc       string   `json:"loc,omitempty" jsonschema:"loc representation: 'name' (default) or 'index' (raw LocTable index stream; decode via getLocTable)"`
}

// GetStateAtInput mirrors /v1/demos/{id}/state-at query params.
type GetStateAtInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Time    float64  `json:"time" jsonschema:"required; match-relative seconds"`
	Players []string `json:"players,omitempty"`
	Fields  []string `json:"fields,omitempty"`
	Loc     string   `json:"loc,omitempty" jsonschema:"loc representation: 'name' (default) or 'index' (raw LocTable index; decode via getLocTable)"`
}

// GetLocTrailsInput mirrors /v1/demos/{id}/loc-trails query params.
type GetLocTrailsInput struct {
	DemoID     string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players    []string `json:"players,omitempty"`
	MinDwellMs int      `json:"minDwellMs,omitempty"`
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
	DemoID   string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	WindowMs int    `json:"windowMs,omitempty" jsonschema:"bucket size for per-region state strings; default 1000 (1 s) — finer resolution multiplies the bucketStates string length"`
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

// GetFragsInput filters /v1/demos/{id}/frags.
type GetFragsInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players []string `json:"players,omitempty" jsonschema:"restrict aggregates + kill log to entries involving these players (killer OR victim)"`
	Weapon  []string `json:"weapon,omitempty" jsonschema:"restrict aggregates + kill log to these weapon codes (rl, lg, gl, ssg, sng, ng, axe, sg, ...)"`
}

// GetDamageInput mirrors /v1/demos/{id}/damage query params.
type GetDamageInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Players []string `json:"players,omitempty" jsonschema:"restrict aggregates + damage log to entries involving these players (attacker OR victim)"`
	Weapon  []string `json:"weapon,omitempty" jsonschema:"restrict aggregates + damage log to these attacker weapon codes (rl, lg, gl, ssg, sng, sg, tele, ...)"`
}

// GetLocGraphInput identifies a demo for its per-loc adjacency graph.
type GetLocGraphInput struct {
	DemoID string `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
}

// GetChatInput filters /v1/demos/{id}/chat by player, time window,
// and chat kind (`say` / `teamsay`).
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
	Weapon  string   `json:"weapon,omitempty" jsonschema:"'rl' or 'lg' (single-weapon filter)"`
}

// GetItemsInput filters /v1/demos/{id}/items.
type GetItemsInput struct {
	DemoID  string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Items   []string `json:"items,omitempty" jsonschema:"item name or kind token (case-insensitive). A kind matches every instance of a type (YA → ya_1, ya_2; RA; MH; Quad; Pent; Ring; RL; LG; GL; SSG; SNG; NG); a suffixed name matches one instance (ya_1)."`
	Players []string `json:"players,omitempty" jsonschema:"restrict phases to those taken by these player names (phases with no TakenBy survive)"`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"item category (case-insensitive): armor, mega, health, powerup, weapon, ammo. A raw kind token (ra, quad, rl, ...) is also accepted."`
}

// GetMapEntitiesInput filters /v1/demos/{id}/map-entities — the static
// designed layout of the demo's map.
type GetMapEntitiesInput struct {
	DemoID string   `json:"demoId" jsonschema:"the demo id (gameId:N or sha:HEX)"`
	Types  []string `json:"types,omitempty" jsonschema:"restrict to entity types (case-insensitive): item, spawn, teleportDst, teleportSrc, button, door"`
	Kinds  []string `json:"kinds,omitempty" jsonschema:"restrict items by category (armor, mega, health, powerup, weapon, ammo) or a raw kind token (ra, quad, rl, ...)"`
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
	Weapon  []string `json:"weapon,omitempty" jsonschema:"weapon codes: rl, lg, gl, ssg, sng, ng"`
	Source  string   `json:"source,omitempty" jsonschema:"'world' (spawner) or 'backpack' (RL/LG drop)"`
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
}

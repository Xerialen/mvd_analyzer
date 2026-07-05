package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
)

// demoStore is the subset of *democache.Cache the handlers depend on.
// Tests inject a fake.
type demoStore interface {
	GetResult(ctx context.Context, id democache.DemoID) (*result.Result, democache.CacheMeta, error)
	// EnsureShotStreams returns the Result with the opt-in spatial weapon-fire
	// streams built (projectiles + beams; plus nails when nails is true),
	// re-parsing the cached MVD bytes on first request. It serializes the
	// rebuild per demo SHA internally, so no server-wide lock is needed.
	EnsureShotStreams(ctx context.Context, id democache.DemoID, nails bool) (*result.Result, democache.CacheMeta, error)
}

// httpError carries the wire-format error body.
type httpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error httpError `json:"error"`
}

// writeError emits the error envelope and the appropriate status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: httpError{Code: code, Message: msg}})
}

// writeJSON emits a JSON body with the standard cache headers (set by
// the caller via the resp.cacheHeader call before invoking writeJSON).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// resolveDemo parses the {id} path param, fetches the *Result, and
// sets the cache headers. Returns (r, meta, ok=true) on success; on
// failure, writes the error to w and returns ok=false.
func (s *server) resolveDemo(w http.ResponseWriter, r *http.Request) (*result.Result, democache.CacheMeta, bool) {
	raw := r.PathValue("id")
	id, err := democache.ParseDemoID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
		return nil, democache.CacheMeta{}, false
	}
	res, meta, err := s.store.GetResult(r.Context(), id)
	if err != nil {
		mapStoreError(w, err)
		return nil, democache.CacheMeta{}, false
	}
	setCacheHeaders(w, meta)
	// Honor If-None-Match for cheap 304s.
	etag := fmt.Sprintf(`"%s-v%d"`, meta.SHA256, meta.SchemaVersion)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil, meta, false
	}
	return res, meta, true
}

func mapStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, democache.ErrInvalidDemoID):
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
	case errors.Is(err, democache.ErrDemoNotFound):
		writeError(w, http.StatusNotFound, "demo_not_found", err.Error())
	case errors.Is(err, democache.ErrHubUpstream):
		writeError(w, http.StatusBadGateway, "hub_upstream", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// writeUnavailable maps a view.ErrUnavailable (a section the demo lacks
// the enabling signal for) to a 422 with the section-specific code/message,
// and anything else to 500. This is the HTTP face of the R3 rule —
// object-shaped sections that require a capability return 422 when it's
// absent; always-computable / list sections return 200 with an empty body.
func writeUnavailable(w http.ResponseWriter, err error, code, msg string) {
	if errors.Is(err, view.ErrUnavailable) {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
}

func setCacheHeaders(w http.ResponseWriter, meta democache.CacheMeta) {
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Schema-Version", fmt.Sprintf("%d", meta.SchemaVersion))
	switch {
	case meta.FromCache:
		w.Header().Set("X-Cache", "HIT")
	case meta.FromMVDTier:
		w.Header().Set("X-Cache", "WARM")
	default:
		w.Header().Set("X-Cache", "MISS")
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%s-v%d"`, meta.SHA256, meta.SchemaVersion))
}

// --- Endpoint handlers ---

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"schemaVersion": result.CurrentSchemaVersion,
	})
}

func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"hash":      GitHash,
		"tag":       GitTag,
		"buildDate": BuildDate,
	})
}

// handleLoad: POST /v1/demos/{id} — warm the cache for an id and
// return identity metadata. Idempotent.
func (s *server) handleLoad(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id, err := democache.ParseDemoID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
		return
	}
	_, meta, err := s.store.GetResult(r.Context(), id)
	if err != nil {
		mapStoreError(w, err)
		return
	}
	setCacheHeaders(w, meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"demoId":        "sha:" + meta.SHA256,
		"sha256":        meta.SHA256,
		"fromCache":     meta.FromCache,
		"schemaVersion": meta.SchemaVersion,
	})
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, BuildOverview(res))
}

// handleMetadata: GET /v1/demos/{id}/metadata — full server cvars +
// KTX match settings (timelimit, fraglimit, antilag, midair, spawnmodel,
// instagib, ...). Used by the web's Summary tab.
func (s *server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	if res.Metadata == nil {
		writeError(w, http.StatusUnprocessableEntity, "metadata_unavailable",
			"this demo has no metadata (no fullserverinfo / no countdown centerprint)")
		return
	}
	writeJSON(w, http.StatusOK, res.Metadata)
}

// handleLocGraph: GET /v1/demos/{id}/loc-graph — per-map loc
// adjacency graph (which locs are reachable from which). Used by
// the web's Loc Graph tab.
func (s *server) handleLocGraph(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	if res.LocGraph == nil {
		writeError(w, http.StatusUnprocessableEntity, "locgraph_unavailable",
			"this demo has no loc graph (probably no position track was emitted)")
		return
	}
	writeJSON(w, http.StatusOK, res.LocGraph)
}

// handleFrags: GET /v1/demos/{id}/frags — top-level frag aggregates +
// the full kill log. Optional filters narrow both views to entries
// involving the named players / weapon. Filtering lives in view.Frags so
// REST, MCP, and WASM share one implementation.
//
// Query params:
//
//	players  csv — restrict ByPlayer keys + the Frags list to entries
//	             where killer or victim is in the set
//	weapon   csv — restrict ByWeapon keys + the Frags list to these weapons
func (s *server) handleFrags(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	out, err := view.Frags(res, view.FragOptions{
		Players: parseCSV(ciGet(q, "players")),
		Weapons: parseCSV(ciGet(q, "weapon")),
	})
	if err != nil {
		writeUnavailable(w, err, "frags_unavailable", "this demo has no frag log")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDamage: GET /v1/demos/{id}/damage — per-hit damage log +
// aggregates (matrix, per-weapon, given/taken, EWep victim-weapon
// buckets) + the KTX-scoreboard cross-check. Optional filters narrow all
// views to entries involving the named players / weapon.
//
// Query params:
//
//	players  csv — restrict ByPlayer / Matrix / Events / Scoreboard to
//	             entries where attacker or victim is in the set
//	weapon   csv — restrict ByWeapon keys + Matrix/Events + per-player
//	             ByWeapon to these (attacker) weapons
func (s *server) handleDamage(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	out, err := view.Damage(res, view.DamageOptions{
		Players: parseCSV(ciGet(q, "players")),
		Weapons: parseCSV(ciGet(q, "weapon")),
	})
	if err != nil {
		writeUnavailable(w, err, "damage_unavailable",
			"this demo has no damage data (no KTX mvdhidden_dmgdone stream)")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleShots: GET /v1/demos/{id}/shots — the per-fire weapon stream
// (result.Shots): every detected fire with time/player/weapon/source, hit +
// victims where linkable, per-player match-time aggregates, and the KTX
// reconciliation cross-check. Served from the stream-enriched parse (like
// /aim, built on first request) so rl/gl fires carry their projectile-linked
// hits. Pass nails=1 to include ng/sng fires (opt-in — high volume).
func (s *server) handleShots(w http.ResponseWriter, r *http.Request) {
	res, ok := s.resolveShotStreams(w, r, parseBool(r.URL.Query(), "nails"))
	if !ok {
		return
	}
	if res.Shots == nil {
		writeError(w, http.StatusUnprocessableEntity, "shots_unavailable",
			"this demo has no shot data (no weapon fires decoded)")
		return
	}
	writeJSON(w, http.StatusOK, res.Shots)
}

// handleAim: GET /v1/demos/{id}/aim — per-player aim analysis (result.Aim):
// per-weapon effectiveness (shots/hits, SG/SSG pellet stats, RL/GL
// direct/splash, the LG near/blocked/out-of-range whiff split), columnar
// crosshair-error samples (hitscan), and the LG ramp series.
//
// Served from the stream-enriched parse (EnsureShotStreams — built on first
// request like the /streams/* endpoints, then cached) so the projectile/
// beam-derived weapon blocks are always present.
func (s *server) handleAim(w http.ResponseWriter, r *http.Request) {
	res, ok := s.resolveShotStreams(w, r, false)
	if !ok {
		return
	}
	if res.Aim == nil {
		writeError(w, http.StatusUnprocessableEntity, "aim_unavailable",
			"this demo has no aim data (needs shots + position/view streams)")
		return
	}
	writeJSON(w, http.StatusOK, res.Aim)
}

// handleChat: GET /v1/demos/{id}/chat — chat-only slice of
// result.Messages.Events, with optional player / time-window / type
// filters.
//
// Query params:
//
//	from, to   match-relative seconds, both inclusive
//	players    csv — restrict to these speakers
//	types      csv — defaults to ["chat","teamsay"]; pass a subset to narrow
//
// Returned shape mirrors result.MatchEvent, so callers see Time,
// Type, Player, Team, Message, MessageClean directly (no MCP-event
// envelope, unlike getEvents).
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	to, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view.Chat(res, view.ChatOptions{
		From:    from,
		To:      to,
		Players: parseCSV(ciGet(q, "players")),
		Types:   parseCSV(ciGet(q, "types")),
	}))
}

// handleDemoInfo: GET /v1/demos/{id}/demoinfo — KTX demoinfo blob
// pass-through. Carries per-player weapon accuracy, kills, deaths,
// damage, sprees, item pickup counts, RL/LG transfers.
func (s *server) handleDemoInfo(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	if res.DemoInfo == nil {
		writeError(w, http.StatusUnprocessableEntity, "demoinfo_unavailable",
			"this demo has no KTX demoinfo block (likely non-KTX or pre-match abort)")
		return
	}
	writeJSON(w, http.StatusOK, res.DemoInfo)
}

// handleBackpacks: GET /v1/demos/{id}/backpacks — RL/LG drops with
// optional player/weapon filters.
//
// Query params:
//
//	players  csv — restrict to drops by these dropper names
//	weapon   csv — restrict to these weapons ("rl"/"lg"; case-insensitive)
func (s *server) handleBackpacks(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, view.Backpacks(res, view.BackpackOptions{
		Players: parseCSV(ciGet(q, "players")),
		Weapons: parseCSV(ciGet(q, "weapon")),
	}))
}

// handleItems: GET /v1/demos/{id}/items — per-item pickup/respawn
// timeline with optional filters.
//
// Query params (all case-insensitive):
//
//	items    csv — restrict to items whose Name or Kind matches. Accepts
//	             a kind token to match every instance of a type ("ya" →
//	             ya_1, ya_2; "ra"; "mh") or a specific instance Name
//	             ("ya_1"). RA/YA/GA/MH/Quad/Pent/Ring/RL/LG all work.
//	players  csv — restrict to phases where TakenBy is one of these names
//	kinds    csv — restrict to item categories: armor, mega, health,
//	             powerup, weapon, ammo (see ItemTimeline.Category). A raw
//	             kind token ("ra", "quad") is also accepted.
//
// items/kinds match the canonical lowercase tokens regardless of input
// case; players is matched against the exact display name (case-
// sensitive — QW names are case-significant).
//
// Phases with no TakenBy survive any players= filter (they represent
// the item's availability state at match end / dropped runs).
func (s *server) handleItems(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, view.Items(res, view.ItemOptions{
		Items:   parseCSV(ciGet(q, "items")),
		Players: parseCSV(ciGet(q, "players")),
		Kinds:   parseCSV(ciGet(q, "kinds")),
	}))
}

// handleWeaponPickups: GET /v1/demos/{id}/weapon-pickups — slot-weapon
// acquisitions with effectiveness (kills-before-next-death). Optional
// filters by player / weapon / source.
//
// Query params:
//
//	players  csv — restrict to picks by these names
//	weapon   csv — "rl","lg","gl","ssg","sng","ng" (case-insensitive)
//	source   "world" | "backpack" | "unknown"
func (s *server) handleWeaponPickups(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, view.WeaponPickups(res, view.WeaponPickupOptions{
		Players: parseCSV(ciGet(q, "players")),
		Weapons: parseCSV(ciGet(q, "weapon")),
		Source:  ciGet(q, "source"),
	}))
}

// csvSetLower is csvSet with each token lowercased — for filters
// matched against canonical lowercase tokens (item names, kinds,
// categories) where the caller's case shouldn't matter.
func csvSetLower(v string) map[string]bool {
	if v == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out[p] = true
		}
	}
	return out
}

func (s *server) handleBuckets(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	windowMs, err := parseInt(q, "windowMs", 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	start, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	end, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	reducers, err := parseReducers(ciGet(q, "reducers"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	locIndex, err := parseLocIndex(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	layout, err := parseLayout(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	opts := view.BucketsOptions{
		WindowMs:    windowMs,
		StartTime:   start,
		EndTime:     end,
		Players:     parseCSV(ciGet(q, "players")),
		Fields:      parseCSV(ciGet(q, "fields")),
		Reducers:    reducers,
		IncludeTeam: parseBool(q, "includeTeam"),
		LocIndex:    locIndex,
		Layout:      layout,
	}
	if layout == "column" {
		cb, err := view.BucketsColumnar(res, opts)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cb)
		return
	}
	bv, err := view.Buckets(res, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bv)
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	start, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	end, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	locIndex, err := parseLocIndex(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	filter := view.EventsFilter{
		StartTime: start,
		EndTime:   end,
		Players:   parseCSV(ciGet(q, "players")),
		Types:     parseCSV(ciGet(q, "types")),
		LocIndex:  locIndex,
	}
	ev, err := view.Events(res, filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) handleStreamSlice(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	start, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	end, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	locIndex, err := parseLocIndex(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	opts := view.StreamSliceOptions{
		StartTime: start,
		EndTime:   end,
		Players:   parseCSV(ciGet(q, "players")),
		Fields:    parseCSV(ciGet(q, "fields")),
		LocIndex:  locIndex,
	}
	sl, err := view.StreamSlice(res, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sl)
}

func (s *server) handleStateAt(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if ciGet(q, "time") == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "time is required")
		return
	}
	t, err := parseFloat(q, "time", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	locIndex, err := parseLocIndex(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	opts := view.StateAtOptions{
		Time:     t,
		Players:  parseCSV(ciGet(q, "players")),
		Fields:   parseCSV(ciGet(q, "fields")),
		LocIndex: locIndex,
	}
	sa, err := view.StateAt(res, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sa)
}

// handleLOS: GET /v1/demos/{id}/los — per-player line-of-sight intervals.
//
// Line of sight is the heaviest position-derived pass and has no other
// consumer, so it is computed lazily: the first request for a demo triggers
// the raycast pass and caches it on the in-memory Result (ComputeLOS is
// idempotent via Streams.LOSComputed), so later requests are free. The on-disk
// gob stays lean — LOS is never baked into it. Returns 200 with a players
// array; los is omitted for a player with no sightlines and empty for every
// player on a map with no provisioned BSP.
func (s *server) handleLOS(w http.ResponseWriter, r *http.Request) {
	res, meta, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	// Per-SHA lock: concurrent /los for one demo serialize on the shared
	// Result, but /los for a different demo does not queue behind it.
	unlock := s.losLocks.Lock(meta.SHA256)
	analyzer.ComputeLOS(res)
	unlock()

	type losPlayer struct {
		Name string            `json:"name"`
		LOS  []result.LosTrack `json:"los,omitempty"`
		PVS  []result.LosTrack `json:"pvs,omitempty"`
	}
	out := struct {
		Players []losPlayer `json:"players"`
	}{}
	if res.Streams != nil {
		out.Players = make([]losPlayer, len(res.Streams.Players))
		for i := range res.Streams.Players {
			out.Players[i].Name = res.Streams.Players[i].Name
			out.Players[i].LOS = res.Streams.Players[i].LOS
			out.Players[i].PVS = res.Streams.Players[i].PVS
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveShotStreams mirrors resolveDemo but routes through EnsureShotStreams
// so the requested spatial weapon-fire streams are built — a one-time
// re-parse of the cached MVD bytes, since they are opt-in and not in the lean
// default Result. EnsureShotStreams serializes the rebuild per demo SHA
// internally (see cache.shotLocks), so no server-wide lock is held here.
func (s *server) resolveShotStreams(w http.ResponseWriter, r *http.Request, nails bool) (*result.Result, bool) {
	id, err := democache.ParseDemoID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
		return nil, false
	}
	res, meta, err := s.store.EnsureShotStreams(r.Context(), id, nails)
	if err != nil {
		mapStoreError(w, err)
		return nil, false
	}
	setCacheHeaders(w, meta)
	if meta.ShotStreamsUnavailable {
		// The tier-1 bytes were gone, so the stream-derived parts are absent.
		// Flag the degrade and don't let clients cache the incomplete body as
		// immutable (see API.md §4.5c/4.5d/4.11c).
		w.Header().Set("X-Shot-Streams", "unavailable")
		w.Header().Set("Cache-Control", "no-store")
	}
	etag := fmt.Sprintf(`"%s-v%d"`, meta.SHA256, meta.SchemaVersion)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil, false
	}
	return res, true
}

// handleProjectiles serves the rocket/grenade flight stream (opt-in; built on
// first request). Body is {"projectiles": ...}, null when the demo has none.
func (s *server) handleProjectiles(w http.ResponseWriter, r *http.Request) {
	res, ok := s.resolveShotStreams(w, r, false)
	if !ok {
		return
	}
	var pr *result.ProjectileStreams
	if res.Streams != nil {
		pr = res.Streams.Projectiles
	}
	writeJSON(w, http.StatusOK, struct {
		Projectiles *result.ProjectileStreams `json:"projectiles"`
	}{pr})
}

// handleBeams serves the LG bolt stream (opt-in; built on first request).
func (s *server) handleBeams(w http.ResponseWriter, r *http.Request) {
	res, ok := s.resolveShotStreams(w, r, false)
	if !ok {
		return
	}
	var bm *result.BeamStreams
	if res.Streams != nil {
		bm = res.Streams.Beams
	}
	writeJSON(w, http.StatusOK, struct {
		Beams *result.BeamStreams `json:"beams"`
	}{bm})
}

// handleNails serves the ng/sng nail-flight stream (opt-in, highest volume;
// built on first request, separate from projectiles/beams).
func (s *server) handleNails(w http.ResponseWriter, r *http.Request) {
	res, ok := s.resolveShotStreams(w, r, true)
	if !ok {
		return
	}
	var nl *result.ProjectileStreams
	if res.Streams != nil {
		nl = res.Streams.Nails
	}
	writeJSON(w, http.StatusOK, struct {
		Nails *result.ProjectileStreams `json:"nails"`
	}{nl})
}

func (s *server) handleLocTrails(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	start, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	end, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	minDwell, err := parseInt(q, "minDwellMs", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	locIndex, err := parseLocIndex(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	opts := view.LocTrailsOptions{
		Players:    parseCSV(ciGet(q, "players")),
		MinDwellMs: minDwell,
		StartTime:  start,
		EndTime:    end,
		LocIndex:   locIndex,
	}
	tr, err := view.LocTrails(res, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

// handleLocTable: GET /v1/demos/{id}/loc-table — the interned loc-name
// table, the decoder for the `li` indices returned by the loc-bearing
// views in index mode (?loc=index). Index 0 is the "" no-loc sentinel.
// Empty array when the demo carried no loc data.
func (s *server) handleLocTable(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	table := []string{}
	if res.TimelineAnalysis != nil && res.TimelineAnalysis.LocTable != nil {
		table = res.TimelineAnalysis.LocTable
	}
	writeJSON(w, http.StatusOK, map[string]any{"locTable": table})
}

func (s *server) handleRegionControl(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	if res.TimelineAnalysis == nil || res.TimelineAnalysis.RegionControl == nil {
		writeError(w, http.StatusUnprocessableEntity, "region_control_unavailable", "this demo has no region-control layout")
		return
	}
	q := r.URL.Query()
	windowMs, err := parseInt(q, "windowMs", 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	from, err := parseFloat(q, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	to, err := parseFloat(q, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	rcv, err := view.RegionControl(res, view.RegionControlOptions{
		WindowMs:  windowMs,
		StartTime: from,
		EndTime:   to,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rcv)
}

// handleAirgibs: GET /v1/demos/{id}/airgibs — the Key Moments airgib list
// (timelineAnalysis.airgibs): every DIRECT enemy rocket hit on an airborne
// victim above the height threshold, sorted by height descending. Height
// needs the map's clip hull, so the list is empty (not an error) when the
// map's BSP was not provisioned at parse time.
func (s *server) handleAirgibs(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	if res.TimelineAnalysis == nil {
		writeError(w, http.StatusUnprocessableEntity, "airgibs_unavailable",
			"this demo has no timeline analysis")
		return
	}
	airgibs := res.TimelineAnalysis.Airgibs
	if airgibs == nil {
		airgibs = []result.AirgibEvent{}
	}
	writeJSON(w, http.StatusOK, airgibs)
}

// recoverMiddleware turns a panic into a 500 + slog error line so a
// single buggy handler can't take down the server.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic in handler",
					"method", r.Method, "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "panic", fmt.Sprintf("%v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

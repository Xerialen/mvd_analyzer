package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
)

// demoStore is the subset of *democache.Cache the handlers depend on.
// Tests inject a fake.
type demoStore interface {
	GetResult(ctx context.Context, id democache.DemoID) (*result.Result, democache.CacheMeta, error)
	// EnsureLOS returns the Result with the per-player line-of-sight / PVS
	// interval sets materialised (the lazy raycast pass), serialising the
	// compute per demo SHA internally. It persists the result to the tier-3
	// cache so a restart/eviction does not recompute.
	//
	// (There is no EnsureShotStreams: since phase 12 the spatial weapon-fire
	// streams are baked into the always-full GetResult parse, so /shots, /aim
	// and /streams/* read them straight off the base Result.)
	EnsureLOS(ctx context.Context, id democache.DemoID) (*result.Result, democache.CacheMeta, error)
}

// httpError carries the wire-format error body.
type httpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error httpError `json:"error"`
}

// writeError emits the error envelope and the appropriate status. It also
// strips any ETag a success-path header helper may have set before the
// error was decided (setCacheHeaders/setArtifactCacheHeaders run in
// resolveDemo / handleArtifact before the availability check that can 422):
// an ETag on an error body is misleading, and no-store already disables
// caching.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Del("ETag")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: httpError{Code: code, Message: msg}})
}

// genericInternalMsg is the client-facing body for any 5xx: no cache paths
// or upstream URLs, just the request id so an operator can find the real
// error in the server log (F19).
func genericInternalMsg(id string) string {
	if id == "" {
		return "internal server error"
	}
	return "internal server error (request id " + id + ")"
}

// writeInternal logs the real error against the request id and returns the
// generic 5xx body — the single 500 path for the handler layer (F19).
func (s *server) writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	id := requestID(r.Context())
	s.logger.Error("internal error",
		"request_id", id, "method", r.Method, "path", r.URL.Path, "err", err.Error())
	writeError(w, http.StatusInternalServerError, "internal", genericInternalMsg(id))
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
		s.mapStoreError(w, r, err)
		return nil, democache.CacheMeta{}, false
	}
	setCacheHeaders(w, meta)
	if revalidated(w, r, meta) {
		return nil, meta, false
	}
	return res, meta, true
}

// revalidated writes a cheap 304 (and reports true) when the request's
// If-None-Match matches meta's ETag. setCacheHeaders must have run first
// so the ETag header is already set. This is the shared conditional-GET
// tail of resolveDemo and the /los handler.
func revalidated(w http.ResponseWriter, r *http.Request, meta democache.CacheMeta) bool {
	etag := fmt.Sprintf(`"%s-v%d"`, meta.SHA256, meta.SchemaVersion)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// mapStoreError maps a democache error to its HTTP status. The 4xx/502
// branches carry the specific (user-actionable, path-free) message; only
// the unclassified default is a 5xx, where the real error goes to the log
// and the client gets the generic request-id body (F19).
func (s *server) mapStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, democache.ErrInvalidDemoID):
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
	case errors.Is(err, democache.ErrDemoNotFound):
		writeError(w, http.StatusNotFound, "demo_not_found", err.Error())
	case errors.Is(err, democache.ErrHubUpstream):
		writeError(w, http.StatusBadGateway, "hub_upstream", err.Error())
	default:
		s.writeInternal(w, r, err)
	}
}

// writeUnavailable maps a view.ErrUnavailable (a section the demo lacks
// the enabling signal for) to a 422 with the section-specific code/message,
// and anything else to 500. This is the HTTP face of the R3 rule —
// object-shaped sections that require a capability return 422 when it's
// absent; always-computable / list sections return 200 with an empty body.
func (s *server) writeUnavailable(w http.ResponseWriter, r *http.Request, err error, code, msg string) {
	if errors.Is(err, view.ErrUnavailable) {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	s.writeInternal(w, r, err)
}

// writeInvalidParam writes the 400 invalid_param envelope for a non-nil
// err — a malformed query param (via qp.Err) or a view-layer rejection of
// an otherwise-parseable value (unknown field code, bad reducer). Reports
// whether it wrote, so callers do `if writeInvalidParam(w, err) { return }`.
func writeInvalidParam(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid_param", err.Error())
	return true
}

// cacheState is the X-Cache value for the tier that served meta.
func cacheState(meta democache.CacheMeta) string {
	switch {
	case meta.FromCache:
		return "HIT"
	case meta.FromMVDTier:
		return "WARM"
	default:
		return "MISS"
	}
}

func setCacheHeaders(w http.ResponseWriter, meta democache.CacheMeta) {
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Schema-Version", fmt.Sprintf("%d", meta.SchemaVersion))
	w.Header().Set("X-Cache", cacheState(meta))
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
		s.mapStoreError(w, r, err)
		return
	}
	// A POST is not a cacheable resource: no Cache-Control/ETag here (they'd
	// be meaningless-to-misleading on the warm-up call). Keep the informational
	// tier + schema headers.
	w.Header().Set("X-Schema-Version", fmt.Sprintf("%d", meta.SchemaVersion))
	w.Header().Set("X-Cache", cacheState(meta))
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
	md, err := view.Metadata(res)
	if err != nil {
		s.writeUnavailable(w, r, err, "metadata_unavailable",
			"this demo has no metadata (no fullserverinfo / no countdown centerprint)")
		return
	}
	writeJSON(w, http.StatusOK, md)
}

// handleLocGraph: GET /v1/demos/{id}/loc-graph — per-map loc
// adjacency graph (which locs are reachable from which). Used by
// the web's Loc Graph tab.
func (s *server) handleLocGraph(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	lg, err := view.LocGraph(res)
	if err != nil {
		s.writeUnavailable(w, r, err, "locgraph_unavailable",
			"this demo has no loc graph (probably no position track was emitted)")
		return
	}
	writeJSON(w, http.StatusOK, lg)
}

// handleFrags: GET /v1/demos/{id}/frags — top-level frag aggregates +
// the full kill log. Optional filters narrow both views to entries
// involving the named players / weapon. Filtering lives in view.Frags so
// REST, MCP, and WASM share one implementation.
//
// When any scoping filter (players / weapon / from / to) is active, ALL
// aggregates are recomputed from the filtered kill log so they are consistent
// with the entries shown. With no filter the authoritative stored aggregates
// are returned unchanged.
//
// Query params:
//
//	players  csv   — restrict aggregates + the Frags list to entries
//	               where killer or victim is in the set
//	weapons  csv   — restrict aggregates + the Frags list to these weapons
//	               (legacy alias: weapon)
//	from     float — window start, match-relative seconds (0 = no bound)
//	to       float — window end, match-relative seconds (0 = no bound)
//	summary  bool  — drop the per-event Frags log; return only aggregates
func (s *server) handleFrags(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.FragOptions{
		Players: p.CSV("players"),
		Weapons: p.CSVAny("weapons", "weapon"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
		Summary: p.Bool("summary"),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	out, err := view.Frags(res, opts)
	if err != nil {
		s.writeUnavailable(w, r, err, "frags_unavailable", "this demo has no frag log")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDamage: GET /v1/demos/{id}/damage — per-hit damage log +
// aggregates (matrix, per-weapon, given/taken, EWep victim-weapon
// buckets) + the KTX-scoreboard cross-check. Optional filters narrow all
// views to entries involving the named players / weapon.
//
// When any scoping filter (players / weapon / from / to) is active, ALL
// aggregates (totalDamage, byPlayer, byWeapon, matrix) are recomputed from the
// filtered per-hit log so they are consistent with the entries shown. With no
// filter the authoritative stored aggregates are returned unchanged.
//
// The dmg param selects the damage family (schema v54): raw = the unbound wire
// values (the v53 shape); bounded = KTX-scoreboard semantics materialized into
// the same field names; both = raw fields plus per-player `bounded` nests and a
// per-event `bounded`. Unset resolves here to `both` for a summary request and
// `raw` otherwise. dmg=bounded on a demo whose bounded reconstruction was
// skipped (boundedMode skipped:*) is a 422 bounded_unavailable.
//
// Query params:
//
//	players  csv   — restrict aggregates / Matrix / Events / Scoreboard to
//	               entries where attacker or victim is in the set
//	weapons  csv   — restrict aggregates + Matrix/Events + per-player
//	               ByWeapon to these (attacker) weapons (legacy alias: weapon)
//	from     float — window start, match-relative seconds (0 = no bound)
//	to       float — window end, match-relative seconds (0 = no bound)
//	summary  bool  — drop the per-hit Events log; return only aggregates
//	dmg      enum  — raw | bounded | both (default: both if summary, else raw)
func (s *server) handleDamage(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.DamageOptions{
		Players: p.CSV("players"),
		Weapons: p.CSVAny("weapons", "weapon"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
		Summary: p.Bool("summary"),
		Dmg:     p.Dmg(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	// Single resolution point for the damage-family default — the MCP layer
	// inherits it (it proxies the REST call without a dmg param). An unset dmg
	// yields the richer "both" family for a summary request (the aggregates are
	// small, so carrying both is cheap and answers more) and the lean v53 "raw"
	// shape for the full log. NOTE: the summary-driven default is deliberately
	// resolved here and may be revisited.
	if opts.Dmg == "" {
		if opts.Summary {
			opts.Dmg = "both"
		} else {
			opts.Dmg = "raw"
		}
	}
	out, err := view.Damage(res, opts)
	if err != nil {
		// ErrBoundedUnavailable wraps ErrUnavailable, so it must be singled out
		// before the generic writeUnavailable. Name the demo's boundedMode so
		// the caller learns why the bounded family is missing.
		if errors.Is(err, view.ErrBoundedUnavailable) {
			mode := ""
			if res.Damage != nil {
				mode = res.Damage.BoundedMode
			}
			writeError(w, http.StatusUnprocessableEntity, "bounded_unavailable",
				fmt.Sprintf("this demo has no bounded damage family (boundedMode %q); use dmg=raw", mode))
			return
		}
		s.writeUnavailable(w, r, err, "damage_unavailable",
			"this demo has no damage data (no KTX mvdhidden_dmgdone stream)")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleShots: GET /v1/demos/{id}/shots — the per-fire weapon stream
// (result.Shots): every detected fire with time/player/weapon/source, hit +
// victims where linkable, per-player match-time aggregates, and the KTX
// reconciliation cross-check. Served from the always-full base parse (like
// /aim), so rl/gl fires carry their projectile-linked hits and ng/sng fires
// their nail-linked ones — the streams are baked into every cached Result
// since phase 12, so this is a plain resolveDemo read (no re-parse, no
// degrade). The former `nails` opt-in param is accepted and ignored: ng/sng
// fires were always in the stream — only their linking was gated.
func (s *server) handleShots(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	sh, err := view.Shots(res)
	if err != nil {
		s.writeUnavailable(w, r, err, "shots_unavailable",
			"this demo has no shot data (no weapon fires decoded)")
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

// handleAim: GET /v1/demos/{id}/aim — per-player aim analysis (result.Aim):
// per-weapon effectiveness (shots/hits, SG/SSG pellet stats, RL/GL
// direct/splash, the LG near/blocked/out-of-range whiff split), columnar
// crosshair-error samples (hitscan), and the LG ramp series.
//
// Served from the always-full base parse (the projectile/beam/nail streams are
// baked into every cached Result since phase 12), so the stream-derived weapon
// blocks are always present — a plain resolveDemo read.
//
// Optional filters narrow the response. With no time window the stored aim is
// returned (players= selects named shooters' match-wide aim); a from/to window
// recomputes aim over the shots in that window so every figure scopes to it.
// summary drops the big per-fire crosshair + lgRamp blocks — the recommended
// way to avoid overflowing an LLM context.
//
// Query params:
//
//	players  csv   — scope to these shooters
//	from     float — window start, match-relative seconds (0 = no bound)
//	to       float — window end, match-relative seconds (0 = no bound)
//	summary  bool  — return only the per-player weapons aggregates
func (s *server) handleAim(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.AimOptions{
		Players: p.CSV("players"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
		Summary: p.Bool("summary"),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	am, err := view.Aim(res, opts)
	if err != nil {
		s.writeUnavailable(w, r, err, "aim_unavailable",
			"this demo has no aim data (needs shots + position/view streams)")
		return
	}
	writeJSON(w, http.StatusOK, am)
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
	p := newQP(r.URL.Query())
	opts := view.ChatOptions{
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
		Players: p.CSV("players"),
		Types:   p.CSV("types"),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, view.Chat(res, opts))
}

// handleDemoInfo: GET /v1/demos/{id}/demoinfo — KTX demoinfo blob
// pass-through. Carries per-player weapon accuracy, kills, deaths,
// damage, sprees, item pickup counts, RL/LG transfers.
func (s *server) handleDemoInfo(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	di, err := view.DemoInfo(res)
	if err != nil {
		s.writeUnavailable(w, r, err, "demoinfo_unavailable",
			"this demo has no KTX demoinfo block (likely non-KTX or pre-match abort)")
		return
	}
	writeJSON(w, http.StatusOK, di)
}

// handleBackpacks: GET /v1/demos/{id}/backpacks — RL/LG drops with
// optional player/weapon filters.
//
// Query params:
//
//	players  csv — restrict to drops by these dropper names
//	weapons  csv — restrict to these weapons ("rl"/"lg"; case-insensitive;
//	             legacy alias: weapon)
//	from/to  match-relative seconds — window the drop time
func (s *server) handleBackpacks(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.BackpackOptions{
		Players: p.CSV("players"),
		Weapons: p.CSVAny("weapons", "weapon"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, view.Backpacks(res, opts))
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
//
//	from/to  match-relative seconds — keep phases OVERLAPPING the window
//	         (a phase covers [availableFrom, respawnAt), open-ended when
//	         respawnAt is 0)
//	summary  bool — per-item take aggregates (takenCount, byPlayer,
//	         firstTake) instead of the phase timeline; takes are counted
//	         INSIDE the window
func (s *server) handleItems(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.ItemOptions{
		Items:   p.CSV("items"),
		Players: p.CSV("players"),
		Kinds:   p.CSV("kinds"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
	}
	summary := p.Bool("summary")
	if writeInvalidParam(w, p.Err()) {
		return
	}
	if summary {
		writeJSON(w, http.StatusOK, view.ItemsSummary(res, opts))
		return
	}
	writeJSON(w, http.StatusOK, view.Items(res, opts))
}

// handleWeaponPickups: GET /v1/demos/{id}/weapon-pickups — slot-weapon
// acquisitions with effectiveness (kills-before-next-death). Optional
// filters by player / weapon / source.
//
// Query params:
//
//	players  csv — restrict to picks by these names
//	weapons  csv — "rl","lg","gl","ssg","sng","ng" (case-insensitive;
//	             legacy alias: weapon)
//	source   "world" | "backpack" | "unknown"
//	from/to  match-relative seconds — window the pickup time
func (s *server) handleWeaponPickups(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.WeaponPickupOptions{
		Players: p.CSV("players"),
		Weapons: p.CSVAny("weapons", "weapon"),
		Source:  ciGet(r.URL.Query(), "source"),
		From:    p.Sec("from", 0),
		To:      p.Sec("to", 0),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	// source is an enum like loc/layout — a typo must 400, not silently
	// match nothing (the other enum params already reject unknowns).
	switch strings.ToLower(opts.Source) {
	case "", "world", "backpack", "unknown":
	default:
		writeError(w, http.StatusBadRequest, "invalid_param",
			fmt.Sprintf("unknown source %q; valid: world, backpack, unknown", opts.Source))
		return
	}
	writeJSON(w, http.StatusOK, view.WeaponPickups(res, opts))
}

func (s *server) handleBuckets(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.BucketsOptions{
		WindowMs:    p.Int("windowMs", 50),
		StartTime:   p.Sec("from", 0),
		EndTime:     p.Sec("to", 0),
		Players:     p.CSV("players"),
		Fields:      p.CSV("fields"),
		Reducers:    p.Reducers("reducers"),
		IncludeTeam: p.Bool("includeTeam"),
		LocIndex:    p.LocIndex(),
		Layout:      p.Layout(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	if opts.Layout == "column" {
		cb, err := view.BucketsColumnar(res, opts)
		if writeInvalidParam(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, cb)
		return
	}
	bv, err := view.Buckets(res, opts)
	if writeInvalidParam(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, bv)
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	filter := view.EventsFilter{
		StartTime: p.Sec("from", 0),
		EndTime:   p.Sec("to", 0),
		Players:   p.CSV("players"),
		Types:     p.CSV("types"),
		LocIndex:  p.LocIndex(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	ev, err := view.Events(res, filter)
	if writeInvalidParam(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) handleStreamSlice(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
	if !ok {
		return
	}
	p := newQP(r.URL.Query())
	opts := view.StreamSliceOptions{
		StartTime: p.Sec("from", 0),
		EndTime:   p.Sec("to", 0),
		Players:   p.CSV("players"),
		Fields:    p.CSV("fields"),
		LocIndex:  p.LocIndex(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	sl, err := view.StreamSlice(res, opts)
	if writeInvalidParam(w, err) {
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
	p := newQP(q)
	opts := view.StateAtOptions{
		Time:     p.Sec("time", 0),
		Players:  p.CSV("players"),
		Fields:   p.CSV("fields"),
		LocIndex: p.LocIndex(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	sa, err := view.StateAt(res, opts)
	if writeInvalidParam(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, sa)
}

// handleLOS: GET /v1/demos/{id}/los — per-player line-of-sight intervals.
//
// Line of sight is the heaviest position-derived pass and has no other
// consumer, so it is computed lazily via EnsureLOS: the first request for a
// demo triggers the raycast pass (serialised per SHA in the cache) and writes
// the result to the tier-3 artifact cache, so later requests — and later
// processes, after a restart or an LRU eviction — splice it from disk instead
// of recomputing. The tier-2 gob stays lean — LOS is never baked into it.
// Returns 200 with a players array; los is omitted for a player with no
// sightlines and empty for every player on a map with no provisioned BSP.
func (s *server) handleLOS(w http.ResponseWriter, r *http.Request) {
	id, err := democache.ParseDemoID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_demo_id", err.Error())
		return
	}
	res, meta, err := s.store.EnsureLOS(r.Context(), id)
	if err != nil {
		s.mapStoreError(w, r, err)
		return
	}
	setCacheHeaders(w, meta)
	if revalidated(w, r, meta) {
		return
	}
	writeJSON(w, http.StatusOK, losBody(res))
}

// losBody is the /los (and `los` artifact) response body: per-player LOS/PVS
// interval sets. Shared so the curated endpoint and the generic artifact
// endpoint never fork the shape.
func losBody(res *result.Result) any {
	type losPlayer struct {
		Name string            `json:"name"`
		LOS  []result.LosTrack `json:"los,omitempty"`
		PVS  []result.LosTrack `json:"pvs,omitempty"`
	}
	out := struct {
		Players []losPlayer `json:"players"`
	}{Players: []losPlayer{}} // never null: the doc + API.md promise a players array
	if res.Streams != nil {
		out.Players = make([]losPlayer, len(res.Streams.Players))
		for i := range res.Streams.Players {
			out.Players[i].Name = res.Streams.Players[i].Name
			out.Players[i].LOS = res.Streams.Players[i].LOS
			out.Players[i].PVS = res.Streams.Players[i].PVS
		}
	}
	return out
}

// handleProjectiles serves the rocket/grenade flight stream. Body is
// {"projectiles": ...}, null when the demo has none. The streams are baked
// into the always-full base parse (phase 12), so this is a plain resolveDemo
// read.
func (s *server) handleProjectiles(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
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

// handleBeams serves the LG bolt stream (from the always-full base parse).
func (s *server) handleBeams(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
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

// handleNails serves the ng/sng nail-flight stream (highest volume; from the
// always-full base parse).
func (s *server) handleNails(w http.ResponseWriter, r *http.Request) {
	res, _, ok := s.resolveDemo(w, r)
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
	p := newQP(r.URL.Query())
	// Field order here fixes which of several malformed params is reported:
	// keep from, to, minDwellMs, loc — the historical read order.
	opts := view.LocTrailsOptions{
		StartTime:  p.Sec("from", 0),
		EndTime:    p.Sec("to", 0),
		MinDwellMs: p.Int("minDwellMs", 0),
		Players:    p.CSV("players"),
		LocIndex:   p.LocIndex(),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	tr, err := view.LocTrails(res, opts)
	if writeInvalidParam(w, err) {
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
	if err := view.RegionControlAvailable(res); err != nil {
		s.writeUnavailable(w, r, err, "region_control_unavailable", "this demo has no region-control layout")
		return
	}
	p := newQP(r.URL.Query())
	opts := view.RegionControlOptions{
		WindowMs:  p.Int("windowMs", 50),
		StartTime: p.Sec("from", 0),
		EndTime:   p.Sec("to", 0),
	}
	if writeInvalidParam(w, p.Err()) {
		return
	}
	rcv, err := view.RegionControl(res, opts)
	if writeInvalidParam(w, err) {
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
	airgibs, err := view.Airgibs(res)
	if err != nil {
		s.writeUnavailable(w, r, err, "airgibs_unavailable",
			"this demo has no timeline analysis")
		return
	}
	writeJSON(w, http.StatusOK, airgibs)
}

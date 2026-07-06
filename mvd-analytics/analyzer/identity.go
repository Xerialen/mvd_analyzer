package analyzer

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// IdentityAnalyzer reconstructs player identity across reconnects.
//
// Every per-player ("Family A") output accumulates events keyed by wire
// slot and historically resolved that slot to its *final* occupant's
// name (Context.ResolveSlotDemoInfo). That breaks when a player
// disconnects and reconnects onto a different slot mid-match: the slot
// they vacated may be taken by someone else (or stamped with a late
// userinfo name), so their earlier events get relabelled with the wrong
// player. KTX itself unifies the player via its ghost mechanism
// (restore-stats-by-netname on reconnect, ktx/src/client.c:1513-1538);
// this analyzer reproduces that unification for the pipeline.
//
// It does two things during the event pass:
//
//   - Tracks per-slot *sessions* — a session is one contiguous
//     occupancy of a wire slot by a single userid. A new session opens
//     when a slot's userid changes (a fresh connection); a plain name
//     change with the same userid stays one session (a rename, which
//     today's final-name resolution already handles correctly).
//   - Records the KTX reconnect broadcast prints
//     (`rejoins the game with`, `reenters the game without stats`),
//     which name the player that just reconnected.
//
// At PopulateCore it folds sessions into canonical identities and
// publishes a per-slot, time-sorted, identity-resolved session list on
// CoreOutputs. Downstream resolves an event by co.SlotIdentityAt(slot,
// tMs) instead of the slot's final name.
type IdentityAnalyzer struct {
	ctx *Context

	// open is the currently-open session per slot (nil = unoccupied).
	open map[int]*rawSession
	// sessions is every closed-or-open session, in observation order.
	sessions []*rawSession
	// reconnectPrints are the verbatim rejoin/reenter broadcast lines;
	// resolved to netnames in PopulateCore once every session netname is
	// known (the userinfo precedes the bprint, but deferring keeps the
	// prefix match robust against names that contain the marker words).
	reconnectPrints []string
}

// rawSession is the mutable per-occupancy record built during OnEvent.
type rawSession struct {
	slot    int
	userid  int
	name    string // latest non-empty netname seen this occupancy
	team    string // latest non-empty team
	auth    string // latest non-empty *auth login
	startMs int32
	endMs   int32 // set when the session closes; math.MaxInt32 while open
}

// KTX reconnect broadcast markers (post Q-normalisation to ASCII; the
// `\220`/`\221` team brackets fold to `[`/`]` and redtext folds to
// plain — see mvd-reader/parser/userinfo.go qNormalizeTable). Pinned to
// ktx/src/client.c:1529 (team rejoin), :1536 (non-team rejoin),
// :1550/:1555 (reenter without stats).
var reconnectMarkers = []string{
	"rejoins the game with",
	"reenters the game without stats",
}

func NewIdentityAnalyzer() *IdentityAnalyzer {
	return &IdentityAnalyzer{open: make(map[int]*rawSession)}
}

func (a *IdentityAnalyzer) Name() string { return "identity" }

func (a *IdentityAnalyzer) Init(ctx *Context) error {
	a.ctx = ctx
	return nil
}

// Finalize is a no-op: the identity table is built in PopulateCore,
// which the registry runs after this analyser's Finalize and after the
// demoinfo analyser has set ctx.DemoInfo.
func (a *IdentityAnalyzer) Finalize(result *Result) error { return nil }

func (a *IdentityAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.UserInfoEvent:
		a.onUserInfo(e)
	case *events.PrintEvent:
		a.onPrint(e)
	}
	return nil
}

// onUserInfo opens / continues / rotates the session for a slot. It must
// copy the scalar fields off e.Player rather than retain the pointer:
// parseUserInfo mutates the same *PlayerInfo in place on the next
// occupancy (mvd-reader/parser/userinfo.go:47-54), so a retained pointer
// would later read the *next* player's identity.
func (a *IdentityAnalyzer) onUserInfo(e *events.UserInfoEvent) {
	if e.Player == nil {
		return
	}
	slot := e.Player.Slot
	if slot < 0 || slot >= events.MaxClients {
		return
	}
	uid := e.Player.UserID
	tMs := msTime(e.Time)

	cur := a.open[slot]
	if cur == nil {
		a.open[slot] = a.openSession(slot, uid, e.Player, tMs)
		return
	}
	// A genuinely different (both nonzero) userid means a fresh
	// connection: close the old session and open a new one. userid==0 is
	// a resend artefact (some servers null the id) — adopt it into the
	// current session rather than splitting (mirrors timeline's
	// "first valid UserID wins").
	if uid != 0 && cur.userid != 0 && uid != cur.userid {
		cur.endMs = tMs
		a.open[slot] = a.openSession(slot, uid, e.Player, tMs)
		return
	}
	if cur.userid == 0 && uid != 0 {
		cur.userid = uid
	}
	if e.Player.Name != "" {
		cur.name = e.Player.Name
	}
	if e.Player.Team != "" {
		cur.team = e.Player.Team
	}
	if e.Player.Auth != "" {
		cur.auth = e.Player.Auth
	}
}

func (a *IdentityAnalyzer) openSession(slot, uid int, p *events.PlayerInfo, tMs int32) *rawSession {
	s := &rawSession{
		slot:    slot,
		userid:  uid,
		name:    p.Name,
		team:    p.Team,
		auth:    p.Auth,
		startMs: tMs,
		endMs:   math.MaxInt32,
	}
	a.sessions = append(a.sessions, s)
	return s
}

func (a *IdentityAnalyzer) onPrint(e *events.PrintEvent) {
	for _, m := range reconnectMarkers {
		if strings.Contains(e.Message, m) {
			a.reconnectPrints = append(a.reconnectPrints, e.Message)
			return
		}
	}
}

// PopulateCore folds sessions into canonical identities and writes the
// per-slot resolved session table onto CoreOutputs. Runs after the
// demoinfo analyser (registered earlier in the core slice) so
// a.ctx.DemoInfo is available for the join.
func (a *IdentityAnalyzer) PopulateCore(co *CoreOutputs) {
	if len(a.sessions) == 0 {
		return
	}

	idx := newDemoInfoIndex(a.ctx.DemoInfo)

	// Per-session demoinfo match (login → name). Distinct sessions that
	// resolve to the same demoinfo entry are the same human.
	demoMatch := make([]*DemoInfoPlayer, len(a.sessions))
	for i, s := range a.sessions {
		if dp, ok := idx.resolve(s.name, s.auth); ok {
			demoMatch[i] = dp
		}
	}

	// Which netnames KTX told us reconnected (primary signal). Match each
	// rejoin/reenter line against the known session netnames by prefix so
	// names containing spaces or marker words still resolve.
	reconnected := a.reconnectedNames()
	anyAuth := false
	for _, s := range a.sessions {
		if s.auth != "" {
			anyAuth = true
			break
		}
	}

	uf := newUnionFind(len(a.sessions))

	// Source 1 — same nonzero *auth login (authenticated identity).
	byAuth := make(map[string]int)
	for i, s := range a.sessions {
		if s.auth == "" {
			continue
		}
		if j, ok := byAuth[s.auth]; ok {
			uf.union(i, j)
		} else {
			byAuth[s.auth] = i
		}
	}
	// Source 2 — same demoinfo player (login or name join).
	byDemo := make(map[*DemoInfoPlayer]int)
	for i, dp := range demoMatch {
		if dp == nil {
			continue
		}
		if j, ok := byDemo[dp]; ok {
			uf.union(i, j)
		} else {
			byDemo[dp] = i
		}
	}
	// Source 3 — KTX reconnect prints: every session whose netname KTX
	// announced as reconnecting is the same human.
	byReconName := make(map[string]int)
	for i, s := range a.sessions {
		norm := normalizePlayerName(s.name)
		if !reconnected[norm] {
			continue
		}
		if j, ok := byReconName[norm]; ok {
			uf.union(i, j)
		} else {
			byReconName[norm] = i
		}
	}
	// Source 4 — fallback for bare demos (no demoinfo, no auth, no KTX
	// reconnect prints): unify by normalized netname. Restricted to that
	// case so we never over-merge two distinct same-name players on a
	// modern demo where the richer signals apply.
	if idx == nil && !anyAuth && len(reconnected) == 0 {
		byName := make(map[string]int)
		for i, s := range a.sessions {
			norm := normalizePlayerName(s.name)
			if j, ok := byName[norm]; ok {
				uf.union(i, j)
			} else {
				byName[norm] = i
			}
		}
	}

	// Canonical display name + team per identity group. Prefer a
	// demoinfo match; else the latest session's netname/team.
	type ident struct {
		name, team string
		dp         *DemoInfoPlayer
		lastStart  int32
	}
	groups := make(map[int]*ident)
	for i, s := range a.sessions {
		root := uf.find(i)
		g := groups[root]
		if g == nil {
			g = &ident{lastStart: math.MinInt32}
			groups[root] = g
		}
		if demoMatch[i] != nil && g.dp == nil {
			g.dp = demoMatch[i]
		}
		if s.startMs >= g.lastStart {
			g.lastStart = s.startMs
			if s.name != "" {
				g.name = s.name
				g.team = s.team
			}
		}
	}
	for _, g := range groups {
		if g.dp != nil {
			g.name = g.dp.Name
			if g.dp.Team != "" {
				g.team = g.dp.Team
			}
		}
	}

	// Build the per-slot, time-sorted resolved session list. The first
	// session on a slot extends back to -inf and the last forward to
	// +inf so events on the edges (before the first userinfo, after the
	// last) still resolve.
	sessions := make(map[int][]ResolvedSession)
	identityKey := func(root int) string { return "id:" + strconv.Itoa(root) }
	for i, s := range a.sessions {
		root := uf.find(i)
		g := groups[root]
		sessions[s.slot] = append(sessions[s.slot], ResolvedSession{
			StartMs:     s.startMs,
			EndMs:       s.endMs,
			Name:        g.name,
			Team:        g.team,
			IdentityKey: identityKey(root),
		})
	}
	for slot := range sessions {
		ss := sessions[slot]
		sort.Slice(ss, func(i, j int) bool { return ss[i].StartMs < ss[j].StartMs })
		ss[0].StartMs = math.MinInt32
		ss[len(ss)-1].EndMs = math.MaxInt32
		sessions[slot] = ss
	}
	co.Sessions = sessions
}

// reconnectedNames resolves each stored rejoin/reenter line to the set
// of normalized netnames that reconnected. A line renders as
// "<name> [<team>] rejoins the game with N frags" (team) or
// "<name> rejoins the game with N frags" (non-team); the netname can
// itself contain spaces, so we match against the known session netnames
// by longest prefix rather than trying to tokenize the line.
func (a *IdentityAnalyzer) reconnectedNames() map[string]bool {
	out := make(map[string]bool)
	if len(a.reconnectPrints) == 0 {
		return out
	}
	// Distinct session netnames, longest first for prefix matching.
	names := make([]string, 0, len(a.sessions))
	seen := make(map[string]bool)
	for _, s := range a.sessions {
		if s.name != "" && !seen[s.name] {
			seen[s.name] = true
			names = append(names, s.name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	for _, msg := range a.reconnectPrints {
		for _, n := range names {
			if strings.HasPrefix(msg, n+" ") {
				out[normalizePlayerName(n)] = true
				break
			}
		}
	}
	return out
}

// --- union-find ---

type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(i, j int) {
	ri, rj := u.find(i), u.find(j)
	if ri != rj {
		u.parent[rj] = ri
	}
}

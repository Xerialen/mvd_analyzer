package analyzer

import "fmt"

// This file makes the analyzer pipeline's previously-implicit dependency
// DAG explicit as data (Stage 1 of PLAN-improve-analytics.md §5). Each
// analyzer and post-processor is wrapped in a nodeSpec declaring what
// artifacts it Requires and Provides; the engine validates the wiring
// and derives a deterministic execution order from it instead of relying
// on hand-ordered registration slices.
//
// The execution order is derived purely from the declared edges; the
// output is a pure function of the demo under ANY valid topological
// order (TestOrderIndependence), so registration order is inventory,
// not a correctness mechanism. Post-processors still
// mutate the assembled Result in place — every one is flagged
// Mutates:true as a temporary marker of the debt Stage 2 (the clock /
// roster refactor) removes.
//
// The two out-of-band lazy passes — ComputeLOS (los.go) and the shot/nail
// spatial-stream re-parse — are modelled as lazy DAG nodes since Stage 3
// (materialize.go): each is a nodeSpec with Lazy:true, registered in
// lazyArtifacts rather than in the eager core/derived/post slices. They do
// NOT enter analyzeSource's execution order (r.specs / r.nodes stay the 21
// eager nodes), so the eager bundle and the golden corpus are unchanged;
// they appear in -graph output marked lazy and are materialised on demand
// through the LazyArtifact hooks (mvd-api's per-artifact tier-3 cache).

// nodeSpec is one pipeline node declared as data: an analyzer's Finalize
// or a post-processor, with the artifact edges the engine schedules on.
//
// Artifacts are plain string names. Every node provides an artifact
// named after itself (so any node can be depended on by name), plus any
// extra pseudo-artifacts it publishes (the ordering barriers
// "epoch:match" / "teams:final" / "telefrags:recovered"). Requires names
// the artifacts a node's Finalize / post-processor reads.
type nodeSpec struct {
	Name     string   // unique kebab-case node id and its primary artifact
	Requires []string // artifact names this node reads
	Provides []string // artifact names this node writes (includes Name)
	Mutates  bool     // true for every post-processor (Stage-2 debt marker)
	Lazy     bool     // materialised on demand, not in the eager bundle (Stage 3)
	tier     string   // "core" | "derived" | "post" | "lazy" — export grouping only
	regIndex int      // registration position; the deterministic topo tie-break

	// Stage-4 manifest metadata (surfaced by ArtifactManifest / -graph json /
	// ARTIFACTS.md). These describe the node as a servable artifact; they do
	// not affect scheduling.
	resultKey string // top-level Result JSON key the artifact lands in ("" = pseudo/internal, not served)
	cost      string // "light" (default) | "heavy" — advisory for API gating / docs
	desc      string // one-sentence contributor-facing description

	analyzer Analyzer            // set for analyzer (event-tier) nodes
	post     ResultPostProcessor // set for post-processor nodes
}

// nodeMeta is the static dependency declaration for one node, keyed by
// the live handle's identity (an analyzer's Name(), or a post-processor's
// resolved function name). name is the node's kebab-case DAG id; the
// node's primary artifact is name itself, so provides lists only the
// EXTRA (pseudo-)artifacts it publishes beyond its own name.
type nodeMeta struct {
	name     string
	tier     string
	requires []string
	provides []string
	mutates  bool

	// Stage-4 manifest metadata — see nodeSpec's like-named fields. cost is
	// "" for every eager node (defaulted to "light" in specFromMeta); the two
	// heavy passes are the lazy nodes, which set it directly.
	resultKey string
	cost      string
	desc      string
}

// analyzerNodeMeta declares the DAG edges for each analyzer, keyed by its
// Analyzer.Name(). This encodes §1.3 of the plan's reverse-engineered
// edge list:
//
//   - the CoreOutputs edges (demoinfo → identity → frag; the co.* reads
//     of every derived analyzer via ResolveSlotAt / co.FragEntries);
//   - the hidden intra-derived edge timeline → shots (shots writes
//     Projectiles/Beams/Nails into result.Streams, which timeline creates).
//
// The clock artifact (co.Clock) is required by every producer that stamps a
// timestamp match-relative at Finalize (the born-correct conversion that
// replaced the whole-Result rebase), so those nodes declare "clock" —
// map_entities and match carry no timestamps and do not.
var analyzerNodeMeta = map[string]nodeMeta{
	// Core producers.
	"clock": {name: "clock", tier: "core",
		desc: "Match clock — match start/end, pauses, and the demo-start wall-clock anchor every timestamped producer converts against."},
	"demoinfo": {name: "demoinfo", tier: "core", resultKey: "demoInfo",
		desc: "KTX demoinfo scoreboard blob: per-player weapon accuracy, kills, deaths, damage, sprees, and item pickup counts, verbatim from the server."},
	"identity": {name: "identity", tier: "core", requires: []string{"demoinfo"},
		desc: "Player identity sessions — the slot→name/userid/team resolution every downstream producer reads."},
	"frag": {name: "frag", tier: "core", requires: []string{"clock", "demoinfo", "identity"}, resultKey: "frags",
		desc: "Frag aggregates and the full chronological kill log with weapon, suicide, and team-kill flags."},
	"roster": {name: "roster", tier: "core", requires: []string{"demoinfo"},
		desc: "Canonical player roster with team labels (duel player-as-team rewrite applied), read by every team-aware producer."},

	// Derived consumers / independent peers.
	"metadata": {name: "metadata", tier: "derived", resultKey: "metadata",
		desc: "Server cvars and parsed KTX match settings (mode, timelimit, antilag, midair, instagib, ...)."},
	"match": {name: "match", tier: "derived", requires: []string{"demoinfo"}, resultKey: "match",
		desc: "Match summary: map, mode, duration, and the corrected per-player scoreboard."},
	"messages": {name: "messages", tier: "derived", requires: []string{"clock", "demoinfo", "roster"}, resultKey: "messages",
		desc: "Chat, teamsay, and other match print messages with markup-stripped text."},
	"timelineAnalysis": {name: "timeline", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "frag", "roster"}, resultKey: "timelineAnalysis",
		desc: "Match timeline: phases, streaks, powerup runs, pauses, region-control layout, airgibs, and the per-player event-stream container."},
	"items": {name: "items", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "roster"}, resultKey: "items",
		desc: "Per-item pickup/respawn timeline with world position and nearest loc."},
	"damage": {name: "damage", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "roster"}, resultKey: "damage",
		desc: "Per-hit damage from the KTX stream: totals, matrix, per-weapon, EWep buckets, telefrags, stomps, and the scoreboard cross-check."},
	"shots": {name: "shots", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "timeline", "roster"}, resultKey: "shots",
		desc: "Per-fire weapon stream with per-player accuracy aggregates and the KTX cross-check (stream-derived splits need the shot-streams artifact)."},
	"map_entities": {name: "map-entities", tier: "derived", resultKey: "mapEntities",
		desc: "The map's static designed entity layout (item spawns, spawnpoints, teleporters) resolved from the embedded BSP corpus."},
	"backpacks": {name: "backpacks", tier: "derived", requires: []string{"clock", "roster"}, resultKey: "backpacks",
		desc: "RL/LG backpack drops from KTX drop hints, with dropper, weapon, origin, and the ent number joining to weapon pickups."},
	"weaponPickups": {name: "weapon-pickups", tier: "derived", requires: []string{"clock", "identity", "frag", "roster"}, resultKey: "weaponPickups",
		desc: "Slot-weapon acquisitions (world spawners and backpacks) with kills-before-next-death effectiveness."},
}

// postNodeMeta declares the DAG edges for each post-processor, keyed by
// its resolved function name (postProcName). It encodes §1.3's result.*
// read edges. Both whole-Result barrier pseudo-artifacts have now retired:
//
//   - "epoch:match" retired with the clock refactor — timestamps are born
//     match-relative in each producer's Finalize.
//   - "teams:final" retired with the roster refactor — team labels are born
//     correct in each producer's Finalize (roster/match/timeline/messages/
//     items/pickups/backpacks/shots read co.Roster). aim / scoreboard-stats /
//     loc-graph / region-control therefore keep only their data edges: they
//     already read final team labels through those artifacts (aim via shots,
//     scoreboard via match, loc-graph/region-control via streams + match).
//
// recover-telefrag-teamkills still runs before scoreboard-stats (which requires
// it by name to read the corrected frag log); it requires clock because it
// converts victim-named teamkill times against co.Clock.
var postNodeMeta = map[string]nodeMeta{
	"recoverTelefragTeamkills": {
		name: "recover-telefrag-teamkills", tier: "post", mutates: true,
		requires: []string{"clock", "demoinfo", "frag", "timeline"},
		desc:     "Recovers victim-named telefrag team-kills into the frag log.",
	},
	"aimPost": {
		name: "aim", tier: "post", mutates: true,
		requires:  []string{"shots", "timeline", "damage"},
		resultKey: "aim",
		desc:      "Per-player aim analysis: per-weapon effectiveness, crosshair-error samples, and the LG ramp series (full splits need the shot-streams artifact).",
	},
	"airgibsPost": {
		name: "airgibs", tier: "post", mutates: true,
		requires: []string{"demoinfo", "frag", "timeline", "damage"},
		desc:     "Folds the Key-Moments airgib list into the timeline (direct enemy rocket hits on airborne victims above the height threshold).",
	},
	"scoreboardStatsPost": {
		name: "scoreboard-stats", tier: "post", mutates: true,
		requires: []string{"match", "frag", "recover-telefrag-teamkills"},
		desc:     "Folds corrected kills/deaths/suicides into the match scoreboard.",
	},
	"locGraphPost": {
		name: "loc-graph", tier: "post", mutates: true,
		requires:  []string{"timeline", "demoinfo"},
		resultKey: "locGraph",
		desc:      "Per-map loc adjacency graph with directed transition weights derived from player movement.",
	},
	"regionControlPost": {
		name: "region-control", tier: "post", mutates: true,
		requires: []string{"timeline", "match", "demoinfo"},
		desc:     "Folds the default-window region-control aggregation into the timeline (arbitrary windows are a view, not an artifact).",
	},
}

// specFromMeta builds a nodeSpec from a live handle's metadata, attaching
// the primary artifact (Name) to Provides.
func specFromMeta(m nodeMeta, regIndex int, a Analyzer, p ResultPostProcessor) nodeSpec {
	provides := make([]string, 0, 1+len(m.provides))
	provides = append(provides, m.name)
	provides = append(provides, m.provides...)
	cost := m.cost
	if cost == "" {
		cost = costLight // every eager node is light; the heavy passes are lazy
	}
	return nodeSpec{
		Name:      m.name,
		Requires:  m.requires,
		Provides:  provides,
		Mutates:   m.mutates,
		tier:      m.tier,
		regIndex:  regIndex,
		resultKey: m.resultKey,
		cost:      cost,
		desc:      m.desc,
		analyzer:  a,
		post:      p,
	}
}

// collectSpecs wraps every registered analyzer and post-processor in a
// nodeSpec, assigning regIndex in registration order (core, then derived,
// then post-processors). The regIndex is the topo sort's tie-break — an
// arbitrary deterministic default kept for stable -graph output, log
// readability and comparable PhaseTimings; output is identical under any
// valid order (TestOrderIndependence).
func (r *Registry) collectSpecs() []nodeSpec {
	specs := make([]nodeSpec, 0, len(r.core)+len(r.derived)+len(r.postProcessors))
	idx := 0
	for _, a := range r.core {
		m, ok := analyzerNodeMeta[a.Name()]
		if !ok {
			panic(fmt.Sprintf("dag: core analyzer %q has no node metadata", a.Name()))
		}
		specs = append(specs, specFromMeta(m, idx, a, nil))
		idx++
	}
	for _, a := range r.derived {
		m, ok := analyzerNodeMeta[a.Name()]
		if !ok {
			panic(fmt.Sprintf("dag: derived analyzer %q has no node metadata", a.Name()))
		}
		specs = append(specs, specFromMeta(m, idx, a, nil))
		idx++
	}
	for _, p := range r.postProcessors {
		pn := postProcName(p)
		m, ok := postNodeMeta[pn]
		if !ok {
			panic(fmt.Sprintf("dag: post-processor %q has no node metadata", pn))
		}
		specs = append(specs, specFromMeta(m, idx, nil, p))
		idx++
	}
	return specs
}

// buildGraph collects the registry's node specs, validates the wiring,
// and derives the deterministic execution order. It is called once from
// NewDefaultRegistry; a wiring bug is a programmer error, so it panics
// with the validation message (dag_test.go asserts the default graph is
// valid so a panic can never ship). Registries built by hand (NewRegistry
// + Register*) never call this and fall back to registration-order
// execution in analyzeSource.
func (r *Registry) buildGraph() {
	r.specs = r.collectSpecs()
	sorted, err := buildDAG(r.specs)
	if err != nil {
		panic(err.Error())
	}
	r.nodes = sorted
	// Validate the lazy nodes against the eager set too, so a lazy Requires
	// typo is a startup panic (they read eager artifacts but never enter the
	// execution order — see materialize.go). r.specs / r.nodes are unaffected.
	if err := validateDAG(append(append([]nodeSpec(nil), r.specs...), lazyArtifactSpecs()...)); err != nil {
		panic(err.Error())
	}
}

// buildDAG validates the spec set and returns it in topological
// execution order. Used by NewDefaultRegistry and the tests.
func buildDAG(specs []nodeSpec) ([]nodeSpec, error) {
	if err := validateDAG(specs); err != nil {
		return nil, err
	}
	return topoSortDAG(specs)
}

// validateDAG checks that every provided artifact has exactly one
// provider and every required artifact has a provider. Errors name the
// offending artifact and node so a wiring typo is self-describing.
func validateDAG(specs []nodeSpec) error {
	provider := make(map[string]string, len(specs)*2)
	for _, s := range specs {
		for _, art := range s.Provides {
			if prev, ok := provider[art]; ok {
				return fmt.Errorf("dag: artifact %q is provided by both %q and %q", art, prev, s.Name)
			}
			provider[art] = s.Name
		}
	}
	for _, s := range specs {
		for _, req := range s.Requires {
			if _, ok := provider[req]; !ok {
				return fmt.Errorf("dag: node %q requires artifact %q, which no node provides", s.Name, req)
			}
		}
	}
	return nil
}

// topoSortDAG orders specs by Kahn's algorithm, breaking ties by
// registration index — an arbitrary deterministic tie-break (any valid
// order yields identical output; see TestOrderIndependence). Determinism
// is kept only so the default schedule, -graph output and PhaseTimings
// stay stable run-to-run. The scan is index-based (no map iteration on the ordering path), so
// the output is deterministic regardless of GOMAXPROCS / map seed.
//
// Assumes validateDAG has passed (every Requires has a unique provider);
// a remaining unschedulable node means a cycle, which is reported by name.
func topoSortDAG(specs []nodeSpec) ([]nodeSpec, error) {
	// Production tie-break: min registration index. Because registration
	// order is used as the tie-break among ready nodes.
	return topoSortDAGTieBreak(specs, func(i, j int) bool {
		return specs[i].regIndex < specs[j].regIndex
	})
}

// topoSortDAGTieBreak is topoSortDAG with a caller-supplied tie-break:
// among the ready (indegree-zero) nodes it schedules the one for which
// prefer(i, j) reports i should come before every other ready j. All
// declared edges are still respected, so every output is a valid
// topological order regardless of the tie-break. The seam exists so
// TestOrderIndependence can drive K seeded-random valid orders through
// analyzeSource and prove the Result is schedule-free (and, continuously,
// that the declared edge list is complete). Production always passes the
// regIndex comparator via topoSortDAG.
func topoSortDAGTieBreak(specs []nodeSpec, prefer func(i, j int) bool) ([]nodeSpec, error) {
	n := len(specs)
	provider := make(map[string]int, n*2)
	for i, s := range specs {
		for _, art := range s.Provides {
			provider[art] = i
		}
	}

	indeg := make([]int, n)
	adj := make([][]int, n)
	for i, s := range specs {
		seen := make(map[int]bool, len(s.Requires))
		for _, req := range s.Requires {
			p, ok := provider[req]
			if !ok || p == i || seen[p] {
				continue // unknown (validation catches), self, or duplicate edge
			}
			seen[p] = true
			adj[p] = append(adj[p], i)
			indeg[i]++
		}
	}

	order := make([]nodeSpec, 0, n)
	done := make([]bool, n)
	for len(order) < n {
		best := -1
		for i := 0; i < n; i++ {
			if done[i] || indeg[i] != 0 {
				continue
			}
			if best == -1 || prefer(i, best) {
				best = i
			}
		}
		if best == -1 {
			var stuck []string
			for i := 0; i < n; i++ {
				if !done[i] {
					stuck = append(stuck, specs[i].Name)
				}
			}
			return nil, fmt.Errorf("dag: dependency cycle among nodes %v", stuck)
		}
		done[best] = true
		order = append(order, specs[best])
		for _, m := range adj[best] {
			indeg[m]--
		}
	}
	return order, nil
}

// execOrder returns the node list analyzeSource drives execution from:
// the validated topological order for a default registry, or the raw
// registration order for a hand-built one (NewRegistry + Register*),
// which has no declared graph. Which valid order runs is immaterial to
// the output (TestOrderIndependence).
func (r *Registry) execOrder() []nodeSpec {
	if r.orderOverride != nil {
		return r.orderOverride // test-only injected valid topo order
	}
	if r.nodes != nil {
		return r.nodes
	}
	specs := make([]nodeSpec, 0, len(r.core)+len(r.derived)+len(r.postProcessors))
	for _, a := range r.core {
		specs = append(specs, nodeSpec{Name: a.Name(), tier: "core", analyzer: a})
	}
	for _, a := range r.derived {
		specs = append(specs, nodeSpec{Name: a.Name(), tier: "derived", analyzer: a})
	}
	for _, p := range r.postProcessors {
		specs = append(specs, nodeSpec{Name: postProcName(p), tier: "post", post: p})
	}
	return specs
}

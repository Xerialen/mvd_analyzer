package analyzer

import "fmt"

// This file makes the analyzer pipeline's previously-implicit dependency
// DAG explicit as data (Stage 1 of PLAN-improve-analytics.md §5). Each
// analyzer and post-processor is wrapped in a nodeSpec declaring what
// artifacts it Requires and Provides; the engine validates the wiring
// and derives a deterministic execution order from it instead of relying
// on hand-ordered registration slices.
//
// Stage 1 is a zero-behaviour-change refactor: the derived topological
// order is asserted (dag_test.go) to be byte-identical to today's
// registration order, so the Result is unchanged. Post-processors still
// mutate the assembled Result in place — every one is flagged
// Mutates:true as a temporary marker of the debt Stage 2 (the clock /
// roster refactor) removes.
//
// Two out-of-band lazy passes are deliberately NOT modelled as nodes
// here: ComputeLOS (los.go) and the shot/nail spatial-stream re-parse
// (mvd-api democache.EnsureShotStreams). They run on demand outside
// analyzeSource, are not part of the eager bundle, and would each need a
// lazy-execution flag the Stage-1 engine does not have. They enter the
// DAG in Stage 3; adding them now would cost execution-path branching
// for no behavioural gain.

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
	tier     string   // "core" | "derived" | "post" — export grouping only
	regIndex int      // registration position; the deterministic topo tie-break

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
	"clock":    {name: "clock", tier: "core"},
	"demoinfo": {name: "demoinfo", tier: "core"},
	"identity": {name: "identity", tier: "core", requires: []string{"demoinfo"}},
	"frag":     {name: "frag", tier: "core", requires: []string{"clock", "demoinfo", "identity"}},
	"roster":   {name: "roster", tier: "core", requires: []string{"demoinfo"}},

	// Derived consumers / independent peers.
	"metadata":         {name: "metadata", tier: "derived"},
	"match":            {name: "match", tier: "derived", requires: []string{"demoinfo"}},
	"messages":         {name: "messages", tier: "derived", requires: []string{"clock", "demoinfo", "roster"}},
	"timelineAnalysis": {name: "timeline", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "frag", "roster"}},
	"items":            {name: "items", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "roster"}},
	"damage":           {name: "damage", tier: "derived", requires: []string{"clock", "demoinfo", "identity"}},
	"shots":            {name: "shots", tier: "derived", requires: []string{"clock", "demoinfo", "identity", "timeline", "roster"}},
	"map_entities":     {name: "map-entities", tier: "derived"},
	"backpacks":        {name: "backpacks", tier: "derived", requires: []string{"clock", "roster"}},
	"weaponPickups":    {name: "weapon-pickups", tier: "derived", requires: []string{"clock", "identity", "frag", "roster"}},
}

// postNodeMeta declares the DAG edges for each post-processor, keyed by
// its resolved function name (postProcName). It encodes §1.3's result.*
// read edges plus the one remaining "Ordering barriers" pseudo-artifact:
//
//   - duel-team-normalize provides "teams:final" (stable team labels),
//     required by aim / scoreboard-stats / loc-graph / region-control.
//
// The old "epoch:match" barrier retired with the clock refactor: timestamps
// are born match-relative in each producer's Finalize, so aim / airgibs /
// loc-graph / region-control no longer wait on a whole-Result time rebase —
// they keep only their data edges. recover-telefrag-teamkills still runs
// before scoreboard-stats (which requires it by name to read the corrected
// frag log); it requires clock because it converts victim-named teamkill
// times against co.Clock.
var postNodeMeta = map[string]nodeMeta{
	"recoverTelefragTeamkills": {
		name: "recover-telefrag-teamkills", tier: "post", mutates: true,
		requires: []string{"clock", "demoinfo", "frag", "timeline"},
	},
	"duelTeamNormalize": {
		name: "duel-team-normalize", tier: "post", mutates: true,
		requires: []string{"demoinfo", "match", "frag", "timeline", "messages", "items", "shots", "backpacks", "weapon-pickups"},
		provides: []string{"teams:final"},
	},
	"aimPost": {
		name: "aim", tier: "post", mutates: true,
		requires: []string{"shots", "timeline", "damage", "teams:final"},
	},
	"airgibsPost": {
		name: "airgibs", tier: "post", mutates: true,
		requires: []string{"demoinfo", "frag", "timeline", "damage"},
	},
	"scoreboardStatsPost": {
		name: "scoreboard-stats", tier: "post", mutates: true,
		requires: []string{"match", "frag", "recover-telefrag-teamkills", "teams:final"},
	},
	"locGraphPost": {
		name: "loc-graph", tier: "post", mutates: true,
		requires: []string{"timeline", "demoinfo", "teams:final"},
	},
	"regionControlPost": {
		name: "region-control", tier: "post", mutates: true,
		requires: []string{"timeline", "match", "demoinfo", "teams:final"},
	},
}

// specFromMeta builds a nodeSpec from a live handle's metadata, attaching
// the primary artifact (Name) to Provides.
func specFromMeta(m nodeMeta, regIndex int, a Analyzer, p ResultPostProcessor) nodeSpec {
	provides := make([]string, 0, 1+len(m.provides))
	provides = append(provides, m.name)
	provides = append(provides, m.provides...)
	return nodeSpec{
		Name:     m.name,
		Requires: m.requires,
		Provides: provides,
		Mutates:  m.mutates,
		tier:     m.tier,
		regIndex: regIndex,
		analyzer: a,
		post:     p,
	}
}

// collectSpecs wraps every registered analyzer and post-processor in a
// nodeSpec, assigning regIndex in registration order (core, then derived,
// then post-processors). The regIndex is the topo sort's tie-break, so
// the derived order provably reproduces registration order (dag_test.go).
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
// registration index. Because the registration order is itself a valid
// topological order, the min-regIndex tie-break provably reproduces it
// exactly while still verifying the declared edges are consistent with
// it. The scan is index-based (no map iteration on the ordering path), so
// the output is deterministic regardless of GOMAXPROCS / map seed.
//
// Assumes validateDAG has passed (every Requires has a unique provider);
// a remaining unschedulable node means a cycle, which is reported by name.
func topoSortDAG(specs []nodeSpec) ([]nodeSpec, error) {
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
			if best == -1 || specs[i].regIndex < specs[best].regIndex {
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
// which has no declared graph. The two coincide for the default registry
// (topo == registration), so driving from the sorted list is
// byte-identical to the legacy slice iteration.
func (r *Registry) execOrder() []nodeSpec {
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

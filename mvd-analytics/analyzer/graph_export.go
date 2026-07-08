package analyzer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ExportGraph renders the analyzer dependency DAG (nodes + declared
// artifact edges) in the requested format. It builds a default registry
// to obtain the canonical eager node set, then appends the lazy node
// (los — materialised on demand, not in the eager bundle),
// and needs no demo. Supported formats:
//
//	"mermaid" — a flowchart TB grouped into DAG-depth layers. Post-processor
//	            nodes carry a coloured border and the lazy node a dashed one;
//	            unmarked nodes are event-reading analyzers.
//	"json"    — {nodes:[{name,requires,provides,mutates,lazy,depth}], edges:[{from,to,artifact}]}.
//
// It is the single exported entry point the qw-analyze -graph flag needs.
func ExportGraph(format string) (string, error) {
	specs := append(NewDefaultRegistry().specs, lazyArtifactSpecs()...)
	switch format {
	case "mermaid":
		return renderGraphMermaid(specs), nil
	case "json":
		return renderGraphJSON(specs)
	default:
		return "", fmt.Errorf("unknown graph format %q (want mermaid | json)", format)
	}
}

// providerIndex maps each artifact name to the node that provides it.
func providerIndex(specs []nodeSpec) map[string]string {
	provider := make(map[string]string, len(specs)*2)
	for _, s := range specs {
		for _, art := range s.Provides {
			provider[art] = s.Name
		}
	}
	return provider
}

type graphNodeJSON struct {
	Name      string   `json:"name"`
	Requires  []string `json:"requires"`
	Provides  []string `json:"provides"`
	Mutates   bool     `json:"mutates"`
	Lazy      bool     `json:"lazy"`
	Depth     int      `json:"depth"`
	Cost      string   `json:"cost"`
	ResultKey string   `json:"resultKey"`
}

// nodeDepth returns each node's longest-path depth in the DAG: 0 for a
// node whose requirements have no in-graph provider (a root), else
// 1 + the max depth of its requirements' providers. The graph is acyclic
// (topoSortDAG panics in buildGraph otherwise), so the memoised recursion
// terminates. This is a display grouping only — the execution order is
// topoSortDAG, not this layering.
func nodeDepth(specs []nodeSpec) map[string]int {
	provider := providerIndex(specs)
	byName := make(map[string]nodeSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}
	depth := make(map[string]int, len(specs))
	var visit func(name string) int
	visit = func(name string) int {
		if d, ok := depth[name]; ok {
			return d
		}
		d := 0
		for _, req := range byName[name].Requires {
			from, ok := provider[req]
			if !ok || from == name {
				continue
			}
			if pd := visit(from) + 1; pd > d {
				d = pd
			}
		}
		depth[name] = d
		return d
	}
	for _, s := range specs {
		visit(s.Name)
	}
	return depth
}

type graphEdgeJSON struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Artifact string `json:"artifact"`
}

type graphJSON struct {
	Nodes []graphNodeJSON `json:"nodes"`
	Edges []graphEdgeJSON `json:"edges"`
}

func renderGraphJSON(specs []nodeSpec) (string, error) {
	provider := providerIndex(specs)
	depth := nodeDepth(specs)
	g := graphJSON{
		Nodes: make([]graphNodeJSON, 0, len(specs)),
		Edges: make([]graphEdgeJSON, 0),
	}
	for _, s := range specs {
		g.Nodes = append(g.Nodes, graphNodeJSON{
			Name:      s.Name,
			Requires:  append([]string(nil), s.Requires...),
			Provides:  append([]string(nil), s.Provides...),
			Mutates:   s.Mutates,
			Lazy:      s.Lazy,
			Depth:     depth[s.Name],
			Cost:      s.cost,
			ResultKey: s.resultKey,
		})
		for _, req := range s.Requires {
			if from, ok := provider[req]; ok {
				g.Edges = append(g.Edges, graphEdgeJSON{From: from, To: s.Name, Artifact: req})
			}
		}
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// mermaidID sanitises a node/artifact name into a mermaid-safe id
// (hyphens and colons are not legal in bare ids).
func mermaidID(name string) string {
	r := strings.NewReplacer("-", "_", ":", "_")
	return r.Replace(name)
}

func renderGraphMermaid(specs []nodeSpec) string {
	provider := providerIndex(specs)
	depth := nodeDepth(specs)
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}

	var b strings.Builder
	b.WriteString("flowchart TB\n")

	// Group nodes into subgraphs by DAG depth (dependency layers). Nodes
	// keep their registration order within a layer for stable output. This
	// is a readability grouping, not the execution order (topoSortDAG).
	for d := 0; d <= maxDepth; d++ {
		var atDepth []nodeSpec
		for _, s := range specs {
			if depth[s.Name] == d {
				atDepth = append(atDepth, s)
			}
		}
		if len(atDepth) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  subgraph d%d[\"depth %d\"]\n", d, d)
		for _, s := range atDepth {
			fmt.Fprintf(&b, "    %s[\"%s\"]\n", mermaidID(s.Name), s.Name)
		}
		b.WriteString("  end\n")
	}

	// Edges, sorted for stable output: provider --|artifact|--> requirer.
	type edge struct{ from, to, artifact string }
	var edges []edge
	for _, s := range specs {
		for _, req := range s.Requires {
			if from, ok := provider[req]; ok {
				edges = append(edges, edge{from: from, to: s.Name, artifact: req})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		if edges[i].to != edges[j].to {
			return edges[i].to < edges[j].to
		}
		return edges[i].artifact < edges[j].artifact
	})
	for _, e := range edges {
		fmt.Fprintf(&b, "  %s -->|\"%s\"| %s\n", mermaidID(e.from), e.artifact, mermaidID(e.to))
	}

	// Mark the node kinds the depth layers don't show — the one behavioral
	// axis besides depth. Post-processors (no event pass; they only refine
	// the assembled Result) get a coloured border; the lazy node
	// (materialised on demand) a dashed one. Unmarked nodes are event-reading
	// analyzers, so the styling doubles as "which nodes read the event
	// stream": the plain ones do, the marked ones don't.
	var post, lazy []string
	for _, s := range specs {
		switch {
		case s.Lazy:
			lazy = append(lazy, mermaidID(s.Name))
		case s.post != nil:
			post = append(post, mermaidID(s.Name))
		}
	}
	if len(post) > 0 {
		b.WriteString("  classDef post stroke:#d9730d,stroke-width:2px;\n")
		fmt.Fprintf(&b, "  class %s post;\n", strings.Join(post, ","))
	}
	if len(lazy) > 0 {
		b.WriteString("  classDef lazy stroke-dasharray:4 3;\n")
		fmt.Fprintf(&b, "  class %s lazy;\n", strings.Join(lazy, ","))
	}
	return b.String()
}

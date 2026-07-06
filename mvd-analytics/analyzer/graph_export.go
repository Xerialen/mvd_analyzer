package analyzer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ExportGraph renders the analyzer dependency DAG (nodes + declared
// artifact edges) in the requested format. It builds a default registry
// to obtain the canonical node set and needs no demo. Supported formats:
//
//	"mermaid" — a flowchart TB grouped into core / derived / post tiers.
//	"json"    — {nodes:[{name,requires,provides,mutates,tier}], edges:[{from,to,artifact}]}.
//
// It is the single exported entry point the qw-analyze -graph flag needs.
func ExportGraph(format string) (string, error) {
	specs := NewDefaultRegistry().specs
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
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Provides []string `json:"provides"`
	Mutates  bool     `json:"mutates"`
	Tier     string   `json:"tier"`
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
	g := graphJSON{
		Nodes: make([]graphNodeJSON, 0, len(specs)),
		Edges: make([]graphEdgeJSON, 0),
	}
	for _, s := range specs {
		g.Nodes = append(g.Nodes, graphNodeJSON{
			Name:     s.Name,
			Requires: append([]string(nil), s.Requires...),
			Provides: append([]string(nil), s.Provides...),
			Mutates:  s.Mutates,
			Tier:     s.tier,
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

	var b strings.Builder
	b.WriteString("flowchart TB\n")

	tiers := []struct{ id, label string }{
		{"core", "core (state reconstruction)"},
		{"derived", "derived (finalize)"},
		{"post", "post-processors (in-place Result mutation)"},
	}
	for _, t := range tiers {
		fmt.Fprintf(&b, "  subgraph %s[\"%s\"]\n", t.id, t.label)
		for _, s := range specs {
			if s.tier != t.id {
				continue
			}
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
	return b.String()
}

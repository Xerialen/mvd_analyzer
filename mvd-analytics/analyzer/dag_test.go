package analyzer

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// registrationOrder is the expected node INVENTORY (used as a set by
// TestDAGNodeInventory; the sequence shown is the default tie-break order
// for readability). Since phase 10 the sequence is not a correctness
// property — TestOrderIndependence proves any valid topological order
// produces identical output.
var registrationOrder = []string{
	// core
	"clock", "demoinfo", "identity", "frag", "roster",
	// derived
	"metadata", "match", "messages", "timeline", "items", "damage",
	"shots", "map-entities", "backpacks", "weapon-pickups",
	// post-processors
	"frags-final", "aim", "airgibs",
	"match-final", "loc-graph", "region-control",
}

func nodeNames(specs []nodeSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// TestDefaultDAGValidates guarantees NewDefaultRegistry's buildGraph never
// panics in production: the declared graph is well-formed (every Requires
// has exactly one provider, no cycles).
func TestDefaultDAGValidates(t *testing.T) {
	r := NewDefaultRegistry() // panics via buildGraph if the graph is invalid
	if err := validateDAG(r.specs); err != nil {
		t.Fatalf("default DAG failed validation: %v", err)
	}
	if _, err := buildDAG(r.specs); err != nil {
		t.Fatalf("default DAG failed to build: %v", err)
	}
}

// TestDAGNodeInventory pins the default registry's node SET (membership,
// not sequence). The Stage-1 predecessor of this test asserted the derived
// topological order equalled the registration order byte-for-byte — the
// zero-behaviour-change certificate the initial DAG conversion needed. That
// constraint is retired: TestOrderIndependence proves ANY valid order
// yields identical output, so the registration list is inventory only and
// a new node may be registered anywhere. What still matters is that the
// expected node set (and with it ARTIFACTS.md / the manifest) is updated
// deliberately when nodes are added or removed — which is what this
// membership check makes explicit.
func TestDAGNodeInventory(t *testing.T) {
	r := NewDefaultRegistry()

	want := map[string]bool{}
	for _, n := range registrationOrder {
		want[n] = true
	}
	got := map[string]bool{}
	for _, n := range nodeNames(r.specs) {
		if got[n] {
			t.Fatalf("duplicate node %q in registry", n)
		}
		got[n] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node set drifted:\n got:  %v\n want: %v", nodeNames(r.specs), registrationOrder)
	}
	if len(r.nodes) != len(r.specs) {
		t.Fatalf("topo order lost nodes: %d != %d", len(r.nodes), len(r.specs))
	}
}

// TestDAGMissingProviderNamesIt: a Requires with no provider is a startup
// error naming both the missing artifact and the node that wanted it.
func TestDAGMissingProviderNamesIt(t *testing.T) {
	specs := []nodeSpec{
		{Name: "a", regIndex: 0, Provides: []string{"a"}},
		{Name: "b", regIndex: 1, Provides: []string{"b"}, Requires: []string{"ghost"}},
	}
	_, err := buildDAG(specs)
	if err == nil {
		t.Fatal("expected error for missing provider, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error should name the missing artifact and node: %v", err)
	}
}

// TestDAGDuplicateProviderNamesIt: two providers of one artifact is a
// startup error naming the artifact and both nodes.
func TestDAGDuplicateProviderNamesIt(t *testing.T) {
	specs := []nodeSpec{
		{Name: "a", regIndex: 0, Provides: []string{"a", "shared"}},
		{Name: "b", regIndex: 1, Provides: []string{"b", "shared"}},
	}
	_, err := buildDAG(specs)
	if err == nil {
		t.Fatal("expected error for duplicate provider, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "shared") || !strings.Contains(msg, `"a"`) || !strings.Contains(msg, `"b"`) {
		t.Fatalf("error should name the artifact and both providers: %v", err)
	}
}

// TestDAGCycleDetected: a dependency cycle is reported (validation passes
// because every Requires has a provider; the topo sort catches it).
func TestDAGCycleDetected(t *testing.T) {
	specs := []nodeSpec{
		{Name: "a", regIndex: 0, Provides: []string{"a"}, Requires: []string{"b"}},
		{Name: "b", regIndex: 1, Provides: []string{"b"}, Requires: []string{"a"}},
	}
	_, err := buildDAG(specs)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error should mention a cycle: %v", err)
	}
}

// TestDAGDeterministic: the sort is stable across repeated runs (no
// map-iteration nondeterminism leaks into the order). Run many times so a
// randomised Go map seed would eventually surface a difference.
func TestDAGDeterministic(t *testing.T) {
	specs := NewDefaultRegistry().specs
	want, err := buildDAG(specs)
	if err != nil {
		t.Fatalf("buildDAG: %v", err)
	}
	wantNames := nodeNames(want)
	for i := 0; i < 50; i++ {
		got, err := buildDAG(specs)
		if err != nil {
			t.Fatalf("buildDAG run %d: %v", i, err)
		}
		if !reflect.DeepEqual(nodeNames(got), wantNames) {
			t.Fatalf("run %d order differs:\n got:  %v\n want: %v", i, nodeNames(got), wantNames)
		}
	}
}

// TestExportGraph smoke-tests the two export formats the -graph flag
// exposes: mermaid is non-empty, json parses and carries every node.
func TestExportGraph(t *testing.T) {
	mermaid, err := ExportGraph("mermaid")
	if err != nil {
		t.Fatalf("mermaid: %v", err)
	}
	if !strings.HasPrefix(mermaid, "flowchart TB") || len(mermaid) == 0 {
		t.Fatalf("mermaid output looks wrong:\n%s", mermaid)
	}

	jsonStr, err := ExportGraph("json")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	var g graphJSON
	if err := json.Unmarshal([]byte(jsonStr), &g); err != nil {
		t.Fatalf("json does not parse: %v", err)
	}
	// The graph carries the eager registration nodes plus the lazy artifact
	// (los), which is marked lazy but never enters the eager execution order.
	const lazyNodeCount = 1
	if len(g.Nodes) != len(registrationOrder)+lazyNodeCount {
		t.Fatalf("json node count = %d, want %d", len(g.Nodes), len(registrationOrder)+lazyNodeCount)
	}
	if len(g.Edges) == 0 {
		t.Fatal("json graph has no edges")
	}

	// The lazy node appears, marked lazy; the eager nodes are not lazy.
	lazySeen := map[string]bool{}
	depthByName := map[string]int{}
	for _, n := range g.Nodes {
		depthByName[n.Name] = n.Depth
		switch n.Name {
		case "los":
			if !n.Lazy {
				t.Errorf("node %q should be marked lazy", n.Name)
			}
			lazySeen[n.Name] = true
		default:
			if n.Lazy {
				t.Errorf("eager node %q should not be marked lazy", n.Name)
			}
		}
	}
	if !lazySeen["los"] {
		t.Fatalf("expected los lazy node in graph, saw %v", lazySeen)
	}

	// Depth is the display layering: roots (no in-graph requirements) sit at
	// 0, and a node with dependencies sits deeper than all of them.
	if depthByName["clock"] != 0 || depthByName["demoinfo"] != 0 {
		t.Errorf("root nodes should be depth 0: clock=%d demoinfo=%d", depthByName["clock"], depthByName["demoinfo"])
	}
	if depthByName["frag"] <= depthByName["demoinfo"] {
		t.Errorf("frag (requires demoinfo) should sit deeper than demoinfo: frag=%d demoinfo=%d", depthByName["frag"], depthByName["demoinfo"])
	}
	if depthByName["los"] == 0 {
		t.Errorf("los (requires demoinfo, timeline) should not be depth 0")
	}
	// The lazy node's Requires resolve to eager providers (edges into it).
	if !strings.Contains(mermaid, "los") {
		t.Errorf("mermaid missing lazy node:\n%s", mermaid)
	}

	// Node kinds are styled: post-processors get the `post` class, the lazy
	// node the `lazy` class; analyzers are unmarked.
	for _, want := range []string{"classDef post ", "classDef lazy ", "class los lazy;"} {
		if !strings.Contains(mermaid, want) {
			t.Errorf("mermaid missing %q:\n%s", want, mermaid)
		}
	}
	if !strings.Contains(mermaid, "frags_final") || !strings.Contains(mermaid, " post;") {
		t.Errorf("mermaid should mark post-processor nodes with the post class:\n%s", mermaid)
	}

	if _, err := ExportGraph("dot"); err == nil {
		t.Fatal("expected error for unsupported format 'dot'")
	}
}

package analyzer

// Phase-timings measurement (data, not machinery). TestPhaseTimingsReport
// runs the full default pipeline over every cached corpus demo, aggregates
// the per-node PhaseTimings the registry already records, and prints a
// markdown report: a per-node mean/max table, the event-pass (parse) cost
// vs the serial Finalize+post tail, and the DAG critical path (longest
// measured chain through the tail) vs that serial tail — i.e. the best-case
// parallel speedup a future scheduler could win on the tail. It shares the
// corpus loaders in order_test.go and adds no public API.

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// timingAgg accumulates a node's per-demo timing samples.
type timingAgg struct {
	sum, max float64
	n        int
}

// TestPhaseTimingsReport is DATA, not part of the correctness suite: it is
// opt-in via MVDA_TIMINGS=1 (skipped otherwise) so it never slows
// `make test`.
//
//	MVDA_TIMINGS=1 go test ./mvd-analytics/analyzer -run TestPhaseTimingsReport -v
func TestPhaseTimingsReport(t *testing.T) {
	if os.Getenv("MVDA_TIMINGS") == "" {
		t.Skip("set MVDA_TIMINGS=1 to run the phase-timings report")
	}
	corpus := loadOrderCorpus(t)
	if len(corpus) == 0 {
		t.Skip("testdata/corpus.json has no entries")
	}

	nodeAgg := map[string]*timingAgg{} // finalize:/post: node name -> stats
	special := map[string]*timingAgg{} // "eventPass", "init", "los"
	add := func(m map[string]*timingAgg, key string, ms float64) {
		a := m[key]
		if a == nil {
			a = &timingAgg{}
			m[key] = a
		}
		a.sum += ms
		if ms > a.max {
			a.max = ms
		}
		a.n++
	}

	demos := 0
	for _, e := range corpus {
		path := cachedDemoPath(corpus, e.Label)
		if path == "" {
			continue
		}
		demos++
		r := NewDefaultRegistry()
		res, err := r.Analyze(path)
		if err != nil {
			t.Fatalf("analyze %s: %v", e.Label, err)
		}
		for _, pt := range r.PhaseTimings {
			switch {
			case pt.Name == "init" || pt.Name == "eventPass":
				add(special, pt.Name, pt.Ms)
			case len(pt.Name) > 9 && pt.Name[:9] == "finalize:":
				add(nodeAgg, analyzerLabel(pt.Name[9:]), pt.Ms)
			case len(pt.Name) > 5 && pt.Name[:5] == "post:":
				add(nodeAgg, postLabel(pt.Name[5:]), pt.Ms)
			}
		}
		// los is lazy and off the default path; measure it separately so the
		// report can show its cost without ever enabling it by default.
		losStart := time.Now()
		ComputeLOS(res)
		add(special, "los", float64(time.Since(losStart).Microseconds())/1000)
	}
	if demos == 0 {
		t.Skip("no cached corpus demos")
	}

	mean := func(a *timingAgg) float64 {
		if a == nil || a.n == 0 {
			return 0
		}
		return a.sum / float64(a.n)
	}

	// Per-node table, sorted by mean descending.
	type row struct {
		name      string
		mean, max float64
	}
	var rows []row
	tailSerial := 0.0
	for name, a := range nodeAgg {
		rows = append(rows, row{name, mean(a), a.max})
		tailSerial += mean(a)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].mean > rows[j].mean })

	var b []byte
	b = append(b, fmt.Sprintf("\n## Phase timings (mean over %d cached corpus demos)\n\n", demos)...)
	b = append(b, "| node (tail) | mean ms | max ms |\n|---|---:|---:|\n"...)
	for _, r := range rows {
		b = append(b, fmt.Sprintf("| %s | %.2f | %.2f |\n", r.name, r.mean, r.max)...)
	}

	parse := mean(special["eventPass"])
	initMs := mean(special["init"])
	crit := criticalPathMs(NewDefaultRegistry().specs, nodeAgg, mean)

	b = append(b, "\n### Parse vs tail\n\n"...)
	b = append(b, fmt.Sprintf("- init: %.2f ms\n", initMs)...)
	b = append(b, fmt.Sprintf("- event pass (parse + OnEvent): %.2f ms\n", parse)...)
	b = append(b, fmt.Sprintf("- Finalize+post tail, serial sum: %.2f ms\n", tailSerial)...)
	b = append(b, fmt.Sprintf("- Finalize+post tail, DAG critical path: %.2f ms\n", crit)...)
	if crit > 0 {
		b = append(b, fmt.Sprintf("- best-case tail parallel speedup: %.2fx (serial/critical)\n", tailSerial/crit)...)
	}
	b = append(b, fmt.Sprintf("- los (lazy, off-path; NOT in default pipeline): %.2f ms mean\n", mean(special["los"]))...)

	t.Log(string(b))
}

// postLabel / analyzerLabel map a PhaseTimings label to its DAG node name
// so criticalPathMs (and the printed table) join measured times onto the
// declared graph. PhaseTimings records post-processors by function name
// (e.g. "locGraphPost") and analyzers by Analyzer.Name() (e.g.
// "timelineAnalysis"), neither of which always equals the DAG node id
// ("loc-graph", "timeline"). The meta maps are keyed by those same
// identities, so their .name field is the join.
func postLabel(fn string) string {
	if m, ok := postNodeMeta[fn]; ok {
		return m.name
	}
	return fn
}

func analyzerLabel(name string) string {
	if m, ok := analyzerNodeMeta[name]; ok {
		return m.name
	}
	return name
}

// criticalPathMs computes the longest measured chain through the declared
// DAG (longest[node] = mean[node] + max over its required providers), i.e.
// the wall-clock floor of the Finalize+post tail if every node ran on its
// own core the instant its inputs were ready. Nodes with no measurement
// (should be none for the eager set) contribute zero weight.
func criticalPathMs(specs []nodeSpec, nodeAgg map[string]*timingAgg, mean func(*timingAgg) float64) float64 {
	provider := map[string]int{}
	for i, s := range specs {
		for _, art := range s.Provides {
			provider[art] = i
		}
	}
	weight := func(name string) float64 { return mean(nodeAgg[name]) }
	memo := make([]float64, len(specs))
	done := make([]bool, len(specs))
	var longest func(i int) float64
	longest = func(i int) float64 {
		if done[i] {
			return memo[i]
		}
		done[i] = true // specs is a validated DAG, so no cycle to guard
		best := 0.0
		for _, req := range specs[i].Requires {
			p, ok := provider[req]
			if !ok || p == i {
				continue
			}
			if v := longest(p); v > best {
				best = v
			}
		}
		memo[i] = best + weight(specs[i].Name)
		return memo[i]
	}
	max := 0.0
	for i := range specs {
		if v := longest(i); v > max {
			max = v
		}
	}
	return max
}

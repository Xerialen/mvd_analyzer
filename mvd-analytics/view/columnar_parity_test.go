package view_test

// Real-demo parity + size/alloc measurements for the columnar bucket
// builder. This lives in an external test package because it imports the
// analyzer pipeline (which imports view) to produce a realistic
// result.Result — an internal test would create an import cycle. It
// therefore reconstructs the row shape from the columnar output using
// only exported fields, rather than the unexported columnarToRow oracle
// used by the internal synthetic parity test.
//
// Demos are read from the golden corpus cache (gitignored, populated on
// the first online golden run). When a demo is absent the relevant
// subtest/benchmark skips rather than fails, so offline runs stay green.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-analytics/view"
)

// corpus game IDs that exercise the interesting shapes: a 4on4 (teams,
// many players, deaths/respawns) and a 1on1 (smallest, fastest).
const (
	corpus4on4 = 212260 // 4on4_osams_ra_230426_dm3
	corpus1on1 = 212422 // 1on1_42_ahemlockslie_240426_skull
)

var demoCache sync.Map // gameID(int) -> *result.Result

func loadDemo(tb testing.TB, gameID int) *result.Result {
	tb.Helper()
	if v, ok := demoCache.Load(gameID); ok {
		return v.(*result.Result)
	}
	path := filepath.Join("..", "testdata", "cache", fmt.Sprintf("%d.mvd.gz", gameID))
	if _, err := os.Stat(path); err != nil {
		tb.Skipf("demo %d not cached at %s — run the golden test online once to populate", gameID, path)
	}
	res, err := analyzer.NewDefaultRegistry().Analyze(path)
	if err != nil {
		tb.Fatalf("analyze %d: %v", gameID, err)
	}
	demoCache.Store(gameID, res)
	return res
}

// colVal mirrors ColumnarPlayer.valAt using only exported fields.
func colVal(cp *view.ColumnarPlayer, field string, i int) any {
	col, ok := cp.Cols[field]
	if !ok {
		return nil
	}
	if vf, ok := cp.ValidFrom[field]; ok && i < vf {
		return nil
	}
	j := i - cp.First
	switch s := col.(type) {
	case []int16:
		return s[j]
	case []int32:
		return s[j]
	case result.Coords:
		return s[j]
	case []float64:
		return s[j]
	case []string:
		return s[j]
	case []bool:
		return s[j]
	}
	return nil
}

// reconstructPlayers rebuilds bucket i's row-major player map from the
// columnar output: alive players only, non-nil fields only, x/y/z folded
// back into a [3]result.Coord under FieldPosition.
func reconstructPlayers(cb *view.ColumnarBuckets, i int) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for name, cp := range cb.Players {
		if i < cp.First || i >= cp.First+cp.N {
			continue
		}
		if !cp.Alive[i-cp.First] {
			continue
		}
		pdata := make(map[string]any)
		if colVal(cp, "x", i) != nil {
			pdata[view.FieldPosition] = [3]result.Coord{
				result.Coord(colVal(cp, "x", i).(float32)),
				result.Coord(colVal(cp, "y", i).(float32)),
				result.Coord(colVal(cp, "z", i).(float32)),
			}
		}
		if colVal(cp, "vp", i) != nil {
			pdata[view.FieldView] = [2]int16{
				colVal(cp, "vp", i).(int16),
				colVal(cp, "vya", i).(int16),
			}
		}
		if colVal(cp, "vx", i) != nil {
			pdata[view.FieldVelocity] = [3]result.Coord{
				result.Coord(colVal(cp, "vx", i).(float32)),
				result.Coord(colVal(cp, "vy", i).(float32)),
				result.Coord(colVal(cp, "vz", i).(float32)),
			}
		}
		for field := range cp.Cols {
			switch field {
			case "x", "y", "z", "vp", "vya", "vx", "vy", "vz":
				continue
			}
			if v := colVal(cp, field, i); v != nil {
				pdata[field] = v
			}
		}
		if len(pdata) == 0 {
			continue
		}
		out[name] = pdata
	}
	return out
}

func rowInt(v any) int {
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

// compareTeams checks the columnar team arrays at bucket i against the
// row team maps, treating an absent counter as 0 in both directions.
func compareTeams(t *testing.T, ctx string, i int, rowTeams map[string]map[string]any, cb *view.ColumnarBuckets) {
	t.Helper()
	names := map[string]bool{}
	for name := range rowTeams {
		names[name] = true
	}
	for name := range cb.Teams {
		names[name] = true
	}
	for name := range names {
		rowTD := rowTeams[name]
		ct := cb.Teams[name]
		// Counter keys.
		keys := map[string]bool{}
		for k := range rowTD {
			if k != "abt" {
				keys[k] = true
			}
		}
		if ct != nil {
			for k := range ct.Cols {
				keys[k] = true
			}
		}
		for k := range keys {
			want := rowInt(rowTD[k])
			got := 0
			if ct != nil {
				if arr := ct.Cols[k]; arr != nil {
					got = arr[i]
				}
			}
			if want != got {
				t.Fatalf("%s bucket %d team %s counter %q = %d, want %d", ctx, i, name, k, got, want)
			}
		}
		// Armor-by-type.
		ats := map[string]bool{}
		var rowABT map[string]int
		if rowTD != nil {
			rowABT, _ = rowTD["abt"].(map[string]int)
		}
		for at := range rowABT {
			ats[at] = true
		}
		if ct != nil {
			for at := range ct.ABT {
				ats[at] = true
			}
		}
		for at := range ats {
			want := rowABT[at]
			got := 0
			if ct != nil {
				if arr := ct.ABT[at]; arr != nil {
					got = arr[i]
				}
			}
			if want != got {
				t.Fatalf("%s bucket %d team %s abt %q = %d, want %d", ctx, i, name, at, got, want)
			}
		}
	}
}

func TestColumnarParityCorpus(t *testing.T) {
	for _, gameID := range []int{corpus4on4, corpus1on1} {
		gameID := gameID
		t.Run(fmt.Sprint(gameID), func(t *testing.T) {
			r := loadDemo(t, gameID)
			for _, win := range []int{50, 100, 1000} {
				for _, team := range []bool{false, true} {
					opts := view.BucketsOptions{WindowMs: win, IncludeTeam: team, LocIndex: true}
					row, err := view.Buckets(r, opts)
					if err != nil {
						t.Fatalf("Buckets: %v", err)
					}
					cb, err := view.BucketsColumnar(r, opts)
					if err != nil {
						t.Fatalf("BucketsColumnar: %v", err)
					}
					if cb.WindowMs != row.WindowMs {
						t.Fatalf("windowMs = %d, want %d", cb.WindowMs, row.WindowMs)
					}
					if cb.Count != len(row.Buckets) {
						t.Fatalf("count = %d, want %d", cb.Count, len(row.Buckets))
					}
					if cb.Count > 0 {
						if want := int32(row.Buckets[0].T * 1000); cb.StartMs != want {
							t.Fatalf("startMs = %d, want %d", cb.StartMs, want)
						}
						lastPartial := row.Buckets[len(row.Buckets)-1].Partial
						if (cb.PartialLastMs != 0) != lastPartial {
							t.Fatalf("partialLastMs=%d but last bucket Partial=%v", cb.PartialLastMs, lastPartial)
						}
					}
					ctx := fmt.Sprintf("win=%d team=%v", win, team)
					for i := range row.Buckets {
						if got := reconstructPlayers(cb, i); !reflect.DeepEqual(got, row.Buckets[i].Players) {
							t.Fatalf("%s bucket %d player parity mismatch:\n columnar→row: %+v\n row:          %+v",
								ctx, i, got, row.Buckets[i].Players)
						}
						if team {
							compareTeams(t, ctx, i, row.Buckets[i].Team, cb)
						}
					}
				}
			}
		})
	}
}

// TestColumnarParityViewFields exercises the opt-in view/hgt/lq field
// codes (absent from AllStandardFields, so the default parity test never
// reaches them) — row and columnar must still agree bucket-for-bucket.
func TestColumnarParityViewFields(t *testing.T) {
	fields := []string{
		view.FieldPosition, view.FieldView, view.FieldHeight,
		view.FieldLiquid, view.FieldVelocity, view.FieldHealth,
	}
	for _, gameID := range []int{corpus4on4, corpus1on1} {
		gameID := gameID
		t.Run(fmt.Sprint(gameID), func(t *testing.T) {
			r := loadDemo(t, gameID)
			for _, win := range []int{50, 1000} {
				opts := view.BucketsOptions{WindowMs: win, Fields: fields, LocIndex: true}
				row, err := view.Buckets(r, opts)
				if err != nil {
					t.Fatalf("Buckets: %v", err)
				}
				cb, err := view.BucketsColumnar(r, opts)
				if err != nil {
					t.Fatalf("BucketsColumnar: %v", err)
				}
				if cb.Count != len(row.Buckets) {
					t.Fatalf("win=%d count=%d, want %d", win, cb.Count, len(row.Buckets))
				}
				for i := range row.Buckets {
					if got := reconstructPlayers(cb, i); !reflect.DeepEqual(got, row.Buckets[i].Players) {
						t.Fatalf("win=%d bucket %d parity mismatch:\n columnar→row: %+v\n row:          %+v",
							win, i, got, row.Buckets[i].Players)
					}
				}
			}
		})
	}
}

// TestColumnarSizeReport logs marshalled-payload sizes for the row
// BucketsView and the columnar layout at the web's 50 ms / all-field
// settings, plus a per-field-kind byte attribution. Informational; run
// with -v.
func TestColumnarSizeReport(t *testing.T) {
	r := loadDemo(t, corpus4on4)
	opts := view.BucketsOptions{WindowMs: 50, IncludeTeam: true, LocIndex: true, Reducers: view.LegacyReducerSet}
	row, err := view.Buckets(r, opts)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	cb, err := view.BucketsColumnar(r, opts)
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	rowJSON, _ := json.Marshal(row)
	colJSON, _ := json.Marshal(cb)
	t.Logf("buckets=%d players=%d", cb.Count, len(cb.Players))
	t.Logf("row BucketsView JSON: %10d bytes", len(rowJSON))
	t.Logf("columnar        JSON: %10d bytes  (%.1fx smaller than row)",
		len(colJSON), float64(len(rowJSON))/float64(len(colJSON)))

	// Byte attribution across the columnar player columns (wire form:
	// booleans + alive are emitted as 0/1, so marshal them that way for
	// an accurate picture of where the payload goes).
	var boolBytes, numBytes, strBytes, aliveBytes int
	wireLen := func(bs []bool) int {
		v := make([]int8, len(bs))
		for i, b := range bs {
			if b {
				v[i] = 1
			}
		}
		b, _ := json.Marshal(v)
		return len(b)
	}
	for _, cp := range cb.Players {
		aliveBytes += wireLen(cp.Alive)
		for _, col := range cp.Cols {
			switch c := col.(type) {
			case []bool:
				boolBytes += wireLen(c)
			case []string:
				b, _ := json.Marshal(c)
				strBytes += len(b)
			default:
				b, _ := json.Marshal(c)
				numBytes += len(b)
			}
		}
	}
	t.Logf("  player bool columns:  %10d bytes", boolBytes)
	t.Logf("  player numeric cols:  %10d bytes", numBytes)
	t.Logf("  player string cols:   %10d bytes", strBytes)
	t.Logf("  alive masks:          %10d bytes", aliveBytes)
}

// TestDumpColumnarJSON writes matched columnar + row JSON for one 4on4 to
// /tmp so the JS accessor logic in app.js can be validated against real
// Go-generated data offline. Env-gated (mirrors the golden -update-golden
// pattern); a no-op in normal runs.
func TestDumpColumnarJSON(t *testing.T) {
	if os.Getenv("DUMP_COLUMNAR") == "" {
		t.Skip("set DUMP_COLUMNAR=1 to dump /tmp/col.json + /tmp/row.json")
	}
	r := loadDemo(t, corpus4on4)
	opts := view.BucketsOptions{WindowMs: 50, IncludeTeam: true, LocIndex: true, Reducers: view.LegacyReducerSet}
	row, err := view.Buckets(r, opts)
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	cb, err := view.BucketsColumnar(r, opts)
	if err != nil {
		t.Fatalf("BucketsColumnar: %v", err)
	}
	rb, _ := json.Marshal(row)
	cbb, _ := json.Marshal(cb)
	if err := os.WriteFile("/tmp/row.json", rb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/col.json", cbb, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("dumped /tmp/row.json (%d) + /tmp/col.json (%d), count=%d", len(rb), len(cbb), cb.Count)
}

func BenchmarkBucketsRow(b *testing.B) {
	r := loadDemo(b, corpus4on4)
	opts := view.BucketsOptions{WindowMs: 50, IncludeTeam: true, LocIndex: true, Reducers: view.LegacyReducerSet}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := view.Buckets(r, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBucketsColumnar(b *testing.B) {
	r := loadDemo(b, corpus4on4)
	opts := view.BucketsOptions{WindowMs: 50, IncludeTeam: true, LocIndex: true, Reducers: view.LegacyReducerSet}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := view.BucketsColumnar(r, opts); err != nil {
			b.Fatal(err)
		}
	}
}

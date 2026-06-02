package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestCombineTeamkillSignals(t *testing.T) {
	tests := []struct {
		name  string
		pos   map[string]bool
		delta map[string]bool
		want  string
	}{
		{"both agree on one", set("a"), set("a"), "a"},
		{"position alone, delta silent", set("a"), set(), "a"},
		{"delta alone, position silent", set(), set("a"), "a"},
		{"conflict — disagree", set("a"), set("b"), ""},
		{"position ambiguous, delta breaks tie", set("a", "b"), set("b"), "b"},
		{"delta ambiguous, position breaks tie", set("a"), set("a", "b"), "a"},
		{"ambiguous in both, overlap >1", set("a", "b"), set("a", "b"), ""},
		{"position ambiguous, delta silent", set("a", "b"), set(), ""},
		{"both empty", set(), set(), ""},
		{"position double, delta picks neither", set("a", "b"), set("c"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := combineTeamkillSignals(tc.pos, tc.delta); got != tc.want {
				t.Fatalf("combineTeamkillSignals(%v, %v) = %q, want %q", tc.pos, tc.delta, got, tc.want)
			}
		})
	}
}

func TestPositionAt(t *testing.T) {
	pt := &result.PositionTrack{
		T: []int32{1000, 1050, 1100, 5000},
		X: []int32{10, 20, 30, 99},
		Y: []int32{11, 21, 31, 99},
		Z: []int32{12, 22, 32, 99},
	}
	tests := []struct {
		name       string
		q          int32
		wantOK     bool
		wantX      int32
	}{
		{"exact sample", 1050, true, 20},
		{"nearest below within window", 1060, true, 20},
		{"nearest above within window", 1090, true, 30},
		{"too far from any sample", 3000, false, 0},
		{"before first but within window", 900, true, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, _, _, ok := positionAt(pt, tc.q)
			if ok != tc.wantOK {
				t.Fatalf("positionAt(%d) ok=%v, want %v", tc.q, ok, tc.wantOK)
			}
			if ok && x != tc.wantX {
				t.Fatalf("positionAt(%d) x=%d, want %d", tc.q, x, tc.wantX)
			}
		})
	}
}

func TestPositionAtEmpty(t *testing.T) {
	if _, _, _, ok := positionAt(&result.PositionTrack{}, 100); ok {
		t.Fatal("positionAt on empty track should return ok=false")
	}
}

package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/loc"
)

func TestMedoidLocations(t *testing.T) {
	// "RL" appears at three collinear points; the medoid is the middle
	// one (min summed distance). "GA" appears once. Names emit in
	// first-seen order.
	in := []loc.Location{
		{X: 0, Y: 0, Z: 0, Name: "RL"},
		{X: 10, Y: 0, Z: 0, Name: "RL"},
		{X: 100, Y: 0, Z: 0, Name: "RL"},
		{X: 50, Y: 50, Z: 50, Name: "GA"},
	}

	got := medoidLocations(in)

	if len(got) != 2 {
		t.Fatalf("got %d label points, want 2 (one per name): %+v", len(got), got)
	}
	if got[0].Name != "RL" || got[1].Name != "GA" {
		t.Fatalf("name order = [%s %s], want [RL GA]", got[0].Name, got[1].Name)
	}
	if got[0].X != 10 || got[0].Y != 0 || got[0].Z != 0 {
		t.Errorf("RL medoid = (%v,%v,%v), want (10,0,0)", got[0].X, got[0].Y, got[0].Z)
	}
	if got[1].X != 50 || got[1].Y != 50 || got[1].Z != 50 {
		t.Errorf("GA medoid = (%v,%v,%v), want (50,50,50)", got[1].X, got[1].Y, got[1].Z)
	}
}

func TestMedoidLocations_Empty(t *testing.T) {
	if got := medoidLocations(nil); got != nil {
		t.Errorf("medoidLocations(nil) = %+v, want nil", got)
	}
}

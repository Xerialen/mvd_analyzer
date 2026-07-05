package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

func TestWeaponStayDetectorFullserverinfo(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"dmm1", `fullserverinfo "\maxfps\77\deathmatch\1\map\dm3"`, false},
		{"dmm2", `fullserverinfo "\deathmatch\2"`, true},
		{"dmm3", `fullserverinfo "\deathmatch\3\mode\2on2"`, true},
		{"dmm4", `fullserverinfo "\deathmatch\4"`, false},
		{"dmm5", `fullserverinfo "\deathmatch\5"`, true},
		{"coop", `fullserverinfo "\deathmatch\0\coop\1"`, true},
		{"coop off dmm1", `fullserverinfo "\deathmatch\1\coop\0"`, false},
		{"no deathmatch key", `fullserverinfo "\maxfps\77\map\dm3"`, false},
		{"non-numeric", `fullserverinfo "\deathmatch\x"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d weaponStayDetector
			d.OnStuffText(&events.StuffTextEvent{Command: tc.cmd})
			if got := d.WeaponStay(); got != tc.want {
				t.Errorf("WeaponStay() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWeaponStayDetectorServerInfoEvent(t *testing.T) {
	var d weaponStayDetector
	if d.WeaponStay() {
		t.Fatal("zero-value detector should report false")
	}
	d.OnServerInfo(&events.ServerInfoEvent{Key: "deathmatch", Value: "3"})
	if !d.WeaponStay() {
		t.Fatal("deathmatch=3 via ServerInfoEvent should report true")
	}
}

func TestWeaponStayDetectorFirstValueWins(t *testing.T) {
	var d weaponStayDetector
	d.OnStuffText(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`})
	d.OnServerInfo(&events.ServerInfoEvent{Key: "deathmatch", Value: "1"})
	if !d.WeaponStay() {
		t.Fatal("later deathmatch update must not override the latched value")
	}

	var d2 weaponStayDetector
	d2.OnServerInfo(&events.ServerInfoEvent{Key: "deathmatch", Value: "1"})
	d2.OnStuffText(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`})
	if d2.WeaponStay() {
		t.Fatal("later deathmatch update must not override the latched value")
	}
}

func TestPosTrackerMinDistSqIn(t *testing.T) {
	var p posTracker
	target := [3]float32{100, 0, 0}

	if _, ok := p.MinDistSqIn(0, 0, 1, target); ok {
		t.Fatal("empty tracker should report no sample")
	}

	p.Record(0, [3]float32{0, 0, 0}, 1.0)   // dist² = 10000
	p.Record(0, [3]float32{90, 0, 0}, 1.2)  // dist² = 100
	p.Record(0, [3]float32{500, 0, 0}, 1.4) // dist² = 160000

	d, ok := p.MinDistSqIn(0, 0.9, 1.5, target)
	if !ok || d != 100 {
		t.Fatalf("MinDistSqIn = (%v, %v), want (100, true)", d, ok)
	}

	// Window excludes the near sample → the min comes from what's left.
	d, ok = p.MinDistSqIn(0, 1.3, 1.5, target)
	if !ok || d != 160000 {
		t.Fatalf("MinDistSqIn = (%v, %v), want (160000, true)", d, ok)
	}

	// Window with no samples at all.
	if _, ok := p.MinDistSqIn(0, 2.0, 3.0, target); ok {
		t.Fatal("window past all samples should report no sample")
	}

	// Other slot untouched.
	if _, ok := p.MinDistSqIn(1, 0, 2, target); ok {
		t.Fatal("unknown slot should report no sample")
	}
}

func TestPosTrackerPrunesOldSamples(t *testing.T) {
	var p posTracker
	p.Record(0, [3]float32{1, 0, 0}, 1.0)
	p.Record(0, [3]float32{2, 0, 0}, 5.0) // prunes everything older than 4.0
	if _, ok := p.MinDistSqIn(0, 0.5, 1.5, [3]float32{0, 0, 0}); ok {
		t.Fatal("sample older than the 1s horizon should have been pruned")
	}
	if _, ok := p.MinDistSqIn(0, 4.5, 5.5, [3]float32{0, 0, 0}); !ok {
		t.Fatal("recent sample should survive pruning")
	}
}

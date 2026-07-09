package analyzer

import (
	"github.com/mvd-analyzer/mvd-analytics/aimcore"
)

// Aim analytics (schema v41). aimPost derives per-player aim metrics from the
// already-assembled Shots + Streams + Damage + LG beams. The computation core
// lives in package aimcore (imported by both this analyzer and the view layer
// for filtered/windowed variants — see aimcore for the full derivation notes).
// aimPost is a thin caller that produces the stored, unfiltered result: the
// zero Query yields the whole-match, all-players aim, byte-identical to the
// pre-extraction behaviour (the golden corpus is the safety check).
func aimPost(res *Result, co *CoreOutputs) {
	if am := aimcore.Compute(res, aimcore.Query{}); am != nil {
		res.Aim = am
	}
}

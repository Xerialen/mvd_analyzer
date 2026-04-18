package analyzer

import "testing"

// Helper: build a []*playerBucketRawData from a string of loc letters,
// one bucket per rune. '.' is a missing-loc bucket (filter must treat
// as a run break). Upper-case letters are stable locs, lower-case are
// blips — actual classification is the filter's job, this is just a
// terse way to write the input.
func mkRun(locs string) []*playerBucketRawData {
	out := make([]*playerBucketRawData, 0, len(locs))
	for _, r := range locs {
		if r == '.' {
			out = append(out, &playerBucketRawData{location: ""})
			continue
		}
		out = append(out, &playerBucketRawData{location: string(r)})
	}
	return out
}

func locsOf(run []*playerBucketRawData) string {
	b := make([]byte, 0, len(run))
	for _, pd := range run {
		if pd.location == "" {
			b = append(b, '.')
		} else {
			b = append(b, pd.location[0])
		}
	}
	return string(b)
}

// 50 ms per bucket, threshold 500 ms -> stable requires ≥ 10 buckets.
const (
	tbfBucketDur = 0.05
	tbfThreshold = 0.5
)

// A blip between two same-loc stables collapses entirely into that loc
// (A ... bleed ... A => uninterrupted A).
func TestFilter_BlipBetweenSameLocCollapses(t *testing.T) {
	// 10 A, 3 B, 10 A. B is a 150 ms blip surrounded by A's.
	input := "AAAAAAAAAA" + "BBB" + "AAAAAAAAAA"
	want := "AAAAAAAAAA" + "AAA" + "AAAAAAAAAA"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	if got := locsOf(run); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A blip between two different stables splits its buckets half-to-A,
// half-to-D, with the odd-bucket tie going to A.
func TestFilter_BlipBetweenDifferentLocsSplitsHalfHalf(t *testing.T) {
	// 10 A, 5 X (blip), 10 C. 5 blip buckets -> 3 go to A, 2 to C.
	input := "AAAAAAAAAA" + "XXXXX" + "CCCCCCCCCC"
	want := "AAAAAAAAAA" + "AAACC" + "CCCCCCCCCC"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	if got := locsOf(run); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Multiple short blips between two stables collapse together and are
// split half-to-A, half-to-D on the combined total.
func TestFilter_MultipleBlipsCollapse(t *testing.T) {
	// 10 A, 3 X, 2 Y, 3 Z, 10 D. 8 blip buckets total -> 4 to A, 4 to D.
	input := "AAAAAAAAAA" + "XXXYYZZZ" + "DDDDDDDDDD"
	want := "AAAAAAAAAA" + "AAAADDDD" + "DDDDDDDDDD"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	if got := locsOf(run); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Leading blips (before the first stable) adopt the first stable's
// loc; trailing blips adopt the last stable's loc.
func TestFilter_LeadingAndTrailingBlips(t *testing.T) {
	// 3 X, 10 A, 2 Y. X's absorb into A (leading), Y's absorb into A (trailing).
	input := "XXX" + "AAAAAAAAAA" + "YY"
	want := "AAA" + "AAAAAAAAAA" + "AA"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	if got := locsOf(run); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A run with no stable segment at all is left untouched — without an
// anchor the filter can't decide which neighbor to absorb into.
func TestFilter_AllBlipsLeftAlone(t *testing.T) {
	input := "AABBCCDD"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	if got := locsOf(run); got != input {
		t.Errorf("got %q, want %q (all-blip run should not change)", got, input)
	}
}

// The exact schloss scenario for ocoini, at bucket-count granularity:
// (stable cathedral) - 6 cathedral.SSG - 4 Quad.high - 3 RA - (stable cemetary).
// End-to-end through the filter, every blip between the two stables
// collapses and the ocoini edge becomes a single cathedral -> cemetary.
func TestFilter_OcoiniSchlossScenarioCollapses(t *testing.T) {
	// Use letters for brevity: S = cathedral (stable), X = cathedral.SSG (6),
	// Q = Quad.high (4), R = RA (3), C = cemetary (stable).
	input := "SSSSSSSSSSSS" + "XXXXXX" + "QQQQ" + "RRR" + "CCCCCCCCCCCC"
	run := mkRun(input)
	filterBlipsInRun(run, tbfThreshold, tbfBucketDur)
	got := locsOf(run)

	// Expect no X, Q, or R buckets to survive.
	for _, c := range got {
		if c == 'X' || c == 'Q' || c == 'R' {
			t.Errorf("blip loc survived filter: %q", got)
			break
		}
	}
	// And exactly one transition S -> C in the output.
	transitions := 0
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1] {
			transitions++
		}
	}
	if transitions != 1 {
		t.Errorf("expected exactly 1 S->C transition, got %d in %q", transitions, got)
	}
}

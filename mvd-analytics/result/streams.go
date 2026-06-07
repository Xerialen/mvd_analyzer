package result

// Streams is the canonical native-rate storage for per-player and
// global state changes. Read by the qwanalytics/view query API.
//
// Each PlayerStream records every change to a tracked field at the
// rate it actually changed (see appendChange semantics). Position is
// the only field that records every native-rate sample without dedup;
// every other field is sparse — entries represent transitions, not
// per-tick samples.
type Streams struct {
	Players []PlayerStream `json:"players"`
	Global  GlobalStream   `json:"global"`
}

// PlayerStream is one player's full event-rate state record. Name is
// the canonical demoinfo-resolved player name; if two slots collide
// on a single name within one match, the second is suffixed
// "name#slotIndex". Mid-match name changes are folded into the same
// stream by the analyser's existing canonicalisation.
type PlayerStream struct {
	Name string `json:"name"`
	Team string `json:"team,omitempty"`

	// Position track at native rate. Always populated in-memory; whether
	// it is serialised to JSON is controlled at marshal time (the CLI's
	// -include positions flag and equivalent transports). Nil when the
	// player produced no position events.
	Position *PositionTrack `json:"pos,omitempty"`

	// Discrete state-change streams. Sparse — every entry is a transition.
	// Health/armor use int16: Quake values can reach 250 (mega-health,
	// red armor) which exceeds int8 range.
	Health    []ChangeI16 `json:"h,omitempty"`
	Armor     []ChangeI16 `json:"a,omitempty"`
	ArmorType []ChangeStr `json:"at,omitempty"` // "ga"|"ya"|"ra"|""
	Loc       []ChangeI16 `json:"li,omitempty"` // index into TimelineAnalysisResult.LocTable

	// Inventory presence as half-open intervals [Start, End). One entry
	// per period the field was true. Open intervals at match end are
	// closed at MatchEnd by the analyser.
	RL  []Interval `json:"rl,omitempty"`
	LG  []Interval `json:"lg,omitempty"`
	GL  []Interval `json:"gl,omitempty"`
	SSG []Interval `json:"ssg,omitempty"`
	SNG []Interval `json:"sng,omitempty"`

	Quad []Interval `json:"q,omitempty"`
	Pent []Interval `json:"pe,omitempty"`
	Ring []Interval `json:"r,omitempty"`

	// Ammo as change streams (dedup against last value).
	Shells  []ChangeI16 `json:"sh,omitempty"`
	Nails   []ChangeI16 `json:"nl,omitempty"`
	Rockets []ChangeI16 `json:"rk,omitempty"`
	Cells   []ChangeI16 `json:"cl,omitempty"`

	// Discrete event timestamps (no value). Integer milliseconds since
	// the stream's time origin (the same epoch as match-relative seconds
	// elsewhere; schema v8 changed the type and unit to give exact
	// comparisons against PositionTrack.T — see PositionTrack comment).
	Spawns []int32 `json:"sp,omitempty"`
	Deaths []int32 `json:"d,omitempty"`
}

// GlobalStream carries the match window plus the demo/wall-clock anchor —
// everything needed to interpret a stream time without cross-referencing
// other Result sections. Match-relative times are integer milliseconds
// since the stream's time origin (schema v8 — see PositionTrack for the
// unit rationale); MatchStart is always 0 (it *is* the origin) and is kept
// as an explicit anchor.
//
// Wall-clock mapping (schema v23 moved the anchor here from
// TimelineAnalysisResult). For any match-relative game time g (ms):
//
//	wallClockMs = DemoStartUnixMs + DemoOffset + g + P(g)   (±DemoStartAccuracyMs)
//	P(g)        = Σ Pauses[i].DurationMs for Pauses[i].AtMs <= g
//
// The game clock freezes during a pause while wall-clock time runs on, so
// the P(g) term is what keeps the mapping correct on paused demos; it is 0
// (and Pauses may be empty) otherwise.
type GlobalStream struct {
	MatchStart int32 `json:"matchStart"` // always 0 — the match-relative time origin
	MatchEnd   int32 `json:"matchEnd"`   // match end (≈ duration) in match-relative ms
	// DemoOffset is ms from demo open (demo t=0, ≈ countdown start) to match
	// start; it bridges match-relative time and demo time.
	DemoOffset int32 `json:"demoOffset,omitempty"`
	// DemoStartUnixMs is the server's clock (Unix epoch ms) at demo open.
	DemoStartUnixMs int64 `json:"demoStartUnixMs,omitempty"`
	// DemoStartAccuracyMs is its resolution: 1 from the mvdhidden 0x000B
	// millisecond block, 1000 from the whole-second serverinfo `epoch` cvar.
	// Absent (0) when no wall-clock source is present.
	DemoStartAccuracyMs int32 `json:"demoStartAccuracyMs,omitempty"`
	// Pauses lists each game pause as a flat segment in the game→wall-clock
	// mapping, in match-relative AtMs order. Derived from the mvdhidden
	// 0x000A (paused_duration) blocks; absent on demos with no pauses or
	// recorded by a server that does not embed the block.
	Pauses []TimelinePause `json:"pauses,omitempty"`
}

// PositionTrack is columnar to compress JSON. Indices align across the
// five arrays. Coordinates are int32 — Quake maps can exceed ±32 768
// in any axis, so int16 would silently truncate.
//
// T is integer milliseconds since the stream's time origin (the same
// epoch as the float-seconds version it replaced in schema v8). The
// JSON key stayed "t" for compactness; consumers that previously read
// it as seconds must scale by 1/1000 — the schema-version bump is the
// signal. The wire format gives us a 1-byte ms delta per message, so
// integer-ms storage keeps that exact value all the way from the
// decoder through the persistence layer; float seconds reintroduced a
// 1e-6 drift that caused spawn/death-boundary comparisons in locgraph
// and the blip filter to land on the wrong side of an edge.
//
// Range: int32 ms gives ±24.8 days. Demos run minutes to hours, so
// overflow isn't a concern. Negative values are valid after the
// post-processor subtracts matchStart (warmup samples shift below 0).
//
// Li is the resolved loc-name index per native-rate sample (indexes
// into TimelineAnalysisResult.LocTable, with 0 = "no loc"). Populated
// during analyzer Finalize (after the loc finder is loaded), then
// smoothed by the blip filter. Downstream consumers — the loc graph
// builder, region control, and the FieldLoc bucket reducer in
// view.Buckets — read this column directly instead of deriving locs
// from x/y/z separately.
type PositionTrack struct {
	T  []int32 `json:"t"` // milliseconds since the stream's time origin
	X  []int32 `json:"x"`
	Y  []int32 `json:"y"`
	Z  []int32 `json:"z"`
	Li []int16 `json:"li,omitempty"`
}

// ChangeI16 is a single transition in an int16 stream. T is integer
// milliseconds since the stream's time origin (schema v8).
type ChangeI16 struct {
	T int32 `json:"t"`
	V int16 `json:"v"`
}

// ChangeStr is a single transition in a string-valued stream. T is
// integer milliseconds since the stream's time origin (schema v8).
type ChangeStr struct {
	T int32  `json:"t"`
	V string `json:"v"`
}

// Interval is a half-open period [Start, End) during which a boolean
// field was true. Bounds are integer milliseconds since the stream's
// time origin (schema v8).
type Interval struct {
	Start int32 `json:"s"`
	End   int32 `json:"e"`
}

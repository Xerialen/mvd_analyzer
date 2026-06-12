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
//
// H is the player's height above the floor beneath them at each sample —
// how far the feet are above the highest solid surface at or below the
// player, from straight-down traces through the map's player clip hulls
// (mapclip, schema v24). Since v26 it is measured over the player's
// bounding-box footprint, not just the origin column: the highest floor
// found under a 3x3 grid of columns sampled ±8 around the origin wins
// (an effective ~48-wide footprint on the already-±16-box-inflated
// hull), so a player skimming a ledge / well rim — origin momentarily
// over the pit while the box overhangs the rim — reads the near floor
// rather than the distant one far below. Since v27 the trace scene also
// includes every moving brush-model entity (lift, door, train) posed at
// its demo-streamed origin for the sample's time, so a player riding
// the dm2 RA lift stands on the lift, not the shaft floor beneath it.
// It reads ~0 when grounded and grows positive during a jump or
// airborne hit (airgib), so a consumer can flag those without any
// coordinate arithmetic. (The absolute floor surface, if needed, is
// Z[i] - 24 - H[i] — the player origin rides 24 units above the floor
// its feet rest on.)
// Since v28 liquids participate too: a player in liquid (Lq level >= 1)
// reads H = 0 by definition, and a player airborne above water / slime
// / lava measures down to the liquid surface when it is the highest
// support beneath them (bspvis.LiquidSurfaceBelow).
// Sentinel NoFloor marks samples with no floor to measure from — over a
// void/pit, an embedded origin, or the zero origin. Populated only when
// a clip hull is loaded for the map (a provisioned BSP); otherwise the
// column is nil/absent (omitempty). Same length as T when present.
// Grounded samples are ~0 with a unit or two of slack from slopes and
// the trace epsilon, so test |H| small rather than == 0.
// Lq is the player's liquid state per sample (schema v28), computed by
// mirroring the engine's PM_CategorizePosition waterlevel probes
// against the map's render BSP (bspvis.WaterLevel): 0 = dry, else
// (type << 2) | level with level 1–3 (feet / waist / eyes submerged)
// and type LqWater/LqSlime/LqLava — so water reads 5/6/7, slime
// 9/10/11, lava 13/14/15. Decode with LqLevel / LqType. Samples with
// Lq level >= 1 have H = 0 by definition (the liquid surface is the
// support); when a player is airborne ABOVE liquid, H measures down to
// the liquid surface if it is the highest support under them.
// Populated only when the map's BSP is provisioned (same source as H);
// same length as T when present.
type PositionTrack struct {
	T  []int32 `json:"t"` // milliseconds since the stream's time origin
	X  []int32 `json:"x"`
	Y  []int32 `json:"y"`
	Z  []int32 `json:"z"`
	Li []int16 `json:"li,omitempty"`
	H  []int32 `json:"h,omitempty"`  // height above the floor beneath the player; NoFloor = none
	Lq []int8  `json:"lq,omitempty"` // liquid state: 0 dry, else (type<<2)|level
}

// Lq liquid-type codes (the high bits of a PositionTrack.Lq value).
const (
	LqWater int8 = 1
	LqSlime int8 = 2
	LqLava  int8 = 3
)

// LqLevel extracts the submersion level (0 none, 1 feet, 2 waist,
// 3 eyes) from a PositionTrack.Lq value.
func LqLevel(v int8) int { return int(v & 3) }

// LqType extracts the liquid type (LqWater/LqSlime/LqLava, 0 when dry)
// from a PositionTrack.Lq value.
func LqType(v int8) int { return int(v >> 2) }

// NoFloor is the sentinel in PositionTrack.H for a sample with no floor
// beneath it (over a void/pit, or an embedded/zero origin) — the height
// is undefined there. Chosen as math.MinInt32 so it can never be
// mistaken for a real height.
const NoFloor int32 = -2147483648

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

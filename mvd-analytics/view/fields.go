package view

// Field codes from PLAN-event-streams-and-views-v3.md §4.3. These are
// the identical strings used in:
//
//   - JSON wire keys on result.PlayerStream
//   - BucketsOptions.Fields / StreamSliceOptions.Fields / StateAtOptions.Fields
//   - CLI -fields values (qw-analyze)
//   - WASM bridge JSON args
//   - future MCP tool inputs
//
// Adding a new field means: define a constant here, add it to
// AllStandardFields, register the default reducer in DefaultReducers,
// and document in qwanalytics/RESULT_SCHEMA.md.
const (
	FieldHealth       = "h"
	FieldArmor        = "a"
	FieldArmorType    = "at"
	FieldLoc          = "li"
	FieldActiveWeapon = "w"
	FieldPosition     = "pos"
	// View / Height / Liquid project the per-sample view-direction,
	// floor-height, and liquid-state columns of the position track
	// independently of x/y/z. Opt-in (not in AllStandardFields): a
	// consumer that wants where a player looks, how high they are, or
	// whether they're submerged asks for it explicitly, and `pos` stays
	// strictly x/y/z. Height (`hgt`, not `h` — that's Health) and Liquid
	// are present only when the map's BSP was provisioned.
	FieldView     = "view"
	FieldHeight   = "hgt"
	FieldLiquid   = "lq"
	FieldVelocity = "vel" // velocity vx/vy/vz (units/sec), projected from the position track

	FieldRL  = "rl"
	FieldLG  = "lg"
	FieldGL  = "gl"
	FieldSSG = "ssg"
	FieldSNG = "sng"

	FieldQuad = "q"
	FieldPent = "pe"
	FieldRing = "r"

	FieldShells  = "sh"
	FieldNails   = "nl"
	FieldRockets = "rk"
	FieldCells   = "cl"

	FieldSpawns = "sp"
	FieldDeaths = "d"
)

// FieldKind classifies a field's stream form so reducers + slicers can
// dispatch generically.
type FieldKind int

const (
	KindChangeI16 FieldKind = iota
	KindChangeStr
	KindInterval  // bool intervals
	KindPosition  // *PositionTrack (x/y/z)
	KindEventList // []float64 timestamps (spawn/death)
	KindView      // view direction (vp/vya) projected from *PositionTrack
	KindHeight    // height-above-floor (h) projected from *PositionTrack
	KindLiquid    // liquid state (lq) projected from *PositionTrack
	KindVelocity  // velocity (vx/vy/vz) projected from *PositionTrack
)

// FieldKindFor returns the kind for a known field code; ok=false on an
// unknown code.
func FieldKindFor(code string) (FieldKind, bool) {
	k, ok := fieldKinds[code]
	return k, ok
}

var fieldKinds = map[string]FieldKind{
	FieldHealth:       KindChangeI16,
	FieldArmor:        KindChangeI16,
	FieldArmorType:    KindChangeStr,
	FieldLoc:          KindChangeI16,
	FieldActiveWeapon: KindChangeI16,
	FieldPosition:     KindPosition,
	FieldView:         KindView,
	FieldHeight:       KindHeight,
	FieldLiquid:       KindLiquid,
	FieldVelocity:     KindVelocity,

	FieldRL:  KindInterval,
	FieldLG:  KindInterval,
	FieldGL:  KindInterval,
	FieldSSG: KindInterval,
	FieldSNG: KindInterval,

	FieldQuad: KindInterval,
	FieldPent: KindInterval,
	FieldRing: KindInterval,

	FieldShells:  KindChangeI16,
	FieldNails:   KindChangeI16,
	FieldRockets: KindChangeI16,
	FieldCells:   KindChangeI16,

	FieldSpawns: KindEventList,
	FieldDeaths: KindEventList,
}

// AllStandardFields is the canonical iteration order — used as the
// default Fields filter and by the legacy bucket shim. Order chosen so
// downstream JSON has a stable key sequence (helpful for byte-level
// diffs across runs). FieldView / FieldHeight / FieldLiquid /
// FieldVelocity are deliberately absent: they are opt-in, so a default
// query keeps the pre-v31 shape and a consumer only pays for view /
// height / liquid / velocity when it asks for them by code.
var AllStandardFields = []string{
	FieldHealth, FieldArmor, FieldArmorType, FieldLoc, FieldActiveWeapon, FieldPosition,
	FieldRL, FieldLG, FieldGL, FieldSSG, FieldSNG,
	FieldQuad, FieldPent, FieldRing,
	FieldShells, FieldNails, FieldRockets, FieldCells,
	FieldSpawns, FieldDeaths,
}

// DefaultReducers maps each field to its default reducer name. Bucket
// N's data represents player state at time t = N*bucketDur — i.e.,
// "first sample of bucket" / "value at start of window" semantics.
// Bucket 0 == match-start state, consistent with the timeline
// playback model where each bucket is a snapshot at its own T.
//
// Per-field rationale:
//
//   - Change streams (h, a, at, li, sh, nl, rk, cl) → "first":
//     carry-forward to bStart returns the value at start of window.
//   - Position → "first": first native sample with T >= bStart, or
//     carry-forward in gap buckets. See positionSamples in buckets.go.
//   - Intervals (rl, lg, ...) → "first": intervalContains(bStart),
//     i.e. "is the player carrying it at the start of the bucket".
//   - Spawns / deaths → "any": discrete events; bool true if the
//     event happened anywhere in [bStart, bEnd). "first" would return
//     a timestamp instead of a bool.
//
// Consumers can override per-field via BucketsOptions.Reducers (e.g.
// `{"h": "min"}` for stress-moment graphs, `{"li": "dominant"}` for
// "what loc did the player spend the most time in this window").
var DefaultReducers = map[string]string{
	FieldHealth:       "first",
	FieldArmor:        "first",
	FieldArmorType:    "first",
	FieldLoc:          "first",
	FieldActiveWeapon: "first",
	FieldPosition:     "first",
	FieldView:         "first",
	FieldHeight:       "first",
	FieldLiquid:       "first",
	FieldVelocity:     "first",

	FieldRL:  "first",
	FieldLG:  "first",
	FieldGL:  "first",
	FieldSSG: "first",
	FieldSNG: "first",

	FieldQuad: "first",
	FieldPent: "first",
	FieldRing: "first",

	FieldShells:  "first",
	FieldNails:   "first",
	FieldRockets: "first",
	FieldCells:   "first",

	FieldSpawns: "any",
	FieldDeaths: "any",
}

// LegacyReducerSet is kept as a named alias for DefaultReducers for
// callers that want to explicitly opt in to v6-equivalent
// "first-sample-of-bucket" semantics. After the v7 default-policy
// alignment the two are identical, but the alias preserves the
// option to diverge later (if e.g. a future analytics-flavoured
// default flips back to "last" / "mean" for some fields).
var LegacyReducerSet = DefaultReducers

// resolveReducerName picks the reducer for a field, allowing per-call
// overrides. Returns the registered Reducer or an error if the chosen
// name is not registered.
func resolveReducerName(field string, overrides map[string]string) (Reducer, error) {
	name, ok := overrides[field]
	if !ok {
		name = DefaultReducers[field]
	}
	if name == "" {
		name = "last"
	}
	return LookupReducer(name)
}

// validateFields returns an error if any code in fields is not a known
// field. Empty input is fine.
func validateFields(fields []string) error {
	for _, f := range fields {
		if _, ok := fieldKinds[f]; !ok {
			return fieldErr(f)
		}
	}
	return nil
}

type unknownFieldError struct{ Code string }

func (e unknownFieldError) Error() string { return "unknown field code " + e.Code }

func fieldErr(code string) error { return unknownFieldError{Code: code} }

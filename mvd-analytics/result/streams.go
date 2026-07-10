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
	// Movers is the pose timeline of every tracked brush-model entity
	// (lifts, doors, plats, trains). Schema v32; omitted when the demo has
	// no movers.
	Movers []MoverStream `json:"movers,omitempty"`

	// Projectiles and Beams are the spatial weapon-fire streams for the map
	// view (schema v40): every tracked rocket/grenade flight and every LG
	// bolt. They are opt-in — built only when the shot-stream flag is set
	// (qw-analyze -include projectiles,beams; the WASM map build) — so the
	// default output and golden corpus stay lean. Omitted when not built.
	Projectiles *ProjectileStreams `json:"projectiles,omitempty"`
	Beams       *BeamStreams       `json:"beams,omitempty"`
	// Nails is the nail-flight stream (ng/sng spikes), same columnar shape as
	// Projectiles with Weapon == "nail". Doubly opt-in: built only when nail
	// tracking is on AND shot streams are requested, since nails are far
	// higher volume than rockets/grenades.
	Nails *ProjectileStreams `json:"nails,omitempty"`

	// LOSComputed records whether the (lazy) line-of-sight pass has run on
	// this in-memory Result, so a caller (web overlay, -include los, the
	// mvd-api /los endpoint) computes it on demand exactly once and does not
	// retry on maps that genuinely have no LOS (no BSP). It latches for the
	// lifetime of this Result value. The API's tier-2 gob is written once at
	// parse (before any LOS pass), so a Result re-decoded from it starts with
	// LOSComputed=false; but the API persists the computed LOS separately in
	// its tier-3 artifact cache (mvd-api/internal/democache), so the /los pass
	// is not re-run after a restart or eviction — the warm request splices the
	// cached intervals and re-sets this latch. Excluded from JSON (`json:"-"`):
	// consumers read presence/absence of PlayerStream.LOS itself, and the
	// goldens stay agnostic to it. See analyzer.ComputeLOS and the "los" lazy
	// artifact (analyzer/materialize.go).
	LOSComputed bool `json:"-"`

	// ShotStreamsComputed / NailsComputed latch the spatial weapon-fire streams:
	// the eager build (shots.go buildSpatialStreams) sets each flag truthfully
	// when its build flag (Registry.BuildShotStreams / BuildNails) was on, so a
	// consumer can tell "streams built, possibly empty" from "streams never
	// built". mvd-api turns both flags on for every parse (the always-full
	// cache), and the WASM web build likewise; the default CLI parse leaves them
	// off (lean output). There is no on-demand rebuild anymore — phase 12 folded
	// the streams into the base parse and deleted the lazy "shot-streams"
	// artifact. JSON-excluded — clients read presence/absence of the streams
	// themselves.
	ShotStreamsComputed bool `json:"-"`
	NailsComputed       bool `json:"-"`
}

// ProjectileStreams is every tracked rocket/grenade flight as parallel
// columns (one entry per flight). Flight i renders as a dot moving from
// (Sx,Sy,Sz)[i] at Spawn[i] to (Ex,Ey,Ez)[i] at End[i], match-relative ms —
// linear, which is exact for rockets and an approximation for bouncing
// grenades. Weapon[i] is "rl" or "gl". Built only when shot streams are
// requested (see Streams.Projectiles).
type ProjectileStreams struct {
	Weapon []string  `json:"w"`
	Spawn  []int32   `json:"s"`
	End    []int32   `json:"e"`
	Sx     []float32 `json:"sx"`
	Sy     []float32 `json:"sy"`
	Sz     []float32 `json:"sz"`
	Ex     []float32 `json:"ex"`
	Ey     []float32 `json:"ey"`
	Ez     []float32 `json:"ez"`
}

// BeamStreams is every Lightning Gun bolt (TE_LIGHTNING2) as parallel
// columns. Beam i is the segment (Sx,Sy,Sz)[i] → (Ex,Ey,Ez)[i] flashed at
// T[i] match-relative ms (Sx is the muzzle, Ex the trace endpoint). Built
// only when shot streams are requested (see Streams.Beams).
type BeamStreams struct {
	T  []int32   `json:"t"`
	Sx []float32 `json:"sx"`
	Sy []float32 `json:"sy"`
	Sz []float32 `json:"sz"`
	Ex []float32 `json:"ex"`
	Ey []float32 `json:"ey"`
	Ez []float32 `json:"ez"`
}

// MoverStream is one brush-model entity's pose timeline (a lift, door,
// plat or train). The columns align by index: at T[i] match-relative
// milliseconds the model sits at (X,Y,Z)[i] and is drawn when Vis[i]. The
// viewer offsets the corpus SubModel mesh (mapgeom SubModelMesh with the
// same ID as SubModel here) by (X,Y,Z) to place it.
//
// Origins are float32 — wire values are exact 1/8-unit multiples and
// int32 would quantize the pose stepping. Tracks are short: MVD delta
// compression only re-sends an origin when it moves, so a parked mover is
// a single entry and a travelling one re-sends per frame only while in
// motion. The first entry is clamped to T=0 carrying the match-start pose
// (normalizeMatchRelativeTimes) so a parked mover whose only wire state
// predates the match still has a pose.
type MoverStream struct {
	EntNum   int       `json:"ent"`
	SubModel int       `json:"sub"`
	T        []int32   `json:"t"`
	X        []float32 `json:"x"`
	Y        []float32 `json:"y"`
	Z        []float32 `json:"z"`
	Vis      []bool    `json:"vis"`
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

	// LOS records when this player (the looker) had a clear line of sight
	// to each other player, one LosTrack per opponent ever seen (schema
	// v37). Line of sight is asymmetric — the looker's single eye point vs.
	// the target's whole body — so A→B lives in A's LOS and B→A lives in
	// B's, computed independently. Populated only on maps with a
	// provisioned BSP (same gate as PositionTrack.H/Lq); absent otherwise.
	// Raw transitions, no smoothing (surface authoritative data).
	LOS []LosTrack `json:"los,omitempty"`

	// PVS records when each other player was potentially visible to this player
	// (the looker), reproducing exactly the server's per-client entity cull —
	// i.e. whether a live mvdsv would have sent that opponent's entity to this
	// player's client that frame (SV_PlayerVisibleToClient): the looker's fat PVS
	// (CM_FatPVS of origin+view_ofs) intersected with the opponent's entity leaf
	// set (its 1-unit-expanded bounding box, non-solid leaves), or always when
	// the opponent overflows MAX_ENT_LEAFS. The recorded MVD does not carry this
	// (the demo recorder sets pvs = NULL and stores every entity); it is
	// reconstructed here from the position tracks. Same LosTrack shape, same lazy
	// pass (analyzer.ComputeLOS) and BSP gate as LOS, schema v38. This same test
	// gates the LOS raycast (cast only for potentially-visible pairs), so PVS ⊇
	// LOS by construction. The gap between them (on the wire, but no clear ray)
	// is an occlusion-tolerant proximity/awareness signal. Raw transitions, no
	// smoothing.
	PVS []LosTrack `json:"pvs,omitempty"`
}

// LosTrack is one looker's line-of-sight onto a single opponent, as the
// half-open [Start, End) ms intervals (match-relative) during which the
// looker had a clear sightline. It hangs off the looker's PlayerStream; Other
// identifies the seen player.
//
// Other is the index into Streams.Players of the opponent (the seen player),
// not a name — the compact index the viewer resolves back to a name. A looker
// has at most one LosTrack per opponent; an opponent never seen is omitted.
//
// "Clear" means at least one of the 9 rays from the looker's eye
// (origin + (0,0,22)) to the opponent's 8 bounding-box corners + box midpoint
// reached the target without crossing CONTENTS_SOLID — worldspawn or any
// active mover (door/lift/plat) posed in the way (bspvis.RayHitsSolid /
// RayHitsSolidModel). Computed only while both players are alive. View
// direction is not considered: this is geometric visibility, not whether the
// opponent is within the looker's FOV.
type LosTrack struct {
	Other int16      `json:"o"`  // index into Streams.Players (the seen player)
	Iv    []Interval `json:"iv"` // half-open [Start,End) ms the looker saw Other
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
	// TimeBase is "demo" when no match start could be detected: nothing
	// was rebased, so every timestamp in the whole Result is on the raw
	// demo clock (t=0 = demo open, warmup included). Omitted on the
	// normal match-relative result. We cannot invent a time origin —
	// flagging honestly beats coercing (schema v52).
	TimeBase string `json:"timeBase,omitempty"`
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
//
// VP / VYa are the player's view direction per sample (schema v31): the
// raw angle16 wire shorts for view pitch and yaw, kept losslessly (the
// exact 2-byte value the server wrote). Decode to degrees with
// float(uint16(v)) * 360/65536 — values are in [0,360), so a pitch
// > 180 means looking up. The wire's roll is always 0 (the server zeroes
// it), so it is not stored. A forward unit vector is one trig call away:
// with p,y the decoded pitch/yaw in radians,
// forward = (cos p·cos y, cos p·sin y, −sin p). Same source as the
// origin columns (svc_playerinfo), so they are populated whenever T is;
// same length as T when present.
//
// VX / VY / VZ are the player's velocity per sample in **Quake units per
// second** (schema v32), derived — not a wire field — from the position
// columns by a central-difference estimator (second-order accurate) over
// the native-rate samples. The estimator does not differentiate across a
// respawn teleport or an abnormal time gap (death / pause / reconnect),
// so a velocity reads ~0 over those rather than spiking; an isolated
// sample reads 0. The source x/y/z are float32 Quake units (the wire's
// sub-unit origin, no longer rounded to whole units), sampled ~every
// 13 ms, so the derivative is sub-unit precise — smooth client-side
// only if a softer speed curve is wanted. Like x/y/z and h these are
// native float32; only the JSON text is rounded to 3 decimals (see
// PositionTrack.MarshalJSON / coord.go). Speed is hypot(vx,vy,vz); horizontal speed
// (the usual "are they bunnying" metric) is hypot(vx,vy). Populated
// whenever T is (no BSP needed); same length as T when present.
// X/Y/Z, H and VX/VY/VZ are plain float32 (native resolution); the JSON
// text is rounded to 3 decimals by PositionTrack.MarshalJSON (see
// coord.go) — lossless for eighth-unit positions, trimming only the
// float tail on derived velocity / height.
type PositionTrack struct {
	T   []int32   `json:"t"` // milliseconds since the stream's time origin
	X   []float32 `json:"x"`
	Y   []float32 `json:"y"`
	Z   []float32 `json:"z"`
	Li  []int16   `json:"li,omitempty"`
	H   []float32 `json:"h,omitempty"`   // height above the floor beneath the player; NoFloor = none
	Lq  []int8    `json:"lq,omitempty"`  // liquid state: 0 dry, else (type<<2)|level
	VP  []int16   `json:"vp,omitempty"`  // view pitch, raw angle16 (decode: u16*360/65536; >180 = up)
	VYa []int16   `json:"vya,omitempty"` // view yaw, raw angle16 (decode: u16*360/65536)
	VX  []float32 `json:"vx,omitempty"`  // velocity X, Quake units/sec (central difference)
	VY  []float32 `json:"vy,omitempty"`  // velocity Y, units/sec
	VZ  []float32 `json:"vz,omitempty"`  // velocity Z, units/sec
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
// is undefined there. Chosen as -1e9: far outside any real height (Quake
// maps span at most ±32768 per axis) so it can never be mistaken for one,
// and exactly representable in both float32 and float64 so it round-trips
// through JSON unchanged (unlike math.MinInt32, which a float32 serializes
// as -2147483600).
const NoFloor float32 = -1e9

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

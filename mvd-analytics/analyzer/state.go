package analyzer

// Shared sub-structs used by the timeline analyzer to group related fields
// instead of leaving them as a flat sprawl of `hasRL` / `hasLG` / `hasQuad` /
// `health` / `shells` / `x` / ... fields on every state struct.
//
// Grouping pulls a few small wins out of the analyzer:
//
//   - per-bucket and per-window aggregator structs can copy a whole substruct
//     in one assignment (`agg.ammo = pRaw.ammo`) instead of stamping each
//     field by hand;
//   - the boolean weapon/powerup flags get an OR-fold helper so the
//     `aggregateWindow` loop reads as one line per group instead of five;
//   - powerupKinds (used by detectPowerupEvents) can iterate the powerup
//     loadout as data instead of going through closures over each named
//     field.

// weaponLoadout records which weapons a player is carrying at a sample point.
// SG (baseline) and NG are intentionally not tracked.
type weaponLoadout struct {
	rl, lg, ssg, sng, gl bool
}

// powerupLoadout records the three QuakeWorld powerups.
type powerupLoadout struct {
	quad, pent, ring bool
}

// vitals holds the health/armor pair at a sample point. armorType is part
// of the same logical group because picking up RA replaces YA replaces GA.
type vitals struct {
	health    int
	armor     int
	armorType string // "ga" | "ya" | "ra" | ""
}

// ammoCounts holds the four ammo pools.
type ammoCounts struct {
	shells  int
	nails   int
	rockets int
	cells   int
}

// playerPosition is the world-space player origin sampled from svc_playerinfo.
type playerPosition struct {
	x, y, z float32
}

// streamBuilder is the per-slot append-only record that becomes
// result.PlayerStream at finalize. It's the historical companion to
// timelinePlayerState (the running cursor): the cursor holds "what
// is the value right now," the builder holds "every transition we've
// seen." Both share OnEvent dispatch.
//
// Append rules (D11 in PLAN-v3):
//
//   - Change streams (health, armor, ammo, ...) dedup against the
//     previous value: appendChange iff v != lastValue.
//   - Position track appends every native sample (positions almost
//     always differ; checking is overhead with no payoff).
//   - Interval streams open an anchor on false→true and close on
//     true→false; intervals open at match end are closed in
//     finalize.
//   - Spawns / deaths are timestamps; just append.
type streamBuilder struct {
	health       []changeI16
	armor        []changeI16
	armorType    []changeStr
	loc          []changeI16
	activeWeapon []changeI16

	rl, lg, gl, ssg, sng intervalState
	quad, pent, ring     intervalState

	shells, nails, rockets, cells []changeI16

	// posT / spawns / deaths are integer milliseconds — the canonical,
	// wire-native unit. Comparisons between pt.T and spawn/death boundaries
	// stay exact int32 here and downstream; converting to float seconds
	// would reintroduce the precision drift this schema-v8 type was
	// chosen to eliminate.
	posT   []int32
	posX   []float32
	posY   []float32
	posZ   []float32
	posLi  []int16   // resolved loc index per sample, populated in finalize
	posH   []float32 // height above floor per sample (result.NoFloor = none), populated in finalize when a clip hull is loaded
	posLq  []int8    // liquid state per sample ((type<<2)|level, 0 = dry), populated in finalize when the render BSP is loaded
	posVP  []int16   // view pitch per sample, raw angle16 — appended at record time alongside x/y/z
	posVYa []int16   // view yaw per sample, raw angle16 — appended at record time alongside x/y/z
	posVX  []float32 // velocity X per sample (units/sec), derived in finalize (resolveVelocities)
	posVY  []float32 // velocity Y per sample (units/sec), derived in finalize
	posVZ  []float32 // velocity Z per sample (units/sec), derived in finalize

	spawns []int32
	deaths []int32
}

// changeI16 / changeStr mirror result.ChangeI16 etc. Stored here in
// the analyser package so tests don't have to round-trip through
// result every time. Health/armor/loc/ammo all share int16, since
// Quake values regularly exceed int8 range (mega-health = 200, RA = 200).
// t is integer milliseconds (schema v8) — same unit as the result type.
type changeI16 struct {
	t int32
	v int16
}

type changeStr struct {
	t int32
	v string
}

// intervalState tracks an open-anchor period for a boolean stream.
// When held flips true the analyser sets anchor; on flip-false (or at
// match end) the [anchor, t) interval is appended to closed. Times
// are integer milliseconds (schema v8).
type intervalState struct {
	held   bool
	anchor int32
	closed []intervalRecord
}

type intervalRecord struct {
	start, end int32
}

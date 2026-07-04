package result

// AimResult is the per-player aim analysis (schema v41), derived as a
// post-process from Shots + Streams (interpolated position/view at fire time)
// + Damage + the LG beam stream. It is experimental and additive: it never
// changes the inputs it reads.
//
// Truthfulness contract:
//   - The crosshair-error samples are HITSCAN-ONLY (sg/ssg/lg). Rockets are
//     led, so crosshair-to-enemy is not "error" — they get Rocket instead.
//   - Error is reported both as signed degrees (DYaw/DPitch — the literal
//     "degrees off the enemy" drift metric) AND normalized by the target's
//     angular half-size (NYaw/NPitch — comparable across range; |n|<=1 is
//     roughly on the hitbox). The frontend bins the normalized values.
//   - Hit/miss is Shot.Hit (the Go-linked truth), never re-derived here.
//   - Target attribution is exact in duels (Mode "duel") and a labeled
//     nearest-crosshair-enemy heuristic in team games (Mode "team"). A shot
//     is only attributed to an enemy whose position track brackets the fire
//     time and who is alive at it (dead players keep streaming position
//     samples, so a corpse would otherwise remain a candidate).
type AimResult struct {
	Players []PlayerAim `json:"players"`
}

// PlayerAim holds one player's aim sub-blocks. Sub-blocks are nil when their
// inputs are absent (e.g. Rocket needs projectile linking, LGReach needs the
// beam stream), so a non-KTX or non-shot-stream analysis still yields the
// crosshair/ramp blocks it can compute.
type PlayerAim struct {
	Player string `json:"player"`
	Team   string `json:"team,omitempty"`
	// Mode is "duel" (single enemy, exact attribution) or "team" (nearest-
	// crosshair-enemy heuristic).
	Mode string `json:"mode"`

	Crosshair *CrosshairSamples `json:"crosshair,omitempty"`
	LGRamp    *LGRampSamples    `json:"lgRamp,omitempty"`
	// Weapons is the rich per-weapon effectiveness breakdown (one entry per
	// weapon the player fired): shots/hits for all, plus weapon-specific
	// detail — SG/SSG pellet stats, RL/GL direct/splash, LG whiff geometry.
	Weapons []WeaponAim `json:"weapons,omitempty"`
}

// CrosshairSamples is the columnar per-hitscan-fire crosshair error to the
// attributed target. All slices share one index. DYaw/DPitch are signed
// degrees (right/up positive); NYaw/NPitch are those divided by the target's
// angular half-width/half-height at Dist. Dist is the eye→target-center
// distance in Quake units.
type CrosshairSamples struct {
	T      []int32   `json:"t"`
	Weapon []string  `json:"w"`
	DYaw   []float32 `json:"dyaw"`
	DPitch []float32 `json:"dpitch"`
	NYaw   []float32 `json:"nyaw"`
	NPitch []float32 `json:"npitch"`
	Dist   []float32 `json:"dist"`
	Hit    []bool    `json:"hit"`
	Target []string  `json:"tgt"`
}

// LGRampSamples is the columnar per-LG-fire "ramp onto target" series. Since
// is milliseconds since the start of the shaft the fire belongs to (fires
// less than the shaft-gap apart are one shaft). Hit shares the index.
type LGRampSamples struct {
	Since []int32 `json:"since"`
	Hit   []bool  `json:"hit"`
}

// WeaponAim is one weapon's effectiveness for a player. Shots (fires) and Hits
// (fires that connected) are populated for every weapon; the rest are
// weapon-specific and omitempty.
//
//   - SG/SSG (per-pellet): Pellets fired (shots × 6/14), PelletHits (Σ damage
//     / 4 — matches KTX acc.hits), and the per-fire split Full (all pellets
//     hit) / Partial (some) / Miss (none).
//   - RL/GL: Direct (non-splash contacts ≈ KTX hits), Splash (linked hits that
//     were splash-only), Missed (fires that linked to no impact);
//     Direct+Splash+Missed == Shots. Present only when projectile linking ran.
//   - LG: of the missed fires, NearMiss (beam ended near an enemy = aim error)
//     vs Blocked (ended on geometry short of max range = an object was in the
//     way) vs OutOfRange (beam reached its ~600-unit max length without
//     hitting anything = aimed into open space / enemy beyond reach) vs
//     Unresolved (no beam matched). Present only when the beam stream was built.
type WeaponAim struct {
	Weapon string `json:"weapon"`
	Shots  int    `json:"shots"`
	Hits   int    `json:"hits"`

	Pellets    int `json:"pellets,omitempty"`
	PelletHits int `json:"pelletHits,omitempty"`
	Full       int `json:"full,omitempty"`
	Partial    int `json:"partial,omitempty"`
	Miss       int `json:"miss,omitempty"`

	Direct int `json:"direct,omitempty"`
	Splash int `json:"splash,omitempty"`
	Missed int `json:"missed,omitempty"`

	NearMiss   int `json:"nearMiss,omitempty"`
	Blocked    int `json:"blocked,omitempty"`
	OutOfRange int `json:"outOfRange,omitempty"`
	Unresolved int `json:"unresolved,omitempty"`
}

package result

// ShotsResult is the per-shot weapon-fire stream plus per-player/weapon
// aggregates. A "shot" is one discrete weapon fire as observed on the wire:
// for SG/SSG/RL/GL/NG/SNG it is one svc_sound fire event on the shooter's
// CHAN_WEAPON (the sound carries the firing entity); for LG — the one
// weapon with no per-shot fire sound — it is one TE_LIGHTNING2 beam
// (KTX emits exactly one per fire tick), flagged Source="beam".
//
// The raw Shots stream is NOT match-gated — warmup fires are real signal and
// consumers window by Time. ByPlayer aggregates ARE match-gated (KTX
// scoreboard parity) so Reconciliation against demoInfo accuracy is
// meaningful.
type ShotsResult struct {
	// Shots is every detected fire, chronological, match-relative ms.
	Shots []Shot `json:"shots"`
	// ByPlayer holds match-time per-weapon counts and (hitscan) accuracy.
	ByPlayer []PlayerShots `json:"byPlayer,omitempty"`
	// Reconciliation cross-checks detected counts against KTX's
	// authoritative acc.attacks; nil when no demoInfo is present (non-KTX).
	Reconciliation *ShotsReconciliation `json:"reconciliation,omitempty"`
}

// Shot is one weapon fire. Time is match-relative milliseconds (same clock
// as DamageEntry.Time). Weapon is the lowercase KTX weapon name
// ("sg","ssg","ng","sng","gl","rl","lg"). Source is "sound" (a CHAN_WEAPON
// fire sound) or "beam" (an LG TE_LIGHTNING2 bolt).
//
// Hit/Victims are populated for instantaneous hitscan weapons (sg/ssg/lg),
// where the shot and its damage land in the same server frame and link
// truthfully via the KTX damage stream, and for projectile fires whose
// tracked flight (entity spawn→despawn) brackets back to the fire and
// forward to impact damage — rl/gl on every parse, ng/sng only when nail
// decoding was enabled. On non-KTX servers there is no damage stream, so
// Hit is always false.
//
// Warmup is true for fires outside the match (prewar / warmup / post-match) —
// the stream keeps them, but the ByPlayer aggregates and match-time consumers
// (e.g. the aim analysis) exclude them. Match-time fires omit the field.
// VictimKinds classifies each Victims entry — "enemy", "team" (same non-empty
// team as the shooter, not self) or "self" (victim slot == attacker slot, an
// rl/gl self-splash such as a rocket jump) — mirroring the Damage layer's
// IsSelf/IsTeam semantics. Omitted when every victim is an enemy (the common
// case); when present it is parallel to Victims. Warmup fires classify with
// warmup-time teams.
type Shot struct {
	Time        int32    `json:"time"`
	Player      string   `json:"player"`
	Team        string   `json:"team,omitempty"`
	Weapon      string   `json:"weapon"`
	Source      string   `json:"source"`
	Hit         bool     `json:"hit,omitempty"`
	Victims     []string `json:"victims,omitempty"`
	VictimKinds []string `json:"victimKinds,omitempty"`
	Warmup      bool     `json:"warmup,omitempty"` // fired outside the match (prewar/warmup/post)
}

// PlayerShots is one player's match-time fire counts per weapon.
type PlayerShots struct {
	Player   string        `json:"player"`
	Team     string        `json:"team,omitempty"`
	Total    int           `json:"total"`
	ByWeapon []WeaponShots `json:"byWeapon"`
}

// WeaponShots is a per-weapon count. Hits/Accuracy are populated only for
// linkable weapons (hitscan sg/ssg/lg + projectile rl/gl) and only when a
// damage stream was present; Accuracy is Hits/Shots in [0,1] over ALL
// victims (KTX scoreboard parity — team and self hits included).
//
// EnemyHits/TeamHits/SelfHits split Hits by victim class (see
// Shot.VictimKinds). A multi-victim fire counts in every bucket it has a
// victim in, so the buckets overlap and none is derivable from the others;
// each is emitted whenever nonzero. Per-bucket accuracy is bucketHits/Shots.
type WeaponShots struct {
	Weapon    string  `json:"weapon"`
	Shots     int     `json:"shots"`
	Hits      int     `json:"hits,omitempty"`
	Accuracy  float64 `json:"accuracy,omitempty"`
	EnemyHits int     `json:"enemyHits,omitempty"` // fires with ≥1 enemy victim
	TeamHits  int     `json:"teamHits,omitempty"`  // fires with ≥1 teammate victim
	SelfHits  int     `json:"selfHits,omitempty"`  // fires with ≥1 self victim (rl/gl splash)
}

// ShotsReconciliation compares detected shot counts to the KTX end-of-match
// accuracy block (demoInfo.players[].weapons[].acc). Diagnostic only —
// divergence is reported, never used to adjust the detected stream.
type ShotsReconciliation struct {
	ByPlayer map[string][]ShotsDelta `json:"byPlayer"`
}

// ShotsDelta is one player+weapon reconciliation row.
//
// KTX's acc.attacks counts in weapon-specific units: pellets for SG/SSG
// (6 and 14 per trigger pull), one per projectile for RL/GL/NG/SNG, and one
// per cell-tick for LG (ktx/src/weapons.c). StreamAttacks converts our
// discrete StreamShots into that same unit (×6 SG, ×14 SSG, ×1 otherwise)
// so StreamAttacks and KtxAttacks are directly comparable; a large gap flags
// a detection problem (or a non-standard mode such as yawnmode SSG).
type ShotsDelta struct {
	Weapon        string `json:"weapon"`
	StreamShots   int    `json:"streamShots"`
	StreamAttacks int    `json:"streamAttacks"`
	KtxAttacks    int    `json:"ktxAttacks"`
	KtxHits       int    `json:"ktxHits"`
}

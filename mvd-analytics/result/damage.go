package result

// DamageResult holds per-hit damage and derived aggregates, reconstructed
// from the KTX mvdhidden_dmgdone stream (see mvd-reader MVD_FORMAT.md). All
// damage figures are raw/unbound amounts including overkill, exactly as KTX
// reports them on the wire.
//
// Unbound vs bounded: the wire carries UNBOUND damage (the full hit, capped
// only at 9999). KTX's end-of-match scoreboard (demoInfo.players[].dmg)
// instead accumulates damage BOUNDED to the victim's remaining health (no
// overkill). So these figures are systematically higher than the scoreboard,
// most dramatically on killing blows. That gap is expected, not a defect —
// see DamageReconciliation for the cross-check.
//
// Telefrags and stomps are NOT weapon damage: they are positional instant
// kills (teleporting onto a player; landing on their head). The wire reports
// a telefrag as the 9999 cap, which would otherwise dominate the attacker's
// Given / ByWeapon / EWep and the totals; a stomp is a movement kill, not a
// weapon. Both are excluded from every damage figure here and surfaced
// separately in Telefrags / Stomps (and as the opt-in "telefrag" / "stomp"
// events in view.Events). The kill still appears in FragResult / the "frag"
// event.
type DamageResult struct {
	TotalDamage int                      `json:"totalDamage"`
	Events      []DamageEntry            `json:"events"`               // per-hit log, time-ordered (excludes telefrags + stomps)
	ByWeapon    map[string]int           `json:"byWeapon"`             // attacker weapon -> total enemy damage
	ByPlayer    map[string]*PlayerDamage `json:"byPlayer"`             // keyed by player name
	Matrix      []DamagePair             `json:"matrix"`               // attacker -> victim totals
	Telefrags   []PositionalKill         `json:"telefrags,omitempty"`  // instant-kill telefrags, separate from damage
	Stomps      []PositionalKill         `json:"stomps,omitempty"`     // head-stomp kills, separate from damage
	Scoreboard  *DamageReconciliation    `json:"scoreboard,omitempty"` // stream vs KTX-scoreboard cross-check
}

// PositionalKill is one telefrag (deathtype "tele") or stomp (deathtype
// "stomp") — an instant kill from occupying a player's space rather than
// from a weapon. There is no meaningful damage amount (a telefrag is the
// 9999 instakill sentinel; a stomp is a movement kill), so none is recorded.
// Time is match-relative milliseconds.
type PositionalKill struct {
	Time     int32  `json:"time"`
	Attacker string `json:"attacker"` // killer ("world" only in the degenerate non-player case)
	Victim   string `json:"victim"`
	IsTeam   bool   `json:"isTeam,omitempty"` // killer and victim on the same team
}

// DamageEntry is a single damage event. Time is match-relative
// milliseconds (matches FragEntry.Time).
type DamageEntry struct {
	Time      int32  `json:"time"`
	Attacker  string `json:"attacker"` // "world" for environmental / non-player inflictor
	Victim    string `json:"victim"`
	Weapon    string `json:"weapon"`              // attacker weapon, or environmental category
	Damage    int    `json:"damage"`              // raw/unbound, including overkill
	IsSplash  bool   `json:"isSplash,omitempty"`  // indirect (e.g. rocket splash)
	IsEnv     bool   `json:"isEnv,omitempty"`     // environmental / world-sourced
	IsSelf    bool   `json:"isSelf,omitempty"`    // attacker == victim
	IsTeam    bool   `json:"isTeam,omitempty"`    // same team, not self
	VictimWep string `json:"victimWep,omitempty"` // victim's weapon class at hit: sg|mid|lg|rl|both ("" if env/self/team)
}

// PlayerDamage holds per-player damage aggregates.
type PlayerDamage struct {
	Given     int            `json:"given"`     // to enemies (the "useful" number); KTX scoreboard analogue: dmg.given (bounded)
	Taken     int            `json:"taken"`     // from ALL sources (enemy + team + self + env). KTX dmg.taken (dmg_t) counts enemy-player damage only (combat.c:1083), so Taken runs higher.
	GivenTeam int            `json:"givenTeam"` // to teammates
	GivenSelf int            `json:"givenSelf"` // attacker == victim
	TakenEnv  int            `json:"takenEnv"`  // from world / environment
	ByWeapon  map[string]int `json:"byWeapon"`  // enemy damage given, by attacker weapon

	// EnemyVs* partition enemy-given damage by the VICTIM's held weapons at
	// the moment of the hit — KTX "ewep" semantics, keyed on the target's
	// inventory (ktx/src/combat.c:1084-1089), NOT the attacker's weapon.
	// Mutually exclusive, priority RL+LG > RL > LG > mid > sg; the five
	// buckets sum to Given.
	EnemyVsSG   int `json:"enemyVsSg"`   // victim holds shotgun-tier only (sg/ng)
	EnemyVsMid  int `json:"enemyVsMid"`  // victim holds ssg/sng/gl, no LG/RL
	EnemyVsLG   int `json:"enemyVsLg"`   // victim holds LG, not RL
	EnemyVsRL   int `json:"enemyVsRl"`   // victim holds RL, not LG
	EnemyVsBoth int `json:"enemyVsBoth"` // victim holds both RL and LG
	EWep        int `json:"ewep"`        // = EnemyVsLG + EnemyVsRL + EnemyVsBoth (KTX dmg_eweapon)

	Telefrags int `json:"telefrags,omitempty"` // instant-kill telefrags DEALT (not damage; excluded from Given)
	Stomps    int `json:"stomps,omitempty"`    // head-stomp kills DEALT (not damage; excluded from Given)
}

// DamagePair is one attacker→victim total in the damage matrix.
type DamagePair struct {
	Attacker string         `json:"attacker"`
	Victim   string         `json:"victim"`
	Damage   int            `json:"damage"`
	ByWeapon map[string]int `json:"byWeapon"` // attacker weapon -> damage to this victim
}

// DamageReconciliation cross-checks the stream-derived per-player totals
// against the KTX end-of-match scoreboard (demoInfo.players[].dmg). It is
// diagnostic: divergence is surfaced as data, never used to coerce the
// stream-derived numbers.
type DamageReconciliation struct {
	ByPlayer map[string]*DamageDelta `json:"byPlayer"`
}

// DamageDelta pairs this pipeline's figure with the KTX-scoreboard figure
// for one player. The Stream* fields are UNBOUND (overkill-inclusive, from
// the mvdhidden_dmgdone stream); the Score* fields are BOUNDED (capped to
// victim health, from the KTX scoreboard JSON). Score* <= Stream* by the
// overkill; the gap is expected, not a reconstruction error.
type DamageDelta struct {
	StreamGiven int `json:"streamGiven"` // unbound, this pipeline
	ScoreGiven  int `json:"scoreGiven"`  // bounded, KTX scoreboard (dmg.given)
	StreamTaken int `json:"streamTaken"` // unbound, this pipeline
	ScoreTaken  int `json:"scoreTaken"`  // bounded, KTX scoreboard (dmg.taken)
	StreamEWep  int `json:"streamEwep"`  // unbound, this pipeline
	ScoreEWep   int `json:"scoreEwep"`   // bounded, KTX scoreboard (dmg.enemy-weapons)
}

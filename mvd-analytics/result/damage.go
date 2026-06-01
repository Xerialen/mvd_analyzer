package result

// DamageResult surfaces per-hit damage decoded from KTX's mvdhidden_dmgdone
// (0x0007) blocks — previously decoded by the reader but consumed by no
// analyzer, so it never reached the schema.
//
// CRITICAL SEMANTICS — read before comparing to demoInfo. The wire reports
// `unbound_dmg_dealt`: armor-adjusted damage that is NOT capped by the
// victim's remaining health (overkill included), bounded only to 9999
// (KTX ktx/src/combat.c:809,819). KTX's demoInfo scoreboard (dmg.given /
// dmg.taken / weapons[w].damage) instead accumulates `dmg_dealt`, which IS
// capped by the victim's health — overkill removed (combat.c:789,796,1082).
//
// The two are therefore DIFFERENT quantities and cannot be made equal by
// summation. The only correct relationship for the raw value is the
// directional invariant
//
//	Σ GivenRaw(enemy)  >=  demoInfo.dmg.given
//
// with the non-negative gap equal to total overkill (largest for splash
// weapons and in crowded 4on4 play). Every "Raw" field is the unbound
// quantity; do not relabel it "given" for a scoreboard.
//
// To recover the canonical (overkill-free) quantity, each hit is run through
// KTX's exact damage accounting (combat.c:634,648,655,796), reconstructed from
// the victim's tracked vitals: save = min(ceil(armortype·D), armour); take =
// D − save; credit = save + bound(0, take, health). armortype (GA .3 / YA .6 /
// RA .8) comes from the held armour bit in StatItems; health/armour from
// StatHealth/StatArmor. This is NOT a naive clamp at health+armour — armour
// only absorbs its fraction, so health overkill on an armoured victim is
// dropped correctly. Σ Eff reconstructs the scoreboard: clean duels reconcile
// EXACTLY with demoInfo, team games within ~0.7% (residual from the
// telefrag-scoring cvar and reconnect folding). So Eff is what skill/carry
// metrics (DDR, stacked-DDR) should use to stay comparable to ktxstats /
// DeepFrag. The 9999 telefrag sentinel is reconstructed as what KTX removed
// (armour + health), not dropped.
//
// Edge case: under k_dmgfrags against a victim holding the Pentagram, KTX's
// scoreboard dmg_dealt is itself NOT health-capped (combat.c:781-783), so for
// those hits the scoreboard equals the unbound value — the directional
// invariant still holds (raw >= capped, with the gap zero for that hit).
type DamageResult struct {
	// ByPlayer aggregates the unbound per-hit damage by resolved
	// (reconnect-unified) player identity. Keyed by display name, matching
	// FragResult.ByPlayer so consumers can join the two.
	ByPlayer map[string]*PlayerDamage `json:"byPlayer,omitempty"`

	// Hits is the raw per-hit log (enemy + teammate hits; self damage is
	// folded into PlayerDamage.SelfRaw only). Ordered by time. This is the
	// unlock for window-restricted carry metrics (e.g. stacked-DDR): each
	// hit carries the resolved attacker/victim identity and a match-relative
	// timestamp so it can be joined against the per-player armor / RL / LG
	// intervals in Streams.
	Hits []DamageHit `json:"hits,omitempty"`
}

// PlayerDamage holds one player's damage totals in two flavours:
//
//   - *Raw  — the UNBOUND wire value (overkill-inclusive). What the MVD
//     actually carries; a "raw pressure" signal.
//   - *Eff  — the EFFECTIVE (overkill-free) value, reconstructed per hit via
//     KTX's armour-absorption + health-cap accounting (see below). Summed over
//     a whole match, Σ Eff reconciles with the demoInfo scoreboard (exactly for
//     duels). This is the canonical quantity to use for skill/carry metrics
//     (DDR, stacked-DDR) so the numbers stay comparable to ktxstats / DeepFrag.
//
// Given/Team/Self partition outgoing damage by victim relationship; Taken is
// incoming enemy damage (mirrors KTX dmg_t, enemy-only).
type PlayerDamage struct {
	GivenRaw int `json:"givenRaw"`          // unbound damage dealt to enemies
	TeamRaw  int `json:"teamRaw,omitempty"` // unbound damage dealt to teammates
	SelfRaw  int `json:"selfRaw,omitempty"` // unbound self damage (attacker == victim)
	TakenRaw int `json:"takenRaw"`          // unbound damage received from enemies

	GivenEff int `json:"givenEff"`          // health-capped enemy damage (== demoInfo.dmg.given)
	TeamEff  int `json:"teamEff,omitempty"` // health-capped teammate damage
	SelfEff  int `json:"selfEff,omitempty"` // health-capped self damage
	TakenEff int `json:"takenEff"`          // health-capped enemy damage received

	ByWeapon map[string]int `json:"byWeapon,omitempty"` // GivenEff split by weapon (rl, lg, sg, …)
	Hits     int            `json:"hits"`               // count of outgoing enemy hits
}

// DamageHit is one decoded per-hit damage record, resolved to player
// identities. Damage is the unbound wire value; DamageEff is that value capped
// at the victim's health before the hit (overkill removed). TimeMs is
// match-relative integer milliseconds after normalizeMatchRelativeTimes.
type DamageHit struct {
	Attacker  string `json:"a"`
	Victim    string `json:"v"`
	Damage    int    `json:"d"`           // unbound (overkill-inclusive)
	DamageEff int    `json:"de"`          // effective (capped at victim health-before)
	Weapon    string `json:"w,omitempty"`
	Splash    bool   `json:"s,omitempty"`
	TimeMs    int32  `json:"t"`
}

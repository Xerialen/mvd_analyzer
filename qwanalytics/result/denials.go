package result

// DenialsResult is the per-match list of "denied" (stolen-from-enemy)
// and "hoovered" (stolen-from-team) item pickups, derived from the
// item-pickup pipeline plus per-bucket player state and the loc
// connectivity graph.
//
// Region semantics: an item at loc A is "in the region" of A plus
// every loc B for which the loc graph has at least 10 traversals in
// both directions (A->B and B->A).
//
// "Weapon" semantics: a player is treated as a weapon-bearer if they
// hold RL or LG, or are currently carrying Quad. Same on both teams.
type DenialsResult struct {
	Denials []DenialEvent `json:"denials"`
	Hoovers []HooverEvent `json:"hoovers"`
}

// DenialEvent is one item pickup classified as a steal from the
// opposing team. The picker had no RL/LG, no enemy-team weapon-bearer
// is missing from the region, and the picker's own team had no
// weapon-bearer in the region either (otherwise the pickup is just a
// normal contested grab).
type DenialEvent struct {
	Time         float64 `json:"time"` // match-relative seconds
	Player       string  `json:"player"`
	Team         string  `json:"team"`
	Item         string  `json:"item"`          // ra/ya/mh/lg/rl/quad/pent/ring
	Loc          string  `json:"loc,omitempty"` // item's spawn loc
	EnemyWeapons int     `json:"enemyWeapons"`  // count of enemy RL/LG/Quad bearers in region
	PlayerUserID int     `json:"playerUserID,omitempty"`
}

// HooverEvent is one item pickup classified as taking value a teammate
// needed. Picker has no RL/LG; a same-team weapon-or-Quad bearer in
// the region was below the per-item threshold.
type HooverEvent struct {
	Time          float64 `json:"time"`
	Player        string  `json:"player"`
	Team          string  `json:"team"`
	Item          string  `json:"item"` // ra/ya/mh
	Loc           string  `json:"loc,omitempty"`
	NeedyTeammate string  `json:"needyTeammate"` // teammate name with weapon/Quad and below threshold
	NeedyStat     string  `json:"needyStat"`     // "armor" or "health"
	NeedyValue    int     `json:"needyValue"`    // teammate's armor or health at pickup
	PlayerUserID  int     `json:"playerUserID,omitempty"`
}

package result

// WeaponPickup is a single slot-weapon acquisition event — either a
// world-spawner pickup (RL/LG/GL/SSG/SNG/NG on its respawn pad) or a
// backpack pickup where the pack contained a weapon.
//
// The attribution signals are authoritative KTX hints:
//   - World pickups come from `//ktx took` (ItemPickupHintEvent);
//     see ktx/src/items.c:1048.
//   - Backpack pickups come from `//ktx bp` (BackpackPickupHintEvent);
//     see ktx/src/items.c:2471. Only RL/LG packs emit this hint —
//     SSG/NG/SNG/GL-only packs have no wire-level pickup signal and
//     do not appear here.
//   - In weapon-stay modes (serverinfo deathmatch 2/3/5, or coop) KTX
//     never emits `//ktx took` for weapons (weapon_touch returns before
//     the stuffcmd, ktx/src/items.c:1046-1052), so entries are instead
//     synthesized from STAT_ITEMS weapon-bit 0→1 transitions and marked
//     Inferred. Source is "world" when the picker passed within pickup
//     range of a matching weapon spawn entity during the stat-lag
//     window, else "unknown" (typically a non-RL/LG backpack grant,
//     which has no hint in any mode).
//
// Kills is credited only to the pickup that actually granted the
// weapon (HadBefore=false). Redundant grabs (HadBefore=true — the
// picker already held Weapon) stay in the list as zero-kill entries
// so denial labelling in the frontend still works, but they do not
// claim kills that would have happened anyway with the weapon the
// picker already carried. Within a single life the first pickup of
// a weapon is by construction HadBefore=false, and all subsequent
// pickups of that same weapon in the same life are HadBefore=true.
type WeaponPickup struct {
	Time          int32  `json:"time"` // Match-relative milliseconds (schema v8)
	Player        string `json:"player"`
	Team          string `json:"team,omitempty"`
	Weapon        string `json:"weapon"` // "rl","lg","gl","ssg","sng","ng"
	Source        string `json:"source"` // "world" | "backpack" | "unknown"
	HadBefore     bool   `json:"hadBefore"`
	Inferred      bool   `json:"inferred,omitempty"` // synthesized from a STAT_ITEMS flip (weapon-stay recovery), not a KTX hint
	Kills         int    `json:"kills"`
	NextDeathTime int32  `json:"nextDeathTime,omitempty"` // ms; 0 if picker never died before match end

	// Backpack-source fields. Only set when Source == "backpack".
	// BackpackEnt pairs with BackpackDrop.EntNum so the frontend can
	// join a pickup row to its originating drop.
	BackpackEnt int    `json:"backpackEnt,omitempty"`
	Dropper     string `json:"dropper,omitempty"`
	DropperTeam string `json:"dropperTeam,omitempty"`
	DropTime    int32  `json:"dropTime,omitempty"` // ms
}

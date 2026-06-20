package result

// MatchResult contains match summary information. Time fields are
// integer milliseconds (schema v8). The match window itself lives in
// streams.global (matchStart/matchEnd); read Duration for "how long was
// the match".
type MatchResult struct {
	Map      string       `json:"map"`
	GameDir  string       `json:"gameDir"`
	Duration int32        `json:"duration"` // ms
	Players  []PlayerStat `json:"players"`
	Teams    []TeamStat   `json:"teams,omitempty"`
}

// PlayerStat is a player's final scoreboard line. Frags is the canonical
// QW net score (from the svc_updatefrags scoreboard via Context.FragsBySlot).
// Kills, Deaths and Suicides are the corrected counts from the frag log —
// they fix the KTX demoinfo stats, which credit several self / positional
// deaths to the wrong entity: a pentagram-deflect telefrag (dtTELE2) inflates
// the deflector's kills, and a world-dealt suicide (fall / lava / squish /
// drown) bumps the world entity's counter instead of the victim's
// (ktx/src/client.c:5132), so demoinfo undercounts suicides. Kills/Deaths
// come from FragResult.ByPlayer; Suicides is the per-victim count of
// IsSuicide frag entries. All 0 when the demo carried no frag log;
// per-weapon kills stay in FragResult.ByPlayer.ByWeapon.
type PlayerStat struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	Frags    int    `json:"frags"`
	Kills    int    `json:"kills"`
	Deaths   int    `json:"deaths"`
	Suicides int    `json:"suicides"`
}

// TeamStat represents a team's statistics.
type TeamStat struct {
	Name  string `json:"name"`
	Frags int    `json:"frags"`
}

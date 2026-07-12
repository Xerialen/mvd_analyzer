package result

// Decisions is the tactical-decision section (schema v57): what a player
// DECIDED, as opposed to what happened. Two sources share the record shape
// so bot-logged and human-inferred decisions are directly comparable:
//
//   - "kdlog": structured KDLOG telemetry emitted by the Komodobot KTX build
//     (kbot-0.23.0-dlog+) into the server console log, joined against this
//     demo by the resolver in the decisions package. Ground truth for what
//     the bot brain chose and which alternatives it scored.
//   - "inferred": pickup-anchored reverse-engineering from the demo alone
//     (no log needed): each item/backpack pickup becomes a goal decision at
//     the estimated commit time. Best-effort — Confidence marks it.
//
// Times are int32 match-relative milliseconds like every other section.
// Vocabulary is the analyzer's own: item kinds via ItemTimeline join, locs
// via the player's PVS-attributed position stream (LocTable), weapons via
// the DeathTypeToWeapon strings.
type Decisions struct {
	Source         string           `json:"source"`                   // "kdlog" | "inferred"
	EmitterVersion string           `json:"emitterVersion,omitempty"` // KDLOG anchor stamp (kdlog only)
	DlogLevel      int              `json:"dlogLevel,omitempty"`      // k_kbot_dlog at emission (kdlog only)
	Records        []DecisionRecord `json:"records"`
	Errors         []string         `json:"errors,omitempty"` // parse/resolve problems (never fatal)
}

// DecisionRecord is one decision by one player.
type DecisionRecord struct {
	T      int32  `json:"t"`      // match-relative ms
	Player string `json:"player"` // canonical stream name (join key)
	Team   string `json:"team,omitempty"`
	Slot   int    `json:"slot"` // demo player slot (KDLOG ed-1)
	Type   string `json:"type"` // "goal" | "enemy" | "evade" | "play"

	// Where the decider stood when deciding.
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
	Loc string  `json:"loc,omitempty"` // from the player's own position stream (PVS-aware)

	State   *DecisionState `json:"state,omitempty"`
	Trigger string         `json:"trigger,omitempty"` // goal: refresh|item_taken|enemy_event|goal_reached|relocate|spawn; inferred: inferred_pickup|inferred_backpack

	// type=goal
	Chosen     *DecisionGoal  `json:"chosen,omitempty"`     // what the bot now pursues (nil = no viable goal)
	Prim       *DecisionGoal  `json:"prim,omitempty"`       // primary goal when chosen is a two-step intermediate
	Candidates []DecisionGoal `json:"candidates,omitempty"` // top-K viable alternatives, score-descending

	// type=enemy
	Target    string  `json:"target,omitempty"`    // new enemy's canonical name ("" = target dropped)
	TargetLoc string  `json:"targetLoc,omitempty"` // target's loc at decision time
	Dist      float32 `json:"dist,omitempty"`      // brain's predicted engage distance (qu)

	// type=evade
	On *bool `json:"on,omitempty"`

	// type=play (movement plays: gapjump/chainhop lanes)
	Play   string `json:"play,omitempty"`
	Lane   string `json:"lane,omitempty"`
	Phase  string `json:"phase,omitempty"` // engage|launch|chainhop|land|fail|decline|abort|yield
	Detail string `json:"detail,omitempty"`

	// source=inferred only: how confident the inference is (0..1].
	Confidence float32 `json:"confidence,omitempty"`
}

// DecisionGoal describes a goal candidate in analyzer vocabulary. For world
// items Kind/Name/Loc come from the ItemTimeline join (Kind is the canonical
// item token: ra/ya/ga/mh/quad/...); for players Kind is "player" and Player
// carries the canonical name; for backpacks Kind is "backpack".
type DecisionGoal struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name,omitempty"`   // ItemTimeline name (ya_1) when resolved
	Player string  `json:"player,omitempty"` // when the goal is a player
	Cls    string  `json:"cls,omitempty"`    // raw server classname from the log
	EntNum int     `json:"entNum,omitempty"` // server edict from the log
	Marker int     `json:"marker,omitempty"` // frogbot nav-marker id (1-based)
	X      float32 `json:"x,omitempty"`
	Y      float32 `json:"y,omitempty"`
	Z      float32 `json:"z,omitempty"`
	Loc    string  `json:"loc,omitempty"`

	Desire   float32 `json:"desire,omitempty"`   // brain desire at eval time
	TravelMs int32   `json:"travelMs,omitempty"` // brain's predicted travel time
	Score    float32 `json:"score,omitempty"`    // desire * (lookahead - t) / (t + 5)
}

// DecisionState is the decider's resource snapshot, decoded to the field-code
// vocabulary (view/fields.go): h/a/at, weapon possession booleans, powerups,
// ammo counts, plus the wielded weapon as a weapon string.
type DecisionState struct {
	H  int16  `json:"h"`
	A  int16  `json:"a"`
	AT string `json:"at,omitempty"` // "ga"|"ya"|"ra"|""
	AW string `json:"aw,omitempty"` // wielded weapon: axe|sg|ssg|ng|sng|gl|rl|lg

	RL  bool `json:"rl,omitempty"`
	LG  bool `json:"lg,omitempty"`
	GL  bool `json:"gl,omitempty"`
	SSG bool `json:"ssg,omitempty"`
	SNG bool `json:"sng,omitempty"`

	Q  bool `json:"q,omitempty"`
	PE bool `json:"pe,omitempty"`
	R  bool `json:"r,omitempty"`

	SH int16 `json:"sh"`
	NL int16 `json:"nl"`
	RK int16 `json:"rk"`
	CL int16 `json:"cl"`
}

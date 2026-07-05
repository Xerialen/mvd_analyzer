package analyzer

import (
	"testing"
)

func TestParseFullserverinfo(t *testing.T) {
	a := NewMetadataAnalyzer()
	a.parseFullserverinfo(
		`fullserverinfo "\maxfps\77\timelimit\10\teamplay\2\hostname\la.quake.world:28504 NAQW\*version\MVDSV 1.20-dev\ktxver\1.45"`)

	cases := map[string]string{
		"maxfps":    "77",
		"timelimit": "10",
		"teamplay":  "2",
		"hostname":  "la.quake.world:28504 NAQW",
		"*version":  "MVDSV 1.20-dev",
		"ktxver":    "1.45",
	}
	for k, want := range cases {
		got := a.serverInfo[k]
		if got != want {
			t.Errorf("serverInfo[%q] = %q, want %q", k, got, want)
		}
	}

	if len(a.serverInfo) != len(cases) {
		t.Errorf("serverInfo has %d entries, want %d: %v", len(a.serverInfo), len(cases), a.serverInfo)
	}
}

func TestParseFullserverinfoNoTrailingQuote(t *testing.T) {
	// Tolerate a fullserverinfo without a closing quote (some servers
	// terminate the stufftext with just a newline).
	a := NewMetadataAnalyzer()
	a.parseFullserverinfo(`fullserverinfo "\maxfps\77\map\dm6`)
	if got := a.serverInfo["maxfps"]; got != "77" {
		t.Errorf("maxfps = %q, want 77", got)
	}
	if got := a.serverInfo["map"]; got != "dm6" {
		t.Errorf("map = %q, want dm6", got)
	}
}

func TestParseFullserverinfoMidGameUpdate(t *testing.T) {
	// Mid-game serverinfo updates via svc_serverinfo overwrite the values
	// originally seen in fullserverinfo (last-write-wins).
	a := NewMetadataAnalyzer()
	a.serverInfo["status"] = "Countdown"
	a.serverInfo["status"] = "3 min left"
	a.serverInfo["status"] = "Standby"
	if got := a.serverInfo["status"]; got != "Standby" {
		t.Errorf("status = %q, want Standby", got)
	}
}

func TestParseCountdownCenterprint_Duel(t *testing.T) {
	// Mirrors the lgc15048 demo: LGC duel with sudden-death overtime,
	// dmgfrags on, gl disabled.
	text := `Countdown:  1


Deathmatch  4
Mode      LGC
Respawns  KT2
Antilag     1
Timelimit   3
Overtime   sd
Dmgfrags   on

Noweapon   gl

no matchtag`

	got := parseCountdownCenterprint(text)
	if got == nil {
		t.Fatal("parseCountdownCenterprint returned nil")
	}
	if got.Mode != "LGC" {
		t.Errorf("Mode = %q, want LGC", got.Mode)
	}
	if got.Deathmatch != 4 {
		t.Errorf("Deathmatch = %d, want 4", got.Deathmatch)
	}
	if got.Spawnmodel != "KT2" {
		t.Errorf("Spawnmodel = %q, want KT2", got.Spawnmodel)
	}
	if got.SpawnK == nil || *got.SpawnK != 4 {
		t.Errorf("SpawnK = %v, want 4", got.SpawnK)
	}
	if got.Antilag != 1 {
		t.Errorf("Antilag = %d, want 1", got.Antilag)
	}
	if got.Timelimit != 3 {
		t.Errorf("Timelimit = %d, want 3", got.Timelimit)
	}
	if got.Overtime != "sd" {
		t.Errorf("Overtime = %q, want sd", got.Overtime)
	}
	if !got.Dmgfrags {
		t.Errorf("Dmgfrags = false, want true")
	}
	if got.Noweapon != "gl" {
		t.Errorf("Noweapon = %q, want gl", got.Noweapon)
	}
}

func TestParseCountdownCenterprint_Team(t *testing.T) {
	// Mirrors a typical KTX team match with matchtag and powerups on.
	// Note: KTX renders "Mode" as "T e a m" with internal spaces — the
	// parser must collapse them back to "Team".
	text := `Countdown:  1


Deathmatch  1
Mode  T e a m
Respawns  KT2
Antilag     1
Teamplay    2
Timelimit  20
Overtime    5
Powerups   on

matchtag qwsldraft`

	got := parseCountdownCenterprint(text)
	if got == nil {
		t.Fatal("parseCountdownCenterprint returned nil")
	}
	if got.Mode != "Team" {
		t.Errorf("Mode = %q, want Team", got.Mode)
	}
	if got.Teamplay != 2 {
		t.Errorf("Teamplay = %d, want 2", got.Teamplay)
	}
	if got.Timelimit != 20 {
		t.Errorf("Timelimit = %d, want 20", got.Timelimit)
	}
	if got.Powerups != "on" {
		t.Errorf("Powerups = %q, want on", got.Powerups)
	}
	if got.Matchtag != "qwsldraft" {
		t.Errorf("Matchtag = %q, want qwsldraft", got.Matchtag)
	}
	if got.Overtime != "5" {
		t.Errorf("Overtime = %q, want 5", got.Overtime)
	}
}

func TestParseCountdownCenterprint_SOCD(t *testing.T) {
	text := `Countdown:  1


Deathmatch  3
Mode  T e a m
Respawns  KT2
SOCDv2  stats

matchtag HTE`

	got := parseCountdownCenterprint(text)
	if got == nil {
		t.Fatal("parseCountdownCenterprint returned nil")
	}
	if got.SOCDv2 != "stats" {
		t.Errorf("SOCDv2 = %q, want stats", got.SOCDv2)
	}
}

func TestParseCountdownCenterprint_NilOnEmpty(t *testing.T) {
	if got := parseCountdownCenterprint(""); got != nil {
		t.Errorf("empty text returned %+v, want nil", got)
	}
	if got := parseCountdownCenterprint("Countdown: 10\n\n\nno matchtag"); got != nil {
		t.Errorf("countdown-only text returned %+v, want nil", got)
	}
}

func TestSpawnmodelToK(t *testing.T) {
	cases := map[string]int{
		"QW":  0,
		"KTS": 1,
		"KT":  2,
		"KTX": 3,
		"KT2": 4,
		"kt2": 4, // case-insensitive
		"???": -1,
		"":    -1,
	}
	for in, want := range cases {
		if got := spawnmodelToK(in); got != want {
			t.Errorf("spawnmodelToK(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCollapseSpaces(t *testing.T) {
	cases := map[string]string{
		"D u e l":          "D u e l", // single spaces stay
		"T  e  a  m":       "T e a m", // double spaces collapse
		"  hello  world  ": "hello world",
		"":                 "",
	}
	for in, want := range cases {
		if got := collapseSpaces(in); got != want {
			t.Errorf("collapseSpaces(%q) = %q, want %q", in, got, want)
		}
	}
}

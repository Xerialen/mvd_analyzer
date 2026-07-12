package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// TestParseObituaryLine locks in the unified obituary parser (obituary_parse.go),
// with a focus on the drift cases that previously disagreed between frag.go and
// messages.go. frag.go's behavior is the reference; these are the codes/forms
// the neutral parser must produce so both consumers agree.
func TestParseObituaryLine(t *testing.T) {
	cases := []struct {
		name   string
		msg    string
		killer string
		victim string
		weapon string
		sui    bool
		tk     bool
	}{
		// Drowning is weapon "water" (frag reference), not "drown" (old
		// messages code). See RESULT_SCHEMA.md FragEntry weapon vocabulary.
		{"drown fishes", "nexus sleeps with the fishes", "nexus", "nexus", "water", true, false},
		{"drown sucks", "nexus sucks it down", "nexus", "nexus", "water", true, false},

		// The unknown-cause catch-all must beat the shorter " becomes bored
		// with life" substring (suicide, not rl). messages.go lacked the long
		// form entirely.
		{"somehow bored", "nexus somehow becomes bored with life", "nexus", "nexus", "suicide", true, false},
		{"plain bored", "nexus becomes bored with life", "nexus", "nexus", "rl", true, false},

		// KTX k_spawnicide variants — absent from messages.go before unification.
		{"shiny spawn", "nexus couldn't resist the shiny spawn point", "nexus", "nexus", "tele", true, false},
		{"baby factory", "nexus got too close to the baby factory", "nexus", "nexus", "tele", true, false},
		{"poor choices", "nexus was fragged by poor life choices", "nexus", "nexus", "tele", true, false},

		// CRMod obituary variants — absent from messages.go before unification.
		{"disembowled sg", "prey was disembowled by killa's shotgun", "killa", "prey", "sg", false, false},
		{"shish-kebabed rl", "prey is shish-kebabed by killa's rocket", "killa", "prey", "rl", false, false},
		{"blown chunks rl", "prey was blown to chunks by killa's rocket", "killa", "prey", "rl", false, false},
		{"blown chunks gl", "prey was blown to chunks by killa's grenade", "killa", "prey", "gl", false, false},
		{"gets intimate gl", "prey gets intimate with killa's grenade", "killa", "prey", "gl", false, false},
		{"warm fuzzy lg", "prey gets a warm fuzzy feeling from killa", "killa", "prey", "lg", false, false},

		// CRMod SSG "eats 2 scoops of" must win over the generic " eats " (gl).
		{"eats scoops ssg", "prey eats 2 scoops of killa's lead shot", "killa", "prey", "ssg", false, false},
		{"eats pineapple gl", "prey eats killa's pineapple", "killa", "prey", "gl", false, false},

		// gibbed-by disambiguates on the weapon suffix.
		{"gibbed rocket", "prey was gibbed by killa's rocket", "killa", "prey", "rl", false, false},
		{"gibbed grenade", "prey was gibbed by killa's grenade", "killa", "prey", "gl", false, false},

		// Phrasing teamkills carry the generic "teammate" placeholder.
		{"tk killer-named", "killa loses another friend", "killa", "teammate", "teamkill", false, true},
		{"tk other-team frag", "killa gets a frag for the other team", "killa", "teammate", "teamkill", true, true},
		{"tk victim-named", "prey was telefragged by his teammate", "teammate", "prey", "teamkill", false, true},

		// Satan's-power self-telefrag (infix suicide).
		{"satan deflect", "Satan's power deflects nexus's telefrag", "nexus", "nexus", "tele", true, false},

		// SSG buckshot "ate N loads".
		{"ate buckshot ssg", "prey ate 2 loads of killa's buckshot", "killa", "prey", "ssg", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseObituaryLine(tc.msg)
			if o == nil {
				t.Fatalf("parseObituaryLine(%q) = nil, want a match", tc.msg)
			}
			if o.Killer != tc.killer || o.Victim != tc.victim || o.Weapon != tc.weapon ||
				o.Suicide != tc.sui || o.TeamKill != tc.tk {
				t.Errorf("parseObituaryLine(%q) = %+v, want killer=%q victim=%q weapon=%q suicide=%v teamkill=%v",
					tc.msg, o, tc.killer, tc.victim, tc.weapon, tc.sui, tc.tk)
			}
		})
	}
}

// TestMessagesObituaryDriftFixed proves the corrected codes now reach the
// MatchEvent timeline stream, not just the frag log — the point of the A2/F4
// unification (messages.go previously had its own drifted table).
func TestMessagesObituaryDriftFixed(t *testing.T) {
	obit := func(msg string) *events.PrintEvent {
		return &events.PrintEvent{Level: events.PrintMedium, Message: msg, Time: 1}
	}
	cases := []struct {
		msg    string
		player string
		victim string
		weapon string
	}{
		{"nexus sleeps with the fishes", "nexus", "nexus", "water"}, // was "drown"
		{"nexus somehow becomes bored with life", "nexus", "nexus", "suicide"},
		{"prey was blown to chunks by killa's grenade", "killa", "prey", "gl"}, // was unparsed
		{"nexus couldn't resist the shiny spawn point", "nexus", "nexus", "tele"},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			got := feedPrints(t, obit(tc.msg))
			if len(got) != 1 {
				t.Fatalf("feedPrints(%q) = %d events, want 1: %+v", tc.msg, len(got), got)
			}
			e := got[0]
			if e.Type != "frag" || e.Player != tc.player || e.Victim != tc.victim || e.Weapon != tc.weapon {
				t.Errorf("%q → %+v, want player=%q victim=%q weapon=%q", tc.msg, e, tc.player, tc.victim, tc.weapon)
			}
		})
	}
}

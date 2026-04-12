package analyzer

import "testing"

func TestExtractKillerName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"basic rocket", "alice's rocket", "alice"},
		{"basic shaft", "bob's shaft", "bob"},
		{"quad rocket beats rocket", "alice's quad rocket", "alice"},
		{"quad shaft beats shaft", "bob's quad shaft", "bob"},
		{"buckshot", "alice's buckshot", "alice"},
		{"discharge", "alice's discharge", "alice"},
		{"apostrophe fall (Cas's variant)", "Cas' fall", "Cas"},
		{"possessive fall", "Cas's fall", "Cas"},
		{"name ending in dot is preserved",
			".N3ophyt3.'s rocket", ".N3ophyt3."},
		{"name with dot, no weapon suffix",
			".N3ophyt3.\n", ".N3ophyt3."},
		{"rockets-from pattern",
			"3 rockets from alice", "alice"},
		{"trailing newline only",
			"alice\n", "alice"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractKillerName(tc.in)
			if got != tc.want {
				t.Errorf("extractKillerName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePlayerName(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"alice":              "alice",
		"Alice":              "alice",
		"bad.rotker":         "badrotker",
		"BadRotker":          "badrotker",
		".N3ophyt3.":         "n3ophyt3",
		"[ServeMe] Bot 7":    "servemebot7",
	}
	for in, want := range cases {
		if got := normalizePlayerName(in); got != want {
			t.Errorf("normalizePlayerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripQuadSuffix(t *testing.T) {
	cases := map[string]string{
		"alice's quad rocket": "alice",
		"alice's quad":        "alice",
		"alice":               "alice",
		"":                    "",
	}
	for in, want := range cases {
		if got := stripQuadSuffix(in); got != want {
			t.Errorf("stripQuadSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

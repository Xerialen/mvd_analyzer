package bsp

import "testing"

func TestParseEntitiesText_ThreeEntities(t *testing.T) {
	src := `{
"classname" "worldspawn"
"message" "Hello"
}
{
"classname" "item_armorInv"
"origin" "128 -64 16"
}
{
"classname" "item_health"
"origin" "256 0 32"
"spawnflags" "2"
}
`
	ents, err := ParseEntitiesText(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := len(ents); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}

	if ents[0].Classname != "worldspawn" {
		t.Errorf("ents[0].Classname = %q", ents[0].Classname)
	}
	if ents[0].RawKeys["message"] != "Hello" {
		t.Errorf("worldspawn.message = %q", ents[0].RawKeys["message"])
	}

	if ents[1].Classname != "item_armorInv" {
		t.Errorf("ents[1].Classname = %q", ents[1].Classname)
	}
	if ents[1].Origin != [3]float32{128, -64, 16} {
		t.Errorf("ra origin = %v", ents[1].Origin)
	}

	if ents[2].Spawnflags != 2 {
		t.Errorf("mh spawnflags = %d, want 2", ents[2].Spawnflags)
	}
	if ents[2].Origin != [3]float32{256, 0, 32} {
		t.Errorf("mh origin = %v", ents[2].Origin)
	}
}

// Reject garbage input without panicking.
func TestParseEntitiesText_Malformed(t *testing.T) {
	cases := []string{
		`{ "classname" "foo"`, // unterminated
		`{ "classname" }`,     // missing value
		`{{`,                  // bad nesting
	}
	for _, c := range cases {
		if _, err := ParseEntitiesText(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

// Comment lines in the entities block are rare but occur in some
// community maps — skip them rather than choke.
func TestParseEntitiesText_SkipsLineComments(t *testing.T) {
	src := `// a mapper comment
{
"classname" "worldspawn"
}
// trailing noise
`
	ents, err := ParseEntitiesText(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ents) != 1 || ents[0].Classname != "worldspawn" {
		t.Fatalf("ents = %+v", ents)
	}
}

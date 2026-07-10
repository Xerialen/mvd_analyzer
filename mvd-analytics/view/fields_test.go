package view

import (
	"strings"
	"testing"
)

// TestUnknownFieldErrorEnumerates: the error names every valid code so
// a typo'd agent self-corrects in one round trip (D6,
// PLAN-api-usability). The classic trap: passing the OUTPUT key `loc`
// as a selector — the selector code is `li`.
func TestUnknownFieldErrorEnumerates(t *testing.T) {
	err := validateFields([]string{"loc"})
	if err == nil {
		t.Fatal("'loc' must be rejected (the selector code is 'li')")
	}
	msg := err.Error()
	for _, code := range AllStandardFields {
		if !strings.Contains(msg, code+" (") {
			t.Errorf("error message missing code %q: %s", code, msg)
		}
	}
	if !strings.Contains(msg, "li (location)") {
		t.Errorf("error must gloss li as location: %s", msg)
	}
}

// TestFieldGlossesCoverFieldKinds pins the gloss list to the accepted
// vocabulary so a new field can't be added without teaching the error.
func TestFieldGlossesCoverFieldKinds(t *testing.T) {
	glossed := map[string]bool{}
	for _, fg := range fieldGlosses {
		if glossed[fg.code] {
			t.Errorf("duplicate gloss for %q", fg.code)
		}
		glossed[fg.code] = true
		if _, ok := fieldKinds[fg.code]; !ok {
			t.Errorf("gloss for unknown code %q", fg.code)
		}
	}
	for code := range fieldKinds {
		if !glossed[code] {
			t.Errorf("field %q has no gloss in unknownFieldError", code)
		}
	}
}

package analyzer

import "strings"

// killerSuffixes is the union of weapon-attribution suffixes that can appear
// after a killer name in a QuakeWorld obituary line. Quad variants must come
// before their non-quad equivalents so that "X's quad rocket" doesn't match
// "X's quad" + " rocket" by accident.
//
// This list is the single source of truth for both the FragAnalyzer
// (internal/analyzer/frag.go) and the MessagesAnalyzer
// (internal/analyzer/messages.go); historically each had its own slightly
// different copy.
var killerSuffixes = []string{
	// Quad variants (must come first)
	"'s quad shaft",
	"'s quad lightning",
	"'s quad rocket",
	"'s quad pineapple",
	"'s quad boomstick",
	"'s quad grenade",
	"'s quad axe",
	// Regular variants
	"'s shaft",
	"'s lightning",
	"'s rocket",
	"'s pineapple",
	"'s boomstick",
	"'s grenade",
	"'s axe",
	// Less common weapon variants used by the messages parser
	"'s buckshot",
	"'s discharge",
	"'s batteries",
	// Apostrophe-only forms (player names ending in 's', e.g. "Cas")
	"' fall",
	"'s fall",
	"' buckshot",
	"' rocket",
	"' grenade",
	"' discharge",
}

// quadOnlySuffixes are quad-specific tail strings used by stripQuadSuffix to
// peel quad annotation off names that have already been extracted by some
// other means (e.g. the "rockets from <name>" path in the frag parser).
var quadOnlySuffixes = []string{
	"'s quad rocket",
	"'s quad shaft",
	"'s quad lightning",
	"'s quad pineapple",
	"'s quad boomstick",
	"'s quad grenade",
	"'s quad axe",
	"'s quad",
}

// extractKillerName trims a known weapon suffix off the tail of an obituary
// fragment and returns the killer's display name.
//
// Note: we deliberately do NOT strip a trailing '.' as punctuation. QuakeWorld
// names can legitimately end in '.' — see the demo broken.mvd.gz, which has a
// player named ".N3ophyt3." after Q_normalizetext folding. Stripping the dot
// splits that player's frags off into a phantom name that the frontend can't
// reconcile.
func extractKillerName(rest string) string {
	for _, suffix := range killerSuffixes {
		if idx := strings.Index(rest, suffix); idx > 0 {
			return strings.TrimSpace(rest[:idx])
		}
	}

	// "<count> rockets from <killer>" pattern: rare splash-damage attribution.
	if idx := strings.Index(rest, " rockets from "); idx >= 0 {
		killer := strings.TrimSpace(rest[idx+len(" rockets from "):])
		return stripQuadSuffix(killer)
	}

	rest = strings.TrimSuffix(rest, "\n")
	return strings.TrimSpace(stripQuadSuffix(rest))
}

// stripQuadSuffix removes a trailing quad-annotation from a name that's
// already been extracted from somewhere upstream.
func stripQuadSuffix(name string) string {
	for _, suffix := range quadOnlySuffixes {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.TrimSpace(name)
}

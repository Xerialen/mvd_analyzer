package mapgeom

import "strings"

// itemKeywords mirrors the ITEM_KEYWORDS const in mvd-web/static/app.js:5534.
// Keep this list byte-for-byte identical to the JS side so Go-generated
// map geometry keys match the frontend's processLocationGroups keys.
var itemKeywords = map[string]bool{
	"RA": true, "YA": true, "GA": true, "MH": true,
	"RL": true, "LG": true, "GL": true,
	"NG": true, "SNG": true, "SSG": true, "SG": true,
	"MEGA": true, "QUAD": true, "PENT": true, "RING": true,
}

// NormalizeLocationName mirrors normalizeLocationName in mvd-web/static/app.js:5537.
//
// Steps:
//  1. Trim whitespace
//  2. Replace runs of whitespace/hyphens with a single "."
//  3. Split on "."
//  4. For each part: uppercase it; if the uppercase form is a known item
//     keyword, keep it uppercase; otherwise lowercase the original part
//  5. Join with "."
func NormalizeLocationName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	// Collapse whitespace and hyphens into a single "." separator.
	// JS: .replace(/[\s-]+/g, '.')
	var b strings.Builder
	b.Grow(len(name))
	inSep := false
	for _, r := range name {
		if isWhitespaceOrHyphen(r) {
			if !inSep {
				b.WriteByte('.')
				inSep = true
			}
			continue
		}
		inSep = false
		b.WriteRune(r)
	}
	collapsed := b.String()

	parts := strings.Split(collapsed, ".")
	for i, p := range parts {
		upper := strings.ToUpper(p)
		if itemKeywords[upper] {
			parts[i] = upper
		} else {
			parts[i] = strings.ToLower(p)
		}
	}
	return strings.Join(parts, ".")
}

// isWhitespaceOrHyphen matches the JS regex \s-. JS \s includes the
// usual whitespace characters (space, tab, newline, carriage return,
// form feed, vertical tab).
func isWhitespaceOrHyphen(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v', '-':
		return true
	}
	return false
}

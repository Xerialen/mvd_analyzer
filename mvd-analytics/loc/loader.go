package loc

import (
	"bufio"
	"path/filepath"
	"strconv"
	"strings"
)

// Variable substitutions for loc file parsing
// These match the ezQuake teamplay_locfiles.c definitions
// Keys are stored lowercase for case-insensitive matching
var locVariables = map[string]string{
	// Items and weapons
	"$loc_name_ra":   "RA",
	"$loc_name_ya":   "YA",
	"$loc_name_ga":   "GA",
	"$loc_name_mh":   "MH",
	"$loc_name_quad": "Quad",
	"$loc_name_pent": "Pent",
	"$loc_name_ring": "Ring",
	"$loc_name_suit": "Suit",
	"$loc_name_gl":   "GL",
	"$loc_name_rl":   "RL",
	"$loc_name_lg":   "LG",
	"$loc_name_ssg":  "SSG",
	"$loc_name_ng":   "NG",
	"$loc_name_sng":  "SNG",
	// Separator
	"$loc_name_separator": ".",
}

// locSeparatorSuffixes lists the known suffixes that can follow $loc_name_separator.
// We expand "$loc_name_separatorXXX" → " XXX" for any suffix found here.
// This is built from all suffixes observed in loc files from maps.quakeworld.nu.
var locSeparatorSuffixes = []string{
	"above", "air", "ammo", "back", "backside", "balcony", "balks", "bars",
	"beam", "beams", "behind", "below", "beside", "big", "bio", "block",
	"blue", "box", "boxes", "bridge", "button", "cellar", "cells", "column",
	"columns", "corridor", "crates", "cross", "crusher", "deathtrap", "door",
	"easy", "end", "entry", "exit", "floor", "fountain", "ga", "gate", "gl",
	"hard", "hatch", "health", "hide", "high", "hole", "jesus", "jetty",
	"ladder", "lava", "ledge", "lg", "lift", "lifts", "low", "main",
	"manhole", "mega", "mh", "mid", "middle", "ng", "normal", "outer",
	"outside", "pad", "path", "pent", "pillar", "pipe", "pipes", "pit",
	"plat", "plates", "platform", "pool", "portal", "quad", "ra", "ramp",
	"ring", "rl", "roof", "room", "rox", "secret", "sg", "shells", "slime",
	"sng", "space", "spikes", "spiral", "square", "ssg", "stairs", "stand",
	"start", "steps", "suit", "surf", "tele", "teleport", "teles", "tonnel",
	"top", "tower", "trap", "trick", "tunnel", "up", "vent", "walk", "wall",
	"water", "way", "well", "window", "ya", "yard",
}

// mapAliases maps variant map basenames onto a canonical loc-file basename.
// Used by callers that want to resolve a demo's map name to the loc file.
// Intentionally empty: every map resolves to its own corpus. In
// particular the phantom version family (phantom / phantoma / phantombase)
// is kept separate — they are distinct map versions with their own loc,
// entity and geometry data, not aliases of one another.
var mapAliases = map[string]string{}

// NormalizeMapName extracts the lowercased base name (no path or extension)
// from a map identifier and applies known aliases. The resulting string is
// the basename of the .loc file (without the ".loc" suffix).
func NormalizeMapName(mapName string) string {
	base := filepath.Base(mapName)
	base = strings.TrimSuffix(base, ".bsp")
	base = strings.ToLower(base)
	if alias, ok := mapAliases[base]; ok {
		base = alias
	}
	return base
}

// buildFinder parses raw loc-file bytes into a Finder for the given basename.
// Shared between the native and WASM LoadForMap implementations.
func buildFinder(baseName string, data []byte) (*Finder, error) {
	locations, err := parseLoc(data)
	if err != nil {
		return nil, err
	}
	return &Finder{mapName: baseName, locations: locations}, nil
}

func parseLoc(data []byte) ([]Location, error) {
	var locations []Location
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		// Format: X Y Z location_name
		// Find the first three numbers, rest is the name
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue // Skip malformed lines
		}

		x, err := strconv.ParseFloat(parts[0], 32)
		if err != nil {
			continue
		}
		y, err := strconv.ParseFloat(parts[1], 32)
		if err != nil {
			continue
		}
		z, err := strconv.ParseFloat(parts[2], 32)
		if err != nil {
			continue
		}

		// Rejoin remaining parts as the name (handles names with spaces)
		name := strings.Join(parts[3:], " ")
		name = substituteVariables(name)

		// Loc file coordinates are stored as integers * 8
		// Divide by 8 to get world coordinates
		locations = append(locations, Location{
			X:    float32(x / 8.0),
			Y:    float32(y / 8.0),
			Z:    float32(z / 8.0),
			Name: name,
		})
	}

	return locations, scanner.Err()
}

func substituteVariables(s string) string {
	// Strip Quake special bytes before variable substitution:
	// - Control chars (0x00-0x1f except \t): color toggles, text formatting
	// - 0x80-0xFF: high-byte "gold/colored" characters (strip bit 7)
	var cleaned []byte
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 && b != '\t' {
			continue // strip control characters
		}
		if b >= 0x80 {
			b &= 0x7F // convert to ASCII equivalent
			if b < 0x20 {
				continue // strip if result is a control char
			}
		}
		cleaned = append(cleaned, b)
	}
	result := string(cleaned)

	// Replace "$." separator shorthand with ".". Many community loc files
	// use "$." as a compact separator between tokens (e.g., "rl$.low" → "rl.low").
	result = strings.ReplaceAll(result, "$.", ".")

	// Strip ezQuake team color macros: $RR, $BB, $R, $B, $G, $Y, $W
	// (longer patterns first to avoid partial matches)
	for _, macro := range []string{"$RR", "$BB", "$GG", "$YY", "$WW", "$R", "$B", "$G", "$Y", "$W"} {
		result = strings.ReplaceAll(result, macro, "")
	}

	// Strip remaining standalone "$" before digits (e.g., "$4" area labels)
	for i := 0; i < 10; i++ {
		result = strings.ReplaceAll(result, "$"+string(rune('0'+i)), string(rune('0'+i)))
	}

	// Fix broken loc files (e.g., an1-beta3.loc) where an authoring tool
	// recursively expanded $loc_name_ra inside $loc_name_separator, producing
	// fragments like $loc_name_sepa$loc_name_rato$loc_name_rabove instead of
	// $loc_name_separatorabove. Reassemble these before variable substitution.
	result = strings.ReplaceAll(result, "$loc_name_rato$loc_name_ra", "$loc_name_ratora")
	result = strings.ReplaceAll(result, "$loc_name_sepa$loc_name_rator", "$loc_name_separator")

	// Case-insensitive variable substitution.
	// We work on a lowercase copy to find matches, then splice replacements
	// into the original-case result.
	lower := strings.ToLower(result)

	// Handle separator+suffix patterns first (longest match)
	const sepPrefix = "$loc_name_separator"
	for {
		idx := strings.Index(lower, sepPrefix)
		if idx == -1 {
			break
		}
		rest := lower[idx+len(sepPrefix):]
		matched := false
		for _, suffix := range locSeparatorSuffixes {
			if strings.HasPrefix(rest, suffix) {
				replacement := "." + suffix
				result = result[:idx] + replacement + result[idx+len(sepPrefix)+len(suffix):]
				lower = strings.ToLower(result)
				matched = true
				break
			}
		}
		if !matched {
			// Plain $loc_name_separator (no known suffix) or unknown suffix
			// Check if it matches the base separator
			if idx+len(sepPrefix) <= len(result) {
				// Check for base "$loc_name_separator" without a known suffix
				// but could be followed by unknown text - just expand the base separator
				result = result[:idx] + "." + result[idx+len(sepPrefix):]
				lower = strings.ToLower(result)
			}
		}
	}

	// Handle remaining $loc_name_* variables
	for varName, replacement := range locVariables {
		if varName == "$loc_name_separator" {
			continue // already handled above
		}
		for {
			idx := strings.Index(lower, varName)
			if idx == -1 {
				break
			}
			result = result[:idx] + replacement + result[idx+len(varName):]
			lower = strings.ToLower(result)
		}
	}

	// Clean up multiple spaces and trim
	result = strings.Join(strings.Fields(result), " ")

	// Capitalize known weapon/armor/item abbreviations when they appear as
	// standalone tokens (delimited by ".", " ", or string boundaries).
	result = capitalizeItems(result)

	return result
}

// itemAbbreviations lists weapon, armor, and item short names that should be
// uppercased when they appear as standalone tokens in location names.
var itemAbbreviations = map[string]string{
	"rl": "RL", "lg": "LG", "gl": "GL", "ssg": "SSG", "ng": "NG", "sng": "SNG",
	"ra": "RA", "ya": "YA", "ga": "GA", "mh": "MH", "mega": "MEGA",
	"quad": "Quad", "pent": "Pent", "ring": "Ring", "suit": "Suit",
}

func capitalizeItems(s string) string {
	// Split on "." first, process each segment, rejoin
	dots := strings.Split(s, ".")
	for i, seg := range dots {
		// Split each dot-segment on spaces, capitalize tokens
		words := strings.Split(seg, " ")
		for j, w := range words {
			if upper, ok := itemAbbreviations[strings.ToLower(w)]; ok {
				words[j] = upper
			}
		}
		dots[i] = strings.Join(words, " ")
	}
	return strings.Join(dots, ".")
}

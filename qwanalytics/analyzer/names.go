package analyzer

import (
	"strings"

	"github.com/mvd-analyzer/qwdemo/events"
)

// normalizePlayerName lowercases and strips non-alphanumeric characters from a
// QuakeWorld display name so that variants like "bad.rotker", "BadRotker" and
// "badrotker" all collapse to the same key.
//
// This is used wherever an MVD-stream name (which still has Q_normalizetext
// folding, color codes, dots, brackets, etc.) needs to be matched against
// another source of truth — most importantly the demoinfo JSON.
func normalizePlayerName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// findPlayerByName looks up a live player by display name using a 3-pass
// match: exact, normalized, then substring (in either direction). Returns
// nil if no candidate matches.
//
// Substring matching exists because some servers fold or rename players in
// ways that drop characters from the displayed name relative to the userinfo
// name; the substring pass is the last-resort fuzzy fallback.
func findPlayerByName(players [events.MaxClients]*events.PlayerInfo, name string) *events.PlayerInfo {
	for i := 0; i < len(players); i++ {
		p := players[i]
		if p != nil && p.Name == name {
			return p
		}
	}
	norm := normalizePlayerName(name)
	if norm == "" {
		return nil
	}
	for i := 0; i < len(players); i++ {
		p := players[i]
		if p != nil && normalizePlayerName(p.Name) == norm {
			return p
		}
	}
	for i := 0; i < len(players); i++ {
		p := players[i]
		if p == nil {
			continue
		}
		pNorm := normalizePlayerName(p.Name)
		if pNorm == "" {
			continue
		}
		if strings.Contains(pNorm, norm) || strings.Contains(norm, pNorm) {
			return p
		}
	}
	return nil
}

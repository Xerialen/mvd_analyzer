package parser

import (
	"strconv"
	"strings"

	"github.com/mvd-analyzer/qwdemo/mvd"
)

// UserInfoEvent is emitted when player info is updated
type UserInfoEvent struct {
	Player *mvd.PlayerInfo
	Time   float64
}

func (e *UserInfoEvent) EventType() EventType { return EventUserInfo }
func (e *UserInfoEvent) EventTime() float64   { return e.Time }

// parseUserInfo parses svc_updateuserinfo message
func (p *Parser) parseUserInfo(r *mvd.BufferReader, time float64) error {
	// Read player slot
	slot, err := r.ReadByte()
	if err != nil {
		return err
	}

	if slot >= mvd.MaxClients {
		// Invalid slot, skip
		r.ReadUint32() // user_id
		r.ReadString() // userinfo
		return nil
	}

	// Read user ID
	userID, err := r.ReadUint32()
	if err != nil {
		return err
	}

	// Read userinfo string
	userinfo, err := r.ReadString()
	if err != nil {
		return err
	}

	// Parse userinfo string
	player := p.players[slot]
	if player == nil {
		player = &mvd.PlayerInfo{Slot: int(slot)}
		p.players[slot] = player
	}

	player.UserID = int(userID)
	parseUserInfoString(userinfo, player)

	// Emit event
	return p.emit(&UserInfoEvent{Player: player, Time: time})
}

// parseSetInfo parses svc_setinfo (single key/value update for a player).
// This is how name/team/skin changes are propagated mid-game; without
// handling it the parser keeps the initial userinfo and chat / timeline
// data fall out of sync with the player's current name.
func (p *Parser) parseSetInfo(r *mvd.BufferReader, time float64) error {
	slot, err := r.ReadByte()
	if err != nil {
		return err
	}
	key, err := r.ReadString()
	if err != nil {
		return err
	}
	value, err := r.ReadString()
	if err != nil {
		return err
	}

	if slot >= mvd.MaxClients {
		return nil
	}

	player := p.players[slot]
	if player == nil {
		player = &mvd.PlayerInfo{Slot: int(slot)}
		p.players[slot] = player
	}

	switch key {
	case "name":
		player.Name = cleanString(value)
	case "team":
		player.Team = cleanString(value)
	case "topcolor":
		if c, err := strconv.Atoi(value); err == nil {
			player.TopColor = c
		}
	case "bottomcolor":
		if c, err := strconv.Atoi(value); err == nil {
			player.BottomColor = c
		}
	case "*auth":
		player.Auth = cleanString(value)
	case "*spectator":
		player.Spectator = value == "1"
	default:
		// Other keys (rate, msg, skin, ...) are not tracked.
		return nil
	}

	return p.emit(&UserInfoEvent{Player: player, Time: time})
}

// parseUserInfoString parses a backslash-delimited userinfo string
// Format: \key1\value1\key2\value2\...
func parseUserInfoString(s string, player *mvd.PlayerInfo) {
	if s == "" {
		return
	}

	// Remove leading backslash if present
	if s[0] == '\\' {
		s = s[1:]
	}

	parts := strings.Split(s, "\\")
	for i := 0; i+1 < len(parts); i += 2 {
		key := parts[i]
		value := parts[i+1]

		switch key {
		case "name":
			player.Name = cleanString(value)
		case "team":
			player.Team = cleanString(value)
		case "topcolor":
			if c, err := strconv.Atoi(value); err == nil {
				player.TopColor = c
			}
		case "bottomcolor":
			if c, err := strconv.Atoi(value); err == nil {
				player.BottomColor = c
			}
		case "*auth":
			player.Auth = cleanString(value)
		case "spectator":
			player.Spectator = value == "1"
		}
	}
}

// qNormalizeTable is the Quake character normalization table used by
// ezquake/mvdsv `Q_normalizetext`. It maps every byte (0-255) to a printable
// ASCII equivalent: high-bit "gold" letters fold back to their plain twins,
// font glyphs in 0x00-0x1F become digits/brackets/dots, and unknown control
// bytes become '#'. Centralizing this table here means every consumer of
// player names (userinfo, setinfo, prints, demoinfo JSON) ends up with the
// same canonical string, so cross-references by name actually join.
var qNormalizeTable = func() [256]byte {
	var t [256]byte
	for i := 0; i < 256; i++ {
		t[i] = '#'
	}
	// Printable low ASCII passes through unchanged.
	for i := 32; i < 127; i++ {
		t[i] = byte(i)
	}
	// Quake font glyphs in 0x00-0x1F.
	t[0] = '#'
	t[5] = '.'
	t[10] = '\n'
	t[13] = '\r'
	t[14] = '.'
	t[15] = '.'
	t[16] = '['
	t[17] = ']'
	for i := 18; i <= 27; i++ {
		t[i] = byte('0' + (i - 18))
	}
	t[28] = '.'
	t[29] = '('
	t[30] = '='
	t[31] = ')'
	t[46] = '.' // already '.', but kept explicit
	t[127] = '>'
	// Mirror everything for the high-bit "gold" range: byte b+128 maps the
	// same way as b. This is what folds 0xCE -> 'N', 0xF0 -> 'p', 0xAE -> '.'.
	for i := 0; i < 128; i++ {
		t[i+128] = t[i]
	}
	// A handful of high-bit specific overrides from ezquake's table.
	t[128] = '('
	t[129] = '='
	t[130] = ')'
	t[141] = '<'
	return t
}()

// NormalizeQuakeText is the exported version that takes raw bytes. Used by
// the analyzer package to normalize names lifted from KTX's demoinfo JSON
// (where each byte arrived as a JSON \u00XX escape).
func NormalizeQuakeText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			continue
		}
		out = append(out, qNormalizeTable[c])
	}
	return string(out)
}

// cleanString normalizes a Quake string to plain ASCII using the
// ezquake/mvdsv Q_normalizetext mapping. Embedded NULs are dropped because
// they would terminate downstream C strings; everything else is mapped via
// the table above.
func cleanString(s string) string {
	return NormalizeQuakeText([]byte(s))
}

// StripChatMarkup removes ezQuake chat markup that survives
// Q-normalisation, leaving plain readable text. Mirrors qw-web's
// formatQuakeMessage (in static/app.js) minus the HTML span generation.
//
// Removed in order:
//
//   - leading "\r" (mvdsv prepends this when broadcasting team chat),
//   - "&cRGB" colour codes (3 hex digits) and "&r" reset,
//   - trailing single-letter sound triggers ("!K"/"!H"/"!G"/"!C", etc.),
//   - macro delimiters "{", "}", "[", "]" (ezQuake teamplay macros).
//
// Whitespace runs are then collapsed to a single space and the result
// is trimmed. The transform is idempotent — re-running on already-clean
// text is a no-op.
func StripChatMarkup(s string) string {
	if s == "" {
		return s
	}
	// 1. Drop a leading "\r" if present.
	if s[0] == '\r' {
		s = s[1:]
	}
	// 2. Drop a trailing single-letter sound trigger like "!K" / "!H".
	if n := len(s); n >= 2 && s[n-2] == '!' {
		c := s[n-1]
		if c >= 'A' && c <= 'Z' {
			s = s[:n-2]
		}
	}
	// 3. Walk the string, dropping &cRGB / &r and macro delimiters.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		// "&cRGB" — three hex digits after &c.
		if i+5 <= len(s) && s[i] == '&' && s[i+1] == 'c' &&
			isHexDigit(s[i+2]) && isHexDigit(s[i+3]) && isHexDigit(s[i+4]) {
			i += 5
			continue
		}
		// "&r" — colour reset.
		if i+2 <= len(s) && s[i] == '&' && s[i+1] == 'r' {
			i += 2
			continue
		}
		// Macro delimiters.
		c := s[i]
		if c == '{' || c == '}' || c == '[' || c == ']' {
			i++
			continue
		}
		out = append(out, c)
		i++
	}
	// 4. Collapse whitespace runs and trim.
	collapsed := make([]byte, 0, len(out))
	prevSpace := true // leading-space trim
	for _, c := range out {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				collapsed = append(collapsed, ' ')
				prevSpace = true
			}
			continue
		}
		collapsed = append(collapsed, c)
		prevSpace = false
	}
	// trim trailing space
	if len(collapsed) > 0 && collapsed[len(collapsed)-1] == ' ' {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return string(collapsed)
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

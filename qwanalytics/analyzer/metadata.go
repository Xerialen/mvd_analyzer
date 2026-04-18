package analyzer

import (
	"strconv"
	"strings"

	"github.com/mvd-analyzer/qwdemo/events"
)

// MetadataAnalyzer collects server-level and match-level metadata that
// arrives via non-payload protocol commands rather than via stat updates.
//
// Three sources feed it:
//
//  1. svc_stufftext at connection time — the server sends a single
//     `fullserverinfo "\key\value\…"` console command containing every
//     CVAR_SERVERINFO cvar (mvdsv side: maxfps, fraglimit, timelimit,
//     teamplay, maxclients, deathmatch, hostname, *version, *z_ext, *admin,
//     *gamedir, *qvm, *progs, map, status, serverdemo, epoch, …) plus any
//     KTX-side keys mirrored via `localcmd "serverinfo …"` (mode, ktxver,
//     fpd, matchtag, status). We split this into ServerInfo[k]=v.
//
//  2. svc_serverinfo (cmd 52) — single-key updates emitted later in the
//     demo when a value changes (`status` cycles through Countdown / "3 min
//     left" / "Standby" / "Forcestart"; `fpd` toggles when admins flip
//     `fpd add` / `fpd del`; etc). Last-write-wins.
//
//  3. svc_centerprint (cmd 26) — KTX renders the full match-settings
//     table here every second of the 10-second countdown (match.c
//     PrintCountdown). The last centerprint we see before the
//     "match has begun!" print is the canonical match settings dump:
//     Mode / Deathmatch / Spawnmodel / Antilag / Teamplay / Timelimit /
//     Fraglimit / Overtime / Powerups / Dmgfrags / NoItems / Midair /
//     Instagib / Yawnmode / Airstep / VWep / Noweapon / matchtag.
//
// We do not try to interpret //ktx-style stufftexts (`//ktx matchstart`,
// `//wps 0 lg 31 17`, `//ktx drop 49 64 3`) — those are client HUD hints,
// not server metadata.
type MetadataAnalyzer struct {
	serverInfo    map[string]string
	countdownRaw  string // last centerprint that contained "Countdown:" (post-Q_normalizetext)
	matchStarted  bool   // gates which countdown sample is the canonical one
}

// NewMetadataAnalyzer creates a metadata analyzer.
func NewMetadataAnalyzer() *MetadataAnalyzer {
	return &MetadataAnalyzer{
		serverInfo: make(map[string]string),
	}
}

func (a *MetadataAnalyzer) Name() string { return "metadata" }

func (a *MetadataAnalyzer) Init(ctx *Context) error { return nil }

func (a *MetadataAnalyzer) OnEvent(event events.Event) error {
	switch e := event.(type) {
	case *events.StuffTextEvent:
		// The bulk cvar dump is the very first stufftext: `fullserverinfo "..."`.
		if cmd := e.Command; strings.HasPrefix(cmd, "fullserverinfo ") {
			a.parseFullserverinfo(cmd)
		}
	case *events.ServerInfoEvent:
		// Mid-game key/value updates — last write wins.
		if e.Key != "" {
			a.serverInfo[e.Key] = e.Value
		}
	case *events.CenterPrintEvent:
		// The KTX countdown centerprint is the only multi-line centerprint
		// during the pre-match window that contains "Countdown:". We only
		// want the last one we saw before the match started, because the
		// final 1-second-remaining centerprint contains the same fields as
		// the rest and is the cleanest sample.
		if a.matchStarted {
			return nil
		}
		text := events.NormalizeQuakeText([]byte(e.Message))
		if strings.Contains(text, "Countdown:") {
			a.countdownRaw = text
		}
	case *events.PrintEvent:
		// Latch the match start so we stop overwriting countdownRaw with
		// any post-match centerprint that happens to mention "Countdown".
		if !a.matchStarted && strings.Contains(e.Message, "match has begun") {
			a.matchStarted = true
		}
	}
	return nil
}

// parseFullserverinfo extracts the quoted cvar string from a stufftext like
// `fullserverinfo "\maxfps\77\timelimit\10\..."` and splits it into key/value
// pairs that get merged into MetadataAnalyzer.serverInfo.
func (a *MetadataAnalyzer) parseFullserverinfo(cmd string) {
	rest := strings.TrimPrefix(cmd, "fullserverinfo ")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "\"")
	if i := strings.LastIndexByte(rest, '"'); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "\\")
	start := 0
	if len(parts) > 0 && parts[0] == "" {
		start = 1
	}
	for i := start; i+1 < len(parts); i += 2 {
		k := parts[i]
		v := parts[i+1]
		if k == "" {
			continue
		}
		a.serverInfo[k] = v
	}
}

// Finalize converts the collected serverinfo + countdown text into a
// structured MetadataResult.
func (a *MetadataAnalyzer) Finalize() (interface{}, error) {
	result := &MetadataResult{}

	if len(a.serverInfo) > 0 {
		// Copy so the analyzer's internal map can't be mutated by callers.
		serverInfo := make(map[string]string, len(a.serverInfo))
		for k, v := range a.serverInfo {
			serverInfo[k] = v
		}
		result.ServerInfo = serverInfo
	}

	if a.countdownRaw != "" {
		result.CountdownText = a.countdownRaw
		result.MatchSettings = parseCountdownCenterprint(a.countdownRaw)
	}

	if result.ServerInfo == nil && result.MatchSettings == nil && result.CountdownText == "" {
		return nil, nil
	}
	return result, nil
}

// parseCountdownCenterprint walks the post-Q_normalizetext countdown table
// and pulls each known KTX setting row into a MatchSettings struct.
//
// The format is one setting per line, key on the left and value
// right-aligned. KTX uses fmt strings like `va("%s %4s\n", "Respawns", ...)`,
// so after normalization we get rows like:
//
//	"Deathmatch  3"
//	"Mode  D u e l"
//	"Respawns  KT2"
//	"Antilag    1"
//	"Teamplay   2"
//	"Timelimit  10"
//	"Overtime    3"
//	"Powerups  on"
//	"Noweapon   gl axe"
//	"matchtag draft"
//
// We split by line, take the first whitespace-separated token as the key,
// and treat the remainder as the value.
func parseCountdownCenterprint(text string) *MatchSettings {
	settings := &MatchSettings{}
	any := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "Countdown:") || strings.HasPrefix(line, "no matchtag") {
			continue
		}
		key, value := splitCountdownLine(line)
		if key == "" {
			continue
		}
		if applyCountdownField(settings, key, value) {
			any = true
		}
	}
	if !any {
		return nil
	}
	return settings
}

// splitCountdownLine splits a centerprint row into (key, value). KTX uses
// padded right-aligned values, so we treat the first whitespace run as the
// separator and keep everything after it (collapsing internal runs of spaces
// for cosmetic mode names like "D u e l" → "Duel").
func splitCountdownLine(line string) (string, string) {
	idx := strings.IndexFunc(line, isSpaceByte)
	if idx < 0 {
		return line, ""
	}
	key := line[:idx]
	rest := strings.TrimSpace(line[idx:])
	rest = collapseSpaces(rest)
	return key, rest
}

func isSpaceByte(r rune) bool { return r == ' ' || r == '\t' }

func collapseSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' {
			if !prevSpace {
				b.WriteRune(r)
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// applyCountdownField sets one row of the MatchSettings struct. Returns
// true if the field was recognised so the caller can tell whether the
// centerprint produced any structured data at all.
func applyCountdownField(s *MatchSettings, key, value string) bool {
	// Mode rendering uses "D u e l", "T e a m", "F F A", etc — strip spaces.
	flat := strings.ReplaceAll(value, " ", "")

	switch key {
	case "Mode":
		s.Mode = flat
	case "Deathmatch":
		s.Deathmatch = atoiSafe(value)
	case "Teamplay":
		s.Teamplay = atoiSafe(value)
	case "Timelimit":
		s.Timelimit = atoiSafe(value)
	case "Fraglimit":
		s.Fraglimit = atoiSafe(value)
	case "Respawns":
		s.Spawnmodel = flat
		if k := spawnmodelToK(flat); k >= 0 {
			s.SpawnK = &k
		}
	case "Antilag":
		s.Antilag = atoiSafe(value)
	case "Overtime":
		s.Overtime = flat
	case "Powerups":
		s.Powerups = flat
	case "Dmgfrags":
		s.Dmgfrags = isOn(flat)
	case "NoItems":
		s.NoItems = isOn(flat)
	case "Midair":
		s.Midair = isOn(flat)
	case "Instagib":
		s.Instagib = isOn(flat)
	case "Yawnmode":
		s.Yawnmode = isOn(flat)
	case "Airstep":
		s.Airstep = isOn(flat)
	case "VWep":
		s.VWep = isOn(flat)
	case "Noweapon":
		s.Noweapon = value
	case "matchtag":
		s.Matchtag = value
	case "SOCDv2":
		s.SOCDv2 = flat
	default:
		return false
	}
	return true
}

func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func isOn(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "on" || v == "1" || v == "yes" || v == "true"
}

// spawnmodelToK reverses respawn_model_name_short(): see
// ktx/src/g_utils.c:2689. Returns -1 for unknown short names.
func spawnmodelToK(name string) int {
	switch strings.ToUpper(name) {
	case "QW":
		return 0
	case "KTS":
		return 1
	case "KT":
		return 2
	case "KTX":
		return 3
	case "KT2":
		return 4
	}
	return -1
}

package parser

import "fmt"

// Warning represents a diagnostic issue found during parsing.
type Warning struct {
	Time    float64 // Demo time when the warning occurred
	Type    string  // Category: "parse_error", "unknown_svc", "unknown_te", "unknown_hidden"
	Message string  // Human-readable description
}

func (w Warning) String() string {
	return fmt.Sprintf("[%.1fs] %s: %s", w.Time, w.Type, w.Message)
}

// SetDiagnosticMode enables warning collection instead of silent error dropping.
// Production code should never call this.
func (p *Parser) SetDiagnosticMode(enabled bool) {
	p.diagnosticMode = enabled
}

// DiagnosticWarnings returns all warnings collected during parsing (only in diagnostic mode).
func (p *Parser) DiagnosticWarnings() []Warning {
	return p.warnings
}

// warn records a diagnostic warning. In non-diagnostic mode this is a no-op.
func (p *Parser) warn(time float64, typ, format string, args ...interface{}) {
	if !p.diagnosticMode {
		return
	}
	p.warnings = append(p.warnings, Warning{
		Time:    time,
		Type:    typ,
		Message: fmt.Sprintf(format, args...),
	})
}

// svcName returns a human-readable name for an svc_* command byte.
var svcNames = map[byte]string{
	0: "svc_bad", 1: "svc_nop", 2: "svc_disconnect", 3: "svc_updatestat",
	4: "svc_version", 5: "svc_setview", 6: "svc_sound", 7: "svc_time",
	8: "svc_print", 9: "svc_stufftext", 10: "svc_setangle",
	11: "svc_serverdata", 12: "svc_lightstyle", 13: "svc_updatename",
	14: "svc_updatefrags", 15: "svc_clientdata", 16: "svc_stopsound",
	17: "svc_updatecolors", 18: "svc_particle", 19: "svc_damage",
	20: "svc_spawnstatic", 21: "svc_fte_spawnstatic2",
	22: "svc_spawnbaseline", 23: "svc_temp_entity", 24: "svc_setpause",
	25: "svc_signonnum", 26: "svc_centerprint", 27: "svc_killedmonster",
	28: "svc_foundsecret", 29: "svc_spawnstaticsound", 30: "svc_intermission",
	31: "svc_finale", 32: "svc_cdtrack", 33: "svc_sellscreen",
	34: "svc_smallkick", 35: "svc_bigkick", 36: "svc_updateping",
	37: "svc_updateentertime", 38: "svc_updatestatlong", 39: "svc_muzzleflash",
	40: "svc_updateuserinfo", 41: "svc_download", 42: "svc_playerinfo",
	43: "svc_nails", 44: "svc_chokecount", 45: "svc_modellist",
	46: "svc_soundlist", 47: "svc_packetentities", 48: "svc_deltapacketentities",
	49: "svc_maxspeed", 50: "svc_entgravity", 51: "svc_setinfo",
	52: "svc_serverinfo", 53: "svc_updatepl", 54: "svc_nails2",
	60: "svc_fte_modellistshort", 66: "svc_fte_spawnbaseline2",
}

// SvcName returns a human-readable name for an svc command byte.
func SvcName(cmd byte) string {
	if name, ok := svcNames[cmd]; ok {
		return name
	}
	return fmt.Sprintf("svc_unknown_%d", cmd)
}

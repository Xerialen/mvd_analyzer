// Package decisions builds the Result.Decisions section (schema v57): the
// tactical-decision layer. Two producers share the DecisionRecord shape:
//
//   - AttachKDLog: joins a Komodobot KDLOG server-log sidecar (structured
//     G_cprint telemetry from the kbot-0.23.0-dlog+ KTX build) against an
//     analyzed demo. The log carries raw server identities (edicts,
//     classnames, origins, STAT_ITEMS bits); this resolver translates them
//     into the analyzer's canonical vocabulary — item kinds/names via the
//     ItemTimeline join, locs via the player's own PVS-attributed position
//     stream, players via the PlayerSlots map — so decisions speak exactly
//     the same language as every other Result section.
//
//   - AttachInferred (infer.go): reverse-engineers pickup-anchored goal
//     decisions from the demo alone, for demos (human play) with no log.
//
// Parse and resolve problems are collected into Decisions.Errors; a broken
// line never fails the analysis.
package decisions

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// classnameKind maps KTX server classnames to the canonical item-kind
// vocabulary (mvd-reader parser/entities.go). Used as a fallback/sanity
// check; the authoritative kind comes from the ItemTimeline join.
var classnameKind = map[string]string{
	"item_armor1":                   "ga",
	"item_armor2":                   "ya",
	"item_armorInv":                 "ra",
	"item_health":                   "", // spawnflags decide mh/h25/h15 — join decides
	"item_artifact_super_damage":    "quad",
	"item_artifact_invulnerability": "pent",
	"item_artifact_invisibility":    "ring",
	"item_artifact_envirosuit":      "suit",
	"weapon_rocketlauncher":         "rl",
	"weapon_lightning":              "lg",
	"weapon_grenadelauncher":        "gl",
	"weapon_supershotgun":           "ssg",
	"weapon_supernailgun":           "sng",
	"weapon_nailgun":                "ng",
	"item_shells":                   "shells",
	"item_spikes":                   "nails",
	"item_rockets":                  "rockets",
	"item_cells":                    "cells",
	"item_weapon":                   "", // ammo/weapon multi-spawner — join decides
	"backpack":                      "backpack",
	"item_backpack":                 "backpack",
	"player":                        "player",
}

// itBitWeapon maps a STAT_ACTIVEWEAPON IT_ bit to the analyzer weapon string.
func itBitWeapon(bit int) string {
	switch bit {
	case mvd.ITAxe:
		return "axe"
	case mvd.ITShotgun:
		return "sg"
	case mvd.ITSuperShotgun:
		return "ssg"
	case mvd.ITNailgun:
		return "ng"
	case mvd.ITSuperNailgun:
		return "sng"
	case mvd.ITGrenadeLauncher:
		return "gl"
	case mvd.ITRocketLauncher:
		return "rl"
	case mvd.ITLightning, mvd.ITSuperLightning:
		return "lg"
	}
	return ""
}

// AttachKDLog parses the KDLOG lines in the file at logPath and attaches the
// resolved Decisions section to res. Non-KDLOG lines are ignored, so the raw
// mvdsv server.log can be passed as-is.
func AttachKDLog(res *result.Result, logPath string) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("decision log: %w", err)
	}
	defer f.Close()
	dec, err := ResolveKDLog(res, f)
	if err != nil {
		return err
	}
	res.Decisions = dec
	return nil
}

// ResolveKDLog reads KDLOG lines from r and resolves them against res.
func ResolveKDLog(res *result.Result, r io.Reader) (*result.Decisions, error) {
	dec := &result.Decisions{Source: "kdlog"}
	rx := newResolver(res, dec)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := kdlogPayload(scanner.Text())
		if strings.HasPrefix(line, "KDLOG_ANCHOR ") {
			rx.anchor(line)
			continue
		}
		if strings.HasPrefix(line, "KDLOG ") {
			if rec, ok := rx.record(line, lineNo); ok {
				dec.Records = append(dec.Records, rec)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("decision log: %w", err)
	}
	sort.SliceStable(dec.Records, func(i, j int) bool { return dec.Records[i].T < dec.Records[j].T })
	return dec, nil
}

// kdlogPayload accepts a telemetry token only at the start of the logical
// console payload. mvdsv/KTX may prepend one or more bracketed timestamp/source
// fields; ordinary prose containing "non-KDLOG" must remain ignored.
func kdlogPayload(line string) string {
	line = strings.TrimSpace(line)
	for strings.HasPrefix(line, "[") {
		i := strings.IndexByte(line, ']')
		if i < 0 {
			return ""
		}
		line = strings.TrimSpace(line[i+1:])
	}
	return line
}

// resolver holds the per-demo lookup tables for the KDLOG -> vocabulary join.
type resolver struct {
	dec      *result.Decisions
	slotName map[int]string    // demo slot -> canonical stream name
	slotTeam map[string]string // name -> team
	streams  map[string]*result.PlayerStream
	locTable []string
	itemsEnt map[int]*result.ItemTimeline // server entNum -> item
	items    []*result.ItemTimeline
}

func newResolver(res *result.Result, dec *result.Decisions) *resolver {
	rx := &resolver{dec: dec, slotName: map[int]string{}, slotTeam: map[string]string{},
		streams: map[string]*result.PlayerStream{}, itemsEnt: map[int]*result.ItemTimeline{}}
	if res.TimelineAnalysis != nil {
		for name, slot := range res.TimelineAnalysis.PlayerSlots {
			rx.slotName[slot] = name
		}
		rx.locTable = res.TimelineAnalysis.LocTable
	}
	if res.Streams != nil {
		for i := range res.Streams.Players {
			p := &res.Streams.Players[i]
			rx.streams[p.Name] = p
			rx.slotTeam[p.Name] = p.Team
		}
	}
	if res.Items != nil {
		for i := range res.Items.Items {
			it := &res.Items.Items[i]
			rx.items = append(rx.items, it)
			if it.EntNum != 0 {
				rx.itemsEnt[it.EntNum] = it
			}
		}
	}
	return rx
}

func (rx *resolver) errf(format string, args ...any) {
	if len(rx.dec.Errors) < 50 {
		rx.dec.Errors = append(rx.dec.Errors, fmt.Sprintf(format, args...))
	}
}

func parseKV(line string) map[string]string {
	kv := map[string]string{}
	for _, tok := range strings.Fields(line)[1:] {
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			kv[tok[:eq]] = tok[eq+1:]
		}
	}
	return kv
}

func (rx *resolver) anchor(line string) {
	kv := parseKV(line)
	if v := kv["emitter"]; v != "" {
		rx.dec.EmitterVersion = v
	}
	if v, err := strconv.Atoi(kv["dlog"]); err == nil {
		rx.dec.DlogLevel = v
	}
}

func atoi16(s string) int16   { v, _ := strconv.Atoi(s); return int16(v) }
func atoiOr(s string) int     { v, _ := strconv.Atoi(s); return v }
func atofOr(s string) float32 { v, _ := strconv.ParseFloat(s, 32); return float32(v) }

func parseVec(s string) (x, y, z float32, ok bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	return atofOr(parts[0]), atofOr(parts[1]), atofOr(parts[2]), true
}

func (rx *resolver) record(line string, lineNo int) (result.DecisionRecord, bool) {
	kv := parseKV(line)
	rec := result.DecisionRecord{Type: kv["type"]}
	if rec.Type == "" {
		rx.errf("line %d: KDLOG without type", lineNo)
		return rec, false
	}

	tSec, err := strconv.ParseFloat(kv["t"], 64)
	if err != nil {
		rx.errf("line %d: bad t=%q", lineNo, kv["t"])
		return rec, false
	}
	rec.T = int32(math.Round(tSec * 1000))

	ed := atoiOr(kv["ed"])
	rec.Slot = ed - 1
	rec.Player = rx.slotName[rec.Slot]
	if rec.Player == "" {
		rx.errf("line %d: no player for ed=%d", lineNo, ed)
		return rec, false
	}
	rec.Team = rx.slotTeam[rec.Player]

	if pos, ok := kv["pos"]; ok {
		rec.X, rec.Y, rec.Z, _ = parseVec(pos)
	}
	rec.Loc = rx.locAt(rec.Player, rec.T)
	rec.State = rx.state(kv)
	rec.Trigger = kv["trig"]

	switch rec.Type {
	case "goal":
		if v := kv["chosen"]; v != "" && v != "none" {
			g := rx.goal(v, lineNo)
			rec.Chosen = &g
		}
		if v := kv["prim"]; v != "" {
			g := rx.goal(v, lineNo)
			rec.Prim = &g
		}
		for i := 1; i <= 8; i++ {
			v, ok := kv[fmt.Sprintf("c%d", i)]
			if !ok {
				break
			}
			rec.Candidates = append(rec.Candidates, rx.goal(v, lineNo))
		}
	case "enemy":
		ted := atoiOr(kv["ted"])
		if ted > 0 {
			rec.Target = rx.slotName[ted-1]
			rec.TargetLoc = rx.locAt(rec.Target, rec.T)
		}
		rec.Dist = atofOr(kv["dist"])
	case "evade":
		on := kv["on"] == "1"
		rec.On = &on
	case "play":
		rec.Play = kv["play"]
		rec.Lane = kv["lane"]
		rec.Phase = kv["phase"]
		rec.Detail = kv["detail"]
	}
	return rec, true
}

// state decodes the raw snapshot (h/a/it/aw/ammo) into field-code vocabulary.
func (rx *resolver) state(kv map[string]string) *result.DecisionState {
	if _, ok := kv["h"]; !ok {
		return nil
	}
	it := atoiOr(kv["it"])
	st := &result.DecisionState{
		H: atoi16(kv["h"]), A: atoi16(kv["a"]),
		SH: atoi16(kv["sh"]), NL: atoi16(kv["nl"]), RK: atoi16(kv["rk"]), CL: atoi16(kv["cl"]),
		RL: it&mvd.ITRocketLauncher != 0, LG: it&(mvd.ITLightning|mvd.ITSuperLightning) != 0,
		GL: it&mvd.ITGrenadeLauncher != 0, SSG: it&mvd.ITSuperShotgun != 0, SNG: it&mvd.ITSuperNailgun != 0,
		Q: it&mvd.ITQuad != 0, PE: it&mvd.ITInvulnerability != 0, R: it&mvd.ITInvisibility != 0,
		AW: itBitWeapon(atoiOr(kv["aw"])),
	}
	switch {
	case it&mvd.ITArmor3 != 0:
		st.AT = "ra"
	case it&mvd.ITArmor2 != 0:
		st.AT = "ya"
	case it&mvd.ITArmor1 != 0:
		st.AT = "ga"
	}
	return st
}

// goal parses one space-free entity descriptor
// ("cls=item_armor2;ied=35;org=x,y,z;m=12;des=2.10;tt=3.40;sc=0.61",
// with cls possibly in the "classname@x,y,z" combined form) and resolves it
// against the demo's items/players.
func (rx *resolver) goal(desc string, lineNo int) result.DecisionGoal {
	g := result.DecisionGoal{}
	for _, tok := range strings.Split(desc, ";") {
		eq := strings.IndexByte(tok, '=')
		if eq <= 0 {
			continue
		}
		key, val := tok[:eq], tok[eq+1:]
		switch key {
		case "cls":
			if at := strings.IndexByte(val, '@'); at >= 0 {
				g.Cls = val[:at]
				g.X, g.Y, g.Z, _ = parseVec(val[at+1:])
			} else {
				g.Cls = val
			}
		case "org":
			g.X, g.Y, g.Z, _ = parseVec(val)
		case "ied":
			g.EntNum = atoiOr(val)
		case "m":
			g.Marker = atoiOr(val)
		case "des":
			g.Desire = atofOr(val)
		case "tt":
			g.TravelMs = int32(math.Round(float64(atofOr(val)) * 1000))
		case "sc":
			g.Score = atofOr(val)
		}
	}

	if g.Cls == "player" {
		g.Kind = "player"
		g.Player = rx.slotName[g.EntNum-1]
		return g
	}
	if k, ok := classnameKind[g.Cls]; ok && (k == "backpack") {
		g.Kind = "backpack"
		return g
	}

	// World item: join to the ItemTimeline — entNum first, else exact-ish
	// origin match (items are static; tolerance covers droptofloor rounding).
	it := rx.itemsEnt[g.EntNum]
	if it == nil {
		best, bestD := (*result.ItemTimeline)(nil), float32(64.0)
		for _, cand := range rx.items {
			dx, dy, dz := cand.X-g.X, cand.Y-g.Y, cand.Z-g.Z
			d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
			if d < bestD {
				bestD, best = d, cand
			}
		}
		it = best
	}
	if it != nil {
		g.Kind, g.Name, g.Loc = it.Kind, it.Name, it.Loc
		return g
	}
	if k := classnameKind[g.Cls]; k != "" {
		g.Kind = k
	} else {
		g.Kind = "unknown"
		rx.errf("line %d: unresolved goal cls=%s ied=%d org=%.0f,%.0f,%.0f", lineNo, g.Cls, g.EntNum, g.X, g.Y, g.Z)
	}
	return g
}

// locAt looks up the player's PVS-attributed loc at match time t (ms) from
// their native-rate position track (li column + LocTable).
func (rx *resolver) locAt(player string, t int32) string {
	p := rx.streams[player]
	if p == nil || p.Position == nil || len(p.Position.T) == 0 || len(p.Position.Li) != len(p.Position.T) || len(rx.locTable) == 0 {
		return ""
	}
	// Last sample at or before t.
	i := sort.Search(len(p.Position.T), func(i int) bool { return p.Position.T[i] > t })
	if i == 0 {
		return ""
	}
	li := p.Position.Li[i-1]
	if li <= 0 || int(li) >= len(rx.locTable) {
		return ""
	}
	return rx.locTable[li]
}

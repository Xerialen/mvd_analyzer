package parser

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// This file implements the subset of the QW entity-state protocol we need
// to observe item pickups and respawns directly from the wire. The logic
// mirrors ezquake's CL_ParseBaseline / CL_ParseDelta / CL_ParsePacketEntities
// (ezquake-source/src/cl_ents.c:487-810, cl_parse.c:1817-1883). Only the
// fields that matter for item identity (modelindex, origin, skin) are
// decoded; the rest of each delta is skipped.

// Extra entity-delta flag bits not already defined on parser.go's
// skip-only path. (uOrigin1/2/3, uAngle1/2/3, uModel, uColormap, uSkin,
// uEffects, uFrame, uMoreBits, uFTEEvenMore, uFTETrans, uFTEModelDbl,
// uFTEYetMore, uFTEColourMod live there.) Values match
// ezquake-source/src/qwprot/src/protocol.h:344-401.
const (
	uRemove       = 1 << 14
	uSolid        = 1 << 6
	uFTEScale     = 1 << 0
	uFTEFatness   = 1 << 2
	uFTEEntityDbl = 1 << 5
	uFTEEntity2   = 1 << 6
)

// EntityState is the subset of fields we care about per entity.
type EntityState struct {
	ModelIndex int
	SkinNum    int
	Frame      int
	Colormap   int
	Effects    int
	Origin     [3]float32
	Present    bool // true if entity is in the current frame
}

// ItemSpawnEvent fires once per recognised item entity, the first time
// the demo stream makes that entity observable. Carries the item's
// classification so downstream consumers don't need to re-derive it.
type ItemSpawnEvent struct {
	EntNum int
	Kind   string // "ra","ya","ga","mh","h25","h15","rl","lg",...,"quad","pent","ring","suit","shells","nails","rockets","cells"
	Origin [3]float32
	Time   float64
}

func (e *ItemSpawnEvent) EventType() EventType { return EventItemSpawn }
func (e *ItemSpawnEvent) EventTime() float64   { return e.Time }

// ItemStateEvent fires on every visibility transition of a tracked item
// entity. Taken=true means the item became invisible (picked up);
// Taken=false means it reappeared (respawned or a fresh baseline
// replaced a taken entity).
type ItemStateEvent struct {
	EntNum int
	Kind   string
	Taken  bool
	Time   float64
}

func (e *ItemStateEvent) EventType() EventType { return EventItemState }
func (e *ItemStateEvent) EventTime() float64   { return e.Time }

// MoverSpawnEvent fires once per inline brush-model entity — an entity
// whose model is a "*N" submodel of the map BSP (func_plat, func_door,
// func_train, func_button, func_wall, func_illusionary) — the first
// time the demo stream makes it observable (normally its
// svc_spawnbaseline). Triggers never appear: Quake progs InitTrigger
// clears their model, and mvdsv only writes entities with a non-zero
// modelindex and model (sv_ents.c:790). SubModel is N, the index of
// the BSP submodel whose geometry the entity poses; Origin is the
// entity origin at spawn — the model-space offset the engine traces
// that submodel at (ezquake cl_ents.c CL_SetSolidEntities).
type MoverSpawnEvent struct {
	EntNum   int
	Model    string // inline model path from the precache list: "*1", "*2", ...
	SubModel int    // N — index into the map BSP's submodel (dmodel) array
	Origin   [3]float32
	Time     float64
	TimeMs   int32
}

func (e *MoverSpawnEvent) EventType() EventType { return EventMoverSpawn }
func (e *MoverSpawnEvent) EventTime() float64   { return e.Time }

// MoverStateEvent fires on every wire-state change of a tracked mover:
// its origin moved (the lift/door/train travelling — MVD deltas only
// re-send a changed origin, so between events the pose is exactly the
// last value) or its visibility flipped (modelindex cleared / U_REMOVE,
// then restored). Origin is the entity origin of the new state; for a
// newly-invisible mover it is the last visible origin.
type MoverStateEvent struct {
	EntNum  int
	Origin  [3]float32
	Visible bool
	Time    float64
	TimeMs  int32 // wire-native demo ms, same clock as PlayerPositionEvent.TimeMs
}

func (e *MoverStateEvent) EventType() EventType { return EventMoverState }
func (e *MoverStateEvent) EventTime() float64   { return e.Time }

// modelPathToKind maps standard Quake 1 item model paths to the compact
// kind strings we surface in the Result schema. Unrecognised paths
// (player models, projectiles, gibs, etc.) return "" so the analyzer
// can filter non-items cheaply.
//
// Armors all share progs/armor.mdl; the skin disambiguates GA/YA/RA
// (see classifyArmor below). Every other item is unambiguous from the
// path alone.
//
// Sources cross-referenced: ktx/src/items.c setmodel() calls for each
// item class, plus Quake 1 progs (id Software originals).
var modelPathToKind = map[string]string{
	"maps/b_bh10.bsp":      "h15",
	"maps/b_bh25.bsp":      "h25",
	"maps/b_bh100.bsp":     "mh",
	"progs/g_shot.mdl":     "ssg",
	"progs/g_nail.mdl":     "ng",
	"progs/g_nail2.mdl":    "sng",
	"progs/g_rock.mdl":     "gl",
	"progs/g_rock2.mdl":    "rl",
	"progs/g_light.mdl":    "lg",
	"maps/b_shell0.bsp":    "shells",
	"maps/b_shell1.bsp":    "shells",
	"maps/b_nail0.bsp":     "nails",
	"maps/b_nail1.bsp":     "nails",
	"maps/b_rock0.bsp":     "rockets",
	"maps/b_rock1.bsp":     "rockets",
	"maps/b_batt0.bsp":     "cells",
	"maps/b_batt1.bsp":     "cells",
	"progs/quaddama.mdl":   "quad",
	"progs/invulner.mdl":   "pent",
	"progs/invisibl.mdl":   "ring",
	"progs/suit.mdl":       "suit",
	"progs/backpack.mdl":   "backpack",
}

// classifyItem returns the compact kind string for an entity based on
// its model path and (for armor) skin number. Empty string means
// "not a tracked item kind".
func classifyItem(modelPath string, skin int) string {
	// Armors share one model; skin selects GA/YA/RA.
	if strings.EqualFold(modelPath, "progs/armor.mdl") {
		switch skin {
		case 0:
			return "ga"
		case 1:
			return "ya"
		case 2:
			return "ra"
		}
		return ""
	}
	return modelPathToKind[strings.ToLower(modelPath)]
}

// classifyMover reports whether modelPath names an inline brush
// submodel of the map BSP ("*1", "*2", …) and returns the submodel
// index. Submodel 0 is the worldspawn itself and never appears as an
// entity model, so it is rejected along with non-inline paths.
func classifyMover(modelPath string) (int, bool) {
	if len(modelPath) < 2 || modelPath[0] != '*' {
		return 0, false
	}
	n, err := strconv.Atoi(modelPath[1:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// wireMs converts a float demo time (seconds) back to the wire-native
// integer milliseconds it was decoded from. Lossless: the decoder
// derives Time as TimeMs/1000.
func wireMs(t float64) int32 {
	return int32(math.Round(t * 1000))
}

// resolveModel returns the model path for a modelindex from the
// parser's model list, empty string if the index is out of range or
// the model is the null model (index 0).
func (p *Parser) resolveModel(modelIndex int) string {
	if modelIndex <= 0 || modelIndex >= len(p.modelList) {
		return ""
	}
	return p.modelList[modelIndex]
}

// parseSpawnBaseline decodes svc_spawnbaseline (2-byte entnum +
// baseline body). Mirrors ezquake CL_ParseBaseline at cl_parse.c:1817.
func (p *Parser) parseSpawnBaseline(r *mvd.BufferReader, time float64, floatCoords bool) error {
	ent, err := r.ReadUint16()
	if err != nil {
		return err
	}
	modelIdx, err := r.ReadByte()
	if err != nil {
		return err
	}
	frame, err := r.ReadByte()
	if err != nil {
		return err
	}
	colormap, err := r.ReadByte()
	if err != nil {
		return err
	}
	skin, err := r.ReadByte()
	if err != nil {
		return err
	}
	var origin [3]float32
	for i := 0; i < 3; i++ {
		if floatCoords {
			origin[i], err = r.ReadFloatCoord()
		} else {
			origin[i], err = r.ReadCoord()
		}
		if err != nil {
			return err
		}
		// angle follows each coord — read and discard.
		if _, err := r.ReadByte(); err != nil {
			return err
		}
	}
	state := &EntityState{
		ModelIndex: int(modelIdx),
		Frame:      int(frame),
		Colormap:   int(colormap),
		SkinNum:    int(skin),
		Origin:     origin,
		Present:    true,
	}
	return p.registerBaseline(int(ent), state, time)
}

// parseSpawnBaseline2 handles the FTE extended form
// (svc_fte_spawnbaseline2 / svc_fte_spawnstatic2): the payload starts
// with a 2-byte delta flag word and uses the same wire encoding as a
// packetentities delta. The entity number comes out of the delta's
// low 9 bits (plus U_FTE_ENTITYDBL extensions).
func (p *Parser) parseSpawnBaseline2(r *mvd.BufferReader, time float64, floatCoords bool) error {
	word, err := r.ReadUint16()
	if err != nil {
		return err
	}
	state, entNum, err := p.readDelta(r, uint32(word), &EntityState{}, floatCoords)
	if err != nil {
		return err
	}
	return p.registerBaseline(entNum, state, time)
}

// registerBaseline stores a baseline, seeds the current-frame state
// from it (so the item starts "up"), emits ItemSpawnEvent if this is
// a tracked item kind and MoverSpawnEvent if it is an inline
// brush-model entity.
func (p *Parser) registerBaseline(entNum int, state *EntityState, time float64) error {
	if p.baselines == nil {
		p.baselines = make(map[int]*EntityState)
	}
	if p.currentEntities == nil {
		p.currentEntities = make(map[int]*EntityState)
	}
	if p.spawnedItems == nil {
		p.spawnedItems = make(map[int]string)
	}
	if p.spawnedMovers == nil {
		p.spawnedMovers = make(map[int]int)
	}
	prev := p.currentEntities[entNum]
	copy := *state
	p.baselines[entNum] = &copy
	// A baseline replacing a prior one is rare but legal (server can
	// resend). The current-frame state reflects the fresh baseline.
	currCopy := *state
	p.currentEntities[entNum] = &currCopy

	// Classify against the model list. If the model list hasn't been
	// received yet (rare — svc_modellist normally precedes baselines),
	// re-classification runs again in diffEntityTransitions.
	path := p.resolveModel(state.ModelIndex)
	kind := ""
	if path != "" {
		kind = classifyItem(path, state.SkinNum)
	}
	if kind != "" && p.spawnedItems[entNum] == "" {
		p.spawnedItems[entNum] = kind
		return p.emit(&ItemSpawnEvent{
			EntNum: entNum,
			Kind:   kind,
			Origin: state.Origin,
			Time:   time,
		})
	}
	if sub, ok := classifyMover(path); ok {
		if _, seen := p.spawnedMovers[entNum]; !seen {
			p.spawnedMovers[entNum] = sub
			return p.emit(&MoverSpawnEvent{
				EntNum:   entNum,
				Model:    path,
				SubModel: sub,
				Origin:   state.Origin,
				Time:     time,
				TimeMs:   wireMs(time),
			})
		}
		// A resent baseline for a known mover resets its pose; surface
		// it as a state change when it actually differs.
		if prev == nil || prev.Origin != state.Origin || !prev.Present || prev.ModelIndex == 0 {
			return p.emit(&MoverStateEvent{
				EntNum:  entNum,
				Origin:  state.Origin,
				Visible: true,
				Time:    time,
				TimeMs:  wireMs(time),
			})
		}
	}
	return nil
}

// parsePacketEntities decodes svc_packetentities (full) or
// svc_deltapacketentities (delta). The MVD format implicitly uses the
// prior frame as the delta reference (cl_ents.c:653-654), so we just
// keep one rolling "current" state.
//
// Key MVD-recording invariant (mvdsv/src/sv_ents.c:851): MVD packets
// ignore PVS entirely. The only filter on whether an entity appears
// in the packet is `modelindex != 0 && model[] != ""` (sv_ents.c:790).
// So for item tracking, "entity absent from packet" genuinely means
// "its model was cleared" — i.e. it was picked up.
func (p *Parser) parsePacketEntities(r *mvd.BufferReader, delta, floatCoords bool, fteExt uint32) error {
	if delta {
		// Consume the 1-byte "from" sequence — MVD deltas always
		// reference the immediately prior frame, so we don't need the
		// index.
		if _, err := r.ReadByte(); err != nil {
			return err
		}
	}

	// newFrame is the entity set after applying this packet.
	//
	// FULL packet: the packet *is* the current visible set. Start
	//   empty; whatever lands in the packet becomes the whole
	//   current state.
	// DELTA packet: packet describes changes relative to the prior
	//   frame. Deep-copy current state, then apply deltas on top
	//   (U_REMOVE deletes, other flags update).
	var newFrame map[int]*EntityState
	if delta {
		newFrame = make(map[int]*EntityState, len(p.currentEntities))
		for k, v := range p.currentEntities {
			cp := *v
			newFrame[k] = &cp
		}
	} else {
		newFrame = make(map[int]*EntityState)
	}

	for {
		word, err := r.ReadUint16()
		if err != nil {
			return err
		}
		if word == 0 {
			break
		}

		bits := uint32(word)
		entNum := int(bits & 511)
		bits &= ^uint32(511)

		if bits&uMoreBits != 0 {
			b, err := r.ReadByte()
			if err != nil {
				return err
			}
			bits |= uint32(b)
		}

		var morebits uint32
		if bits&uFTEEvenMore != 0 && fteExt != 0 {
			b, err := r.ReadByte()
			if err != nil {
				return err
			}
			morebits = uint32(b)
			if morebits&uFTEYetMore != 0 {
				b2, err := r.ReadByte()
				if err != nil {
					return err
				}
				morebits |= uint32(b2) << 8
			}
		}

		if morebits&uFTEEntityDbl != 0 {
			entNum += 512
		}
		if morebits&uFTEEntity2 != 0 {
			entNum += 1024
		}

		if bits&uRemove != 0 {
			delete(newFrame, entNum)
			continue
		}

		// "From" state for the delta: prior frame's entry, else
		// baseline. Matches ezquake cl_ents.c:807.
		from := newFrame[entNum]
		if from == nil {
			from = p.baselines[entNum]
		}
		var base EntityState
		if from != nil {
			base = *from
		}
		state, _, err := p.applyDeltaFields(r, bits, morebits, &base, floatCoords)
		if err != nil {
			return err
		}
		state.Present = true
		newFrame[entNum] = state
	}

	p.diffEntityTransitions(newFrame, p.currentEntities, p.lastEntityPacketTime, p.lastEntityPacketTimeMs)
	p.currentEntities = newFrame
	return nil
}

// applyDeltaFields reads the delta payload after the flag word(s) have
// been consumed and fills in the target state. Returns the state and
// (unused, for future use) entity-number delta offset.
func (p *Parser) applyDeltaFields(r *mvd.BufferReader, bits, morebits uint32, from *EntityState, floatCoords bool) (*EntityState, int, error) {
	state := *from
	if bits&uModel != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		state.ModelIndex = int(b)
		if morebits&uFTEModelDbl != 0 {
			state.ModelIndex += 256
		}
	} else if morebits&uFTEModelDbl != 0 {
		mi, err := r.ReadUint16()
		if err != nil {
			return nil, 0, err
		}
		state.ModelIndex = int(mi)
	}
	if bits&uFrame != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		state.Frame = int(b)
	}
	if bits&uColormap != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		state.Colormap = int(b)
	}
	if bits&uSkin != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		state.SkinNum = int(b)
	}
	if bits&uEffects != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		state.Effects = int(b)
	}
	// Origin + angles are paired per axis (origin N + angle N).
	readCoord := func() (float32, error) {
		if floatCoords {
			return r.ReadFloatCoord()
		}
		return r.ReadCoord()
	}
	if bits&uOrigin1 != 0 {
		v, err := readCoord()
		if err != nil {
			return nil, 0, err
		}
		state.Origin[0] = v
	}
	if bits&uAngle1 != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	if bits&uOrigin2 != 0 {
		v, err := readCoord()
		if err != nil {
			return nil, 0, err
		}
		state.Origin[1] = v
	}
	if bits&uAngle2 != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	if bits&uOrigin3 != 0 {
		v, err := readCoord()
		if err != nil {
			return nil, 0, err
		}
		state.Origin[2] = v
	}
	if bits&uAngle3 != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	// U_SOLID has no payload in QW (see ezquake cl_ents.c:574-576).

	// FTE extension payloads we don't care about but must skip to keep
	// the byte cursor aligned.
	if morebits&uFTETrans != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	if morebits&uFTEScale != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	if morebits&uFTEFatness != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	if morebits&uFTEColourMod != 0 {
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
		if _, err := r.ReadByte(); err != nil {
			return nil, 0, err
		}
	}
	// Other FTE flags (drawflags, abslight, etc.) are rarer still
	// and the project hasn't negotiated them; skipping the known
	// subset above covers every demo in the corpus. If a field's
	// bit is set but we don't recognise it, the next ReadUint16
	// will desynchronise and the parser will error — which is the
	// safe failure mode.

	return &state, 0, nil
}

// readDelta is a convenience wrapper for baseline2: reads the
// two-byte flag word plus payload.
func (p *Parser) readDelta(r *mvd.BufferReader, word uint32, from *EntityState, floatCoords bool) (*EntityState, int, error) {
	bits := word
	entNum := int(bits & 511)
	bits &= ^uint32(511)

	if bits&uMoreBits != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		bits |= uint32(b)
	}
	var morebits uint32
	if bits&uFTEEvenMore != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		morebits = uint32(b)
		if morebits&uFTEYetMore != 0 {
			b2, err := r.ReadByte()
			if err != nil {
				return nil, 0, err
			}
			morebits |= uint32(b2) << 8
		}
	}
	if morebits&uFTEEntityDbl != 0 {
		entNum += 512
	}
	if morebits&uFTEEntity2 != 0 {
		entNum += 1024
	}
	state, _, err := p.applyDeltaFields(r, bits, morebits, from, floatCoords)
	if err != nil {
		return nil, 0, err
	}
	state.Present = true
	return state, entNum, nil
}

// diffEntityTransitions compares the prior current-frame state against
// the new frame and emits ItemStateEvent on every visibility
// transition of a tracked item entity and MoverStateEvent on every
// origin / visibility change of a tracked inline brush-model entity.
//
// "Visible" means the entity is in the frame (Present==true) AND has a
// non-zero modelindex. mvdsv filters entities by modelindex!=0 at emit
// time (sv_ents.c:790), so for items "not in packet" is equivalent
// to "modelindex is 0" — i.e. the item was picked up.
//
// Also emits ItemSpawnEvent / MoverSpawnEvent for entities that hadn't
// been classified yet (e.g. baseline arrived before the model list)
// when we can now resolve the kind.
func (p *Parser) diffEntityTransitions(newFrame, oldFrame map[int]*EntityState, time float64, timeMs int32) {
	// Union of keys from old + new (tracked entities only). Sort the
	// entity numbers before emitting so that downstream stateful
	// consumers (e.g. items.go's layered attribution) see same-frame
	// events in a deterministic order across runs.
	seen := make(map[int]bool, len(newFrame)+len(oldFrame))
	for k := range newFrame {
		seen[k] = true
	}
	for k := range oldFrame {
		seen[k] = true
	}
	ents := make([]int, 0, len(seen))
	for k := range seen {
		ents = append(ents, k)
	}
	sort.Ints(ents)

	for _, ent := range ents {
		s := newFrame[ent]
		o := oldFrame[ent]
		p.diffItemEntity(ent, s, o, time)
		p.diffMoverEntity(ent, s, o, time, timeMs)
	}
}

// diffItemEntity emits the item spawn / state events for one entity of
// a frame diff. Resolve current kind preferring whatever state exists
// now, so baselines that landed before the model list still get an
// ItemSpawnEvent once we can name the model.
func (p *Parser) diffItemEntity(ent int, s, o *EntityState, time float64) {
	kind := p.spawnedItems[ent]
	if kind == "" {
		src := s
		if src == nil {
			src = o
		}
		if src != nil {
			if path := p.resolveModel(src.ModelIndex); path != "" {
				kind = classifyItem(path, src.SkinNum)
				if kind != "" {
					p.spawnedItems[ent] = kind
					origin := src.Origin
					if b := p.baselines[ent]; b != nil {
						origin = b.Origin
					}
					_ = p.emit(&ItemSpawnEvent{
						EntNum: ent,
						Kind:   kind,
						Origin: origin,
						Time:   time,
					})
				}
			}
		}
	}
	if kind == "" {
		return
	}

	oldVisible := o != nil && o.Present && o.ModelIndex != 0
	newVisible := s != nil && s.Present && s.ModelIndex != 0
	if oldVisible == newVisible {
		return
	}
	_ = p.emit(&ItemStateEvent{
		EntNum: ent,
		Kind:   kind,
		Taken:  !newVisible,
		Time:   time,
	})
}

// diffMoverEntity emits the mover spawn / state events for one entity
// of a frame diff. State events fire on visibility flips AND on origin
// changes — a travelling lift re-sends its origin every frame it
// moves, and the analyzer's pose timeline is exactly those changes
// (hold-last between them, see MoverStateEvent).
func (p *Parser) diffMoverEntity(ent int, s, o *EntityState, time float64, timeMs int32) {
	if _, seenMover := p.spawnedMovers[ent]; !seenMover {
		// Late classification: a mover whose baseline arrived before
		// the model list gets its spawn here, same as items.
		src := s
		if src == nil {
			src = o
		}
		if src == nil {
			return
		}
		path := p.resolveModel(src.ModelIndex)
		sub, ok := classifyMover(path)
		if !ok {
			return
		}
		p.spawnedMovers[ent] = sub
		origin := src.Origin
		if b := p.baselines[ent]; b != nil {
			origin = b.Origin
		}
		_ = p.emit(&MoverSpawnEvent{
			EntNum:   ent,
			Model:    path,
			SubModel: sub,
			Origin:   origin,
			Time:     time,
			TimeMs:   timeMs,
		})
	}

	oldVisible := o != nil && o.Present && o.ModelIndex != 0
	newVisible := s != nil && s.Present && s.ModelIndex != 0
	moved := oldVisible && newVisible && s.Origin != o.Origin
	if oldVisible == newVisible && !moved {
		return
	}
	origin := [3]float32{}
	if newVisible {
		origin = s.Origin
	} else if o != nil {
		origin = o.Origin
	}
	_ = p.emit(&MoverStateEvent{
		EntNum:  ent,
		Origin:  origin,
		Visible: newVisible,
		Time:    time,
		TimeMs:  timeMs,
	})
}

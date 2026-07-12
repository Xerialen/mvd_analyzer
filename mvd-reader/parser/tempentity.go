package parser

import (
	"errors"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// errUnknownTE is returned by parseTempEntity when the TE type byte is not
// in its layout table — the payload length can't be guessed, so the rest of
// the message is abandoned. Distinct from a truncated read inside a known
// type so the caller can warn "unknown_te" for the former and "parse_error"
// (naming the TE type) for the latter; mirrors errUnknownSvc.
var errUnknownTE = errors.New("unknown temp entity type")

// Temp-entity types we care about. The lightning types are beams
// (short entity + start + end coords); everything else is a point effect.
// Values match ezquake cl_tent.c / qwprot protocol.h.
const (
	teLightning1 = 5 // bolt (shambler/enforcer — not player LG)
	teLightning2 = 6 // player Lightning Gun bolt (ktx W_FireLightning)
	teLightning3 = 9 // big bolt
)

// BeamEvent is emitted for a TE_LIGHTNING1/2/3 temp entity — a lightning
// beam drawn from Start to End by entity Ent. For the player Lightning Gun
// (Type == TE_LIGHTNING2, ktx/src/weapons.c W_FireLightning) the beam is
// emitted once per fire tick and carries the firing entity directly, so it
// is the authoritative per-shot LG signal: Ent-1 is the shooter, Start is
// the muzzle, End is the hitscan trace endpoint (the impact / aim point).
// One beam == one LG attack == one cell (discharge does not emit a beam),
// so beam counts match KTX's acc.attacks.
type BeamEvent struct {
	Ent    int // firing entity (player slot = Ent-1 when a client; negative on ezquake's rail-trail extension, never from KTX)
	Type   int // raw TE type: 5/6/9
	Start  [3]float32
	End    [3]float32
	Time   float64
	TimeMs int32
}

func (e *BeamEvent) EventType() EventType { return EventBeam }
func (e *BeamEvent) EventTime() float64   { return e.Time }

// parseTempEntity decodes a svc_temp_entity payload. Lightning beams are
// surfaced as BeamEvent; every other (point-effect) type is consumed for
// its known byte length. Returns the TE type so the caller can name it in
// a diagnostic. Wire layout per type is handled in the switch below (ref:
// ezquake cl_tent.c::CL_ParseTEnt); an unknown type returns errUnknownTE
// since its length can't be guessed without drifting the parser.
func (p *Parser) parseTempEntity(r *mvd.BufferReader, time float64, timeMs int32, floatCoords bool) (byte, error) {
	teType, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	coordSize := 2
	if floatCoords {
		coordSize = 4
	}
	readCoord := func() (float32, error) {
		if floatCoords {
			return r.ReadFloatCoord()
		}
		return r.ReadCoord()
	}

	switch teType {
	case teLightning1, teLightning2, teLightning3:
		entRaw, err := r.ReadUint16()
		if err != nil {
			return teType, err
		}
		var start, end [3]float32
		for i := 0; i < 3; i++ {
			if start[i], err = readCoord(); err != nil {
				return teType, err
			}
		}
		for i := 0; i < 3; i++ {
			if end[i], err = readCoord(); err != nil {
				return teType, err
			}
		}
		// The wire value is a signed short (ezquake cl_tent.c:158): ezquake
		// gives negative values protocol meaning (TE_LIGHTNING1 with ent in
		// -512..-1 is its rail-trail extension). KTX always writes a real
		// edict, but decode faithfully so exotic streams stay recoverable.
		return teType, p.emit(&BeamEvent{
			Ent:    int(int16(entRaw)),
			Type:   int(teType),
			Start:  start,
			End:    end,
			Time:   time,
			TimeMs: timeMs,
		})
	case 0, 1, 3, 4, 7, 8, 10, 11, 13:
		return teType, r.Skip(3 * coordSize)
	case 2, 12:
		return teType, r.Skip(1 + 3*coordSize)
	default:
		return teType, errUnknownTE
	}
}

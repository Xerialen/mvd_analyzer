package parser

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// Nail is one spike projectile in a svc_nails snapshot: its id (svc_nails2
// only; 0 for the non-indexed svc_nails) and decoded world origin. The id is
// stable across frames while the nail lives, so the shots analyzer can
// bracket each nail's flight (spawn → despawn) for ng/sng → damage linking.
type Nail struct {
	ID     int
	Origin [3]float32
}

// NailsFrameEvent carries every live nail in one svc_nails / svc_nails2
// message (the full current set for that frame, like packetentities for
// spikes). Emitted only when nail decoding is enabled (Parser.SetDecodeNails)
// — nails are high volume, so the default parse skips them.
type NailsFrameEvent struct {
	Nails  []Nail
	Time   float64
	TimeMs int32
}

func (e *NailsFrameEvent) EventType() EventType { return EventNails }
func (e *NailsFrameEvent) EventTime() float64   { return e.Time }

// SetDecodeNails opts the parser into nail tracking. It enables both nail
// sources: decoding svc_nails / svc_nails2 into NailsFrameEvent (non-nailhack
// servers) AND recognising spike packet entities (progs/spike.mdl,
// progs/s_spike.mdl) as projectiles (sv_nailhack servers — see
// diffProjectileEntity). Off by default: nails are high volume, so this is
// only worthwhile for a consumer that wants ng/sng linking or the nail map
// overlay.
func (p *Parser) SetDecodeNails(enabled bool) { p.decodeNails = enabled }

// parseNails decodes svc_nails (indexed=false) / svc_nails2 (indexed=true).
// Wire format (ezquake CL_ParseProjectiles): 1-byte count, then per nail an
// optional 1-byte id (svc_nails2 only) followed by 6 packed bytes encoding
// origin + angles. When nail decoding is disabled it consumes the payload
// without emitting (the skip path), so the byte cursor stays aligned either
// way.
func (p *Parser) parseNails(r *mvd.BufferReader, indexed bool, time float64, timeMs int32) error {
	count, err := r.ReadByte()
	if err != nil {
		return err
	}
	if !p.decodeNails {
		// Skip arithmetic mirrors the decode loop below: [1-byte id
		// (svc_nails2 only)] + 6 packed origin/angle bytes per nail.
		bytesPerNail := 6
		if indexed {
			bytesPerNail = 7
		}
		return r.Skip(int(count) * bytesPerNail)
	}

	nails := make([]Nail, 0, count)
	for i := 0; i < int(count); i++ {
		id := 0
		if indexed {
			b, err := r.ReadByte()
			if err != nil {
				return err
			}
			id = int(b)
		}
		var bits [6]byte
		for j := 0; j < 6; j++ {
			b, err := r.ReadByte()
			if err != nil {
				return err
			}
			bits[j] = b
		}
		// Origin unpack, mirroring ezquake CL_ParseProjectiles: each axis is a
		// 12-bit field scaled by 2 and biased by -4096 (whole-unit precision).
		ox := ((int(bits[0]) + ((int(bits[1]) & 15) << 8)) << 1) - 4096
		oy := (((int(bits[1]) >> 4) + (int(bits[2]) << 4)) << 1) - 4096
		oz := ((int(bits[3]) + ((int(bits[4]) & 15) << 8)) << 1) - 4096
		nails = append(nails, Nail{
			ID:     id,
			Origin: [3]float32{float32(ox), float32(oy), float32(oz)},
		})
	}
	return p.emit(&NailsFrameEvent{Nails: nails, Time: time, TimeMs: timeMs})
}

package parser

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// svc_sound channel-word flag bits (ezquake-source/src/cl_parse.c:
// SND_VOLUME / SND_ATTENUATION). They live in the high bits of the
// 16-bit channel word; the entity and the 3-bit channel index share the
// remaining bits — see parseSound.
const (
	sndVolume      = 0x8000
	sndAttenuation = 0x4000
)

// SoundEvent is emitted for every svc_sound (cmd 6) — a sound the server
// started on some entity's channel. This is the raw protocol signal; the
// shots analyzer interprets weapon-fire sounds (Name like
// "weapons/rocket1i.wav" on CHAN_WEAPON) into shots.
//
// The wire packs the emitting entity into the channel word, so Ent is the
// authoritative source of the sound (a player firing is Ent in
// [1, MaxClients]; the player slot is Ent-1). Channel is the 3-bit channel
// index (CHAN_WEAPON = 1 for weapon fire). Name is the precache path
// resolved from the parser's svc_soundlist table, or "" if the sound list
// has not been received yet or SoundNum is out of range.
//
// Time is float64 seconds (the ergonomic view); TimeMs is the canonical
// wire-native value in integer milliseconds — consumers that persist into
// the result schema or compare against other ms timestamps must use TimeMs.
type SoundEvent struct {
	Ent      int        // emitting entity (player slot = Ent-1 when a client)
	Channel  int        // 3-bit channel index (CHAN_WEAPON = 1)
	SoundNum int        // wire sound index into the soundlist
	Name     string     // resolved precache path, "" if unknown
	Origin   [3]float32 // world position the sound played at
	Time     float64
	TimeMs   int32
}

func (e *SoundEvent) EventType() EventType { return EventSound }
func (e *SoundEvent) EventTime() float64   { return e.Time }

// parseSound decodes svc_sound. Wire layout
// (ezquake-source/src/cl_parse.c:1951-1986):
//
//	[short] channel   — high bits flag optional volume/attenuation; the
//	                    entity is bits 3..12, the channel index is bits 0..2
//	[byte]  volume      (present iff channel & SND_VOLUME)
//	[byte]  attenuation (present iff channel & SND_ATTENUATION)
//	[byte]  sound_num
//	[coord×3] origin    (short coords, or float when FTE float-coords set)
//
// ent = (channel >> 3) & 1023; channel &= 7.
func (p *Parser) parseSound(r *mvd.BufferReader, time float64, timeMs int32, floatCoords bool) error {
	channel, err := r.ReadUint16()
	if err != nil {
		return err
	}
	if channel&sndVolume != 0 {
		if err := r.Skip(1); err != nil { // volume — not retained
			return err
		}
	}
	if channel&sndAttenuation != 0 {
		if err := r.Skip(1); err != nil { // attenuation — not retained
			return err
		}
	}
	soundNum, err := r.ReadByte()
	if err != nil {
		return err
	}

	var origin [3]float32
	for i := 0; i < 3; i++ {
		var coord float32
		if floatCoords {
			coord, err = r.ReadFloatCoord()
		} else {
			coord, err = r.ReadCoord()
		}
		if err != nil {
			return err
		}
		origin[i] = coord
	}

	ent := (int(channel) >> 3) & 1023

	return p.emit(&SoundEvent{
		Ent:      ent,
		Channel:  int(channel) & 7,
		SoundNum: int(soundNum),
		Name:     p.resolveSound(int(soundNum)),
		Origin:   origin,
		Time:     time,
		TimeMs:   timeMs,
	})
}

// resolveSound maps a wire sound index to its precache path, or "" when
// the soundlist has not arrived yet or the index is out of range.
func (p *Parser) resolveSound(soundNum int) string {
	if soundNum < 0 || soundNum >= len(p.soundList) {
		return ""
	}
	return p.soundList[soundNum]
}

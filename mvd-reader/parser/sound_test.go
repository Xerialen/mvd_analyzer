package parser

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// coordBytes encodes a QW short coord (value*8, little-endian int16).
func coordBytes(b []byte, v float32) {
	binary.LittleEndian.PutUint16(b, uint16(int16(v*8)))
}

// soundPayload builds a svc_sound body (everything after the cmd byte):
// [short channel][?vol][?atten][byte sound_num][coord×3 origin].
func soundPayload(ent, chanIdx, soundNum int, origin [3]float32, vol, atten bool) []byte {
	channel := uint16((ent<<3)|(chanIdx&7)) & 0x1FFF
	if vol {
		channel |= sndVolume
	}
	if atten {
		channel |= sndAttenuation
	}
	var b []byte
	hdr := make([]byte, 2)
	binary.LittleEndian.PutUint16(hdr, channel)
	b = append(b, hdr...)
	if vol {
		b = append(b, 200) // volume byte (skipped)
	}
	if atten {
		b = append(b, 64) // attenuation byte (skipped)
	}
	b = append(b, byte(soundNum))
	for i := 0; i < 3; i++ {
		c := make([]byte, 2)
		coordBytes(c, origin[i])
		b = append(b, c...)
	}
	return b
}

// soundListPayload builds a svc_soundlist body: [byte start] then
// null-terminated names, a "" terminator, and a continuation index byte.
func soundListPayload(start int, names ...string) []byte {
	b := []byte{byte(start)}
	for _, n := range names {
		b = append(b, []byte(n)...)
		b = append(b, 0)
	}
	b = append(b, 0) // empty string terminates the list
	b = append(b, 0) // continuation index
	return b
}

func captureSound(p *Parser) *func() *SoundEvent {
	var got *SoundEvent
	p.OnEvent(func(e Event) error {
		if s, ok := e.(*SoundEvent); ok {
			got = s
		}
		return nil
	})
	f := func() *SoundEvent { return got }
	return &f
}

func TestParseSound_WeaponFireEntityAndChannel(t *testing.T) {
	p := NewParser(nil)
	get := captureSound(p)

	// ent 4 (player slot 3), CHAN_WEAPON, sound_num 2, origin (100,-50,200).
	origin := [3]float32{100, -50, 200}
	payload := soundPayload(4, 1, 2, origin, false, false)
	if err := p.parseSound(mvd.NewBufferReader(payload), 7.5, 7500, false); err != nil {
		t.Fatalf("parseSound: %v", err)
	}
	got := (*get)()
	if got == nil {
		t.Fatal("no SoundEvent emitted")
	}
	if got.Ent != 4 || got.Channel != 1 || got.SoundNum != 2 {
		t.Errorf("ent/chan/num: got %+v", got)
	}
	if got.Origin != origin {
		t.Errorf("origin: got %v want %v", got.Origin, origin)
	}
	if got.Time != 7.5 || got.TimeMs != 7500 {
		t.Errorf("time: got %v/%d", got.Time, got.TimeMs)
	}
	// No soundlist received yet -> Name unresolved.
	if got.Name != "" {
		t.Errorf("Name should be empty without a soundlist, got %q", got.Name)
	}
}

func TestParseSound_VolumeAndAttenuationFlagsSkipped(t *testing.T) {
	p := NewParser(nil)
	get := captureSound(p)

	origin := [3]float32{0, 0, 16}
	payload := soundPayload(7, 1, 5, origin, true, true)
	if err := p.parseSound(mvd.NewBufferReader(payload), 1, 1000, false); err != nil {
		t.Fatalf("parseSound: %v", err)
	}
	got := (*get)()
	if got == nil {
		t.Fatal("no SoundEvent emitted")
	}
	// Optional bytes must be consumed so sound_num / origin still align.
	if got.Ent != 7 || got.Channel != 1 || got.SoundNum != 5 || got.Origin != origin {
		t.Errorf("misaligned decode with vol/atten flags: %+v", got)
	}
}

func TestParseSound_FloatCoords(t *testing.T) {
	p := NewParser(nil)
	get := captureSound(p)

	// Build a float-coord origin payload by hand.
	channel := uint16((3 << 3) | 1)
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, channel)
	b = append(b, byte(9)) // sound_num
	origin := [3]float32{12.5, -3.25, 64}
	for i := 0; i < 3; i++ {
		fb := make([]byte, 4)
		binary.LittleEndian.PutUint32(fb, math.Float32bits(origin[i]))
		b = append(b, fb...)
	}
	if err := p.parseSound(mvd.NewBufferReader(b), 2, 2000, true); err != nil {
		t.Fatalf("parseSound float: %v", err)
	}
	got := (*get)()
	if got == nil || got.Ent != 3 || got.SoundNum != 9 || got.Origin != origin {
		t.Errorf("float-coord decode: %+v", got)
	}
}

func TestParseSoundList_ResolvesName(t *testing.T) {
	p := NewParser(nil)
	// start=0 -> first name lands at index 1 (index 0 reserved for null).
	payload := soundListPayload(0, "weapons/rocket1i.wav", "weapons/grenade.wav")
	if err := p.parseSoundList(mvd.NewBufferReader(payload)); err != nil {
		t.Fatalf("parseSoundList: %v", err)
	}
	if got := p.resolveSound(1); got != "weapons/rocket1i.wav" {
		t.Errorf("index 1: got %q", got)
	}
	if got := p.resolveSound(2); got != "weapons/grenade.wav" {
		t.Errorf("index 2: got %q", got)
	}
	// Index 0 is the reserved null sound; out-of-range resolves to "".
	if got := p.resolveSound(0); got != "" {
		t.Errorf("index 0 should be empty, got %q", got)
	}
	if got := p.resolveSound(99); got != "" {
		t.Errorf("out-of-range should be empty, got %q", got)
	}

	// A sound emitted after the list resolves its name.
	get := captureSound(p)
	if err := p.parseSound(mvd.NewBufferReader(soundPayload(2, 1, 1, [3]float32{1, 2, 3}, false, false)), 1, 1000, false); err != nil {
		t.Fatalf("parseSound: %v", err)
	}
	if got := (*get)(); got == nil || got.Name != "weapons/rocket1i.wav" {
		t.Errorf("resolved name: %+v", got)
	}
}

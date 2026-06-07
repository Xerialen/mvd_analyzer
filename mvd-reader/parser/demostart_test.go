package parser

import "testing"

// TestDecodeULEB128DemoStart pins the ULEB128 decode against real
// mvdhidden_demo_start_timestamp_ms (0x000B) bodies taken from a 2026-05-31
// KTX match series. mvdsv encodes the demo-open Unix-ms time as an unsigned
// LEB128 varint, so a ~2026 value is 6 bytes — decoding it as a fixed-width
// little-endian uint64 yields a year-3779 nonsense value, which is the bug
// this guards against.
func TestDecodeULEB128DemoStart(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want uint64 // Unix epoch milliseconds
	}{
		{"demo1", []byte{0xac, 0x93, 0xb7, 0xfe, 0xe7, 0x33}, 1780260653484},
		{"demo2", []byte{0xf8, 0x8d, 0xe0, 0xfd, 0xe7, 0x33}, 1780259227384},
		{"demo5", []byte{0xb4, 0xa1, 0xe5, 0xfb, 0xe7, 0x33}, 1780255117492},
		{"single-byte", []byte{0x00}, 0},
		{"two-byte", []byte{0x80, 0x01}, 128},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeULEB128(c.body); got != c.want {
				t.Fatalf("decodeULEB128(%x) = %d, want %d", c.body, got, c.want)
			}
		})
	}
}

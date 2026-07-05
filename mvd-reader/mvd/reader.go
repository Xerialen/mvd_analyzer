package mvd

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// byteSource is the minimal interface that both BinaryReader and BufferReader
// satisfy. The shared `read*` free functions below use it so the higher-level
// readers (ReadInt16/ReadCoord/ReadAngle/...) only have to be implemented
// once instead of twice.
type byteSource interface {
	ReadByte() (byte, error)
	ReadUint16() (uint16, error)
	ReadUint32() (uint32, error)
}

func readInt8(s byteSource) (int8, error) {
	b, err := s.ReadByte()
	return int8(b), err
}

func readInt16(s byteSource) (int16, error) {
	v, err := s.ReadUint16()
	return int16(v), err
}

func readInt32(s byteSource) (int32, error) {
	v, err := s.ReadUint32()
	return int32(v), err
}

func readFloat32(s byteSource) (float32, error) {
	v, err := s.ReadUint32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}

// readCoord reads a 16-bit fixed-point coordinate (units of 1/8). This is
// the legacy QuakeWorld coordinate format; servers using the float-coords
// extension call readFloatCoord instead.
func readCoord(s byteSource) (float32, error) {
	v, err := readInt16(s)
	if err != nil {
		return 0, err
	}
	return float32(v) / 8.0, nil
}

func readAngle(s byteSource) (float32, error) {
	b, err := s.ReadByte()
	if err != nil {
		return 0, err
	}
	return float32(b) * (360.0 / 256.0), nil
}

func readAngle16(s byteSource) (float32, error) {
	v, err := s.ReadUint16()
	if err != nil {
		return 0, err
	}
	return float32(v) * (360.0 / 65536.0), nil
}

// BinaryReader wraps an io.Reader for reading binary data
type BinaryReader struct {
	r      io.Reader
	buf    []byte
	offset int64
}

// NewBinaryReader creates a new BinaryReader
func NewBinaryReader(r io.Reader) *BinaryReader {
	return &BinaryReader{
		r:   r,
		buf: make([]byte, 8),
	}
}

// Offset returns the current read offset
func (br *BinaryReader) Offset() int64 {
	return br.offset
}

// ReadByte reads a single byte
func (br *BinaryReader) ReadByte() (byte, error) {
	_, err := io.ReadFull(br.r, br.buf[:1])
	if err != nil {
		return 0, err
	}
	br.offset++
	return br.buf[0], nil
}

// ReadBytes reads n bytes into a new slice
func (br *BinaryReader) ReadBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(br.r, buf)
	if err != nil {
		return nil, err
	}
	br.offset += int64(n)
	return buf, nil
}

// ReadUint16 reads an unsigned 16-bit little-endian integer.
func (br *BinaryReader) ReadUint16() (uint16, error) {
	_, err := io.ReadFull(br.r, br.buf[:2])
	if err != nil {
		return 0, err
	}
	br.offset += 2
	return binary.LittleEndian.Uint16(br.buf[:2]), nil
}

// ReadUint32 reads an unsigned 32-bit little-endian integer.
func (br *BinaryReader) ReadUint32() (uint32, error) {
	_, err := io.ReadFull(br.r, br.buf[:4])
	if err != nil {
		return 0, err
	}
	br.offset += 4
	return binary.LittleEndian.Uint32(br.buf[:4]), nil
}

// ReadString reads a null-terminated string.
func (br *BinaryReader) ReadString() (string, error) {
	var result []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == 0 {
			break
		}
		result = append(result, b)
	}
	return string(result), nil
}

// The remaining typed readers all delegate to the shared free functions.
func (br *BinaryReader) ReadInt8() (int8, error)       { return readInt8(br) }
func (br *BinaryReader) ReadInt16() (int16, error)     { return readInt16(br) }
func (br *BinaryReader) ReadInt32() (int32, error)     { return readInt32(br) }
func (br *BinaryReader) ReadFloat32() (float32, error) { return readFloat32(br) }
func (br *BinaryReader) ReadCoord() (float32, error)   { return readCoord(br) }
func (br *BinaryReader) ReadFloatCoord() (float32, error) {
	return br.ReadFloat32()
}
func (br *BinaryReader) ReadAngle() (float32, error)   { return readAngle(br) }
func (br *BinaryReader) ReadAngle16() (float32, error) { return readAngle16(br) }

// Skip skips n bytes
func (br *BinaryReader) Skip(n int) error {
	_, err := br.ReadBytes(n)
	return err
}

// BufferReader wraps a byte slice for reading
type BufferReader struct {
	data   []byte
	offset int
}

// NewBufferReader creates a new BufferReader
func NewBufferReader(data []byte) *BufferReader {
	return &BufferReader{data: data}
}

// Remaining returns the number of bytes remaining
func (br *BufferReader) Remaining() int {
	return len(br.data) - br.offset
}

// Offset returns the current offset
func (br *BufferReader) Offset() int {
	return br.offset
}

// EOF returns true if at end of buffer
func (br *BufferReader) EOF() bool {
	return br.offset >= len(br.data)
}

// ReadByte reads a single byte
func (br *BufferReader) ReadByte() (byte, error) {
	if br.offset >= len(br.data) {
		return 0, io.EOF
	}
	b := br.data[br.offset]
	br.offset++
	return b, nil
}

// ReadBytes reads n bytes
func (br *BufferReader) ReadBytes(n int) ([]byte, error) {
	if br.offset+n > len(br.data) {
		return nil, io.EOF
	}
	result := br.data[br.offset : br.offset+n]
	br.offset += n
	return result, nil
}

// PeekByte peeks at the next byte without consuming it
func (br *BufferReader) PeekByte() (byte, error) {
	if br.offset >= len(br.data) {
		return 0, io.EOF
	}
	return br.data[br.offset], nil
}

// ReadUint16 reads an unsigned 16-bit little-endian integer.
func (br *BufferReader) ReadUint16() (uint16, error) {
	if br.offset+2 > len(br.data) {
		return 0, io.EOF
	}
	v := binary.LittleEndian.Uint16(br.data[br.offset:])
	br.offset += 2
	return v, nil
}

// ReadUint32 reads an unsigned 32-bit little-endian integer.
func (br *BufferReader) ReadUint32() (uint32, error) {
	if br.offset+4 > len(br.data) {
		return 0, io.EOF
	}
	v := binary.LittleEndian.Uint32(br.data[br.offset:])
	br.offset += 4
	return v, nil
}

// ReadString reads a null-terminated string.
func (br *BufferReader) ReadString() (string, error) {
	start := br.offset
	for br.offset < len(br.data) {
		if br.data[br.offset] == 0 {
			str := string(br.data[start:br.offset])
			br.offset++ // skip null terminator
			return str, nil
		}
		br.offset++
	}
	return "", fmt.Errorf("unterminated string at offset %d", start)
}

// The remaining typed readers all delegate to the shared free functions.
func (br *BufferReader) ReadInt8() (int8, error)       { return readInt8(br) }
func (br *BufferReader) ReadInt16() (int16, error)     { return readInt16(br) }
func (br *BufferReader) ReadInt32() (int32, error)     { return readInt32(br) }
func (br *BufferReader) ReadFloat32() (float32, error) { return readFloat32(br) }
func (br *BufferReader) ReadCoord() (float32, error)   { return readCoord(br) }
func (br *BufferReader) ReadFloatCoord() (float32, error) {
	return br.ReadFloat32()
}
func (br *BufferReader) ReadAngle() (float32, error)   { return readAngle(br) }
func (br *BufferReader) ReadAngle16() (float32, error) { return readAngle16(br) }

// Skip skips n bytes
func (br *BufferReader) Skip(n int) error {
	if br.offset+n > len(br.data) {
		return io.EOF
	}
	br.offset += n
	return nil
}

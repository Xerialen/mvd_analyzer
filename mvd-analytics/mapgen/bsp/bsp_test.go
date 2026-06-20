package bsp

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// buildFixture assembles a minimal but valid Q1 BSP byte slice containing
// one plane, four vertices, one face (quad), four edges, four surfedges,
// and one model. Returned offsets are used by tests to sanity-check the
// parser against known inputs.
func buildFixture() []byte {
	// Lump payloads (little-endian).
	var planes []byte
	// Plane: normal (0,0,1), dist 0, type 2 (PLANE_Z).
	planes = appendF32(planes, 0, 0, 1)
	planes = appendF32(planes, 0)
	planes = appendI32(planes, 2)

	var verts []byte
	verts = appendF32(verts, 0, 0, 0)
	verts = appendF32(verts, 64, 0, 0)
	verts = appendF32(verts, 64, 64, 0)
	verts = appendF32(verts, 0, 64, 0)

	var faces []byte
	// planeId=0, side=0, firstEdge=0, numEdges=4, texinfoId=0, styles=00 00 00 00, lightofs=-1
	faces = appendU16(faces, 0)
	faces = appendU16(faces, 0)
	faces = appendI32(faces, 0)
	faces = appendU16(faces, 4)
	faces = appendU16(faces, 0)
	faces = append(faces, 0, 0, 0, 0)
	faces = appendI32(faces, -1)

	// Edges: (v0,v1), (v1,v2), (v2,v3), (v3,v0)
	var edges []byte
	edges = appendU16(edges, 0, 1)
	edges = appendU16(edges, 1, 2)
	edges = appendU16(edges, 2, 3)
	edges = appendU16(edges, 3, 0)

	// Surfedges: 0, 1, 2, 3 (positive → forward reading)
	var surfedges []byte
	surfedges = appendI32(surfedges, 0, 1, 2, 3)

	// Models: one worldspawn model whose face range covers the single face.
	var models []byte
	models = appendF32(models, 0, 0, 0)    // mins
	models = appendF32(models, 64, 64, 0)  // maxs
	models = appendF32(models, 0, 0, 0)    // origin
	models = appendI32(models, 0, 0, 0, 0) // headnodes
	models = appendI32(models, 1)          // visLeafs
	models = appendI32(models, 0)          // firstFace
	models = appendI32(models, 1)          // numFaces

	// Texinfo: one 40-byte record whose miptex index is 0. The projection
	// vectors and flags are zero (unused by the parser).
	var texinfo []byte
	texinfo = appendF32(texinfo, 0, 0, 0, 0, 0, 0, 0, 0) // vecs[2][4]
	texinfo = appendI32(texinfo, 0)                       // miptex
	texinfo = appendI32(texinfo, 0)                       // flags

	// Miptex: dmiptexlump_t with one entry "floor1". Layout: num(4),
	// dataofs[0](4)=8, then the miptex_t whose first 16 bytes are the name.
	var miptex []byte
	miptex = appendI32(miptex, 1) // nummiptex
	miptex = appendI32(miptex, 8) // dataofs[0] → start of the miptex_t
	miptex = append(miptex, texName16("floor1")...)

	// Clipnodes (v29 dclipnode_t: planenum int32 + 2×int16). Two nodes
	// exercising both child kinds: node 0 routes front→EMPTY, back→node 1;
	// node 1 routes front→EMPTY, back→SOLID. Negative children must
	// sign-extend to the CONTENTS_* codes.
	var clip []byte
	clip = appendI32(clip, 0)      // node 0 planenum
	clip = appendI16(clip, -1, 1)  // children: EMPTY, node 1
	clip = appendI32(clip, 0)      // node 1 planenum
	clip = appendI16(clip, -1, -2) // children: EMPTY, SOLID

	// Assemble header: version + 15 dentries.
	lumps := make([][]byte, numLumps)
	lumps[lumpPlanes] = planes
	lumps[lumpVertexes] = verts
	lumps[lumpFaces] = faces
	lumps[lumpEdges] = edges
	lumps[lumpSurfedges] = surfedges
	lumps[lumpModels] = models
	lumps[lumpClipnodes] = clip
	lumps[lumpTexinfo] = texinfo
	lumps[lumpMiptex] = miptex

	headerSize := 4 + numLumps*8
	offsets := make([]int, numLumps)
	cursor := headerSize
	for i := 0; i < numLumps; i++ {
		if len(lumps[i]) == 0 {
			offsets[i] = cursor
			continue
		}
		offsets[i] = cursor
		cursor += len(lumps[i])
	}

	out := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(out[0:4], uint32(Q1BSPVersion))
	for i := 0; i < numLumps; i++ {
		base := 4 + i*8
		binary.LittleEndian.PutUint32(out[base:base+4], uint32(offsets[i]))
		binary.LittleEndian.PutUint32(out[base+4:base+8], uint32(len(lumps[i])))
	}
	for i := 0; i < numLumps; i++ {
		out = append(out, lumps[i]...)
	}
	return out
}

func appendF32(dst []byte, vs ...float32) []byte {
	var buf [4]byte
	for _, v := range vs {
		binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
		dst = append(dst, buf[:]...)
	}
	return dst
}

func appendI32(dst []byte, vs ...int32) []byte {
	var buf [4]byte
	for _, v := range vs {
		binary.LittleEndian.PutUint32(buf[:], uint32(v))
		dst = append(dst, buf[:]...)
	}
	return dst
}

func appendI16(dst []byte, vs ...int16) []byte {
	var buf [2]byte
	for _, v := range vs {
		binary.LittleEndian.PutUint16(buf[:], uint16(v))
		dst = append(dst, buf[:]...)
	}
	return dst
}

func appendU16(dst []byte, vs ...uint16) []byte {
	var buf [2]byte
	for _, v := range vs {
		binary.LittleEndian.PutUint16(buf[:], v)
		dst = append(dst, buf[:]...)
	}
	return dst
}

// texName16 returns name padded with NULs to the 16-byte miptex_t.name field.
func texName16(name string) []byte {
	b := make([]byte, miptexNameLen)
	copy(b, name)
	return b
}

func TestParseBytes_Fixture(t *testing.T) {
	data := buildFixture()
	bsp, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if bsp.Version != Q1BSPVersion {
		t.Errorf("version = %d, want %d", bsp.Version, Q1BSPVersion)
	}
	if len(bsp.Planes) != 1 {
		t.Errorf("planes = %d, want 1", len(bsp.Planes))
	}
	if bsp.Planes[0].Normal.Z != 1 {
		t.Errorf("plane normal Z = %v, want 1", bsp.Planes[0].Normal.Z)
	}
	if len(bsp.Vertices) != 4 {
		t.Errorf("vertices = %d, want 4", len(bsp.Vertices))
	}
	if bsp.Vertices[2].X != 64 || bsp.Vertices[2].Y != 64 {
		t.Errorf("vertex[2] = %+v, want (64,64,0)", bsp.Vertices[2])
	}
	if len(bsp.Faces) != 1 {
		t.Errorf("faces = %d, want 1", len(bsp.Faces))
	}
	if bsp.Faces[0].NumEdges != 4 {
		t.Errorf("face numEdges = %d, want 4", bsp.Faces[0].NumEdges)
	}
	if len(bsp.Edges) != 4 {
		t.Errorf("edges = %d, want 4", len(bsp.Edges))
	}
	if len(bsp.Surfedges) != 4 {
		t.Errorf("surfedges = %d, want 4", len(bsp.Surfedges))
	}
	if len(bsp.Models) != 1 {
		t.Errorf("models = %d, want 1", len(bsp.Models))
	}
	if bsp.Models[0].FirstFace != 0 || bsp.Models[0].NumFaces != 1 {
		t.Errorf("model[0] face range = (%d,%d), want (0,1)",
			bsp.Models[0].FirstFace, bsp.Models[0].NumFaces)
	}
}

func TestParseBytes_Clipnodes(t *testing.T) {
	bsp, err := ParseBytes(buildFixture())
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(bsp.ClipNodes) != 2 {
		t.Fatalf("clipnodes = %d, want 2", len(bsp.ClipNodes))
	}
	if bsp.ClipNodes[0].Children != [2]int32{-1, 1} {
		t.Errorf("node 0 children = %v, want [-1 1]", bsp.ClipNodes[0].Children)
	}
	if bsp.ClipNodes[1].Children != [2]int32{-1, -2} {
		t.Errorf("node 1 children = %v, want [-1 -2] (EMPTY, SOLID)", bsp.ClipNodes[1].Children)
	}
}

func TestParseBytes_RejectsNonQ1(t *testing.T) {
	t.Run("IBSP", func(t *testing.T) {
		data := make([]byte, 4+numLumps*8)
		copy(data[:4], []byte("IBSP"))
		_, err := ParseBytes(data)
		if err == nil || !strings.Contains(err.Error(), "IBSP") {
			t.Fatalf("expected IBSP error, got %v", err)
		}
	})

	t.Run("v30", func(t *testing.T) {
		data := make([]byte, 4+numLumps*8)
		binary.LittleEndian.PutUint32(data[0:4], 30)
		_, err := ParseBytes(data)
		if err == nil || !strings.Contains(err.Error(), "unsupported version 30") {
			t.Errorf("expected unsupported-version error, got %v", err)
		}
	})
}

func TestParseBytes_TooShort(t *testing.T) {
	_, err := ParseBytes([]byte{29, 0, 0, 0})
	if err == nil {
		t.Fatal("expected too-short error")
	}
}

func TestParseBytes_TexNames(t *testing.T) {
	bsp, err := ParseBytes(buildFixture())
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(bsp.Texinfos) != 1 {
		t.Fatalf("texinfos = %d, want 1", len(bsp.Texinfos))
	}
	if len(bsp.TexNames) != 1 || bsp.TexNames[0] != "floor1" {
		t.Fatalf("texNames = %#v, want [floor1]", bsp.TexNames)
	}
	if got := bsp.FaceTexName(bsp.Faces[0]); got != "floor1" {
		t.Errorf("FaceTexName = %q, want \"floor1\"", got)
	}
	// A face pointing past the texinfo table resolves to "" (no panic).
	if got := bsp.FaceTexName(Face{TexinfoID: 99}); got != "" {
		t.Errorf("out-of-range FaceTexName = %q, want \"\"", got)
	}
}

func TestParseMiptexNames_Robust(t *testing.T) {
	// Valid single entry.
	b := appendI32(nil, 1, 8)
	b = append(b, texName16("water1")...)
	if got := parseMiptexNames(b); len(got) != 1 || got[0] != "water1" {
		t.Errorf("valid: got %#v, want [water1]", got)
	}

	// Empty / too-short lumps degrade to nil.
	if got := parseMiptexNames(nil); got != nil {
		t.Errorf("nil lump: got %#v, want nil", got)
	}
	if got := parseMiptexNames([]byte{1, 2}); got != nil {
		t.Errorf("2-byte lump: got %#v, want nil", got)
	}

	// Count claims 1000 entries but the lump holds only one offset slot:
	// clamp to what fits, never index past the slice.
	huge := appendI32(nil, 1000, -1)
	got := parseMiptexNames(huge)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("over-count: got %#v, want one empty name", got)
	}

	// Offset −1 (missing slot) and an offset past EOF both yield "".
	mixed := appendI32(nil, 2, -1, 9999)
	mixed = append(mixed, texName16("ignored")...)
	got = parseMiptexNames(mixed)
	if len(got) != 2 || got[0] != "" || got[1] != "" {
		t.Errorf("missing/out-of-range: got %#v, want two empty names", got)
	}
}

func TestParseTexinfos_TrailingPartial(t *testing.T) {
	// One full 40-byte record (miptex=3) plus 7 trailing junk bytes: the
	// partial record is ignored, the full one decoded.
	b := make([]byte, texinfoSize)
	binary.LittleEndian.PutUint32(b[32:36], 3)
	b = append(b, 1, 2, 3, 4, 5, 6, 7)
	got := parseTexinfos(b)
	if len(got) != 1 || got[0].MipTex != 3 {
		t.Errorf("got %#v, want one texinfo miptex=3", got)
	}
}

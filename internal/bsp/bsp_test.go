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

	// Assemble header: version + 15 dentries.
	lumps := make([][]byte, numLumps)
	lumps[lumpPlanes] = planes
	lumps[lumpVertexes] = verts
	lumps[lumpFaces] = faces
	lumps[lumpEdges] = edges
	lumps[lumpSurfedges] = surfedges
	lumps[lumpModels] = models

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

func appendU16(dst []byte, vs ...uint16) []byte {
	var buf [2]byte
	for _, v := range vs {
		binary.LittleEndian.PutUint16(buf[:], v)
		dst = append(dst, buf[:]...)
	}
	return dst
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

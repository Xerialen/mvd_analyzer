// Package bsp implements a minimal parser for Quake 1 BSP files.
//
// Only the lumps required for floor-geometry extraction by the sibling
// mapgeom package are decoded: vertices, edges, surfedges, faces, planes,
// and models. Anything else is skipped.
//
// Three flavors are accepted:
//   - Q1 v29 (the original Quake format, little-endian int32 version 29)
//   - "BSP2" magic (extended BSP2 — widened edges/faces/nodes/leafs)
//   - "2PSB" magic (BSP2 29a — intermediate widened variant)
//
// Non-Q1 formats are rejected:
//   - version 30 (Half-Life) → error
//   - "IBSP" magic (Quake 2+) → error
package bsp

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// Q1BSPVersion is the classic Quake 1 BSP file version.
const Q1BSPVersion = 29

// HLBSPVersion is the HL/GoldSrc BSP version. A handful of QW maps ship
// in this format; its entities lump shares Q1's header layout, so the
// entity reader accepts it (the geometry parser does not).
const HLBSPVersion = 30

// Parse reads a Quake 1 BSP v29 file from the given path and returns the
// decoded lumps we care about.
func Parse(path string) (*BSP, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bsp: read %s: %w", path, err)
	}
	return ParseBytes(data)
}

// ParseBytes decodes a BSP from an in-memory byte slice. Used by tests
// and by callers that already have the bytes.
func ParseBytes(data []byte) (*BSP, error) {
	if len(data) < 4+numLumps*8 {
		return nil, fmt.Errorf("bsp: file too short (%d bytes)", len(data))
	}

	// Magic / version sniffing. Q1 v29 starts with the little-endian
	// int32 29. BSP2 and 2PSB use ASCII magics in the same field.
	// Anything else is rejected with a specific error so bad files
	// never silently produce empty output.
	magic := string(data[:4])
	wideEdges := false // BSP2/29a widen edges and faces to 32-bit.
	version := int32(binary.LittleEndian.Uint32(data[0:4]))
	switch {
	case magic == "BSP2" || magic == "2PSB":
		wideEdges = true
	case magic == "IBSP":
		return nil, fmt.Errorf("bsp: IBSP format not supported")
	case version != Q1BSPVersion:
		return nil, fmt.Errorf("bsp: unsupported version %d (expected %d, \"BSP2\", or \"2PSB\")", version, Q1BSPVersion)
	}

	// Read lump directory: 15 entries of (offset, length) int32.
	type dentry struct {
		offset int32
		length int32
	}
	var dirs [numLumps]dentry
	for i := 0; i < numLumps; i++ {
		base := 4 + i*8
		dirs[i].offset = int32(binary.LittleEndian.Uint32(data[base : base+4]))
		dirs[i].length = int32(binary.LittleEndian.Uint32(data[base+4 : base+8]))
	}

	lumpBytes := func(idx int) ([]byte, error) {
		d := dirs[idx]
		if d.offset < 0 || d.length < 0 {
			return nil, fmt.Errorf("bsp: lump %d has negative offset/length", idx)
		}
		end := int64(d.offset) + int64(d.length)
		if end > int64(len(data)) {
			return nil, fmt.Errorf("bsp: lump %d extends past EOF (%d > %d)", idx, end, len(data))
		}
		return data[d.offset:end], nil
	}

	bsp := &BSP{Version: version}

	// PLANES
	planeBytes, err := lumpBytes(lumpPlanes)
	if err != nil {
		return nil, err
	}
	if len(planeBytes)%planeSize != 0 {
		return nil, fmt.Errorf("bsp: planes lump size %d not a multiple of %d", len(planeBytes), planeSize)
	}
	bsp.Planes = make([]Plane, len(planeBytes)/planeSize)
	for i := range bsp.Planes {
		b := planeBytes[i*planeSize:]
		bsp.Planes[i] = Plane{
			Normal: Vec3{
				X: readF32(b[0:4]),
				Y: readF32(b[4:8]),
				Z: readF32(b[8:12]),
			},
			Dist: readF32(b[12:16]),
			Type: int32(binary.LittleEndian.Uint32(b[16:20])),
		}
	}

	// VERTEXES
	vertBytes, err := lumpBytes(lumpVertexes)
	if err != nil {
		return nil, err
	}
	if len(vertBytes)%vertexSize != 0 {
		return nil, fmt.Errorf("bsp: vertexes lump size %d not a multiple of %d", len(vertBytes), vertexSize)
	}
	bsp.Vertices = make([]Vec3, len(vertBytes)/vertexSize)
	for i := range bsp.Vertices {
		b := vertBytes[i*vertexSize:]
		bsp.Vertices[i] = Vec3{
			X: readF32(b[0:4]),
			Y: readF32(b[4:8]),
			Z: readF32(b[8:12]),
		}
	}

	// FACES — layout depends on BSP flavor. v29 dface_t is 20 bytes
	// with 16-bit indices; BSP2/29a dface29a_t is 28 bytes with all
	// indices widened to 32 bits. Styles and lightofs stay the same.
	faceBytes, err := lumpBytes(lumpFaces)
	if err != nil {
		return nil, err
	}
	faceStride := faceSize
	if wideEdges {
		faceStride = faceSize29a
	}
	if len(faceBytes)%faceStride != 0 {
		return nil, fmt.Errorf("bsp: faces lump size %d not a multiple of %d", len(faceBytes), faceStride)
	}
	bsp.Faces = make([]Face, len(faceBytes)/faceStride)
	for i := range bsp.Faces {
		b := faceBytes[i*faceStride:]
		var f Face
		if wideEdges {
			f = Face{
				PlaneID:   binary.LittleEndian.Uint32(b[0:4]),
				Side:      binary.LittleEndian.Uint32(b[4:8]),
				FirstEdge: int32(binary.LittleEndian.Uint32(b[8:12])),
				NumEdges:  binary.LittleEndian.Uint32(b[12:16]),
				TexinfoID: binary.LittleEndian.Uint32(b[16:20]),
				LightOfs:  int32(binary.LittleEndian.Uint32(b[24:28])),
			}
			copy(f.Styles[:], b[20:24])
		} else {
			f = Face{
				PlaneID:   uint32(binary.LittleEndian.Uint16(b[0:2])),
				Side:      uint32(binary.LittleEndian.Uint16(b[2:4])),
				FirstEdge: int32(binary.LittleEndian.Uint32(b[4:8])),
				NumEdges:  uint32(binary.LittleEndian.Uint16(b[8:10])),
				TexinfoID: uint32(binary.LittleEndian.Uint16(b[10:12])),
				LightOfs:  int32(binary.LittleEndian.Uint32(b[16:20])),
			}
			copy(f.Styles[:], b[12:16])
		}
		bsp.Faces[i] = f
	}

	// EDGES — v29 uses 2×uint16 (4 bytes); BSP2/29a uses 2×uint32
	// (8 bytes).
	edgeBytes, err := lumpBytes(lumpEdges)
	if err != nil {
		return nil, err
	}
	edgeStride := edgeSize
	if wideEdges {
		edgeStride = edgeSize29a
	}
	if len(edgeBytes)%edgeStride != 0 {
		return nil, fmt.Errorf("bsp: edges lump size %d not a multiple of %d", len(edgeBytes), edgeStride)
	}
	bsp.Edges = make([]Edge, len(edgeBytes)/edgeStride)
	for i := range bsp.Edges {
		b := edgeBytes[i*edgeStride:]
		if wideEdges {
			bsp.Edges[i] = Edge{V: [2]uint32{
				binary.LittleEndian.Uint32(b[0:4]),
				binary.LittleEndian.Uint32(b[4:8]),
			}}
		} else {
			bsp.Edges[i] = Edge{V: [2]uint32{
				uint32(binary.LittleEndian.Uint16(b[0:2])),
				uint32(binary.LittleEndian.Uint16(b[2:4])),
			}}
		}
	}

	// SURFEDGES
	seBytes, err := lumpBytes(lumpSurfedges)
	if err != nil {
		return nil, err
	}
	if len(seBytes)%surfedgeSize != 0 {
		return nil, fmt.Errorf("bsp: surfedges lump size %d not a multiple of %d", len(seBytes), surfedgeSize)
	}
	bsp.Surfedges = make([]int32, len(seBytes)/surfedgeSize)
	for i := range bsp.Surfedges {
		bsp.Surfedges[i] = int32(binary.LittleEndian.Uint32(seBytes[i*surfedgeSize:]))
	}

	// MODELS
	modelBytes, err := lumpBytes(lumpModels)
	if err != nil {
		return nil, err
	}
	if len(modelBytes)%modelSize != 0 {
		return nil, fmt.Errorf("bsp: models lump size %d not a multiple of %d", len(modelBytes), modelSize)
	}
	bsp.Models = make([]Model, len(modelBytes)/modelSize)
	for i := range bsp.Models {
		b := modelBytes[i*modelSize:]
		m := Model{
			Mins:      Vec3{X: readF32(b[0:4]), Y: readF32(b[4:8]), Z: readF32(b[8:12])},
			Maxs:      Vec3{X: readF32(b[12:16]), Y: readF32(b[16:20]), Z: readF32(b[20:24])},
			Origin:    Vec3{X: readF32(b[24:28]), Y: readF32(b[28:32]), Z: readF32(b[32:36])},
			VisLeafs:  int32(binary.LittleEndian.Uint32(b[52:56])),
			FirstFace: int32(binary.LittleEndian.Uint32(b[56:60])),
			NumFaces:  int32(binary.LittleEndian.Uint32(b[60:64])),
		}
		for j := 0; j < 4; j++ {
			m.HeadNodes[j] = int32(binary.LittleEndian.Uint32(b[36+j*4 : 40+j*4]))
		}
		bsp.Models[i] = m
	}

	// CLIPNODES — the collision-hull tree(s). v29 uses 2×int16 children
	// (8-byte record); BSP2/2PSB widen children to 2×int32 (12-byte
	// record). Children are signed: negative values are CONTENTS_* leaf
	// codes, not node indices, so int16 children are sign-extended.
	clipBytes, err := lumpBytes(lumpClipnodes)
	if err != nil {
		return nil, err
	}
	clipStride := clipnodeSize
	if wideEdges {
		clipStride = clipnodeSize29a
	}
	if len(clipBytes)%clipStride != 0 {
		return nil, fmt.Errorf("bsp: clipnodes lump size %d not a multiple of %d", len(clipBytes), clipStride)
	}
	bsp.ClipNodes = make([]ClipNode, len(clipBytes)/clipStride)
	for i := range bsp.ClipNodes {
		b := clipBytes[i*clipStride:]
		cn := ClipNode{PlaneNum: int32(binary.LittleEndian.Uint32(b[0:4]))}
		if wideEdges {
			cn.Children[0] = int32(binary.LittleEndian.Uint32(b[4:8]))
			cn.Children[1] = int32(binary.LittleEndian.Uint32(b[8:12]))
		} else {
			cn.Children[0] = int32(int16(binary.LittleEndian.Uint16(b[4:6])))
			cn.Children[1] = int32(int16(binary.LittleEndian.Uint16(b[6:8])))
		}
		bsp.ClipNodes[i] = cn
	}

	return bsp, nil
}

// readF32 decodes a little-endian IEEE-754 float32.
func readF32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

// Assert io.Reader-friendly path for completeness (currently unused but
// keeps the package honest — ParseBytes is the workhorse).
var _ = io.EOF

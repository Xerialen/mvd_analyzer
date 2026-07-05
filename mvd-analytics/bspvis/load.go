// Package bspvis loads the Quake 1 BSP lumps required for the
// visibility-aware loc attribution queries used by the locvis package:
//
//   - Nodes and Leaves: the visibility BSP, walked by PointInLeaf and
//     RayHitsSolid.
//   - Planes: re-decoded here (the existing mvd-analytics/mapgen/bsp
//     parser also reads them; we re-read so this package stays
//     standalone — it consumes a different lump set, intended for
//     visibility rather than floor geometry).
//   - Visibility: raw RLE bytes, decompressed lazily per leaf.
//   - Models: only Models[0].HeadNodes[0] is consumed (the worldspawn
//     vis-BSP root); the rest is kept for completeness.
//
// Three on-disk flavours are accepted:
//
//   - Q1 v29 (the classic format, version field == 29)
//   - "2PSB" magic (BSP2 29a, intermediate widened variant)
//   - "BSP2" magic (fully widened, with float32 mins/maxs)
//
// Half-Life (version 30) and Quake 2+ ("IBSP") are rejected. Everything
// is little-endian. No cgo, only the stdlib.
//
// Promoted from experiments/locattr/bsputil (research-quality, same API).
// See experiments/locattr/RESEARCH_BSP.md for the byte-level spec and
// reference C in mvdsv/src/{bspfile.h,cmodel.c}.
package bspvis

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Vec3 is a 3D vector in world units (the same scale as the rest of the
// loc-attribution pipeline — raw .loc coordinates divided by 8).
type Vec3 struct {
	X, Y, Z float32
}

// Plane is the BSP plane lump record (20 bytes on disk on every flavour).
type Plane struct {
	Normal Vec3
	Dist   float32
	Type   int32
}

// Node is the visibility BSP node, widened to the BSP2 layout. v29 and
// 2PSB int16 mins/maxs are zero-extended to float32 at parse time so
// downstream traversal code can ignore the difference.
//
// Children are stored signed:
//
//	c >= 0  -> Nodes[c] (interior node)
//	c <  0  -> Leaves[-1 - c] (leaf; -1 means leaf 0 = CONTENTS_SOLID)
type Node struct {
	PlaneID   uint32
	Children  [2]int32
	Mins      Vec3
	Maxs      Vec3
	FirstFace uint32
	NumFaces  uint32
}

// Leaf is the visibility BSP leaf, widened to the BSP2 layout. Contents
// is one of CONTENTS_EMPTY (-1), CONTENTS_SOLID (-2), CONTENTS_WATER
// (-3), CONTENTS_SLIME (-4), CONTENTS_LAVA (-5), CONTENTS_SKY (-6); see
// the constants in pointinleaf.go.
//
// VisOfs is the byte offset into VisData where this leaf's RLE PVS row
// starts; -1 means "no vis info, treat as all visible".
type Leaf struct {
	Contents         int32
	VisOfs           int32
	Mins             Vec3
	Maxs             Vec3
	FirstMarkSurface uint32
	NumMarkSurfaces  uint32
	Ambient          [4]byte
}

// Model is the BSP model lump record. Only Models[0] (worldspawn) is
// used by this package; HeadNodes[0] is the root of the visibility BSP
// that PointInLeaf and RayHitsSolid descend.
type Model struct {
	Mins      Vec3
	Maxs      Vec3
	Origin    Vec3
	HeadNodes [4]int32
	VisLeafs  int32
	FirstFace int32
	NumFaces  int32
}

// BSP holds the lumps consumed by the spatial queries in this package.
// Only the fields documented here are populated; brush geometry
// (vertices, edges, surfedges, faces) lives in the production
// mvd-analytics/mapgen/bsp parser and is not duplicated.
type BSP struct {
	Version string // "v29" | "2PSB" | "BSP2"
	Planes  []Plane
	Nodes   []Node
	Leaves  []Leaf
	VisData []byte // raw RLE PVS bytes, decompressed lazily
	Models  []Model
}

// Q1 BSP lump indices (matches mvdsv/src/bspfile.h:66-82).
const (
	lumpEntities     = 0
	lumpPlanes       = 1
	lumpTextures     = 2 // unused here
	lumpVertexes     = 3 // unused here
	lumpVisibility   = 4
	lumpNodes        = 5
	lumpTexinfo      = 6 // unused here
	lumpFaces        = 7 // unused here
	lumpLighting     = 8 // unused here
	lumpClipnodes    = 9 // unused here
	lumpLeaves       = 10
	lumpMarksurfaces = 11 // unused here
	lumpEdges        = 12 // unused here
	lumpSurfedges    = 13 // unused here
	lumpModels       = 14
	numLumps         = 15
)

// Per-lump on-disk strides for the three flavours.
const (
	planeSize = 20

	nodeSizeV29  = 24
	nodeSize2PSB = 32
	nodeSizeBSP2 = 44

	leafSizeV29  = 28
	leafSize2PSB = 32
	leafSizeBSP2 = 44

	modelSize = 64
)

// Load reads a BSP file from disk and returns the lumps required for
// PointInLeaf / RayHitsSolid / PVS lookups.
func Load(path string) (*BSP, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bspvis: read %s: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadBytes is the in-memory variant of Load — used by the WASM loader
// (host fetchBspSync returns raw bytes) and by tests that synthesise a
// fixture.
func LoadBytes(data []byte) (*BSP, error) {
	if len(data) < 4+numLumps*8 {
		return nil, fmt.Errorf("bspvis: file too short (%d bytes)", len(data))
	}

	magic := string(data[:4])
	version := int32(binary.LittleEndian.Uint32(data[0:4]))
	var flavour string
	switch {
	case magic == "BSP2":
		flavour = "BSP2"
	case magic == "2PSB":
		flavour = "2PSB"
	case magic == "IBSP":
		return nil, fmt.Errorf("bspvis: IBSP (Q2+) format not supported")
	case version == 29:
		flavour = "v29"
	default:
		return nil, fmt.Errorf("bspvis: unsupported version %d (expected 29, \"BSP2\", or \"2PSB\")", version)
	}

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
			return nil, fmt.Errorf("bspvis: lump %d has negative offset/length", idx)
		}
		end := int64(d.offset) + int64(d.length)
		if end > int64(len(data)) {
			return nil, fmt.Errorf("bspvis: lump %d extends past EOF (%d > %d)", idx, end, len(data))
		}
		return data[d.offset:end], nil
	}

	bsp := &BSP{Version: flavour}

	pb, err := lumpBytes(lumpPlanes)
	if err != nil {
		return nil, err
	}
	if len(pb)%planeSize != 0 {
		return nil, fmt.Errorf("bspvis: planes lump size %d not a multiple of %d", len(pb), planeSize)
	}
	bsp.Planes = make([]Plane, len(pb)/planeSize)
	for i := range bsp.Planes {
		b := pb[i*planeSize:]
		bsp.Planes[i] = Plane{
			Normal: Vec3{X: readF32(b[0:4]), Y: readF32(b[4:8]), Z: readF32(b[8:12])},
			Dist:   readF32(b[12:16]),
			Type:   int32(binary.LittleEndian.Uint32(b[16:20])),
		}
	}

	wideIdx := flavour != "v29"
	wideBounds := flavour == "BSP2"
	var nodeStride int
	switch flavour {
	case "v29":
		nodeStride = nodeSizeV29
	case "2PSB":
		nodeStride = nodeSize2PSB
	case "BSP2":
		nodeStride = nodeSizeBSP2
	}
	nb, err := lumpBytes(lumpNodes)
	if err != nil {
		return nil, err
	}
	if len(nb)%nodeStride != 0 {
		return nil, fmt.Errorf("bspvis: nodes lump size %d not a multiple of %d (%s)", len(nb), nodeStride, flavour)
	}
	bsp.Nodes = make([]Node, len(nb)/nodeStride)
	for i := range bsp.Nodes {
		b := nb[i*nodeStride:]
		var n Node
		n.PlaneID = binary.LittleEndian.Uint32(b[0:4])
		if wideIdx {
			n.Children[0] = int32(binary.LittleEndian.Uint32(b[4:8]))
			n.Children[1] = int32(binary.LittleEndian.Uint32(b[8:12]))
		} else {
			n.Children[0] = int32(int16(binary.LittleEndian.Uint16(b[4:6])))
			n.Children[1] = int32(int16(binary.LittleEndian.Uint16(b[6:8])))
		}
		switch {
		case wideBounds:
			n.Mins = Vec3{X: readF32(b[12:16]), Y: readF32(b[16:20]), Z: readF32(b[20:24])}
			n.Maxs = Vec3{X: readF32(b[24:28]), Y: readF32(b[28:32]), Z: readF32(b[32:36])}
			n.FirstFace = binary.LittleEndian.Uint32(b[36:40])
			n.NumFaces = binary.LittleEndian.Uint32(b[40:44])
		case wideIdx:
			n.Mins = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[12:14]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[14:16]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[16:18]))),
			}
			n.Maxs = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[18:20]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[20:22]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[22:24]))),
			}
			n.FirstFace = binary.LittleEndian.Uint32(b[24:28])
			n.NumFaces = binary.LittleEndian.Uint32(b[28:32])
		default:
			n.Mins = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[8:10]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[10:12]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[12:14]))),
			}
			n.Maxs = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[14:16]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[16:18]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[18:20]))),
			}
			n.FirstFace = uint32(binary.LittleEndian.Uint16(b[20:22]))
			n.NumFaces = uint32(binary.LittleEndian.Uint16(b[22:24]))
		}
		bsp.Nodes[i] = n
	}

	var leafStride int
	switch flavour {
	case "v29":
		leafStride = leafSizeV29
	case "2PSB":
		leafStride = leafSize2PSB
	case "BSP2":
		leafStride = leafSizeBSP2
	}
	lb, err := lumpBytes(lumpLeaves)
	if err != nil {
		return nil, err
	}
	if len(lb)%leafStride != 0 {
		return nil, fmt.Errorf("bspvis: leaves lump size %d not a multiple of %d (%s)", len(lb), leafStride, flavour)
	}
	bsp.Leaves = make([]Leaf, len(lb)/leafStride)
	for i := range bsp.Leaves {
		b := lb[i*leafStride:]
		var lf Leaf
		lf.Contents = int32(binary.LittleEndian.Uint32(b[0:4]))
		lf.VisOfs = int32(binary.LittleEndian.Uint32(b[4:8]))
		switch {
		case wideBounds:
			lf.Mins = Vec3{X: readF32(b[8:12]), Y: readF32(b[12:16]), Z: readF32(b[16:20])}
			lf.Maxs = Vec3{X: readF32(b[20:24]), Y: readF32(b[24:28]), Z: readF32(b[28:32])}
			lf.FirstMarkSurface = binary.LittleEndian.Uint32(b[32:36])
			lf.NumMarkSurfaces = binary.LittleEndian.Uint32(b[36:40])
			copy(lf.Ambient[:], b[40:44])
		case wideIdx:
			lf.Mins = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[8:10]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[10:12]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[12:14]))),
			}
			lf.Maxs = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[14:16]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[16:18]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[18:20]))),
			}
			lf.FirstMarkSurface = binary.LittleEndian.Uint32(b[20:24])
			lf.NumMarkSurfaces = binary.LittleEndian.Uint32(b[24:28])
			copy(lf.Ambient[:], b[28:32])
		default:
			lf.Mins = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[8:10]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[10:12]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[12:14]))),
			}
			lf.Maxs = Vec3{
				X: float32(int16(binary.LittleEndian.Uint16(b[14:16]))),
				Y: float32(int16(binary.LittleEndian.Uint16(b[16:18]))),
				Z: float32(int16(binary.LittleEndian.Uint16(b[18:20]))),
			}
			lf.FirstMarkSurface = uint32(binary.LittleEndian.Uint16(b[20:22]))
			lf.NumMarkSurfaces = uint32(binary.LittleEndian.Uint16(b[22:24]))
			copy(lf.Ambient[:], b[24:28])
		}
		bsp.Leaves[i] = lf
	}

	vb, err := lumpBytes(lumpVisibility)
	if err != nil {
		return nil, err
	}
	if len(vb) > 0 {
		bsp.VisData = make([]byte, len(vb))
		copy(bsp.VisData, vb)
	}

	mb, err := lumpBytes(lumpModels)
	if err != nil {
		return nil, err
	}
	if len(mb)%modelSize != 0 {
		return nil, fmt.Errorf("bspvis: models lump size %d not a multiple of %d", len(mb), modelSize)
	}
	bsp.Models = make([]Model, len(mb)/modelSize)
	for i := range bsp.Models {
		b := mb[i*modelSize:]
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

	if len(bsp.Models) == 0 {
		return nil, fmt.Errorf("bspvis: BSP has no models")
	}
	if len(bsp.Nodes) == 0 {
		return nil, fmt.Errorf("bspvis: BSP has no nodes")
	}
	if len(bsp.Leaves) == 0 {
		return nil, fmt.Errorf("bspvis: BSP has no leaves")
	}

	return bsp, nil
}

func readF32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

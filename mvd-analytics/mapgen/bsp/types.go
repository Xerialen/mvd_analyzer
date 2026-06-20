package bsp

// Q1 BSP structures for v29, "BSP2" and "2PSB" (29a). Only the lumps
// required for floor-geometry extraction are modeled; the rest are
// skipped intentionally.
//
// Widths match the widest variant (BSP2/29a). v29 values are zero-
// extended into the same fields at parse time so downstream code can
// ignore the distinction.

// Vec3 is a 3D vector in world units (matches BSP/.loc/player scale).
type Vec3 struct {
	X, Y, Z float32
}

// Plane is the BSP plane lump record (20 bytes on disk).
type Plane struct {
	Normal Vec3
	Dist   float32
	Type   int32
}

// Edge is the BSP edge lump record. Two vertex indices into the vertex
// lump — 16 bits on v29, 32 bits on BSP2/29a.
type Edge struct {
	V [2]uint32
}

// Face is the BSP face lump record. Widths match the BSP2/29a layout
// (dface29a_t); v29 values are zero-extended into the same fields.
type Face struct {
	PlaneID   uint32
	Side      uint32
	FirstEdge int32
	NumEdges  uint32
	TexinfoID uint32
	Styles    [4]byte
	LightOfs  int32
}

// Model is the BSP model lump record. We only need worldspawn (model 0)
// so we keep the bounding info trimmed but still parse the full 64-byte
// record to advance the cursor correctly.
type Model struct {
	Mins      Vec3
	Maxs      Vec3
	Origin    Vec3
	HeadNodes [4]int32
	VisLeafs  int32
	FirstFace int32
	NumFaces  int32
}

// ClipNode is one node of a collision hull (the CLIPNODES lump). PlaneNum
// indexes Planes; a child >= 0 is another ClipNode index, a child < 0 is a
// CONTENTS_* leaf value (CONTENTS_SOLID = -2, CONTENTS_EMPTY = -1, …). v29
// stores children as signed int16; BSP2/2PSB widen them to int32. Both are
// decoded into the same int32 fields at parse time (negative values are
// sign-extended so the contents codes survive).
//
// The lump holds the clipnodes for every collision hull (player hull 1,
// large hull 2) of every model concatenated; a hull is entered at its
// model's HeadNodes[hull] root. We keep the whole array verbatim and let
// the collision tracer (mapclip) walk it from the chosen root.
type ClipNode struct {
	PlaneNum int32
	Children [2]int32
}

// Texinfo is the BSP texinfo lump record (40 bytes on disk). Only the
// miptex index is decoded — the projection vectors and flags are not
// needed to classify liquid / trigger faces by texture name.
type Texinfo struct {
	MipTex int32 // index into the miptex directory (TexNames); -1 = none
}

// BSP holds the decoded lumps we care about. Texinfos and TexNames are
// best-effort: a malformed texture lump leaves them empty rather than
// failing the parse (floor-geometry extraction never needs textures).
type BSP struct {
	Version   int32
	Planes    []Plane
	Vertices  []Vec3
	Faces     []Face
	Edges     []Edge
	Surfedges []int32
	Models    []Model
	ClipNodes []ClipNode
	Texinfos  []Texinfo // by texinfo index (Face.TexinfoID)
	TexNames  []string  // by miptex index (Texinfo.MipTex)
}

// FaceTexName returns the texture name assigned to face f, or "" when the
// face has no resolvable texture: an out-of-range texinfo or miptex
// index, a −1 "missing" miptex slot, or a texture lump that failed to
// parse. Names are returned verbatim (engine convention is lowercase);
// callers do their own case-insensitive matching.
func (b *BSP) FaceTexName(f Face) string {
	if int(f.TexinfoID) >= len(b.Texinfos) {
		return ""
	}
	mt := b.Texinfos[f.TexinfoID].MipTex
	if mt < 0 || int(mt) >= len(b.TexNames) {
		return ""
	}
	return b.TexNames[mt]
}

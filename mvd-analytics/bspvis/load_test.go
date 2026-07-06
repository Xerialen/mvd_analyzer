package bspvis

import (
	"encoding/binary"
	"math"
	"testing"
)

// buildMinimalV29BSP synthesises the smallest well-formed Q1 v29 BSP the
// spatial queries accept: one plane, one node, two leaves (the solid sink
// plus one empty leaf), one worldspawn model rooted at node 0, and no vis
// data. It is used to exercise the load-time reference validation (F1)
// without depending on the gitignored bsps/ corpus.
func buildMinimalV29BSP() []byte {
	// Signed writers: constant conversions like uint16(int16(-2)) overflow
	// at compile time, so route negatives through typed runtime values.
	putI16 := func(b []byte, v int16) { binary.LittleEndian.PutUint16(b, uint16(v)) }
	putI32 := func(b []byte, v int32) { binary.LittleEndian.PutUint32(b, uint32(v)) }

	planes := make([]byte, planeSize)                                // normal (0,0,1), dist 0, type 2
	binary.LittleEndian.PutUint32(planes[8:12], math.Float32bits(1)) // normal.z
	binary.LittleEndian.PutUint32(planes[16:20], 2)                  // type = axial Z

	nodes := make([]byte, nodeSizeV29)
	binary.LittleEndian.PutUint32(nodes[0:4], 0) // PlaneID = 0
	putI16(nodes[4:6], -2)                       // child0 -> leaf 1 (-1-(-2)=1)
	putI16(nodes[6:8], -1)                       // child1 -> leaf 0 (solid sink)

	leaves := make([]byte, leafSizeV29*2)
	putI32(leaves[0:4], ContentsSolid)                       // leaf 0 solid
	putI32(leaves[4:8], -1)                                  // leaf 0 visofs -1
	putI32(leaves[leafSizeV29:leafSizeV29+4], ContentsEmpty) // leaf 1 empty
	putI32(leaves[leafSizeV29+4:leafSizeV29+8], -1)          // leaf 1 visofs -1

	models := make([]byte, modelSize) // HeadNodes[0] at offset 36 stays 0 -> root node 0

	buf := make([]byte, 4+numLumps*8)
	binary.LittleEndian.PutUint32(buf[0:4], 29)
	put := func(idx int, payload []byte) {
		off := len(buf)
		buf = append(buf, payload...)
		base := 4 + idx*8
		binary.LittleEndian.PutUint32(buf[base:base+4], uint32(off))
		binary.LittleEndian.PutUint32(buf[base+4:base+8], uint32(len(payload)))
	}
	put(lumpPlanes, planes)
	put(lumpNodes, nodes)
	put(lumpLeaves, leaves)
	put(lumpModels, models)
	// Visibility lump dentry stays (0,0) -> empty row, which LoadBytes allows.
	return buf
}

func TestLoadBytes_MinimalFixtureValid(t *testing.T) {
	b, err := LoadBytes(buildMinimalV29BSP())
	if err != nil {
		t.Fatalf("LoadBytes(valid minimal): %v", err)
	}
	if len(b.Nodes) != 1 || len(b.Leaves) != 2 || len(b.Planes) != 1 {
		t.Fatalf("nodes=%d leaves=%d planes=%d, want 1/2/1", len(b.Nodes), len(b.Leaves), len(b.Planes))
	}
	// A point above the plane resolves to the empty leaf, not a panic.
	if leaf := b.PointInLeaf([3]float32{0, 0, 10}); leaf != 1 {
		t.Errorf("PointInLeaf above plane = %d, want 1 (empty leaf)", leaf)
	}
}

// nodeFieldOffset returns the byte offset of node 0's payload within a
// fixture produced by buildMinimalV29BSP (the nodes lump start).
func nodeLumpOffset(t *testing.T, data []byte) int {
	t.Helper()
	base := 4 + lumpNodes*8
	return int(binary.LittleEndian.Uint32(data[base : base+4]))
}

func TestLoadBytes_RejectsOutOfRangePlaneID(t *testing.T) {
	data := buildMinimalV29BSP()
	off := nodeLumpOffset(t, data)
	binary.LittleEndian.PutUint32(data[off:off+4], 999) // PlaneID way past the 1 plane
	if _, err := LoadBytes(data); err == nil {
		t.Fatal("expected error for out-of-range Node.PlaneID, got nil (would panic at query time)")
	}
}

func TestLoadBytes_RejectsOutOfRangeChildNode(t *testing.T) {
	data := buildMinimalV29BSP()
	off := nodeLumpOffset(t, data)
	binary.LittleEndian.PutUint16(data[off+4:off+6], 7) // child0 -> node 7, only 1 node exists
	if _, err := LoadBytes(data); err == nil {
		t.Fatal("expected error for out-of-range child node index, got nil")
	}
}

func TestLoadBytes_RejectsTruncated(t *testing.T) {
	data := buildMinimalV29BSP()
	if _, err := LoadBytes(data[:len(data)-8]); err == nil {
		t.Fatal("expected error for truncated file (lump past EOF), got nil")
	}
}

func TestLoadBytes_RejectsOutOfRangeLeafChild(t *testing.T) {
	data := buildMinimalV29BSP()
	off := nodeLumpOffset(t, data)
	// Negative child c refers to leaf -1-c; -4 -> leaf 3, past the fixture's leaves.
	child := int16(-4)
	binary.LittleEndian.PutUint16(data[off+4:off+6], uint16(child))
	if _, err := LoadBytes(data); err == nil {
		t.Fatal("expected error for out-of-range leaf child, got nil")
	}
}

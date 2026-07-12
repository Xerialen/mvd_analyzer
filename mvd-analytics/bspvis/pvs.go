package bspvis

// LeafPVS returns the decompressed PVS (potentially visible set) bit
// vector for the given leaf. The returned slice has length
// (LeafCount + 7) / 8 bytes — one bit per leaf, big enough to address
// every leaf index including the unused leaf 0 sink.
//
// IMPORTANT off-by-one: in the on-disk format, "bit i" of leaf L's PVS
// row means "leaf L can see leaf (i + 1)" — bit 0 corresponds to leaf
// 1, not leaf 0 (leaf 0 is the universal CONTENTS_SOLID sink and has no
// vis row). See mvdsv/src/cmodel.c:1144 ("in++; // pvs row 0 is leaf
// 1"). PVSContains performs the shift so callers pass leaf indices
// directly.
//
// Special cases:
//   - leafIdx == 0 (the solid sink): returns an all-ones row.
//   - leafIdx out of range: all-ones row.
//   - leaf.VisOfs == -1: all-ones row.
//   - VisData empty: all-ones row.
//
// The decode mirrors DecompressVis (cmodel.c:1076-1107).
func (b *BSP) LeafPVS(leafIdx int) []byte {
	rowBytes := (len(b.Leaves) + 7) >> 3
	out := make([]byte, rowBytes)

	allVisible := func() []byte {
		for i := range out {
			out[i] = 0xff
		}
		return out
	}

	if leafIdx <= 0 || leafIdx >= len(b.Leaves) {
		return allVisible()
	}
	leaf := &b.Leaves[leafIdx]
	if leaf.VisOfs < 0 || len(b.VisData) == 0 {
		return allVisible()
	}
	if int(leaf.VisOfs) >= len(b.VisData) {
		return allVisible()
	}

	in := b.VisData[leaf.VisOfs:]
	wp := 0
	for wp < rowBytes && len(in) > 0 {
		if in[0] != 0 {
			out[wp] = in[0]
			in = in[1:]
			wp++
			continue
		}
		if len(in) < 2 {
			break
		}
		n := int(in[1])
		in = in[2:]
		wp += n
		if wp > rowBytes {
			wp = rowBytes
		}
	}
	return out
}

// PVSContains reports whether otherLeaf is set in the decompressed PVS
// row. The row is in the engine's native bit ordering: bit 0 is leaf 1,
// bit i is leaf (i + 1). This helper performs that shift so callers can
// pass leaf indices directly.
//
//   - otherLeaf <= 0 always returns true (solid sink / out-of-range).
//   - otherLeaf beyond the row size returns false.
func (b *BSP) PVSContains(pvsRow []byte, otherLeaf int) bool {
	if otherLeaf <= 0 {
		return true
	}
	bitIdx := otherLeaf - 1
	byteIdx := bitIdx >> 3
	if byteIdx < 0 || byteIdx >= len(pvsRow) {
		return false
	}
	return pvsRow[byteIdx]&(1<<uint(bitIdx&7)) != 0
}

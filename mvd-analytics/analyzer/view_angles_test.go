package analyzer

import "testing"

// recordPosition appends the raw angle16 view pitch/yaw alongside x/y/z,
// and toPlayerStream must surface them as the VP/VYa columns, bit-exact
// and length-aligned with T. The values are stored losslessly (the raw
// wire short), so a negative int16 round-trips unchanged.
func TestRecordPosition_CarriesViewAngles(t *testing.T) {
	var b streamBuilder
	b.recordPosition(100, 1, 2, 3, 1000, -2000)
	b.recordPosition(200, 4, 5, 6, 1100, -1900)

	ps := b.toPlayerStream("p", "red")
	if ps.Position == nil {
		t.Fatal("Position nil")
	}
	pt := ps.Position
	if len(pt.VP) != len(pt.T) || len(pt.VYa) != len(pt.T) {
		t.Fatalf("view columns not aligned with T: T=%d VP=%d VYa=%d",
			len(pt.T), len(pt.VP), len(pt.VYa))
	}
	if pt.VP[0] != 1000 || pt.VP[1] != 1100 || pt.VYa[0] != -2000 || pt.VYa[1] != -1900 {
		t.Errorf("view angles not preserved losslessly: VP=%v VYa=%v", pt.VP, pt.VYa)
	}
}

// The reconnect merge (appendSlice) must carry VP/VYa through alongside
// x/y/z, or a player's view direction is dropped across a reconnect —
// the same bug class that Li/H/Lq guard against.
func TestAppendSlice_CarriesViewAngles(t *testing.T) {
	var src streamBuilder
	src.recordPosition(100, 1, 2, 3, 1000, -2000)
	src.recordPosition(200, 4, 5, 6, 1100, -1900)

	var dst streamBuilder
	dst.appendSlice(&src, 0, 1000)

	if len(dst.posVP) != len(dst.posT) || len(dst.posVYa) != len(dst.posT) {
		t.Fatalf("merge dropped view columns: T=%d VP=%d VYa=%d",
			len(dst.posT), len(dst.posVP), len(dst.posVYa))
	}
	if dst.posVP[0] != 1000 || dst.posVYa[1] != -1900 {
		t.Errorf("view angles not merged: VP=%v VYa=%v", dst.posVP, dst.posVYa)
	}
}

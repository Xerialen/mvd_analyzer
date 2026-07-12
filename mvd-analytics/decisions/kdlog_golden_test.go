// A GOLDEN test that pins the C brain's real KDLOG emit format for the anchor +
// goal/enemy/evade grammars against their real consumer, ResolveKDLog. The
// existing TestResolveKDLog in kdlog_test.go feeds a HAND-TYPED log string, so it
// only proves parse is the inverse of a Go-authored emitter — it is blind to the
// thing that actually drifts: the KTX C brain's snprintf format (e.g. if
// `KDLOG_ANCHOR ... emitter=` or `type=goal ... chosen=`/`c1=` layout changed, a
// Go-vs-Go test still passes while every real run silently stopped resolving).
// This test closes that gap: testdata/golden-server.log is a verbatim excerpt of a
// REAL mvdsv+KTX run (kbot-0.28.0-dials, 2026-07-06, from the komodobots2-bv1
// bench). Feeding it through ResolveKDLog fails the moment the brain's emit drifts.
// The play/dial/weap/harvest KDLOG lines in the fixture are pinned by KomodoBench's
// own golden test; here they are exercised only as real co-resident records.
package decisions

import (
	"os"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

func goldenFixtureResult() *result.Result {
	return &result.Result{
		TimelineAnalysis: &result.TimelineAnalysisResult{
			PlayerSlots: map[string]int{
				"hib": 1, "dag": 2, "Angua": 3, "Rock": 4, "clan-enemy": 8,
			},
			LocTable: []string{"", "RA", "YA"},
		},
		Streams: &result.Streams{Players: []result.PlayerStream{
			{Name: "hib", Team: "red"},
			{Name: "dag", Team: "red"},
			{Name: "Angua", Team: "red"},
			{Name: "Rock", Team: "red"},
			{Name: "clan-enemy", Team: "blue"},
		}},
		Items: &result.ItemsResult{Items: []result.ItemTimeline{
			{Name: "ring", Kind: "ring", EntNum: 144, X: 240, Y: -32, Z: 56, Loc: "RING"},
			{Name: "ra", Kind: "ra", EntNum: 123, X: 256, Y: -704, Z: 304, Loc: "RA"},
			{Name: "ya", Kind: "ya", EntNum: 79, X: 1232, Y: -904, Z: -48, Loc: "YA"},
			{Name: "mh", Kind: "mh", EntNum: 53, X: 564, Y: -48, Z: -192, Loc: "MEGA"},
		}},
	}
}

func recByType(t *testing.T, dec *result.Decisions, typ string) result.DecisionRecord {
	t.Helper()
	var found []result.DecisionRecord
	for _, r := range dec.Records {
		if r.Type == typ {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 %q record, got %d", typ, len(found))
	}
	return found[0]
}

func TestGoldenServerLog(t *testing.T) {
	f, err := os.Open("testdata/golden-server.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dec, err := ResolveKDLog(goldenFixtureResult(), f)
	if err != nil {
		t.Fatal(err)
	}

	if dec.Source != "kdlog" || dec.EmitterVersion != "kbot-0.28.0-dials.d-adh05-tl20" || dec.DlogLevel != 1 {
		t.Fatalf("anchor not parsed: %+v", dec)
	}

	// This is what pins the real `type=<...>` tokens: if the brain renamed type=goal,
	// its count drops to 0 and this fails.
	census := map[string]int{}
	for _, r := range dec.Records {
		census[r.Type]++
	}
	if census["goal"] != 1 || census["enemy"] != 1 || census["evade"] != 1 || census["play"] != 6 || len(dec.Records) != 9 {
		t.Fatalf("record census wrong: got %v want goal=1 enemy=1 evade=1 play=6 total=9 (errors: %v)", census, dec.Errors)
	}

	// Console prose containing the substring "non-KDLOG" is noise, not a
	// telemetry record. Only a real line-start token (after an optional server
	// timestamp) may enter the parser.
	if len(dec.Errors) != 0 {
		t.Fatalf("want no errors from non-KDLOG console noise, got %d: %v", len(dec.Errors), dec.Errors)
	}

	g := recByType(t, dec, "goal")
	if g.Player != "hib" || g.Team != "red" || g.T != 19 || g.Trigger != "relocate" {
		t.Fatalf("goal identity wrong: %+v", g)
	}
	if g.Chosen == nil {
		t.Fatalf("goal chosen nil: %+v", g)
	}
	if g.Chosen.Kind != "ring" || g.Chosen.Name != "ring" || g.Chosen.Cls != "item_artifact_invisibility" ||
		g.Chosen.EntNum != 144 || g.Chosen.Loc != "RING" || g.Chosen.Marker != 65 ||
		g.Chosen.TravelMs != 2890 || g.Chosen.Score != 1030.886 {
		t.Fatalf("chosen not resolved: %+v", g.Chosen)
	}
	if len(g.Candidates) != 4 {
		t.Fatalf("want 4 candidates, got %d: %+v", len(g.Candidates), g.Candidates)
	}
	wantKinds := []string{"ring", "ra", "ya", "mh"}
	for i, want := range wantKinds {
		if g.Candidates[i].Kind != want {
			t.Fatalf("candidate[%d] kind = %q, want %q: %+v", i, g.Candidates[i].Kind, want, g.Candidates[i])
		}
	}
	if g.Candidates[1].Name != "ra" || g.Candidates[1].Loc != "RA" || g.Candidates[1].TravelMs != 12740 {
		t.Fatalf("candidate[1] not resolved: %+v", g.Candidates[1])
	}
	if g.State == nil {
		t.Fatalf("goal state nil: %+v", g)
	}
	if g.State.H != 100 || g.State.A != 0 || g.State.AW != "sg" {
		t.Fatalf("state not resolved: %+v", g.State)
	}

	e := recByType(t, dec, "enemy")
	if e.Player != "hib" || e.Target != "clan-enemy" || e.Dist != 194 {
		t.Fatalf("enemy not resolved: %+v", e)
	}

	v := recByType(t, dec, "evade")
	if v.Player != "Rock" || v.On == nil || *v.On != true {
		t.Fatalf("evade not resolved: %+v", v)
	}
}

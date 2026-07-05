package analyzer

import (
	"testing"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// newTestItemAnalyzer wires an analyzer for unit tests — skips the
// match-boundary detection by marking the match pre-started so every
// event we feed in is counted.
func newTestItemAnalyzer() (*ItemAnalyzer, *Context) {
	a := NewItemAnalyzer()
	ctx := &Context{FragsBySlot: map[int]int{}}
	_ = a.Init(ctx)
	a.timing.Started = true
	return a, ctx
}

// Single RA, one pickup and respawn. Confirms the happy path: phase
// opens at 0, closes on ItemStateEvent{Taken:true} with RespawnAt
// stamped from the kind table (TakenAt + 20 for armor), and a new
// available phase opens on ItemStateEvent{Taken:false}. The wire
// respawn time is NOT what drives RespawnAt any more — this test also
// pins that by deliberately using a late wire respawn (t=45) while
// asserting RespawnAt = 30.
func TestItemAnalyzer_RAPickupRespawn(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "nexus", Team: "ahoy"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{100, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 75, Kind: "ra", Origin: [3]float32{100, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 75, Kind: "ra", Taken: true, Time: 10})
	// Wire respawn 45 s later — 25 s past the real respawn time.
	// Insta-regrab simulation: we still want RespawnAt=30, not 45.
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 75, Kind: "ra", Taken: false, Time: 45})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	it := res.Items[0]
	if it.Kind != "ra" || it.EntNum != 75 {
		t.Errorf("item meta = %+v", it)
	}
	if len(it.Phases) != 2 {
		t.Fatalf("phases = %+v", it.Phases)
	}
	p0 := it.Phases[0]
	if p0.AvailableFrom != 0 || p0.TakenAt != 10000 || p0.TakenBy != "nexus" || p0.Team != "ahoy" {
		t.Errorf("phase[0] meta = %+v", p0)
	}
	if p0.RespawnAt != 30000 {
		t.Errorf("phase[0] RespawnAt = %v, want 30000 (TakenAt+20s)", p0.RespawnAt)
	}
	if it.Phases[1].AvailableFrom != 45000 || it.Phases[1].TakenAt != 0 {
		t.Errorf("phase[1] = %+v", it.Phases[1])
	}
}

// Quad with no wire respawn at all — insta-regrabbed every cycle.
// The kind-table fallback is the only thing that can produce a sensible
// RespawnAt; this test pins it at TakenAt + 60.
func TestItemAnalyzer_QuadNominalRespawn(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 128, 282}, Time: 16.6})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 43, Kind: "quad", Origin: [3]float32{0, 128, 282}, Time: 0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 43, Kind: "quad", Taken: true, Time: 16.692})
	// No wire respawn yet — quad was insta-regrabbed each cycle.

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	p0 := res.Items[0].Phases[0]
	got := p0.RespawnAt - p0.TakenAt
	if got < 59999 || got > 60001 {
		t.Errorf("quad RespawnAt - TakenAt = %v, want 60000", got)
	}
}

// Two MHs on the same map get separate phase timelines keyed by
// ent num. Names are deterministic ("mh_1", "mh_2") by x-coordinate.
// Each MH's RespawnAt is stamped 20 s after its holder's health drops
// to ≤ 100 — not from the wire respawn time.
func TestItemAnalyzer_TwoMHs(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "p1"}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "p2"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{1000, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 3, Origin: [3]float32{-1000, 0, 0}, Time: 10.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 20, Kind: "mh", Origin: [3]float32{1000, 0, 0}})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 21, Kind: "mh", Origin: [3]float32{-1000, 0, 0}})
	// MH 20 → p1 @ t=10; holder starts at 200 (primed so crossing is
	// observable), rots down past 100 at t=110.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 200, Time: 10})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 20, Kind: "mh", Taken: true, Time: 10})
	// MH 21 → p2 @ t=11; holder at 200, drops past 100 at t=90.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatHealth, Value: 200, Time: 11})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 21, Kind: "mh", Taken: true, Time: 11})

	// Rot-end crossings.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 100, Time: 110})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatHealth, Value: 100, Time: 90})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	if len(res.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(res.Items))
	}
	if res.Items[0].Name != "mh_1" || res.Items[1].Name != "mh_2" {
		t.Errorf("names = %q, %q", res.Items[0].Name, res.Items[1].Name)
	}
	// mh_1 is the one with X=-1000 (ent 21 → p2 picked up).
	mh1 := res.Items[0].Phases[0]
	if mh1.TakenBy != "p2" {
		t.Errorf("mh_1 picker = %q, want p2", mh1.TakenBy)
	}
	if mh1.RespawnAt != 110000 { // rot-end 90s + 20s
		t.Errorf("mh_1 RespawnAt = %v, want 110000 (90s + 20s)", mh1.RespawnAt)
	}
	mh2 := res.Items[1].Phases[0]
	if mh2.RespawnAt != 130000 { // rot-end 110s + 20s
		t.Errorf("mh_2 RespawnAt = %v, want 130000 (110s + 20s)", mh2.RespawnAt)
	}
}

// MH rot-end via health tick-down: holder picks up MH at t=10 with 200
// health; rot drains it to 100 at t=110; RespawnAt is then 130.
func TestItemAnalyzer_MHRotTickdown(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 50, Kind: "mh", Origin: [3]float32{0, 0, 0}})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 200, Time: 10})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 50, Kind: "mh", Taken: true, Time: 10})
	// Interim rot observations: still > 100.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 150, Time: 60})
	// Final crossing.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 100, Time: 110})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	p0 := res.Items[0].Phases[0]
	if p0.RespawnAt != 130000 {
		t.Errorf("MH RespawnAt = %v, want 130000 (crossing 110s + 20s)", p0.RespawnAt)
	}
}

// MH holder dies mid-rot: RespawnAt should be death+20 (assuming death
// comes more than 5 s after pickup).
func TestItemAnalyzer_MHHolderDiesMidRot(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 50, Kind: "mh", Origin: [3]float32{0, 0, 0}})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 200, Time: 10})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 50, Kind: "mh", Taken: true, Time: 10})
	// Holder dies at t=30 (way past the 5 s floor).
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 30})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	p0 := res.Items[0].Phases[0]
	if p0.RespawnAt != 50000 {
		t.Errorf("MH RespawnAt = %v, want 50000 (death 30s + 20s)", p0.RespawnAt)
	}
}

// MH holder instant-deaths inside the 5 s first-rot-tick floor: the
// respawn timer can't arm before pickup+5, so RespawnAt = pickup + 5 + 20.
func TestItemAnalyzer_MHInstantDeathFloor(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 50, Kind: "mh", Origin: [3]float32{0, 0, 0}})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 200, Time: 10})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 50, Kind: "mh", Taken: true, Time: 10})
	// Rocket to the face at t=10.1, instant death.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 0, Time: 10.1})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	p0 := res.Items[0].Phases[0]
	if p0.RespawnAt != 35000 { // pickup 10s + 5s (floor) + 20s
		t.Errorf("MH RespawnAt = %v, want 35000 (pickup+5+20 from the 5 s floor)", p0.RespawnAt)
	}
}

// Same player holds two MHs. KTX lets both be picked up; each has its
// own entity and rot tick, but both run against the same holder health.
// Our detection fires once on the health crossing and stamps both
// entities to the same RespawnAt.
func TestItemAnalyzer_TwoMHsSameHolder(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "hog"}

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 40, Kind: "mh", Origin: [3]float32{0, 0, 0}})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 41, Kind: "mh", Origin: [3]float32{1, 0, 0}})
	// Position samples interleave with the takes — the rolling history
	// only keeps ~1 s, so a sample must exist near each touch instant.
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 200, Time: 10})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 40, Kind: "mh", Taken: true, Time: 10})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 11.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 250, Time: 12})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 41, Kind: "mh", Taken: true, Time: 12})
	// Rot across both; crossing at t=80.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 100, Time: 80})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	if len(res.Items) != 2 {
		t.Fatalf("want 2 MH items, got %d", len(res.Items))
	}
	for _, it := range res.Items {
		if it.Phases[0].RespawnAt != 100000 {
			t.Errorf("%s RespawnAt = %v, want 100000 (crossing 80s + 20s)", it.Name, it.Phases[0].RespawnAt)
		}
	}
}

// Items with no pickup events still show up in the output with a
// single open "available from 0" phase — works on non-KTX demos and
// on items nobody touched.
func TestItemAnalyzer_UntouchedItemListed(t *testing.T) {
	a, _ := newTestItemAnalyzer()
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 50, Kind: "lg", Origin: [3]float32{0, 0, 0}})
	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	if len(res.Items[0].Phases) != 1 || res.Items[0].Phases[0].TakenAt != 0 {
		t.Errorf("untouched item phases = %+v", res.Items[0].Phases)
	}
}

// Pre-match events should be ignored so warmup item bouncing doesn't
// pollute the phase list.
func TestItemAnalyzer_PreMatchEventsIgnored(t *testing.T) {
	a := NewItemAnalyzer()
	ctx := &Context{FragsBySlot: map[int]int{}}
	_ = a.Init(ctx)
	// matchStarted left false.

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 75, Kind: "ra", Origin: [3]float32{0, 0, 0}})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 75, Kind: "ra", Taken: true, Time: 2})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 75, Kind: "ra", Taken: false, Time: 5})

	r := &Result{}
	_ = a.Finalize(r)
	out := r.Items
	res := out
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	// The item exists (we got the baseline spawn) but no phases
	// should have closed because the match never started.
	if res.Items[0].Phases[0].TakenAt != 0 {
		t.Errorf("phase shouldn't have closed during warmup: %+v", res.Items[0].Phases[0])
	}
}

// Layer 1 (KTX hint) wins over a closer-but-uninvolved player. The
// hint identifies the picker authoritatively by entity number, so
// even when slot 3 ('bystander') is at the item's origin and slot 2
// ('far_picker') is 500 u away, the hint pointing to slot 2 wins.
func TestItemAnalyzer_LayeredAttribution_HintWins(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "far_picker", Team: "ahoy"}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "bystander", Team: "bhb"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{500, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 3, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 80, Kind: "ra", Origin: [3]float32{0, 0, 0}, Time: 0})
	// PlayerEnt 3 is edict 3 = slot 2 = far_picker.
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 80, RespawnSec: 20, PlayerEnt: 3, Time: 10.0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 80, Kind: "ra", Taken: true, Time: 10.01})

	r := &Result{}
	_ = a.Finalize(r)
	p0 := r.Items.Items[0].Phases[0]
	if p0.TakenBy != "far_picker" {
		t.Errorf("hint should override distance, got %q", p0.TakenBy)
	}
	if got := a.AttributionCounts()["hint"]; got != 1 {
		t.Errorf("hint count = %d, want 1", got)
	}
}

// Layer 2 (per-client print). With no hint, the print message
// identifies the slot. Distance is irrelevant.
func TestItemAnalyzer_LayeredAttribution_PrintWhenNoHint(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "msg0player"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "closer"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 1, Origin: [3]float32{1000, 0, 0}, Time: 4.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{0, 0, 0}, Time: 4.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 81, Kind: "ya", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemPickupPrintEvent{PlayerNum: 1, Kind: "ya", Time: 5.0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 81, Kind: "ya", Taken: true, Time: 5.01})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "msg0player" {
		t.Errorf("print should attribute slot 1, got %q", got)
	}
	if got := a.AttributionCounts()["print"]; got != 1 {
		t.Errorf("print count = %d, want 1", got)
	}
}

// Layer 3 (stat delta). No hint, no print: the IT_ARMOR3 bit
// transition on slot 4 is the authoritative evidence; the closer
// slot 5 with no stat evidence must NOT win.
func TestItemAnalyzer_LayeredAttribution_StatDeltaWhenNoHintNoPrint(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[4] = &events.PlayerInfo{Slot: 4, Name: "real_picker"}
	ctx.Players[5] = &events.PlayerInfo{Slot: 5, Name: "bystander"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 4, Origin: [3]float32{500, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 5, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 82, Kind: "ra", Origin: [3]float32{0, 0, 0}, Time: 0})
	// Seed slot 4's items snapshot — first update sets baseline silently.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 4, StatIndex: events.StatItems, Value: 0, Time: 1.0})
	// Pickup: IT_ARMOR3 bit transitions 0→1.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 4, StatIndex: events.StatItems, Value: events.ITArmor3, Time: 9.99})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 82, Kind: "ra", Taken: true, Time: 10.0})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "real_picker" {
		t.Errorf("stat-delta should attribute slot 4, got %q", got)
	}
	if got := a.AttributionCounts()["stat"]; got != 1 {
		t.Errorf("stat count = %d, want 1", got)
	}
}

// Layer 3 with an ammo box: STAT_ROCKETS positive delta.
func TestItemAnalyzer_LayeredAttribution_AmmoBoxStatDelta(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "rl_owner"}
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "noisy"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 1, Origin: [3]float32{2000, 0, 0}, Time: 19.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 19.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 83, Kind: "rockets", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatRockets, Value: 5, Time: 1.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatRockets, Value: 10, Time: 19.9})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 83, Kind: "rockets", Taken: true, Time: 20.0})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "rl_owner" {
		t.Errorf("ammo stat-delta should attribute slot 1, got %q", got)
	}
}

// Layer 4 (distance) when no upper-layer signal fires AND the closest
// player is within the touch radius. The fallback still works.
func TestItemAnalyzer_LayeredAttribution_DistanceFallbackUnderRadius(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "close"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{32, 0, 0}, Time: 4.9}) // 32 u away → squared 1024 < 16384

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 84, Kind: "ga", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 84, Kind: "ga", Taken: true, Time: 5})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "close" {
		t.Errorf("distance fallback within radius should attribute slot 0, got %q", got)
	}
	if got := a.AttributionCounts()["distance"]; got != 1 {
		t.Errorf("distance count = %d, want 1", got)
	}
}

// Layer 4 refuses to guess when the closest known player is beyond
// the touch-plausible radius. TakenBy is empty rather than wrong.
func TestItemAnalyzer_LayeredAttribution_DistanceRefusedBeyondRadius(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "far"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{500, 0, 0}, Time: 4.9}) // squared 250000 > 16384

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 85, Kind: "ga", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 85, Kind: "ga", Taken: true, Time: 5})

	r := &Result{}
	_ = a.Finalize(r)
	p0 := r.Items.Items[0].Phases[0]
	if p0.TakenBy != "" {
		t.Errorf("beyond-radius pickup should yield empty TakenBy, got %q", p0.TakenBy)
	}
	if got := a.AttributionCounts()["none"]; got != 1 {
		t.Errorf("none count = %d, want 1", got)
	}
}

// When two slots both have plausible stat evidence for the same Kind
// at the same time, distance breaks the tie among only those
// candidates — a third uninvolved-but-closer slot must NOT win.
func TestItemAnalyzer_LayeredAttribution_StatTieBreakByDistance(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "uninvolved"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "candidate_far"}
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "candidate_near"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})   // closest, but no stat evidence
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 1, Origin: [3]float32{100, 0, 0}, Time: 9.9}) // candidate, far
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{50, 0, 0}, Time: 9.9})  // candidate, near

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 86, Kind: "h25", Origin: [3]float32{0, 0, 0}, Time: 0})
	// Both slot 1 and slot 2 see +25 health at the same time.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatHealth, Value: 100, Time: 1.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 100, Time: 1.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatHealth, Value: 125, Time: 9.99})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 125, Time: 9.99})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 86, Kind: "h25", Taken: true, Time: 10.0})

	r := &Result{}
	_ = a.Finalize(r)
	got := r.Items.Items[0].Phases[0].TakenBy
	if got != "candidate_near" {
		t.Errorf("tie should be broken among stat candidates by distance: got %q, want candidate_near", got)
	}
}

// A KTX hint disagreeing with stat evidence still wins (default
// resolution: trust the hint).
func TestItemAnalyzer_LayeredAttribution_HintBeatsContradictoryStat(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "hint_says"}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "stat_says"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{0, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 3, Origin: [3]float32{0, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 87, Kind: "ra", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatItems, Value: 0, Time: 1.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatItems, Value: events.ITArmor3, Time: 9.99})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 87, RespawnSec: 20, PlayerEnt: 3 /* slot 2 */, Time: 10.0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 87, Kind: "ra", Taken: true, Time: 10.0})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "hint_says" {
		t.Errorf("hint should win over contradictory stat, got %q", got)
	}
}

// Respawn loadout (the burst of stat updates after a SpawnEvent)
// must NOT generate evidence rows that mis-attribute the next
// pickup. The SpawnEvent handler clears the slot's snapshot and
// pending evidence.
func TestItemAnalyzer_LayeredAttribution_RespawnLoadoutNotMistakenForPickup(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "respawned"}
	ctx.Players[1] = &events.PlayerInfo{Slot: 1, Name: "real_picker"}
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{0, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 1, Origin: [3]float32{200, 0, 0}, Time: 9.9})

	// Slot 0 dies and respawns; the post-spawn loadout would otherwise
	// generate +25 shells / +items evidence.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 100, Time: 0.5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 0, Time: 1.0})
	_ = a.OnEvent(&events.DeathEvent{PlayerNum: 0, Time: 1.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatHealth, Value: 100, Time: 5.0})
	_ = a.OnEvent(&events.SpawnEvent{PlayerNum: 0, Time: 5.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatShells, Value: 25, Time: 5.0})

	// Then slot 1 picks up a real shells box.
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 88, Kind: "shells", Origin: [3]float32{200, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatShells, Value: 0, Time: 6.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 1, StatIndex: events.StatShells, Value: 20, Time: 9.99})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 88, Kind: "shells", Taken: true, Time: 10.0})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenBy; got != "real_picker" {
		t.Errorf("post-spawn loadout must not misattribute; got %q", got)
	}
}

// TestItemAnalyzer_ContestedDoubleHealthGoesToGainer reproduces the
// gameId 216835 contested h25: two adjacent +25 boxes grabbed in one
// server frame coalesce into a single +50 health jump on the picker.
// A single capped evidence row would attribute only the first box via
// the stat layer and let the second fall through to the distance
// corroborator — handing it to a bystander who merely stood on the
// boxes while taking damage. The +26..50 jump must mint one evidence
// row per box so both attribute to the gainer.
func TestItemAnalyzer_ContestedDoubleHealthGoesToGainer(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "bystander", Team: "jah"}
	ctx.Players[3] = &events.PlayerInfo{Slot: 3, Name: "gainer", Team: "ahoy"}
	// The bystander stands exactly on the boxes (would win on distance);
	// the gainer is farther away but is the one whose health rose.
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{0, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 3, Origin: [3]float32{100, 0, 0}, Time: 9.9})

	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 80, Kind: "h25", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 81, Kind: "h25", Origin: [3]float32{0, 0, 0}, Time: 0})

	// Seed health baselines silently.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatHealth, Value: 28, Time: 9.8})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 100, Time: 9.8})
	// Gainer jumps +50 (both boxes); bystander takes -24 damage (no pickup).
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 3, StatIndex: events.StatHealth, Value: 78, Time: 10.0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatHealth, Value: 76, Time: 10.0})

	// Both boxes taken in the same frame.
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 80, Kind: "h25", Taken: true, Time: 10.0})
	_ = a.OnEvent(&events.ItemStateEvent{EntNum: 81, Kind: "h25", Taken: true, Time: 10.0})

	r := &Result{}
	_ = a.Finalize(r)

	for _, it := range r.Items.Items {
		if it.Kind != "h25" {
			continue
		}
		if got := it.Phases[0].TakenBy; got != "gainer" {
			t.Errorf("h25 ent %d TakenBy = %q, want gainer (the player whose health rose, not the nearer bystander)", it.EntNum, got)
		}
	}
	if a.attrCounts["stat"] != 2 {
		t.Errorf("stat attributions = %d, want 2 (both boxes via the +50 health jump)", a.attrCounts["stat"])
	}
	if a.attrCounts["distance"] != 0 {
		t.Errorf("distance attributions = %d, want 0 (no box should fall to the distance corroborator)", a.attrCounts["distance"])
	}
}

// Weapon-stay (dmm3): a weapon never emits ItemStateEvent{Taken}, so
// the STAT_ITEMS flip must close the phase itself — with the
// zero-length-unavailability convention (TakenAt == RespawnAt) and a
// new phase opening at the same instant, since the weapon never left
// the map.
func TestItemAnalyzer_WeaponStayFlipClosesPhase(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[2] = &events.PlayerInfo{Slot: 2, Name: "ace", Team: "red"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "rl", Origin: [3]float32{500, 500, 100}, Time: 0})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 2, Origin: [3]float32{505, 500, 100}, Time: 9.9})
	// Seed the STAT_ITEMS baseline silently, then flip the RL bit.
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatItems, Value: 1, Time: 5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 2, StatIndex: events.StatItems, Value: 1 | wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if len(r.Items.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(r.Items.Items))
	}
	it := r.Items.Items[0]
	if len(it.Phases) != 2 {
		t.Fatalf("phases = %+v, want closed phase + reopened phase", it.Phases)
	}
	p0 := it.Phases[0]
	if p0.TakenAt != 10000 || p0.RespawnAt != 10000 {
		t.Errorf("phase[0] = %+v, want TakenAt == RespawnAt == 10000 (zero-length unavailability)", p0)
	}
	if p0.TakenBy != "ace" || p0.Team != "red" {
		t.Errorf("phase[0] attribution = %q/%q, want ace/red", p0.TakenBy, p0.Team)
	}
	if it.Phases[1].AvailableFrom != 10000 || it.Phases[1].TakenAt != 0 {
		t.Errorf("phase[1] = %+v, want open phase from 10000", it.Phases[1])
	}
	if a.attrCounts["weaponstay"] != 1 {
		t.Errorf(`attrCounts["weaponstay"] = %d, want 1`, a.attrCounts["weaponstay"])
	}
}

// Two same-kind weapon entities: the flip attributes to the pad the
// picker was actually standing on (nearest within the gate).
func TestItemAnalyzer_WeaponStayNearestEntityWins(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 91, Kind: "rl", Origin: [3]float32{200, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{190, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	for _, it := range r.Items.Items {
		closed := it.Phases[0].TakenAt != 0
		if it.EntNum == 91 && !closed {
			t.Errorf("ent 91 (the near pad) should have the closed phase")
		}
		if it.EntNum == 90 && closed {
			t.Errorf("ent 90 (the far pad) should be untouched")
		}
	}
}

// No position samples inside the stat lag window → no entity phase is
// closed (kind-level recovery is WeaponPickupsAnalyzer's job).
func TestItemAnalyzer_WeaponStayNoPositionNoPhase(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "lg", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItLightning, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenAt; got != 0 {
		t.Errorf("phase closed at %v without any position data; want it left open", got)
	}
}

// A //ktx bp grant of the same kind just before the flip: the bit gain
// belongs to the backpack, not the pad — no entity phase closes even
// though the picker happens to be standing near a matching pad.
func TestItemAnalyzer_WeaponStayBackpackGrantSkipsPad(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.BackpackDropHintEvent{BackpackEnt: 200, ItemFlags: 32, PlayerEnt: 2, Time: 9.8})
	_ = a.OnEvent(&events.BackpackPickupHintEvent{BackpackEnt: 200, PlayerEnt: 1, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenAt; got != 0 {
		t.Errorf("pad phase closed at %v for a backpack-sourced bit flip; want it left open", got)
	}
	if a.attrCounts["weaponstay"] != 0 {
		t.Errorf(`attrCounts["weaponstay"] = %d, want 0`, a.attrCounts["weaponstay"])
	}
}

// A pending //ktx took hint for the same slot+kind means the wire path
// owns the pickup (weapon-stay mis-detected) — the flip must not
// synthesize a second closure.
func TestItemAnalyzer_WeaponStayPendingHintSkipsSynthesis(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\3"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.ItemPickupHintEvent{ItemEnt: 90, PlayerEnt: 1, Time: 9.95})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if a.attrCounts["weaponstay"] != 0 {
		t.Errorf(`attrCounts["weaponstay"] = %d, want 0 (pending hint owns the pickup)`, a.attrCounts["weaponstay"])
	}
}

// dmm1 is not weapon-stay: the flip feeds Layer-3 stat evidence as
// before and no phase is closed without a wire Taken transition.
func TestItemAnalyzer_NoWeaponStaySynthesisInDmm1(t *testing.T) {
	a, ctx := newTestItemAnalyzer()
	ctx.Players[0] = &events.PlayerInfo{Slot: 0, Name: "p"}

	_ = a.OnEvent(&events.StuffTextEvent{Command: `fullserverinfo "\deathmatch\1"`, Time: 0})
	_ = a.OnEvent(&events.ItemSpawnEvent{EntNum: 90, Kind: "rl", Origin: [3]float32{0, 0, 0}, Time: 0})
	_ = a.OnEvent(&events.PlayerPositionEvent{PlayerNum: 0, Origin: [3]float32{10, 0, 0}, Time: 9.9})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: 0, Time: 5})
	_ = a.OnEvent(&events.StatUpdateEvent{PlayerNum: 0, StatIndex: events.StatItems, Value: wpItRocketLauncher, Time: 10})

	r := &Result{}
	_ = a.Finalize(r)
	if got := r.Items.Items[0].Phases[0].TakenAt; got != 0 {
		t.Errorf("phase closed at %v in dmm1 without a wire transition; want it left open", got)
	}
	if len(a.pendingStatEvidence[0]) == 0 {
		t.Errorf("dmm1 flip should have produced Layer-3 stat evidence")
	}
}

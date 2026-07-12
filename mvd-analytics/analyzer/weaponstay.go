package analyzer

import (
	"strconv"
	"strings"

	"github.com/mvd-analyzer/mvd-reader/events"
)

// weaponStayDetector latches the server's deathmatch / coop mode from
// serverinfo so pickup analyzers can tell whether weapon entities stay
// in the world when touched.
//
// Ground truth is KTX weapon_touch's `leave` flag (ktx/src/items.c:835):
// in deathmatch 2, 3 and 5 (and coop) a touched weapon keeps its model,
// never emits the `//ktx took` hint, and never produces an entity-state
// Taken transition — the only wire signal of the pickup is the
// STAT_ITEMS weapon-bit flip. In deathmatch 0/1/4 the weapon is removed
// and respawns, and the normal hint / entity-state pipeline works.
//
// The detector is embedded by ItemAnalyzer and WeaponPickupsAnalyzer
// (mirroring the MatchTimingDetector pattern) so the mode logic cannot
// drift between them.
type weaponStayDetector struct {
	dmSet   bool
	dm      int
	coopSet bool
	coop    bool
}

// OnStuffText feeds the initial `fullserverinfo "\k\v\..."` dump through
// the shared parseInfoString walker. parseInfoString is last-write-wins
// for a key duplicated inside a single info string (protocol-pathological,
// never observed); observe's first-value latch still governs across
// messages, which is the case that matters.
func (d *weaponStayDetector) OnStuffText(e *events.StuffTextEvent) {
	if !strings.HasPrefix(e.Command, "fullserverinfo ") {
		return
	}
	for key, value := range parseInfoString(e.Command) {
		d.observe(key, value)
	}
}

// OnServerInfo feeds a mid-stream single-key serverinfo update.
func (d *weaponStayDetector) OnServerInfo(e *events.ServerInfoEvent) {
	d.observe(e.Key, e.Value)
}

// observe latches the first value seen per key. Mid-demo deathmatch
// changes are protocol-legal but game-impossible under KTX (the mode is
// fixed before the map spawns items), so first-value-wins keeps a
// stray late update from flipping the synthesis gate mid-match.
func (d *weaponStayDetector) observe(key, value string) {
	switch key {
	case "deathmatch":
		if d.dmSet {
			return
		}
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			d.dm, d.dmSet = n, true
		}
	case "coop":
		if d.coopSet {
			return
		}
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			d.coop, d.coopSet = n != 0, true
		}
	}
}

// WeaponStay reports whether touched weapons stay in the world. False
// when serverinfo never carried a deathmatch value — a demo we can't
// classify gets the plain hint/entity-state pipeline, not synthesis.
func (d *weaponStayDetector) WeaponStay() bool {
	if d.coopSet && d.coop {
		return true
	}
	return d.dmSet && (d.dm == 2 || d.dm == 3 || d.dm == 5)
}

// spawnSuppressWindow is how long after a SpawnEvent a STAT_ITEMS
// weapon-bit gain is treated as spawn loadout rather than a pickup.
// Loadout stat updates land in the respawn frame itself, so the window
// is one-to-two frames; anything wider eats genuine fast grabs — on
// small maps (dm4) players reach a weapon within 250 ms of spawning.
const spawnSuppressWindow = 0.050

// deathGrabWindow: a weapon-bit gain landing this soon after the
// slot's DeathEvent is a grab-then-die — the player touched the pad
// and was killed inside one stat interval, and the death-frame flush
// put the flip after the DeathEvent in packet order (observed on hub
// game 224763: `DEATH → RL+` at the same timestamp). Death bookkeeping
// only ever clears weapon bits (pack drop, respawn strip), so a bit
// gained in the death frame can only be a real touch, and KTX counts
// it (weapon_touch ran before the kill).
const deathGrabWindow = 0.050

// weaponFlipTracker turns per-slot STAT_ITEMS updates into weapon-grant
// detections for weapon-stay synthesis.
//
// The baseline is NEVER reset: stat updates fire on every value change,
// so the last observed value is by construction the player's current
// inventory — including through warmup and across deaths. Any reset
// creates a swallow window in which the next update re-seeds silently
// and a pickup inside it is lost (measured on the corpus: a death-reset
// cost 20-40% of all weapon grants on a 2on2, because a player who dies
// carrying only the spawn loadout produces no respawn stat update at
// all, leaving their first pickup of the new life to do the re-seed).
//
// Instead, flips are filtered by player state:
//
//   - Flips while dead are inventory bookkeeping (pack-drop clears, or
//     the respawn loadout when the stat lands before the SpawnEvent in
//     the same frame — the observed wire order is DEATH → loadout STAT
//     → SPAWN) and are absorbed silently. Exception: a flip within
//     deathGrabWindow of the DeathEvent is a grab-then-die and is
//     reported (see the constant above).
//   - Flips within spawnSuppressWindow after a SpawnEvent are spawn
//     loadout (dmm5-style modes, when the loadout stat lands just after
//     the SpawnEvent) and are absorbed silently. dmm3 spawn loadout is
//     SG+axe only, so the window costs nothing there.
type weaponFlipTracker struct {
	items     map[int]int
	seeded    map[int]bool
	dead      map[int]bool
	lastSpawn map[int]float64
	lastDeath map[int]float64
}

func (t *weaponFlipTracker) OnSpawn(slot int, time float64) {
	if t.lastSpawn == nil {
		t.lastSpawn = make(map[int]float64)
		t.dead = make(map[int]bool)
	}
	t.lastSpawn[slot] = time
	t.dead[slot] = false
}

func (t *weaponFlipTracker) OnDeath(slot int, time float64) {
	if t.dead == nil {
		t.dead = make(map[int]bool)
	}
	if t.lastDeath == nil {
		t.lastDeath = make(map[int]float64)
	}
	t.dead[slot] = true
	t.lastDeath[slot] = time
}

// Observe folds one STAT_ITEMS update into the baseline and returns the
// weapon kinds it granted (0→1 bit flips), already filtered for
// death-frame bookkeeping and spawn loadout. Order follows
// weaponKindsOrdered for determinism.
func (t *weaponFlipTracker) Observe(slot int, value int, time float64) []string {
	if t.items == nil {
		t.items = make(map[int]int)
		t.seeded = make(map[int]bool)
	}
	if !t.seeded[slot] {
		t.items[slot] = value
		t.seeded[slot] = true
		return nil
	}
	prev := t.items[slot]
	t.items[slot] = value
	newly := value & ^prev
	if newly == 0 {
		return nil
	}
	if t.dead[slot] {
		// Grab-then-die: the touch preceded the kill, its flip landed
		// in the death-frame flush. Anything later while dead is
		// inventory bookkeeping, not a pickup.
		if ld, ok := t.lastDeath[slot]; !ok || time-ld > deathGrabWindow {
			return nil
		}
	}
	if ls, ok := t.lastSpawn[slot]; ok && time >= ls && time-ls <= spawnSuppressWindow {
		return nil // spawn loadout, not a pickup
	}
	var kinds []string
	for _, k := range weaponKindsOrdered {
		if newly&weaponBit[k] != 0 {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// posTracker keeps a short rolling per-slot history of player origins
// for analyzers that need "was this player near point P during window
// W" — the question a latest-only position map can't answer once the
// window is in the past. Semantics mirror ItemAnalyzer's playerPosHist
// (1 s horizon, pruned on append).
type posTracker struct {
	hist map[int][]posSample
}

// Record appends one origin sample and prunes entries older than the
// 1 s horizon.
func (p *posTracker) Record(slot int, origin [3]float32, t float64) {
	if p.hist == nil {
		p.hist = make(map[int][]posSample)
	}
	hist := append(p.hist[slot], posSample{origin: origin, time: t})
	cutoff := t - 1.0
	keepFrom := 0
	for keepFrom < len(hist) && hist[keepFrom].time < cutoff {
		keepFrom++
	}
	p.hist[slot] = hist[keepFrom:]
}

// MinDistSqIn returns the smallest squared distance between the slot's
// samples inside [from, to] and target. ok=false when the slot has no
// sample in the window.
func (p *posTracker) MinDistSqIn(slot int, from, to float64, target [3]float32) (float32, bool) {
	return minDistSqOverWindow(p.hist[slot], from, to, target)
}

// minDistSqOverWindow scans a time-ordered sample slice and returns the
// minimum squared distance to target across samples with
// from <= time <= to. Shared by posTracker and ItemAnalyzer's
// playerPosHist so the two weapon-stay classifiers use identical
// proximity semantics.
func minDistSqOverWindow(hist []posSample, from, to float64, target [3]float32) (float32, bool) {
	best := float32(0)
	found := false
	for i := range hist {
		if hist[i].time < from || hist[i].time > to {
			continue
		}
		dx := hist[i].origin[0] - target[0]
		dy := hist[i].origin[1] - target[1]
		dz := hist[i].origin[2] - target[2]
		d := dx*dx + dy*dy + dz*dz
		if !found || d < best {
			best = d
			found = true
		}
	}
	return best, found
}

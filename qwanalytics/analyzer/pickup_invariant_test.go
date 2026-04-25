package analyzer_test

// Pickup-count invariants — compares the items-analyser's per-player
// pickup totals against KTX's authoritative demoinfo numbers
// (`demoInfo.players[*].items[*].took` for armors / healths / MH /
// powerups). KTX's count is the touch-handler's own tally and is
// definitive: every armor/health/MH/powerup pickup that fired the
// QuakeC `Touch` function shows up there.
//
// What this test catches:
//
//  - Phantom over-counts. If items-analyser closes an extra phase per
//    player+kind that KTX never registered, the over-count flags it.
//    Likely root causes: phantom entity-state events, double-attribution
//    on layered-pipeline edge cases.
//
//  - Coverage regressions. The analyser legitimately under-counts
//    insta-regrabs (the entity is taken+respawned within one server
//    tick, never visible-to-invisible on the wire) — see the "known
//    limitation" callout in qwanalytics/analyzer/items.md. The
//    documented baseline is ≤ 4 missed cells per (player, kind) and
//    ≤ 30 total per demo on the corpus; if a refactor makes that
//    materially worse, the test fails.
//
// Weapons aren't asserted: in KTX's "weapon stays" mode (1on1, 2on2,
// some 4on4 configs) weapon entities never disappear from the wire
// when picked up — the wire-level signal items-analyser depends on
// is silent. KTX still records the touches via its own counter.
// Weapon pickup tracking is the responsibility of weapon_pickups.go,
// which uses KTX hints (and shares the same coverage gap when those
// hints aren't emitted; out of scope for this test).
//
// Run:
//
//   go test ./qwanalytics/analyzer/ -run TestItemPickupCountsMatchDemoInfo -v

import (
	"path/filepath"
	"testing"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
)

// ktxItemNameToKind maps the KTX demoinfo `items` map key to the
// item-Kind vocabulary the parser emits. The KTX-side names come from
// ItName() at ktx/src/stats.c:395 — long names for armors and healths,
// single letters for powerups. Suit is intentionally absent: KTX
// doesn't track Biosuit in the per-player items map.
var ktxItemNameToKind = map[string]string{
	"ga":         "ga",
	"ya":         "ya",
	"ra":         "ra",
	"health_15":  "h15",
	"health_25":  "h25",
	"health_100": "mh",
	"q":          "quad",
	"p":          "pent",
	"r":          "ring",
}

// Per-cell and per-demo thresholds for items pickup counts. Set above
// the current observed worst-case so legitimate small drift doesn't
// break the test, but regressions of 2× or more do. Update only with
// an explanatory commit message.
//
// With synthesis enabled (the default since the synthetic-pickups
// branch), the corpus baseline is: 1 over-cell per demo max, ≤2 per
// cell; 3-4 under-cells per demo max, ≤2 per cell. Thresholds set
// above that.
const (
	maxItemsOverPerCell  = 2  // phantom pickups attributed per (player, kind)
	maxItemsOverPerDemo  = 5  // aggregate over-counts per demo
	maxItemsUnderPerCell = 3  // missed pickups per (player, kind) — residual insta-regrabs
	maxItemsUnderPerDemo = 10 // aggregate under-counts per demo
)

func TestItemPickupCountsMatchDemoInfo(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Skip("qwanalytics/testdata/corpus.json has no entries")
	}

	cacheDir := filepath.Join("..", "testdata", "cache")

	for _, entry := range corpus {
		t.Run(entry.Label, func(t *testing.T) {
			mvdPath := ensureCached(t, cacheDir, entry)

			result, err := analyzer.NewDefaultRegistry().Analyze(mvdPath)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if result.DemoInfo == nil {
				t.Skip("demo has no embedded demoinfo")
			}
			if result.Items == nil {
				t.Fatalf("analyzer produced no items result")
			}

			// Aggregate items.go pickup counts per (player, kind).
			counts := map[playerKind]int{}
			for _, it := range result.Items.Items {
				for _, ph := range it.Phases {
					if ph.TakenAt == 0 || ph.TakenBy == "" {
						continue
					}
					counts[playerKind{ph.TakenBy, it.Kind}]++
				}
			}

			var (
				matchCells   int
				overCells    []cellDiff
				underCells   []cellDiff
				totalOver    int
				totalUnder   int
				kindBreakdown = map[string]struct{ ana, ktx int }{}
			)

			for _, p := range result.DemoInfo.Players {
				for ktxName, info := range p.Items {
					kind, ok := ktxItemNameToKind[ktxName]
					if !ok {
						continue
					}
					if info == nil {
						continue
					}
					ana := counts[playerKind{p.Name, kind}]
					ktx := info.Took
					b := kindBreakdown[kind]
					b.ana += ana
					b.ktx += ktx
					kindBreakdown[kind] = b
					switch {
					case ana == ktx:
						matchCells++
					case ana > ktx:
						d := ana - ktx
						overCells = append(overCells, cellDiff{p.Name, kind, ana, ktx, d})
						totalOver += d
					default:
						d := ktx - ana
						underCells = append(underCells, cellDiff{p.Name, kind, ana, ktx, d})
						totalUnder += d
					}
				}
			}

			t.Logf("items pickup counts: %d cells matched, %d over (+%d), %d under (-%d)",
				matchCells, len(overCells), totalOver, len(underCells), totalUnder)

			// Per-kind aggregate breakdown for visibility — easier to
			// spot which item kind is drifting.
			for _, kind := range []string{"ga", "ya", "ra", "h15", "h25", "mh", "quad", "pent", "ring"} {
				b, ok := kindBreakdown[kind]
				if !ok || (b.ana == 0 && b.ktx == 0) {
					continue
				}
				if b.ana == b.ktx {
					t.Logf("  %-4s: ana=%-4d ktx=%-4d", kind, b.ana, b.ktx)
				} else {
					t.Logf("  %-4s: ana=%-4d ktx=%-4d (diff=%+d)", kind, b.ana, b.ktx, b.ana-b.ktx)
				}
			}

			// Hard asserts. Over-counts are stricter than under-counts
			// because under is documented (insta-regrabs) and over is
			// a real attribution bug.
			for _, c := range overCells {
				if c.diff > maxItemsOverPerCell {
					t.Errorf("over-count exceeds per-cell threshold: %s/%s ana=%d ktx=%d (+%d > %d)",
						c.player, c.kind, c.ana, c.ktx, c.diff, maxItemsOverPerCell)
				}
			}
			if totalOver > maxItemsOverPerDemo {
				t.Errorf("aggregate over-count %d exceeds per-demo threshold %d", totalOver, maxItemsOverPerDemo)
				for _, c := range overCells {
					t.Logf("    over: %s/%s ana=%d ktx=%d", c.player, c.kind, c.ana, c.ktx)
				}
			}
			for _, c := range underCells {
				if c.diff > maxItemsUnderPerCell {
					t.Errorf("under-count exceeds per-cell threshold: %s/%s ana=%d ktx=%d (-%d > %d)",
						c.player, c.kind, c.ana, c.ktx, c.diff, maxItemsUnderPerCell)
				}
			}
			if totalUnder > maxItemsUnderPerDemo {
				t.Errorf("aggregate under-count %d exceeds per-demo threshold %d", totalUnder, maxItemsUnderPerDemo)
				for _, c := range underCells {
					t.Logf("    under: %s/%s ana=%d ktx=%d", c.player, c.kind, c.ana, c.ktx)
				}
			}
		})
	}
}

type playerKind struct {
	player string
	kind   string
}

type cellDiff struct {
	player    string
	kind      string
	ana, ktx  int
	diff      int
}


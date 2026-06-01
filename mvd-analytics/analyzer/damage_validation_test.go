package analyzer_test

// Self-running validation harness for the damage feature (and the
// reconstructed pipeline outputs it leans on), reconciling against the
// canonical oracle embedded in every KTX demo: demoInfo (the server-authored
// ktxstats scoreboard). It emits an auditable reconciliation report and fails
// on any hard violation.
//
// The canon ladder and check classes follow the validation method:
//
//   Class A — EXACT reconciliation against an independent oracle. Deaths are
//     reconstructed by the pipeline (DF_DEAD / health-cross / obituary) and
//     must equal demoInfo.stats.deaths, the server's own death counter.
//
//   Class B — INVARIANT / directional. The per-hit damage stream reports
//     KTX's UNBOUND damage (overkill-inclusive, combat.c:819) while the
//     demoInfo scoreboard reports the HEALTH-CAPPED value (combat.c:1082), so
//     they cannot be equal — only Σ raw >= capped, with the gap = overkill
//     >= 0. Plus closed-system + internal-consistency invariants that must
//     hold regardless of the oracle.
//
// Negative controls (TestDamageValidation_NegativeControls) prove the harness
// can fail: a mutated total must trip a Class-B check, and demo A's output
// reconciled against demo B's oracle must trip Class-A.
//
// Demos come from the same hub cache the golden test uses; subtests skip
// cleanly when a demo is neither cached nor fetchable offline.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// violation is one failed reconciliation check, formatted for the report.
type violation struct {
	class string // "A" or "B"
	check string
	msg   string
}

type namedDamage struct {
	name string
	dmg  *result.PlayerDamage
}

// normName mirrors analyzer.normalizePlayerName (unexported): lowercase
// alphanumerics. Used to join demoInfo names to resolved ByPlayer keys.
func normName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func lookupFrags(fr *result.FragResult, name string) *result.PlayerFrags {
	if fr == nil {
		return nil
	}
	if p, ok := fr.ByPlayer[name]; ok {
		return p
	}
	n := normName(name)
	for k, p := range fr.ByPlayer {
		if normName(k) == n {
			return p
		}
	}
	return nil
}

func lookupDamage(dr *result.DamageResult, name string) *result.PlayerDamage {
	if dr == nil {
		return nil
	}
	if p, ok := dr.ByPlayer[name]; ok {
		return p
	}
	n := normName(name)
	for k, p := range dr.ByPlayer {
		if normName(k) == n {
			return p
		}
	}
	return nil
}

func byPlayerSorted(dr *result.DamageResult) []namedDamage {
	if dr == nil {
		return nil
	}
	out := make([]namedDamage, 0, len(dr.ByPlayer))
	for k, v := range dr.ByPlayer {
		out = append(out, namedDamage{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// reconcile runs every check of `res` against the oracle in `oracle` and
// returns the violations plus human-readable report lines. Factored out so the
// negative controls can feed a deliberately wrong (res, oracle) pair.
func reconcile(res *result.Result, oracle *result.DemoInfoResult) (vios []violation, report []string) {
	add := func(class, check, format string, args ...interface{}) {
		vios = append(vios, violation{class, check, fmt.Sprintf(format, args...)})
	}
	if oracle == nil || len(oracle.Players) == 0 {
		report = append(report, "  (no demoInfo oracle — non-KTX demo, nothing to reconcile)")
		return
	}

	// --- Class C0: oracle self-invariant (sanity on the oracle itself). ---
	var oGiven, oTaken, oTeam, oSelf int
	for i := range oracle.Players {
		if d := oracle.Players[i].Dmg; d != nil {
			oGiven += d.Given
			oTaken += d.Taken
			oTeam += d.Team
			oSelf += d.Self
		}
	}
	report = append(report, fmt.Sprintf("  [oracle] Σgiven=%d Σtaken=%d Σteam=%d Σself=%d (closed-system: given==taken)", oGiven, oTaken, oTeam, oSelf))

	// --- Class A: deaths == demoInfo.stats.deaths (per player, exact). ---
	for i := range oracle.Players {
		op := &oracle.Players[i]
		if op.Stats == nil {
			continue
		}
		pf := lookupFrags(res.Frags, op.Name)
		got := 0
		if pf != nil {
			got = pf.Deaths
		}
		want := op.Stats.Deaths
		status := "OK"
		if got != want {
			status = "DIFF"
			add("A", "deaths", "player %q deaths=%d, demoInfo.stats.deaths=%d (delta %+d)", op.Name, got, want, got-want)
		}
		report = append(report, fmt.Sprintf("  [A %-4s] deaths %-16s got=%d oracle=%d", status, op.Name, got, want))
	}

	// --- Class B: damage invariants — raw (unbound) directional + eff (capped) exact. ---
	var sumGivenRaw, sumTakenRaw, sumTeamRaw, sumSelfRaw, sumHitDmg int
	var sumGivenEff, sumTeamEff, sumSelfEff, sumTakenEff, sumHitEff int
	for _, p := range byPlayerSorted(res.Damage) {
		sumGivenRaw += p.dmg.GivenRaw
		sumTakenRaw += p.dmg.TakenRaw
		sumTeamRaw += p.dmg.TeamRaw
		sumSelfRaw += p.dmg.SelfRaw
		sumGivenEff += p.dmg.GivenEff
		sumTeamEff += p.dmg.TeamEff
		sumSelfEff += p.dmg.SelfEff
		sumTakenEff += p.dmg.TakenEff
	}
	if res.Damage != nil {
		for _, h := range res.Damage.Hits {
			sumHitDmg += h.Damage
			sumHitEff += h.DamageEff
		}
	}

	for i := range oracle.Players {
		op := &oracle.Players[i]
		if op.Dmg == nil {
			continue
		}
		pd := lookupDamage(res.Damage, op.Name)
		var gr, tr, sr, ge int
		if pd != nil {
			gr, tr, sr = pd.GivenRaw, pd.TeamRaw, pd.SelfRaw
			ge = pd.GivenEff
		}

		// HARD — classification-independent invariants:
		//   * raw outgoing (givenRaw+teamRaw) >= capped scoreboard (given+team)
		//     — unbound must be >= capped; split-independent.
		//   * raw self >= capped self (self is slot-equality, robust).
		// The EFFECTIVE total reconstruction (givenEff+teamEff+selfEff vs
		// scoreboard) is checked GLOBALLY with a tolerance below — per player
		// it is reported, not asserted, since the residual concentrates on a
		// few players (telefrag victims, reconnect-folded identities).
		if gr+tr < op.Dmg.Given+op.Dmg.Team {
			add("B", "raw-outgoing>=", "player %q givenRaw+teamRaw=%d < demoInfo given+team=%d", op.Name, gr+tr, op.Dmg.Given+op.Dmg.Team)
		}
		if sr < op.Dmg.Self {
			add("B", "raw-self>=", "player %q selfRaw=%d < demoInfo.self=%d", op.Name, sr, op.Dmg.Self)
		}

		// REPORT — per-class given (split-dependent): raw overkill gap and the
		// effective-vs-canon residual (should be ~0; nonzero = enemy/team
		// misattribution at a hit-time identity boundary).
		report = append(report, fmt.Sprintf("  [B    ] %-16s givenRaw=%5d givenEff=%5d oracle.given=%5d  overkill=%+5d effResidual=%+d",
			op.Name, gr, ge, op.Dmg.Given, gr-op.Dmg.Given, ge-op.Dmg.Given))
	}

	// --- Class B internal invariants (oracle-independent). ---
	if sumGivenRaw != sumTakenRaw {
		add("B", "closed-system-raw", "Σ givenRaw=%d != Σ takenRaw=%d (every enemy hit dealt is received)", sumGivenRaw, sumTakenRaw)
	}
	if sumGivenEff != sumTakenEff {
		add("B", "closed-system-eff", "Σ givenEff=%d != Σ takenEff=%d", sumGivenEff, sumTakenEff)
	}
	if sumHitDmg != sumGivenRaw+sumTeamRaw {
		add("B", "hits-consistency-raw", "Σ hit damage=%d != Σ(givenRaw+teamRaw)=%d", sumHitDmg, sumGivenRaw+sumTeamRaw)
	}
	if sumHitEff != sumGivenEff+sumTeamEff {
		add("B", "hits-consistency-eff", "Σ hit effective=%d != Σ(givenEff+teamEff)=%d", sumHitEff, sumGivenEff+sumTeamEff)
	}
	// HARD, classification-independent reconciliation against canon: the
	// reconstructed effective total (all outgoing) must match the scoreboard
	// total within tolerance. This is the Class-A-grade check the unbound
	// stream alone could not provide — with the exact KTX absorption model
	// (save + bound(take,health)) clean duels reconcile EXACTLY (residual 0)
	// and team games land within ~0.7%. The 1.5% allowance covers the
	// remaining wire-ambiguous edge cases (telefrag scoring is k_dmgfrags-cvar
	// dependent; reconnect identity folding; ceil rounding). A genuine
	// extraction error is far larger — armor mishandled was 4–24%, attribution
	// desync likewise — so it trips, as does the negative-control mutation.
	effOut := sumGivenEff + sumTeamEff + sumSelfEff
	scoreOut := oGiven + oTeam + oSelf
	tol := scoreOut*15/1000 + 8 // 1.5% + small absolute floor for tiny demos
	if d := effOut - scoreOut; d > tol || d < -tol {
		add("B", "eff-total-global~=", "Σ effective outgoing=%d vs demoInfo Σ(given+team+self)=%d (delta %+d, > %d tolerance)",
			effOut, scoreOut, d, tol)
	}
	report = append(report, fmt.Sprintf("  [B    ] RAW:  Σgiven=%d Σtaken=%d Σteam=%d Σself=%d Σhits=%d",
		sumGivenRaw, sumTakenRaw, sumTeamRaw, sumSelfRaw, sumHitDmg))
	report = append(report, fmt.Sprintf("  [B    ] EFF:  Σgiven=%d Σtaken=%d Σteam=%d Σself=%d Σhits=%d",
		sumGivenEff, sumTakenEff, sumTeamEff, sumSelfEff, sumHitEff))
	report = append(report, fmt.Sprintf("  [B    ] overkill gap ΣgivenRaw-Σgiven = %d (%.1f%%); eff total residual = %+d (target 0)",
		sumGivenRaw-oGiven, pct(sumGivenRaw-oGiven, oGiven), (sumGivenEff+sumTeamEff+sumSelfEff)-(oGiven+oTeam+oSelf)))

	return
}

func TestDamageValidation(t *testing.T) {
	corpus := loadCorpus(t)
	if len(corpus) == 0 {
		t.Skip("no corpus.json entries")
	}
	cacheDir := damageCacheDir()

	anyRan := false
	for _, entry := range corpus {
		entry := entry
		t.Run(entry.Label, func(t *testing.T) {
			mvdPath := ensureCachedSkip(t, cacheDir, entry)
			anyRan = true

			res, err := analyzer.NewDefaultRegistry().Analyze(mvdPath)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if res.Damage == nil {
				t.Logf("%s: no damage section (no 0x0007 blocks) — skipping damage checks", entry.Label)
			}
			vios, report := reconcile(res, res.DemoInfo)
			t.Logf("reconciliation report for %s:\n%s", entry.Label, strings.Join(report, "\n"))
			for _, v := range vios {
				t.Errorf("[Class %s/%s] %s", v.class, v.check, v.msg)
			}
		})
	}
	if !anyRan {
		t.Skip("no demos cached or fetchable — populate testdata/cache to run the harness")
	}
}

// TestDamageValidation_NegativeControls proves the harness is not a rubber
// stamp: a perturbed total must trip a Class-B check, and cross-demo
// reconciliation (demo A output vs demo B oracle) must trip Class-A.
func TestDamageValidation_NegativeControls(t *testing.T) {
	corpus := loadCorpus(t)
	cacheDir := damageCacheDir()

	type run struct {
		label string
		res   *result.Result
	}
	var runs []run
	for _, entry := range corpus {
		path, ok := cachedPath(cacheDir, entry)
		if !ok {
			continue
		}
		res, err := analyzer.NewDefaultRegistry().Analyze(path)
		if err != nil || res.Damage == nil || res.DemoInfo == nil || len(res.DemoInfo.Players) == 0 {
			continue
		}
		runs = append(runs, run{entry.Label, res})
		if len(runs) == 2 {
			break
		}
	}
	if len(runs) == 0 {
		t.Skip("need at least one cached demo with damage + demoInfo for negative controls")
	}

	// Control 1 — mutation: zero a player's entire outgoing damage
	// (givenRaw AND teamRaw) and confirm the classification-independent
	// "outgoing>=" check trips. Zeroing both (not just givenRaw) makes the
	// control deterministic — teamRaw can't compensate for the dropped
	// given when the demoInfo given+team for that player is positive.
	t.Run("mutation_trips_classB", func(t *testing.T) {
		mut := deepCopyDamage(runs[0].res)
		var victimName string
		for name, pd := range mut.Damage.ByPlayer {
			if pd.GivenRaw+pd.TeamRaw > 0 {
				pd.GivenRaw, pd.TeamRaw = 0, 0 // below any positive oracle given+team
				pd.GivenEff, pd.TeamEff, pd.SelfEff = 0, 0, 0
				victimName = name
				break
			}
		}
		if victimName == "" {
			t.Skip("no positive outgoing damage to perturb")
		}
		vios, _ := reconcile(mut, mut.DemoInfo)
		if !hasCheck(vios, "raw-outgoing>=") {
			t.Errorf("mutation did not trip Class-B raw-outgoing>= check (harness would be a rubber stamp)")
		}
	})

	// Control 2 — cross-demo: reconcile demo A's output against demo B's
	// oracle; deaths/names should not line up → Class-A violations expected.
	t.Run("cross_demo_trips_classA", func(t *testing.T) {
		if len(runs) < 2 {
			t.Skip("need two distinct cached demos")
		}
		vios, _ := reconcile(runs[0].res, runs[1].res.DemoInfo)
		if !hasClass(vios, "A") {
			t.Errorf("cross-demo reconciliation produced no Class-A violation (expected name/deaths mismatch)")
		}
	})
}

func hasCheck(vs []violation, check string) bool {
	for _, v := range vs {
		if v.check == check {
			return true
		}
	}
	return false
}
func hasClass(vs []violation, class string) bool {
	for _, v := range vs {
		if v.class == class {
			return true
		}
	}
	return false
}

// deepCopyDamage clones just enough of a Result (the Damage.ByPlayer map) that
// mutating the copy can't affect the shared run.
func deepCopyDamage(src *result.Result) *result.Result {
	cp := *src
	if src.Damage != nil {
		d := &result.DamageResult{ByPlayer: make(map[string]*result.PlayerDamage, len(src.Damage.ByPlayer)), Hits: src.Damage.Hits}
		for k, v := range src.Damage.ByPlayer {
			pdCopy := *v
			d.ByPlayer[k] = &pdCopy
		}
		cp.Damage = d
	}
	return &cp
}

// --- cache helpers (skip-on-miss variants of the golden test's) ---

func damageCacheDir() string { return filepath.Join("..", "testdata", "cache") }

func cachedPath(cacheDir string, entry corpusEntry) (string, bool) {
	p := filepath.Join(cacheDir, fmt.Sprintf("%d.mvd.gz", entry.GameID))
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

// ensureCachedSkip returns the cached demo path, fetching from the hub on a
// cache miss when online; if the demo is neither cached nor fetchable it
// t.Skips (unlike the golden test's ensureCached, which fails).
func ensureCachedSkip(t *testing.T, cacheDir string, entry corpusEntry) string {
	t.Helper()
	if p, ok := cachedPath(cacheDir, entry); ok {
		return p
	}
	if !networkAllowed() {
		t.Skipf("demo %d (%s) not cached and offline", entry.GameID, entry.Label)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	client := hubfetch.NewClient()
	info, err := client.Resolve(entry.GameID)
	if err != nil {
		t.Skipf("resolve %d: %v", entry.GameID, err)
	}
	data, err := client.Download(info)
	if err != nil {
		t.Skipf("download %d: %v", entry.GameID, err)
	}
	p := filepath.Join(cacheDir, fmt.Sprintf("%d.mvd.gz", entry.GameID))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	return p
}

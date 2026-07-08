package analyzer

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// This file generalises the lazy pipeline passes (Stage 3 of
// PLAN-improve-analytics.md §5). Each is a lazily-materialised,
// separately-cacheable DAG node — a *LazyArtifact — registered in
// lazyArtifacts by name. mvd-api drives a generic materialise-or-load flow
// through the exported hooks (Computed / Build / EncodeTier3 / DecodeTier3),
// so the per-artifact tier-3 disk cache lives in one place and the concrete
// artifact keeps its side-gob shape and latch semantics private here.
//
// Only "los" (the per-player LOS/PVS interval sets) is lazy today. The spatial
// weapon-fire streams used to be a second lazy artifact ("shot-streams") built
// by a full MVD re-parse, but phase 12 folded them into the eager always-full
// mvd-api parse (they cost only a few percent of parse time/cache size and were
// already served on every /shots and /aim request), deleting the re-parse, the
// degrade path, and this artifact. Idempotency keeps the los latch semantics:
// Streams.LOSComputed. No schema bump — the field already exists.

// MaterializeDeps carries external inputs a lazy Build needs that are not on
// the in-memory Result. The one remaining lazy artifact (los) loads its own
// visibility BSP and needs none, so this is empty; it is retained as the Build
// hook's parameter so a future lazy artifact can pass inputs without churning
// the signature.
type MaterializeDeps struct{}

// LazyArtifact is one lazily-materialised, separately-cacheable DAG node
// (Policy Lazy). spec supplies the graph metadata; the function hooks drive
// the generic materialise-or-load flow. Construct only via the registrations
// in lazyArtifacts; consumers reach one by name (LazyArtifactByName).
type LazyArtifact struct {
	spec nodeSpec

	// computed reports whether res already carries this artifact (the latch).
	computed func(res *result.Result) bool
	// build materialises the artifact onto res in place (the compute path).
	// A build that cannot run (no BSP) is not an error — it sets the latch and
	// leaves the artifact absent, matching ComputeLOS's no-BSP behaviour.
	build func(res *result.Result, deps MaterializeDeps) error
	// encode extracts the artifact's side-struct from res as a tier-3 gob;
	// ok=false when there is nothing worth persisting (latch unset).
	encode func(res *result.Result) (data []byte, ok bool, err error)
	// decode splices a tier-3 side-gob onto res and sets the latch. It errors
	// when the cached artifact does not match res (player-set drift within a
	// schema version — a corrupt/partial gob), so the caller recomputes.
	decode func(res *result.Result, data []byte) error
}

// Name is the artifact / node id ("los").
func (a *LazyArtifact) Name() string { return a.spec.Name }

// Computed reports whether res already carries this artifact (its latch is set).
func (a *LazyArtifact) Computed(res *result.Result) bool { return a.computed(res) }

// Build materialises the artifact onto res in place. Idempotent: a no-op when
// already Computed. deps supplies external inputs (the shot-streams re-parse).
func (a *LazyArtifact) Build(res *result.Result, deps MaterializeDeps) error {
	if res == nil || a.computed(res) {
		return nil
	}
	return a.build(res, deps)
}

// EncodeTier3 extracts the artifact from res as a tier-3 side-gob. ok=false
// when the artifact has not been built (nothing to persist).
func (a *LazyArtifact) EncodeTier3(res *result.Result) (data []byte, ok bool, err error) {
	if res == nil {
		return nil, false, nil
	}
	return a.encode(res)
}

// DecodeTier3 splices a tier-3 side-gob onto res and sets the latch. Returns
// an error when the gob does not match res (drift/corruption) so the caller
// discards it and recomputes.
func (a *LazyArtifact) DecodeTier3(res *result.Result, data []byte) error {
	if res == nil {
		return fmt.Errorf("decode %s: nil result", a.spec.Name)
	}
	return a.decode(res, data)
}

// lazyArtifacts is the closed registry of lazy DAG nodes, keyed by name. It
// is the Stage-3 extension point: a new lazy artifact registers here (or, per
// PLAN §3.6, via a future Register call) and inherits the tier-3 cache, the
// graph node, and the generic mvd-api flow.
var lazyArtifacts = map[string]*LazyArtifact{
	"los": losArtifact,
}

// LazyArtifactByName returns the registered lazy artifact, or ok=false for an
// unknown name (a closed registry — no user input reaches it).
func LazyArtifactByName(name string) (*LazyArtifact, bool) {
	a, ok := lazyArtifacts[name]
	return a, ok
}

// lazyArtifactSpecs returns the lazy nodes' specs in name order, for the graph
// export (they are not part of the eager execution order).
func lazyArtifactSpecs() []nodeSpec {
	specs := make([]nodeSpec, 0, len(lazyArtifacts))
	for _, a := range lazyArtifacts {
		specs = append(specs, a.spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// --- los artifact ---

// losArtifact is the per-player line-of-sight / PVS interval sets
// (Streams.Players[].LOS/PVS), the heaviest position-derived pass. It
// requires the timeline (the Streams container) and demoinfo (the map name,
// to load the BSP); the BSP itself is loaded by ComputeLOS, not a DAG edge.
var losArtifact = &LazyArtifact{
	spec: nodeSpec{
		Name:     "los",
		Requires: []string{"timeline", "demoinfo"},
		Provides: []string{"los"},
		Lazy:     true,
		cost:     costHeavy,
		desc:     "Per-player line-of-sight and potential-visibility interval sets — the heaviest position-derived pass, materialised on demand.",
	},
	computed: func(res *result.Result) bool {
		return res.Streams != nil && res.Streams.LOSComputed
	},
	build: func(res *result.Result, _ MaterializeDeps) error {
		ComputeLOS(res) // idempotent; no-op / latch-only when no BSP or <2 players
		return nil
	},
	encode: encodeLOS,
	decode: decodeLOS,
}

// losArtifactData is the los side-gob: the per-player LOS/PVS interval sets
// keyed positionally by PlayerNames, so a decode can splice back by exact
// name and reject a gob whose player set does not match the live Result.
type losArtifactData struct {
	PlayerNames []string
	LOS         [][]result.LosTrack
	PVS         [][]result.LosTrack
}

func encodeLOS(res *result.Result) ([]byte, bool, error) {
	if res.Streams == nil || !res.Streams.LOSComputed {
		return nil, false, nil
	}
	players := res.Streams.Players
	d := losArtifactData{
		PlayerNames: make([]string, len(players)),
		LOS:         make([][]result.LosTrack, len(players)),
		PVS:         make([][]result.LosTrack, len(players)),
	}
	for i := range players {
		d.PlayerNames[i] = players[i].Name
		d.LOS[i] = players[i].LOS
		d.PVS[i] = players[i].PVS
	}
	data, err := gobEncode(d)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func decodeLOS(res *result.Result, data []byte) error {
	if res.Streams == nil {
		return fmt.Errorf("decode los: result has no streams")
	}
	var d losArtifactData
	if err := gobDecode(data, &d); err != nil {
		return fmt.Errorf("decode los: %w", err)
	}
	players := res.Streams.Players
	if len(d.PlayerNames) != len(players) {
		return fmt.Errorf("decode los: cached %d players, result has %d", len(d.PlayerNames), len(players))
	}
	// Match by exact name (order is stable across parses of the same demo, but
	// verify rather than trust position). Any mismatch means drift/corruption:
	// discard and recompute.
	idxByName := make(map[string]int, len(players))
	for i := range players {
		idxByName[players[i].Name] = i
	}
	for i, name := range d.PlayerNames {
		j, ok := idxByName[name]
		if !ok {
			return fmt.Errorf("decode los: cached player %q not in result", name)
		}
		players[j].LOS = d.LOS[i]
		players[j].PVS = d.PVS[i]
	}
	res.Streams.LOSComputed = true
	return nil
}

// gobEncode / gobDecode round-trip a side-struct through gob (the same
// encoding tier-2 uses — lossless for the numeric stream columns JSON would
// coerce to float64).
func gobEncode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, fmt.Errorf("gob encode: %w", err)
	}
	return buf.Bytes(), nil
}

func gobDecode(data []byte, v any) error {
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		return fmt.Errorf("gob decode: %w", err)
	}
	return nil
}

package analyzer

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"sort"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// This file generalises the two hand-rolled lazy passes (Stage 3 of
// PLAN-improve-analytics.md §5). Each is a lazily-materialised,
// separately-cacheable DAG node — a *LazyArtifact — registered in
// lazyArtifacts by name. mvd-api drives a generic materialise-or-load flow
// through the exported hooks (Computed / Build / EncodeTier3 / DecodeTier3),
// so the per-artifact tier-3 disk cache lives in one place and the concrete
// artifacts keep their side-gob shapes and latch semantics private here.
//
// The LAZY UNIT is what is actually materialised atomically today (phase 5.3
// / PLAN-api F12): artifact "shot-streams" is projectiles + beams + nails +
// the rebuilt Shots/Aim splice in ONE re-parse (never per-stream variants),
// and artifact "los" is the per-player LOS/PVS interval sets. Idempotency
// keeps the existing latches' semantics: Streams.LOSComputed for los,
// Streams.ShotStreamsComputed && NailsComputed for shot-streams. No schema
// bump — these fields already exist.

// MaterializeDeps carries external inputs a lazy Build needs that are not on
// the in-memory Result. Only the shot-streams re-parse uses it today; los
// loads its own visibility BSP and ignores it.
type MaterializeDeps struct {
	// Reparse rebuilds the Result from the demo's raw MVD bytes with the
	// shot-stream and nail build flags on (the single F12 variant). It is
	// supplied by the caller that holds the bytes (mvd-api democache). nil
	// disables the shot-streams build — the degrade case the caller detects
	// (tier-1 bytes evicted) before invoking Build.
	Reparse func() (*result.Result, error)
}

// LazyArtifact is one lazily-materialised, separately-cacheable DAG node
// (Policy Lazy). spec supplies the graph metadata; the function hooks drive
// the generic materialise-or-load flow. Construct only via the registrations
// in lazyArtifacts; consumers reach one by name (LazyArtifactByName).
type LazyArtifact struct {
	spec nodeSpec

	// computed reports whether res already carries this artifact (the latch).
	computed func(res *result.Result) bool
	// build materialises the artifact onto res in place (the compute path).
	// A build that cannot run (no BSP, no bytes) is not an error — it sets
	// the latch and leaves the artifact absent, matching today's
	// ComputeLOS / EnsureShotStreams degrade behaviour.
	build func(res *result.Result, deps MaterializeDeps) error
	// encode extracts the artifact's side-struct from res as a tier-3 gob;
	// ok=false when there is nothing worth persisting (latch unset).
	encode func(res *result.Result) (data []byte, ok bool, err error)
	// decode splices a tier-3 side-gob onto res and sets the latch. It errors
	// when the cached artifact does not match res (player-set drift within a
	// schema version — a corrupt/partial gob), so the caller recomputes.
	decode func(res *result.Result, data []byte) error
}

// Name is the artifact / node id ("los", "shot-streams").
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
	"los":          losArtifact,
	"shot-streams": shotStreamsArtifact,
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
		tier:     "lazy",
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

// --- shot-streams artifact ---

// shotStreamsArtifact is the single F12 variant: projectiles + beams + nails
// plus the rebuilt Shots/Aim blocks, materialised in one re-parse. It cannot
// be recomputed from the lean Result (unlike los), so Build needs the raw
// bytes via MaterializeDeps.Reparse.
var shotStreamsArtifact = &LazyArtifact{
	spec: nodeSpec{
		Name:     "shot-streams",
		Requires: []string{"timeline", "shots"},
		Provides: []string{"shot-streams"},
		Lazy:     true,
		tier:     "lazy",
		cost:     costHeavy,
		desc:     "Spatial weapon-fire streams (projectile, beam, nail flights) plus the stream-enriched shots and aim blocks, rebuilt from the demo bytes on demand.",
	},
	computed: func(res *result.Result) bool {
		return res.Streams != nil && res.Streams.ShotStreamsComputed && res.Streams.NailsComputed
	},
	build: func(res *result.Result, deps MaterializeDeps) error {
		if res.Streams == nil || deps.Reparse == nil {
			return nil // no streams container, or no bytes (caller degrades)
		}
		built, err := deps.Reparse()
		if err != nil {
			return err
		}
		spliceShotStreams(res, built)
		return nil
	},
	encode: encodeShotStreams,
	decode: decodeShotStreams,
}

// spliceShotStreams grafts the stream-derived blocks from a freshly rebuilt
// Result (built with BuildShotStreams+BuildNails) onto res and latches. This
// is exactly what EnsureShotStreams spliced inline before Stage 3.
func spliceShotStreams(res, built *result.Result) {
	if built.Streams != nil {
		res.Streams.Projectiles = built.Streams.Projectiles
		res.Streams.Beams = built.Streams.Beams
		res.Streams.Nails = built.Streams.Nails
	}
	res.Streams.ShotStreamsComputed = true
	res.Streams.NailsComputed = true
	if built.Shots != nil {
		res.Shots = built.Shots
	}
	if built.Aim != nil {
		res.Aim = built.Aim
	}
}

// shotStreamsArtifactData is the shot-streams side-gob: the exact blocks
// EnsureShotStreams splices — the three streams plus the rebuilt Shots/Aim.
type shotStreamsArtifactData struct {
	Projectiles *result.ProjectileStreams
	Beams       *result.BeamStreams
	Nails       *result.ProjectileStreams
	Shots       *result.ShotsResult
	Aim         *result.AimResult
}

func encodeShotStreams(res *result.Result) ([]byte, bool, error) {
	if res.Streams == nil || !res.Streams.ShotStreamsComputed || !res.Streams.NailsComputed {
		return nil, false, nil
	}
	d := shotStreamsArtifactData{
		Projectiles: res.Streams.Projectiles,
		Beams:       res.Streams.Beams,
		Nails:       res.Streams.Nails,
		Shots:       res.Shots,
		Aim:         res.Aim,
	}
	data, err := gobEncode(d)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func decodeShotStreams(res *result.Result, data []byte) error {
	if res.Streams == nil {
		return fmt.Errorf("decode shot-streams: result has no streams")
	}
	var d shotStreamsArtifactData
	if err := gobDecode(data, &d); err != nil {
		return fmt.Errorf("decode shot-streams: %w", err)
	}
	res.Streams.Projectiles = d.Projectiles
	res.Streams.Beams = d.Beams
	res.Streams.Nails = d.Nails
	res.Streams.ShotStreamsComputed = true
	res.Streams.NailsComputed = true
	res.Shots = d.Shots
	res.Aim = d.Aim
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

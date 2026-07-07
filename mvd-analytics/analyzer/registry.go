package analyzer

import (
	"io"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/config"
	resultpkg "github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-reader/events"
	mvdsource "github.com/mvd-analyzer/mvd-reader/source/mvd"
)

// PhaseTiming records the wall-clock cost of one pipeline phase. It is
// collected on every run into Registry.PhaseTimings for instrumentation
// (the WASM build surfaces it to the browser console). It is deliberately
// kept off the Result so it never enters the JSON schema.
type PhaseTiming struct {
	Name string  `json:"name"`
	Ms   float64 `json:"ms"`
}

// Registry manages registered analyzers. Config carries the tunable
// parameters individual analyzers read; callers may mutate it before
// analyzing to override defaults for a single run.
type Registry struct {
	// core analysers are the producers / state-reconstruction tier.
	// They populate CoreOutputs (DemoInfo, NameTable, FragEntries, …)
	// that derived analysers consume during their Finalize. Core
	// finalises before any derived analyser, so registration into
	// this slice is the load-bearing "I produce something downstream
	// reads" signal.
	core []Analyzer

	// derived analysers consume CoreOutputs (or are independent
	// peers) and produce their own slice of Result. They never write
	// to CoreOutputs; their own Finalize results stay local to the
	// Result they populate.
	derived []Analyzer

	postProcessors []ResultPostProcessor
	Config         *config.Config

	// specs is the registration-order node list with declared artifact
	// edges; nodes is that list in validated topological execution order.
	// Both are populated by buildGraph (called from NewDefaultRegistry).
	// A hand-built registry (NewRegistry + Register*) leaves them nil and
	// executes in registration order (see execOrder). See dag.go.
	specs []nodeSpec
	nodes []nodeSpec

	// BuildShotStreams opts into the spatial weapon-fire streams
	// (Streams.Projectiles / Streams.Beams) for the map view. Off by
	// default so the standard output and golden corpus stay lean; the WASM
	// map build and qw-analyze -include projectiles,beams turn it on.
	BuildShotStreams bool

	// BuildNails opts into nail (ng/sng) processing: decoding svc_nails,
	// bracketing each nail's flight for ng/sng → damage linking, and (with
	// BuildShotStreams) the nail map stream. Off by default — nails are high
	// volume, so this is a separate request (qw-analyze -include nails).
	BuildNails bool

	// PhaseTimings holds per-phase wall-clock durations from the most
	// recent analyzeSource run (init, event pass, each analyzer's
	// Finalize, each post-processor). Repopulated every run; read by the
	// WASM entry for the browser-console timing breakdown. Not part of
	// the Result schema.
	PhaseTimings []PhaseTiming
}

// ResultPostProcessor mutates the assembled Result after every
// analyser has finalised. Examples: time normalisation (rebase to
// match-relative), duel-mode team rewrites, locgraph synthesis from
// timeline buckets. The function receives CoreOutputs so it can read
// demoinfo / name tables / frag log without re-deriving them.
type ResultPostProcessor func(result *Result, co *CoreOutputs)

// postProcName resolves a post-processor's function name for timing
// labels (e.g. "locGraphPost"), trimming the package path. Used only by
// the instrumentation in analyzeSource.
func postProcName(p ResultPostProcessor) string {
	name := runtime.FuncForPC(reflect.ValueOf(p).Pointer()).Name()
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "anon"
	}
	return name
}

// NewRegistry creates an empty analyzer registry seeded with the
// embedded default config. No analysers or post-processors are
// registered — callers wire those up explicitly (or use
// NewDefaultRegistry).
func NewRegistry() *Registry {
	return &Registry{Config: config.Default()}
}

// Register is a backwards-compatible alias for RegisterDerived. Most
// analysers are derived (they consume CoreOutputs or are independent
// peers); use RegisterCore explicitly when an analyser populates
// CoreOutputs via the CoreProducer interface.
func (r *Registry) Register(a Analyzer) {
	r.RegisterDerived(a)
}

// RegisterCore adds an analyser whose Finalize populates CoreOutputs
// (i.e. it implements CoreProducer). Core analysers finalise before
// any derived analyser so downstream consumers see the produced
// fields. Within the core slice, registration order is preserved —
// later core analysers can read fields populated by earlier ones via
// CoreConsumer.
func (r *Registry) RegisterCore(a Analyzer) {
	r.core = append(r.core, a)
}

// RegisterDerived adds an analyser that consumes CoreOutputs (or is
// independent of it). Derived analysers finalise after every core
// analyser has populated CoreOutputs.
func (r *Registry) RegisterDerived(a Analyzer) {
	r.derived = append(r.derived, a)
}

// RegisterPostProcessor adds a Result post-processor. They run in
// registration order after every analyser has finalised.
// SetRegionsOverride threads a caller-supplied region definition list
// down to whatever TimelineAnalyzer is registered. Used by the CLI's
// -regions flag and by tests pinning specific region layouts. Pass nil
// to clear. No-op when no TimelineAnalyzer is registered.
func (r *Registry) SetRegionsOverride(regs []config.MapRegionOverride) {
	for _, a := range r.derived {
		if ta, ok := a.(*TimelineAnalyzer); ok {
			ta.SetRegionsOverride(regs)
		}
	}
}

func (r *Registry) RegisterPostProcessor(p ResultPostProcessor) {
	r.postProcessors = append(r.postProcessors, p)
}

// Analyze runs all registered analyzers on an MVD file at the given path.
// Gzip is auto-detected.
func (r *Registry) Analyze(filePath string) (*Result, error) {
	src, err := mvdsource.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	src.Parser().SetDecodeNails(r.BuildNails)
	return r.analyzeSource(src, filePath)
}

// AnalyzeReader runs all registered analyzers on an MVD byte stream.
// Provided as a convenience for callers that already have bytes in hand
// (notably the WASM entry, which receives a JS Uint8Array).
func (r *Registry) AnalyzeReader(reader io.Reader, filename string) (*Result, error) {
	src, err := mvdsource.NewFromReader(reader)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	src.Parser().SetDecodeNails(r.BuildNails)
	return r.analyzeSource(src, filename)
}

// AnalyzeSource runs all registered analyzers against an events.Source.
// This is the source-agnostic entry point: any Source implementation
// (MVD file, QTV live, JSON replay) satisfies the interface.
// `filename` is a display label that flows into Result.FilePath.
func (r *Registry) AnalyzeSource(source events.Source, filename string) (*Result, error) {
	return r.analyzeSource(source, filename)
}

func (r *Registry) analyzeSource(source events.Source, filename string) (*Result, error) {
	r.PhaseTimings = r.PhaseTimings[:0]
	record := func(name string, start time.Time) {
		r.PhaseTimings = append(r.PhaseTimings, PhaseTiming{
			Name: name,
			Ms:   float64(time.Since(start).Microseconds()) / 1000,
		})
	}

	ctx := &Context{
		ShotStreams: r.BuildShotStreams,
		Nails:       r.BuildNails,
	}

	// Execution is driven by the DAG's topological node order (dag.go).
	// For the default registry this is the validated topo sort; for a
	// hand-built one it falls back to registration order. Either way all
	// analyzer nodes precede all post-processor nodes, and core precedes
	// derived, so the phase structure below is identical to the previous
	// hand-ordered slices.
	nodes := r.execOrder()

	initStart := time.Now()
	for _, n := range nodes {
		if n.analyzer == nil {
			continue
		}
		if err := n.analyzer.Init(ctx); err != nil {
			return nil, err
		}
	}
	record("init", initStart)

	eventStart := time.Now()
	var streamErr error
	for {
		event, err := source.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A clean end of demo arrives as io.EOF (reader F2); any other
			// error means the event stream was truncated mid-demo (a decode
			// failure, a corrupt or cut-off file). Partial results are still
			// usable, so stop the pass but record the abort into
			// Result.Errors below so a consumer can distinguish a truncated
			// parse from a clean one.
			streamErr = err
			break
		}

		if e, ok := event.(*events.ServerDataEvent); ok {
			ctx.ServerData = e.Data
		}
		if e, ok := event.(*events.UserInfoEvent); ok {
			ctx.Players[e.Player.Slot] = e.Player
		}
		// Analyser nodes see every event in topological order, which
		// places core before derived exactly as the previous two-slice
		// loop did.
		for _, n := range nodes {
			if n.analyzer == nil {
				continue
			}
			if err := n.analyzer.OnEvent(event); err != nil {
				return nil, err
			}
		}
	}
	record("eventPass", eventStart)

	result := &Result{
		SchemaVersion: resultpkg.CurrentSchemaVersion,
		FilePath:      filename,
	}
	if streamErr != nil {
		result.Errors = append(result.Errors, "event stream aborted: "+streamErr.Error())
	}

	co := &CoreOutputs{}

	// finalizeOne runs one analyser's Finalize with CoreOutputs plumbing:
	// a CoreConsumer reads the running CoreOutputs before its Finalize, and
	// a CoreProducer publishes into it after — so a node finalised later
	// (core or derived) sees an earlier core node's fields (e.g. Frag reads
	// co.Names produced by DemoInfo). Topological order keeps all core
	// nodes ahead of derived, so CoreOutputs is complete before any derived
	// Finalize runs.
	finalizeOne := func(a Analyzer) {
		start := time.Now()
		defer func() { record("finalize:"+a.Name(), start) }()
		if cc, ok := a.(CoreConsumer); ok {
			cc.UseCoreOutputs(co)
		}
		if err := a.Finalize(result); err != nil {
			result.Errors = append(result.Errors, err.Error())
			return
		}
		if cp, ok := a.(CoreProducer); ok {
			cp.PopulateCore(co)
		}
	}
	// Finalize analyser nodes, then run post-processors, in one pass over
	// the topological order. The DAG guarantees every analyser node
	// precedes every post-processor node (post nodes only require analyser
	// artifacts or barrier pseudo-artifacts), so this reproduces the old
	// two-phase "all Finalize, then all post-processors" structure — and
	// core precedes derived within the analyser prefix. CoreOutputs is
	// fully populated by the time any derived Finalize or post-processor
	// runs. The default post-processor order (encoded in dag.go as the
	// §1.3 edge list + ordering barriers) is: recover-telefrag-teamkills →
	// normalize-match-relative-times → derive-demo-start-anchor →
	// duel-team-normalize → aim → airgibs → scoreboard-stats → loc-graph →
	// region-control.
	for _, n := range nodes {
		switch {
		case n.analyzer != nil:
			finalizeOne(n.analyzer)
		case n.post != nil:
			start := time.Now()
			n.post(result, co)
			record("post:"+postProcName(n.post), start)
		}
	}

	return result, nil
}

// NewDefaultRegistry creates a registry with all default analyzers,
// configured from the embedded defaults in qwanalytics/config. Callers
// that want to override config values should construct this registry
// and mutate r.Config fields before calling Analyze — analyzers pick
// up their configured values from the registry at construction time,
// so further mutations are applied here via targeted setters.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()

	// Core: the producers that downstream analysers read via
	// CoreOutputs. Clock runs first — it has no core dependencies and
	// publishes co.Clock (the match-relative time base) that every
	// producer converts against at Finalize, replacing the old
	// whole-Result time rebase.
	r.RegisterCore(NewClockAnalyzer())
	// DemoInfo runs next so co.{DemoInfo,Names,Slots} are populated
	// before Frag's Finalize re-evaluates teamkills against co.Names.
	r.RegisterCore(NewDemoInfoAnalyzer())
	// Identity runs right after demoinfo: its PopulateCore reads
	// ctx.DemoInfo (set by demoinfo's Finalize) to fold reconnect
	// sessions into canonical identities, and publishes the per-slot
	// session table the discrete + stream outputs resolve against.
	r.RegisterCore(NewIdentityAnalyzer())
	r.RegisterCore(NewFragAnalyzer())

	// Derived: every other analyser. They consume CoreOutputs (via
	// UseCoreOutputs) or are independent peers, and they never write
	// to CoreOutputs themselves. Order within the derived slice is
	// preserved but no derived analyser depends on another's output.
	r.RegisterDerived(NewMetadataAnalyzer())
	r.RegisterDerived(NewMatchAnalyzer())
	r.RegisterDerived(NewMessagesAnalyzer())
	ta := NewTimelineAnalyzer()
	ta.SetBlipThresholdMs(r.Config.LocGraph.BlipThresholdMs)
	r.RegisterDerived(ta)
	r.RegisterDerived(NewItemAnalyzer())
	r.RegisterDerived(NewDamageAnalyzer())
	r.RegisterDerived(NewShotsAnalyzer())
	r.RegisterDerived(NewMapEntitiesAnalyzer())
	r.RegisterDerived(NewBackpackAnalyzer())
	r.RegisterDerived(NewWeaponPickupsAnalyzer())

	// Post-processors run in registration order on the assembled Result.
	// Timestamps are already match-relative and the demo-start anchor is
	// already written (every producer converts against co.Clock at Finalize),
	// so there is no longer a whole-Result time rebase here. Order still
	// matters: telefrag-teamkill recovery runs first (it appends to the frag
	// log the scoreboard then reads); the duel team rewrite runs before the
	// consumers that read per-player team labels (aim, scoreboard, locgraph,
	// regionControl); locgraph and regionControl last.
	r.RegisterPostProcessor(recoverTelefragTeamkills)
	// Line of sight is NOT a default post-processor — it is the heaviest
	// position-derived pass and has no in-pipeline consumer, so it is computed
	// lazily on demand via analyzer.ComputeLOS (web overlay / -include los /
	// the mvd-api /los endpoint).
	r.RegisterPostProcessor(duelTeamNormalize)
	// Aim runs after the duel team rewrite so it sees stable team labels for
	// enemy attribution; fire/position times are already match-relative. It
	// reads Shots + Streams + Damage; it writes only Result.Aim.
	r.RegisterPostProcessor(aimPost)
	r.RegisterPostProcessor(airgibsPost)
	r.RegisterPostProcessor(scoreboardStatsPost)
	r.RegisterPostProcessor(locGraphPost)
	r.RegisterPostProcessor(regionControlPost)

	// Make the pipeline's dependency DAG explicit: declare each node's
	// Requires/Provides (dag.go), validate the wiring, and derive the
	// execution order from it. The derived order equals this registration
	// order by construction (dag_test.go), so behaviour is unchanged — the
	// DAG turns silent mis-ordering into a startup panic. Panics on a
	// wiring bug (a programmer error); a test asserts the default graph is
	// valid so it can never ship.
	r.buildGraph()
	return r
}

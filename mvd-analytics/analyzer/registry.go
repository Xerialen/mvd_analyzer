package analyzer

import (
	"fmt"
	"io"
	"reflect"
	"runtime"
	"sort"
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
	// analyzers are the event-reading nodes: each sees Init / OnEvent over
	// the event stream and then Finalize. Some publish CoreOutputs
	// (DemoInfo, NameTable, FragEntries, …) via the CoreProducer interface;
	// others consume them via CoreConsumer or are independent peers.
	// Membership here is inventory, not an ordering mechanism — what
	// guarantees a producer runs before its consumers is the consumers'
	// declared edges in dag.go.
	analyzers []Analyzer

	// postProcessors are the non-event nodes: they run only in the finalize
	// pass, reading already-produced artifacts (and CoreOutputs) off the
	// assembled Result and refining it in place. They are ordered, like
	// everything else, by their declared edges — not by being "last".
	postProcessors []ResultPostProcessor
	Config         *config.Config

	// specs is the registration-order node list with declared artifact
	// edges; nodes is that list in validated topological execution order.
	// Both are populated by buildGraph (called from NewDefaultRegistry).
	// A hand-built registry (NewRegistry + Register*) leaves them nil and
	// executes in registration order (see execOrder). See dag.go.
	specs []nodeSpec
	nodes []nodeSpec

	// orderOverride, when non-nil, forces analyzeSource to drive execution
	// (both the event pass and Finalize/post) from this node list instead
	// of the derived topological order. It is a test-only seam used by
	// TestOrderIndependence to feed a shuffled but still-valid topological
	// order; production never sets it. Not part of any public API.
	orderOverride []nodeSpec

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

// ResultPostProcessor is a non-event node: it runs in the finalize pass,
// reading already-produced artifacts off the assembled Result (and the
// CoreOutputs bundle, so it can reach demoinfo / name tables / frag log
// without re-deriving them) and refining the Result in place. Examples:
// telefrag team-kill recovery, aim analysis, locgraph synthesis from the
// timeline. It declares Requires/Provides edges in dag.go exactly like an
// analyzer node; only the absence of an event pass distinguishes it.
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

// Register adds an event-reading analyzer node. Whether it publishes
// CoreOutputs (CoreProducer), consumes them (CoreConsumer), or does
// neither is a property of the concrete type, not of how it is registered;
// execution order comes entirely from the node's declared edges in dag.go,
// so a producer and its consumers may be registered in any order.
func (r *Registry) Register(a Analyzer) {
	r.analyzers = append(r.analyzers, a)
}

// SetRegionsOverride threads a caller-supplied region definition list
// down to whatever TimelineAnalyzer is registered. Used by the CLI's
// -regions flag and by tests pinning specific region layouts. Pass nil
// to clear. No-op when no TimelineAnalyzer is registered.
func (r *Registry) SetRegionsOverride(regs []config.MapRegionOverride) {
	for _, a := range r.analyzers {
		if ta, ok := a.(*TimelineAnalyzer); ok {
			ta.SetRegionsOverride(regs)
		}
	}
}

// EnableDiagnosticPositionCapture threads the explicit whole-demo position
// mode down to the registered TimelineAnalyzer. It exists for closed
// diagnostic views only; NewDefaultRegistry otherwise remains match-gated.
func (r *Registry) EnableDiagnosticPositionCapture() {
	for _, a := range r.analyzers {
		if ia, ok := a.(*IdentityAnalyzer); ok {
			ia.EnableDiagnosticIdentityIsolation()
		}
		if ta, ok := a.(*TimelineAnalyzer); ok {
			ta.EnableDiagnosticPositionCapture()
		}
	}
}

// RegisterPostProcessor adds a non-event post-processor node. Its declared
// edges (dag.go) place it after the artifacts it reads; there is no "runs
// last" guarantee beyond what those edges express.
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

// AnalyzeSourceStrict runs all registered analyzers but rejects source decode
// errors instead of finalizing a partial result. It is intended for evidence
// paths where incomplete output must never be mistaken for a complete demo.
func (r *Registry) AnalyzeSourceStrict(source events.Source, filename string) (*Result, error) {
	return r.analyzeSourceWithMode(source, filename, true)
}

func (r *Registry) analyzeSource(source events.Source, filename string) (*Result, error) {
	return r.analyzeSourceWithMode(source, filename, false)
}

func (r *Registry) analyzeSourceWithMode(source events.Source, filename string, strict bool) (*Result, error) {
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
	// hand-built one it falls back to registration order. The three passes
	// below (Init, event, Finalize+post) each iterate this one node list;
	// only n.analyzer vs n.post — not any tier — decides what each pass does
	// with a node.
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
	eventCount := 0
	for {
		event, err := source.Next()
		if err == io.EOF {
			if strict && eventCount == 0 {
				return nil, fmt.Errorf("read source: no events")
			}
			break
		}
		if err != nil {
			if strict {
				return nil, fmt.Errorf("read source: %w", err)
			}
			// A clean end of demo arrives as io.EOF (reader F2); any other
			// error means the event stream was truncated mid-demo (a decode
			// failure, a corrupt or cut-off file). Partial results are still
			// usable, so stop the pass but record the abort into
			// Result.Errors below so a consumer can distinguish a truncated
			// parse from a clean one.
			streamErr = err
			break
		}
		eventCount++

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
	// a CoreProducer publishes into it after — so a consumer sees every
	// field whose producer its declared edges schedule first (e.g. Frag
	// reads co.Names because frag requires demoinfo). That per-edge
	// guarantee is the only one: an unrelated pair may finalise in either
	// order (TestOrderIndependence proves the output doesn't care).
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
	// Finalize analyser nodes and run post-processors in one pass over
	// the topological order. Only the declared edges constrain the
	// sequence; under the default registration tie-break this comes out
	// as the familiar core → derived → post grouping, but that grouping
	// is a readability property of the default order, not a guarantee —
	// any valid order yields identical output (TestOrderIndependence).
	// The whole-Result time rebase and duel team rewrite are gone —
	// producers are born correct via co.Clock / co.Roster.
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

	canonicalizeErrors(result.Errors)
	if strict && len(result.Errors) > 0 {
		return nil, fmt.Errorf("analysis failed: %s", strings.Join(result.Errors, "; "))
	}
	return result, nil
}

// canonicalizeErrors sorts Result.Errors into a schedule-independent
// order. The three sinks (event-pass stream abort, per-node Finalize
// failures, the region-control post-processor) all *append* during
// execution, so their array order is the execution order — two valid
// topological schedules would otherwise emit differently-ordered arrays
// for the same demo, breaking the "output is a pure function of the demo"
// property (enforced by TestOrderIndependence). Errors are diagnostics,
// not data, so this is not a schema change.
//
// Rule (simplest correct one): the stream-abort entry, if present, sorts
// first — it reports a truncated parse, the signal a consumer most needs
// up front — and the remaining node/post errors sort lexicographically.
func canonicalizeErrors(errs []string) {
	if len(errs) < 2 {
		return
	}
	const abortPrefix = "event stream aborted: "
	sort.SliceStable(errs, func(i, j int) bool {
		ai := strings.HasPrefix(errs[i], abortPrefix)
		aj := strings.HasPrefix(errs[j], abortPrefix)
		if ai != aj {
			return ai // abort entry first
		}
		return errs[i] < errs[j]
	})
}

// NewDefaultRegistry creates a registry with all default analyzers,
// configured from the embedded defaults in qwanalytics/config. Callers
// that want to override config values should construct this registry
// and mutate r.Config fields before calling Analyze — analyzers pick
// up their configured values from the registry at construction time,
// so further mutations are applied here via targeted setters.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()

	// The event-reading analyzers. Registration order is inventory, not
	// scheduling: the DAG orders every node by its declared edges (dag.go),
	// so the sequence below is only a stable default tie-break. The
	// per-node comments record why each producer's data is ready when its
	// consumers read it — but it is the declared edge, not the position in
	// this list, that guarantees it.
	//
	// Clock publishes co.Clock (the match-relative time base) that every
	// timestamped producer converts against at Finalize, replacing the old
	// whole-Result time rebase.
	r.Register(NewClockAnalyzer())
	// DemoInfo publishes co.{DemoInfo,Names,Slots}; Frag re-evaluates
	// teamkills against co.Names.
	r.Register(NewDemoInfoAnalyzer())
	// Identity's PopulateCore reads ctx.DemoInfo (set by demoinfo's
	// Finalize) to fold reconnect sessions into canonical identities, and
	// publishes the per-slot session table the discrete + stream outputs
	// resolve against.
	r.Register(NewIdentityAnalyzer())
	r.Register(NewFragAnalyzer())
	// Roster's PopulateCore reads the fully populated co.DemoInfo to publish
	// the canonical player/team table with the duel rewrite folded in, which
	// every team-aware producer reads to stamp final team labels at emission.
	r.Register(NewRosterAnalyzer())

	r.Register(NewMetadataAnalyzer())
	r.Register(NewMatchAnalyzer())
	r.Register(NewMessagesAnalyzer())
	ta := NewTimelineAnalyzer()
	ta.SetBlipThresholdMs(r.Config.LocGraph.BlipThresholdMs)
	r.Register(ta)
	r.Register(NewItemAnalyzer())
	r.Register(NewDamageAnalyzer())
	r.Register(NewShotsAnalyzer())
	r.Register(NewMapEntitiesAnalyzer())
	r.Register(NewBackpackAnalyzer())
	r.Register(NewWeaponPickupsAnalyzer())

	// Post-processors operate on the assembled Result. Registration order
	// here is INVENTORY, not scheduling: execution order is derived from the
	// declared edges in dag.go, and any valid order produces byte-identical
	// output (TestOrderIndependence) — e.g. the frags-final telefrag recovery
	// runs before match-final because the edge says so, not because of this
	// list. Register new nodes wherever reads naturally; declare their edges
	// in dag.go. Timestamps arrive match-relative and team labels born-final
	// (co.Clock / co.Roster at each producer's Finalize), so there is no
	// whole-Result rebase or duel rewrite here.
	r.RegisterPostProcessor(recoverTelefragTeamkills)
	// Line of sight is NOT a default post-processor — it is the heaviest
	// position-derived pass and has no in-pipeline consumer, so it is computed
	// lazily on demand via analyzer.ComputeLOS (web overlay / -include los /
	// the mvd-api /los endpoint).
	//
	// Aim reads Shots + Streams + Damage — all born with final team labels — and
	// writes only Result.Aim; fire/position times are already match-relative.
	r.RegisterPostProcessor(aimPost)
	r.RegisterPostProcessor(airgibsPost)
	r.RegisterPostProcessor(scoreboardStatsPost)
	r.RegisterPostProcessor(locGraphPost)
	r.RegisterPostProcessor(regionControlPost)
	r.RegisterPostProcessor(openingPost)

	// Declare each node's Requires/Provides (dag.go), validate the wiring,
	// and derive the execution order from it — the DAG turns silent
	// mis-ordering into a startup panic, and the output is identical under
	// any valid order (TestOrderIndependence). Panics on a wiring bug (a
	// programmer error); a test asserts the default graph is valid so it
	// can never ship.
	r.buildGraph()
	return r
}

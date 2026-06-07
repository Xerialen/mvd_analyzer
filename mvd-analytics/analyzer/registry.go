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
		FragsBySlot: make(map[int]int),
	}

	initStart := time.Now()
	for _, a := range r.core {
		if err := a.Init(ctx); err != nil {
			return nil, err
		}
	}
	for _, a := range r.derived {
		if err := a.Init(ctx); err != nil {
			return nil, err
		}
	}
	record("init", initStart)

	eventStart := time.Now()
	for {
		event, err := source.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Log and stop; partial results still usable downstream.
			break
		}

		if e, ok := event.(*events.ServerDataEvent); ok {
			ctx.ServerData = e.Data
		}
		if e, ok := event.(*events.UserInfoEvent); ok {
			ctx.Players[e.Player.Slot] = e.Player
		}
		if e, ok := event.(*events.FragUpdateEvent); ok {
			ctx.FragsBySlot[e.PlayerNum] = e.Frags
		}

		// Core analysers see events first, then derived. Within each
		// slice, registration order is preserved.
		for _, a := range r.core {
			if err := a.OnEvent(event); err != nil {
				return nil, err
			}
		}
		for _, a := range r.derived {
			if err := a.OnEvent(event); err != nil {
				return nil, err
			}
		}
	}
	record("eventPass", eventStart)

	result := &Result{
		SchemaVersion: resultpkg.CurrentSchemaVersion,
		FilePath:      filename,
	}

	co := &CoreOutputs{}

	// Phase 1 — core finalises and populates CoreOutputs. Each core
	// analyser also gets a chance to read the running CoreOutputs
	// (UseCoreOutputs) so a later core entry can consume an earlier
	// core entry's fields (e.g. Frag reads co.Names produced by
	// DemoInfo).
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
	for _, a := range r.core {
		finalizeOne(a)
	}
	// Phase 2 — derived. CoreOutputs is fully populated by the time
	// any derived Finalize runs.
	for _, a := range r.derived {
		finalizeOne(a)
	}

	// Run registered post-processors in order. Each one operates on the
	// fully-finalized Result; CoreOutputs is passed through for read
	// access. The default ordering (set in NewDefaultRegistry) is:
	//   1. recoverTelefragTeamkills
	//   2. normalizeMatchRelativeTimes
	//   3. duelTeamNormalize
	//   4. scoreboardStatsPost
	//   5. locGraphPost
	//   6. regionControlPost
	// — but the slice is otherwise unconstrained. Add a step by
	// calling r.RegisterPostProcessor(...) before Analyze.
	for _, p := range r.postProcessors {
		start := time.Now()
		p(result, co)
		record("post:"+postProcName(p), start)
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
	// CoreOutputs. DemoInfo runs first so co.{DemoInfo,Names,Slots}
	// are populated before Frag's Finalize re-evaluates teamkills
	// against co.Names.
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
	r.RegisterDerived(NewMapEntitiesAnalyzer())
	r.RegisterDerived(NewBackpackAnalyzer())
	r.RegisterDerived(NewWeaponPickupsAnalyzer())

	// Post-processors run in registration order on the assembled
	// Result. Order matters: telefrag-teamkill recovery runs first, before
	// the match-relative shift, so obituary times, Streams positions, and
	// FragEvents still share one (demo-relative) clock; time normalisation
	// lands next so downstream processors see match-relative timestamps;
	// duel team rewrite after that so per-player team labels are stable;
	// locgraph and regionControl last because they consume both rewritten
	// teams and normalised time anchors.
	r.RegisterPostProcessor(recoverTelefragTeamkills)
	r.RegisterPostProcessor(normalizeMatchRelativeTimes)
	r.RegisterPostProcessor(deriveDemoStartAnchor)
	r.RegisterPostProcessor(duelTeamNormalize)
	r.RegisterPostProcessor(scoreboardStatsPost)
	r.RegisterPostProcessor(locGraphPost)
	r.RegisterPostProcessor(regionControlPost)
	return r
}

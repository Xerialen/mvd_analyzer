package analyzer

import (
	"io"

	"github.com/mvd-analyzer/qwanalytics/config"
	resultpkg "github.com/mvd-analyzer/qwanalytics/result"
	"github.com/mvd-analyzer/qwdemo/events"
	mvdsource "github.com/mvd-analyzer/qwdemo/source/mvd"
)

// Registry manages registered analyzers. Config carries the tunable
// parameters individual analyzers read; callers may mutate it before
// analyzing to override defaults for a single run.
type Registry struct {
	analyzers []Analyzer
	Config    *config.Config
}

// NewRegistry creates an empty analyzer registry seeded with the
// embedded default config.
func NewRegistry() *Registry {
	return &Registry{Config: config.Default()}
}

// Register adds an analyzer to the registry
func (r *Registry) Register(a Analyzer) {
	r.analyzers = append(r.analyzers, a)
}

// Analyze runs all registered analyzers on an MVD file at the given path.
// Gzip is auto-detected.
func (r *Registry) Analyze(filePath string) (*Result, error) {
	src, err := mvdsource.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	return r.analyzeSource(src, filePath, src.CurrentTime)
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
	return r.analyzeSource(src, filename, src.CurrentTime)
}

// AnalyzeSource runs all registered analyzers against an events.Source.
// This is the source-agnostic entry point: any Source implementation
// (MVD file, QTV live, JSON replay) satisfies the interface.
// `filename` is a display label that flows into Result.FilePath.
func (r *Registry) AnalyzeSource(source events.Source, filename string) (*Result, error) {
	// currentTime is filled in by the MVD source wrapper; an abstract
	// source may not expose a decoder clock, so default to the last
	// event timestamp seen.
	return r.analyzeSource(source, filename, nil)
}

func (r *Registry) analyzeSource(source events.Source, filename string, currentTime func() float64) (*Result, error) {
	ctx := &Context{
		FragsBySlot: make(map[int]int),
	}

	for _, a := range r.analyzers {
		if err := a.Init(ctx); err != nil {
			return nil, err
		}
	}

	var lastTime float64
	for {
		event, err := source.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Log and stop; partial results still usable downstream.
			break
		}
		lastTime = event.EventTime()

		if e, ok := event.(*events.ServerDataEvent); ok {
			ctx.ServerData = e.Data
		}
		if e, ok := event.(*events.UserInfoEvent); ok {
			ctx.Players[e.Player.Slot] = e.Player
		}
		if e, ok := event.(*events.FragUpdateEvent); ok {
			ctx.FragsBySlot[e.PlayerNum] = e.Frags
		}

		for _, a := range r.analyzers {
			if err := a.OnEvent(event); err != nil {
				return nil, err
			}
		}
	}

	duration := lastTime
	if currentTime != nil {
		duration = currentTime()
	}

	result := &Result{
		SchemaVersion: resultpkg.CurrentSchemaVersion,
		FilePath:      filename,
		Duration:      duration,
	}

	for _, a := range r.analyzers {
		output, err := a.Finalize()
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}

		switch a.Name() {
		case "match":
			if m, ok := output.(*MatchResult); ok {
				result.Match = m
				if m.EndTime > 0 {
					result.Duration = m.EndTime
				}
			}
		case "frag":
			if f, ok := output.(*FragResult); ok {
				result.Frags = f
				ctx.FragEntries = f.Frags // Share with timeline analyzer
			}
		case "demoinfo":
			if di, ok := output.(*DemoInfoResult); ok {
				result.DemoInfo = di
				// Patch player names to display names from DemoInfo.
				// This fixes all downstream Finalize() reads so consumers
				// see the in-game display name instead of the auth/login name.
				for slot, info := range ctx.ResolveSlotDemoInfo() {
					if ctx.Players[slot] != nil {
						ctx.Players[slot].Name = info.Name
					}
				}
			}
		case "messages":
			if m, ok := output.(*MessagesResult); ok {
				result.Messages = m
			}
		case "timelineAnalysis":
			if ta, ok := output.(*TimelineAnalysisResult); ok {
				result.TimelineAnalysis = ta
			}
		case "metadata":
			if m, ok := output.(*MetadataResult); ok {
				result.Metadata = m
			}
		}
	}

	// Normalize all times to be match-relative (0-based from match start)
	// This eliminates the need for the frontend to subtract matchStartTime everywhere
	matchStart := 0.0
	if result.TimelineAnalysis != nil {
		matchStart = result.TimelineAnalysis.MatchStartTime
	}
	if matchStart > 0 {
		result.Duration -= matchStart

		if ta := result.TimelineAnalysis; ta != nil {
			for i := range ta.HighResBuckets {
				ta.HighResBuckets[i].T -= matchStart
			}
			for i := range ta.FragEvents {
				ta.FragEvents[i].Time -= matchStart
			}
			for i := range ta.PowerupEvents {
				ta.PowerupEvents[i].Time -= matchStart
				ta.PowerupEvents[i].EndTime -= matchStart
			}
			for i := range ta.FragStreaks {
				ta.FragStreaks[i].Time -= matchStart
				ta.FragStreaks[i].EndTime -= matchStart
			}
			ta.DemoOffset = matchStart
			ta.MatchStartTime = 0

			// Filter out warmup buckets (negative times after normalization)
			filteredHR := ta.HighResBuckets[:0]
			for _, b := range ta.HighResBuckets {
				if b.T >= 0 {
					filteredHR = append(filteredHR, b)
				}
			}
			ta.HighResBuckets = filteredHR
		}

		if result.Messages != nil {
			for i := range result.Messages.Events {
				result.Messages.Events[i].Time -= matchStart
			}
		}

		if result.Frags != nil {
			for i := range result.Frags.Frags {
				result.Frags.Frags[i].Time -= matchStart
			}
		}

		if result.Match != nil {
			result.Match.StartTime -= matchStart
			result.Match.EndTime -= matchStart
		}
	}

	// 1v1 normalization: for duel demos the "team" concept is either
	// meaningless (arbitrary colour tags) or actively broken (bots have
	// no team, get dropped from team-keyed aggregates). Rewrite every
	// team reference to the player's own name so all downstream
	// consumers still see a uniform team-keyed model, and the UI can
	// suppress the now-redundant "Per Team" panels.
	normalizeDuelTeams(result)

	// Aggregate loc-to-loc movement into a graph (runs after time
	// normalization and duel team rewrite so nodes/edges use the same
	// time base and team labels as the rest of the result).
	result.LocGraph = BuildLocGraph(result)

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
	// DemoInfo first so it's available in Context for other analyzers.
	r.Register(NewDemoInfoAnalyzer())
	r.Register(NewMetadataAnalyzer())
	r.Register(NewMatchAnalyzer())
	r.Register(NewFragAnalyzer())
	r.Register(NewMessagesAnalyzer())
	ta := NewTimelineAnalyzer()
	ta.SetBlipThresholdMs(r.Config.LocGraph.BlipThresholdMs)
	r.Register(ta)
	return r
}

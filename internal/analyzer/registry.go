package analyzer

import (
	"github.com/mvd-analyzer/internal/mvd"
	"github.com/mvd-analyzer/internal/parser"
	"github.com/mvd-analyzer/pkg/mvdfile"
)

// Registry manages registered analyzers
type Registry struct {
	analyzers []Analyzer
}

// NewRegistry creates a new analyzer registry
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds an analyzer to the registry
func (r *Registry) Register(a Analyzer) {
	r.analyzers = append(r.analyzers, a)
}

// Analyze runs all registered analyzers on an MVD file
func (r *Registry) Analyze(filePath string) (*Result, error) {
	// Open the file
	f, err := mvdfile.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Create decoder and parser
	decoder := mvd.NewDecoder(f)
	p := parser.NewParser(decoder)

	// Create context
	ctx := &Context{}

	// Initialize all analyzers
	for _, a := range r.analyzers {
		if err := a.Init(ctx); err != nil {
			return nil, err
		}
	}

	// Set up event handler to dispatch to all analyzers
	p.OnEvent(func(event parser.Event) error {
		// Update context on server data
		if e, ok := event.(*parser.ServerDataEvent); ok {
			ctx.ServerData = e.Data
		}
		// Update context on user info
		if e, ok := event.(*parser.UserInfoEvent); ok {
			ctx.Players[e.Player.Slot] = e.Player
		}

		// Dispatch to all analyzers
		for _, a := range r.analyzers {
			if err := a.OnEvent(event); err != nil {
				return err
			}
		}
		return nil
	})

	// Parse the demo
	if err := p.Parse(); err != nil && err != mvd.ErrEndOfDemo {
		// Log error but continue to get partial results
	}

	// Build result
	result := &Result{
		FilePath: filePath,
		Duration: decoder.CurrentTime(),
	}

	// Finalize all analyzers
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
				// Use match duration if detected (more accurate than file duration)
				if m.Duration > 0 && m.StartTime > 0 {
					result.Duration = m.Duration
				}
			}
		case "frag":
			if f, ok := output.(*FragResult); ok {
				result.Frags = f
			}
		case "stats":
			if s, ok := output.(*StatsResult); ok {
				result.Stats = s
			}
		}
	}

	return result, nil
}

// NewDefaultRegistry creates a registry with all default analyzers
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(NewMatchAnalyzer())
	r.Register(NewFragAnalyzer())
	r.Register(NewStatsAnalyzer())
	return r
}

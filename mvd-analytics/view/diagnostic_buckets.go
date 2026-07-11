package view

import (
	"fmt"

	"github.com/mvd-analyzer/mvd-analytics/result"
)

// DiagnosticBucketsOptions closes the non-match position evidence axis.
// SourceEndMs is the concrete MVD duration and therefore the exclusive end of
// [0, SourceEndMs). A captured position at that exact timestamp extends the
// stream's MatchEnd to timestamp+1; DiagnosticBuckets takes the later bound so
// the endpoint sample remains observable in a one-millisecond partial bucket.
type DiagnosticBucketsOptions struct {
	WindowMs    int
	SourceEndMs int32
}

// DiagnosticBuckets projects only native position samples onto a complete
// demo-relative axis. It deliberately does not reuse Buckets: the ordinary
// match view has spawn/death liveness and carry-forward semantics that would
// make diagnostic evidence depend on non-position streams.
func DiagnosticBuckets(r *result.Result, opts DiagnosticBucketsOptions) (*BucketsView, error) {
	windowMs, _, err := resolveWindow(opts.WindowMs)
	if err != nil {
		return nil, err
	}
	if opts.SourceEndMs < 0 {
		return nil, fmt.Errorf("SourceEndMs must be >= 0, got %d", opts.SourceEndMs)
	}

	exclusiveEndMs := int64(opts.SourceEndMs)
	var players []result.PlayerStream
	if r != nil && r.Streams != nil {
		if streamEnd := int64(r.Streams.Global.MatchEnd); streamEnd > exclusiveEndMs {
			exclusiveEndMs = streamEnd
		}
		players = r.Streams.Players
	}
	if exclusiveEndMs == 0 {
		return &BucketsView{WindowMs: windowMs, Buckets: nil}, nil
	}

	window := int64(windowMs)
	bucketCount := int((exclusiveEndMs + window - 1) / window)
	buckets := make([]ViewBucket, bucketCount)
	for i := range buckets {
		startMs := int64(i) * window
		endMs := startMs + window
		if endMs > exclusiveEndMs {
			endMs = exclusiveEndMs
		}
		buckets[i] = ViewBucket{
			T:       float64(startMs) * 0.001,
			Players: make(map[string]map[string]any),
			Partial: endMs-startMs < window,
		}
	}

	seenNames := make(map[string]struct{}, len(players))
	for i := range players {
		player := &players[i]
		track := player.Position
		if track == nil || len(track.T) == 0 {
			continue
		}
		if player.Name == "" {
			return nil, fmt.Errorf("diagnostic position stream %d has an empty identity", i)
		}
		if _, exists := seenNames[player.Name]; exists {
			return nil, fmt.Errorf("diagnostic position identity %q is ambiguous", player.Name)
		}
		seenNames[player.Name] = struct{}{}

		count := len(track.T)
		if len(track.X) != count || len(track.Y) != count || len(track.Z) != count {
			return nil, fmt.Errorf("diagnostic position identity %q has inconsistent columns", player.Name)
		}
		for sample := 0; sample < count; sample++ {
			if sample > 0 && track.T[sample] < track.T[sample-1] {
				return nil, fmt.Errorf("diagnostic position identity %q timestamps regress", player.Name)
			}
			tMs := int64(track.T[sample])
			if tMs < 0 || tMs >= exclusiveEndMs {
				continue
			}
			bucketIndex := int(tMs / window)
			if _, alreadySampled := buckets[bucketIndex].Players[player.Name]; alreadySampled {
				continue
			}
			buckets[bucketIndex].Players[player.Name] = map[string]any{
				FieldPosition: positionTriple(track, sample),
			}
		}
	}

	return &BucketsView{WindowMs: windowMs, Buckets: buckets}, nil
}

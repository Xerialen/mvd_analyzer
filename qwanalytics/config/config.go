// Package config holds the tunable parameters every qwanalytics
// pipeline run reads. Defaults are embedded from defaults.json so a
// binary built on qwanalytics works with sensible values out of the
// box; callers that want to override a value construct Default() and
// mutate the returned struct before handing it to the registry.
//
// Keep the struct layout mirroring the JSON shape — fields grouped by
// the analyzer or post-processor that owns them — so adding a new
// knob is (1) a field on the right sub-struct, (2) a JSON default,
// and (3) a read site in the code that should consume it.
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed defaults.json
var defaultsJSON []byte

// Config is the full set of analyzer-tunable parameters.
type Config struct {
	LocGraph LocGraphConfig `json:"locGraph"`
}

// LocGraphConfig controls the loc-track smoothing that feeds the loc
// graph, the region-control timeline, and the map view's loc labels.
type LocGraphConfig struct {
	// BlipThresholdMs is the minimum duration a player must reside in a
	// loc for that residence to count as stable. Any shorter "blip"
	// (wall-bleed jitter, nearest-point flicker along a boundary) is
	// re-attributed to an adjacent stable loc before downstream
	// consumers read the per-bucket loc index. 0 disables the filter.
	BlipThresholdMs int `json:"blipThresholdMs"`
}

// Default returns a fresh Config populated from the embedded
// defaults.json. Callers may mutate the returned pointer to override
// individual values for a single pipeline run.
func Default() *Config {
	var c Config
	if err := json.Unmarshal(defaultsJSON, &c); err != nil {
		panic(fmt.Errorf("qwanalytics/config: embedded defaults.json is malformed: %w", err))
	}
	return &c
}

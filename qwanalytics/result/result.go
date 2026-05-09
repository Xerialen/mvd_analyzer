// Package result defines the stable JSON contract produced by running a
// qwanalytics pipeline over a qwdemo.events.Source. Every analyzer's
// Finalize output is a value defined in this package; the top-level
// Result struct is the aggregate returned by the pipeline.
//
// Consumers of qwanalytics (web UI, CLIs, AI agents) should depend on
// this package directly and stay indifferent to where each sub-result
// is computed. The JSON schema is versioned via SchemaVersion so JS
// callers can feature-detect breaking changes.
package result

// CurrentSchemaVersion identifies the JSON schema shape. Bump on any
// breaking change to the Result structure or its sub-types. Consumers
// can pin or switch on this value when reading a stored analysis.
//
// v4 adds Backpacks: a list of RL/LG backpack drops sourced from
// KTX's //ktx drop STUFFCMD_DEMOONLY directive. Pickup tracking is
// intentionally deferred until the wire-flutter reliability issue
// is resolved — see qwanalytics/analyzer/backpacks.go for the full
// reasoning.
//
// v5 adds WeaponPickups: a list of slot-weapon acquisition events
// (world spawners via //ktx took, RL/LG backpacks via //ktx bp)
// with an effectiveness metric — kills with the weapon before the
// picker's next death. Backpack pickups carry BackpackEnt which pairs
// with Backpacks[i].EntNum so frontends can join drop ↔ pickup.
//
// v6:
//   - HighResPlayerData adds GL, Shells, Nails (sh/nl/gl JSON keys);
//     HighResTeamData adds GL.
//   - MatchEvent adds MessageClean (markup-stripped chat text); raw
//     Message preserved.
//   - RegionControlResult adds explicit Locs[] on each region plus
//     TeamA/TeamB labels, BucketStates (compact one-char-per-bucket)
//     and Stats (match-aggregate percentages) — region control is now
//     computed in Go and re-callable via WASM.
//   - Top-level Result.Duration removed (use Match.Duration or
//     DemoInfo.Duration).
//   - MatchResult.PlayerStat drops dead Kills/Deaths fields (always
//     0; consumers read FragResult.ByPlayer or DemoInfoResult).
const CurrentSchemaVersion = 6

// Result is the aggregate output of a qwanalytics pipeline run. Each
// top-level field is produced by one or more analyzers; omitted fields
// mean no analyzer contributed that section (for example, because the
// source lacked the necessary events).
//
// Match length: read MatchResult.Duration (float seconds, parser-derived)
// or DemoInfoResult.Duration (integer seconds, KTX-authoritative).
type Result struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	FilePath         string                  `json:"filePath"`
	Match            *MatchResult            `json:"match,omitempty"`
	Frags            *FragResult             `json:"frags,omitempty"`
	Messages         *MessagesResult         `json:"messages,omitempty"`
	DemoInfo         *DemoInfoResult         `json:"demoInfo,omitempty"`
	TimelineAnalysis *TimelineAnalysisResult `json:"timelineAnalysis,omitempty"`
	Metadata         *MetadataResult         `json:"metadata,omitempty"`
	LocGraph         *LocGraphResult         `json:"locGraph,omitempty"`
	Items            *ItemsResult            `json:"items,omitempty"`
	Backpacks        []BackpackDrop          `json:"backpacks,omitempty"`
	WeaponPickups    []WeaponPickup          `json:"weaponPickups,omitempty"`
	Errors           []string                `json:"errors,omitempty"`
}

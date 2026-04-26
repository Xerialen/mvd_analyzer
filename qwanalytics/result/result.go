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
// v6 adds Denials: per-match lists of "denied" (stolen from enemy)
// and "hoovered" (stolen from teammate) item pickups, derived from
// Items + LocGraph + the per-bucket player-state stream.
const CurrentSchemaVersion = 6

// Result is the aggregate output of a qwanalytics pipeline run. Each
// top-level field is produced by one or more analyzers; omitted fields
// mean no analyzer contributed that section (for example, because the
// source lacked the necessary events).
type Result struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	FilePath         string                  `json:"filePath"`
	Duration         float64                 `json:"duration"`
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
	Denials          *DenialsResult          `json:"denials,omitempty"`
	Errors           []string                `json:"errors,omitempty"`
}

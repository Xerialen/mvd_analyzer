// Package events defines the source-agnostic event schema that any
// QuakeWorld data source — recorded MVD demo, live QTV stream, etc. —
// produces. Analytics consumers depend only on this package; they should
// never need to import the mvd-reader/mvd or mvd-reader/parser packages
// directly.
//
// The package is intentionally small: it re-exports the concrete event
// types and their payload domain types from the underlying parser/mvd
// implementations via Go type aliases, and adds the Source iterator
// interface that every source must satisfy.
//
// Type aliases mean that events.ServerDataEvent IS parser.ServerDataEvent —
// not a convertible wrapper — so switching over an events.Event with the
// aliased types works unchanged. A QTV source would construct and emit
// these same types from its own wire format.
package events

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
	"github.com/mvd-analyzer/mvd-reader/parser"
)

// Source is a pull-style iterator over events from a QuakeWorld data
// source. Next returns the next decoded event, or io.EOF at a clean end
// of stream (for an MVD that is either the stream running out or the
// standard svc_disconnect "EndOfDemo" termination). A non-EOF error is
// fatal for the stream, but it is surfaced only after the events the
// failing read had already produced have been returned, so a consumer
// sees the tail of a truncated stream before the error. Callers should
// still call Close to release any underlying resources.
type Source interface {
	Next() (Event, error)
	Close() error
}

// Event is the interface implemented by every concrete event type. Use
// a type switch on Event to dispatch on the specific event kind.
type Event = parser.Event

// Concrete event types emitted on the Source.
type (
	ServerDataEvent          = parser.ServerDataEvent
	UserInfoEvent            = parser.UserInfoEvent
	PrintEvent               = parser.PrintEvent
	StatUpdateEvent          = parser.StatUpdateEvent
	FragUpdateEvent          = parser.FragUpdateEvent
	PlayerPositionEvent      = parser.PlayerPositionEvent
	DamageEvent              = parser.DamageEvent
	DemoInfoEvent            = parser.DemoInfoEvent
	IntermissionEvent        = parser.IntermissionEvent
	StuffTextEvent           = parser.StuffTextEvent
	CenterPrintEvent         = parser.CenterPrintEvent
	ServerInfoEvent          = parser.ServerInfoEvent
	DeathEvent               = parser.DeathEvent
	SpawnEvent               = parser.SpawnEvent
	ItemSpawnEvent           = parser.ItemSpawnEvent
	ItemStateEvent           = parser.ItemStateEvent
	BackpackDropHintEvent    = parser.BackpackDropHintEvent
	ItemPickupHintEvent      = parser.ItemPickupHintEvent
	BackpackPickupHintEvent  = parser.BackpackPickupHintEvent
	ItemPickupPrintEvent     = parser.ItemPickupPrintEvent
	BackpackPickupPrintEvent = parser.BackpackPickupPrintEvent
	DemoStartTimestampEvent  = parser.DemoStartTimestampEvent
	PausedDurationEvent      = parser.PausedDurationEvent
	MoverSpawnEvent          = parser.MoverSpawnEvent
	MoverStateEvent          = parser.MoverStateEvent
	SoundEvent               = parser.SoundEvent
	ProjectileSpawnEvent     = parser.ProjectileSpawnEvent
	ProjectileDespawnEvent   = parser.ProjectileDespawnEvent
	BeamEvent                = parser.BeamEvent
	NailsFrameEvent          = parser.NailsFrameEvent
	Nail                     = parser.Nail
)

// Domain types carried by events — not MVD-specific, shared across all
// QuakeWorld data sources.
type (
	ServerData = mvd.ServerData
	PlayerInfo = mvd.PlayerInfo
)

// Commonly-used constants re-exported.
const (
	MaxClients  = mvd.MaxClients
	PrintLow    = mvd.PrintLow
	PrintMedium = mvd.PrintMedium
	PrintHigh   = mvd.PrintHigh
	PrintChat   = mvd.PrintChat
)

// MatchStartPatterns re-exports the canonical Layer 1 match-start phrase
// table (parser.MatchStartPatterns) so analytics consumers can share the
// single definition without importing the parser package directly. It is
// read-only; do not mutate the returned slice.
var MatchStartPatterns = parser.MatchStartPatterns

// Stat indices for StatUpdateEvent.StatIndex — KTX/QW stat slot IDs.
const (
	StatHealth       = mvd.StatHealth
	StatFrags        = mvd.StatFrags
	StatWeapon       = mvd.StatWeapon
	StatAmmo         = mvd.StatAmmo
	StatArmor        = mvd.StatArmor
	StatWeaponFrame  = mvd.StatWeaponFrame
	StatShells       = mvd.StatShells
	StatNails        = mvd.StatNails
	StatRockets      = mvd.StatRockets
	StatCells        = mvd.StatCells
	StatActiveWeapon = mvd.StatActiveWeapon
	StatTotalSecrets = mvd.StatTotalSecrets
	StatSecrets      = mvd.StatSecrets
	StatMonsters     = mvd.StatMonsters
	StatItems        = mvd.StatItems
	StatViewHeight   = mvd.StatViewHeight
	StatTime         = mvd.StatTime
)

// Item flags decoded from the StatItems stat; used to detect weapons,
// ammo stocks, armor, keys, and powerups.
const (
	ITShotgun         = mvd.ITShotgun
	ITSuperShotgun    = mvd.ITSuperShotgun
	ITNailgun         = mvd.ITNailgun
	ITSuperNailgun    = mvd.ITSuperNailgun
	ITGrenadeLauncher = mvd.ITGrenadeLauncher
	ITRocketLauncher  = mvd.ITRocketLauncher
	ITLightning       = mvd.ITLightning
	ITSuperLightning  = mvd.ITSuperLightning
	ITShells          = mvd.ITShells
	ITNails           = mvd.ITNails
	ITRockets         = mvd.ITRockets
	ITCells           = mvd.ITCells
	ITAxe             = mvd.ITAxe
	ITArmor1          = mvd.ITArmor1 // Green armor
	ITArmor2          = mvd.ITArmor2 // Yellow armor
	ITArmor3          = mvd.ITArmor3 // Red armor
	ITSuperHealth     = mvd.ITSuperHealth
	ITInvisibility    = mvd.ITInvisibility    // Ring of shadows
	ITInvulnerability = mvd.ITInvulnerability // Pentagram
	ITSuit            = mvd.ITSuit
	ITQuad            = mvd.ITQuad
)

// Damage-source helpers re-exported so Layer-2 consumers map a
// DamageEvent.DeathType (and obituary deathtype) to a weapon / cause
// without reaching into the mvd wire package directly.
var (
	// DeathTypeToWeapon maps a deathtype to a weapon name ("rl", "lg",
	// "sg", ...) or damage-source label.
	DeathTypeToWeapon = mvd.DeathTypeToWeapon
	// IsEnvironmentalDamage reports whether a deathtype is
	// world/self-inflicted (lava, slime, drowning, fall, trigger, suicide).
	IsEnvironmentalDamage = mvd.IsEnvironmentalDamage
	// EnvironmentalDamageType returns the environmental category
	// ("lava", "fall", "drown", "trigger", ...) or "" for non-environmental.
	EnvironmentalDamageType = mvd.EnvironmentalDamageType
)

// NormalizeQuakeText folds the Quake extended-ASCII character set into
// plain UTF-8. Players' names and chat come off the wire in the Quake
// encoding; analytics code normalises via this helper before comparing
// names or surfacing chat to consumers.
func NormalizeQuakeText(b []byte) string {
	return parser.NormalizeQuakeText(b)
}

// StripChatMarkup removes ezQuake chat markup (color codes, sound
// triggers, macro delimiters, leading CR) from already Q-normalised
// chat text, leaving plain readable ASCII. Idempotent. Used by
// qwanalytics to populate MatchEvent.MessageClean.
func StripChatMarkup(s string) string {
	return parser.StripChatMarkup(s)
}

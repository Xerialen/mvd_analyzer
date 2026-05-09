// Package events defines the source-agnostic event schema that any
// QuakeWorld data source — recorded MVD demo, live QTV stream, etc. —
// produces. Analytics consumers depend only on this package; they should
// never need to import qwdemo/mvd or qwdemo/parser directly.
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
	"github.com/mvd-analyzer/qwdemo/mvd"
	"github.com/mvd-analyzer/qwdemo/parser"
)

// Source is a pull-style iterator over events from a QuakeWorld data
// source. Next returns the next decoded event, or io.EOF at a clean end
// of stream. A non-EOF error is fatal for the stream; callers should
// still call Close to release any underlying resources.
type Source interface {
	Next() (Event, error)
	Close() error
}

// Event is the interface implemented by every concrete event type. Use
// a type switch on Event to dispatch on the specific event kind.
type Event = parser.Event

// Kind enumerates the concrete event types carried on the Source.
type Kind = parser.EventType

// Kind values — match 1:1 with the concrete event types below.
const (
	KindServerData          = parser.EventServerData
	KindUserInfo            = parser.EventUserInfo
	KindPrint               = parser.EventPrint
	KindStatUpdate          = parser.EventStatUpdate
	KindFragUpdate          = parser.EventFragUpdate
	KindPlayerInfo          = parser.EventPlayerInfo
	KindDamage              = parser.EventDamage
	KindDemoInfo            = parser.EventDemoInfo
	KindIntermission        = parser.EventIntermission
	KindStuffText           = parser.EventStuffText
	KindCenterPrint         = parser.EventCenterPrint
	KindServerInfo          = parser.EventServerInfo
	KindDeath               = parser.EventDeath
	KindSpawn               = parser.EventSpawn
	KindItemSpawn           = parser.EventItemSpawn
	KindItemState           = parser.EventItemState
	KindBackpackDropHint    = parser.EventBackpackDropHint
	KindItemPickupHint      = parser.EventItemPickupHint
	KindBackpackPickupHint  = parser.EventBackpackPickupHint
	KindItemPickupPrint     = parser.EventItemPickupPrint
	KindBackpackPickupPrint = parser.EventBackpackPickupPrint
)

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
	EntityState              = parser.EntityState
)

// Domain types carried by events — not MVD-specific, shared across all
// QuakeWorld data sources.
type (
	ServerData   = mvd.ServerData
	PlayerInfo   = mvd.PlayerInfo
	PlayerState  = mvd.PlayerState
	Stats        = mvd.Stats
	PrintMessage = mvd.PrintMessage
	FragEvent    = mvd.FragEvent
	Vec3         = mvd.Vec3
	Angle3       = mvd.Angle3
)

// Commonly-used constants re-exported.
const (
	MaxClients  = mvd.MaxClients
	PrintLow    = mvd.PrintLow
	PrintMedium = mvd.PrintMedium
	PrintHigh   = mvd.PrintHigh
	PrintChat   = mvd.PrintChat
)

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

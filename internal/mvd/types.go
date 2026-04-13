// Package mvd provides parsing functionality for QuakeWorld MVD demo files.
package mvd

// Message types (dem_*)
const (
	DemCmd      = 0 // User command (QWD only)
	DemRead     = 1 // Network message
	DemSet      = 2 // Sequence numbers (once at start)
	DemMultiple = 3 // Message for multiple players (MVD only)
	DemSingle   = 4 // Message for single player (MVD only)
	DemStats    = 5 // Stats update for player (MVD only)
	DemAll      = 6 // Message for all players (MVD only)
)

// Server command types (svc_*)
const (
	SvcBad                   = 0
	SvcNop                   = 1
	SvcDisconnect            = 2
	SvcUpdateStat            = 3
	SvcSound                 = 6
	SvcPrint                 = 8
	SvcStuffText             = 9
	SvcSetAngle              = 10
	SvcServerData            = 11
	SvcLightStyle            = 12
	SvcUpdateFrags           = 14
	SvcDamage                = 19
	SvcSpawnStatic           = 20
	SvcSpawnBaseline         = 22
	SvcTempEntity            = 23
	SvcSetPause              = 24
	SvcCenterPrint           = 26
	SvcKilledMonster         = 27
	SvcFoundSecret           = 28
	SvcSpawnStaticSound      = 29
	SvcIntermission          = 30
	SvcFinale                = 31
	SvcCDTrack               = 32
	SvcSellScreen            = 33
	SvcSmallKick             = 34
	SvcBigKick               = 35
	SvcUpdatePing            = 36
	SvcUpdateEnterTime       = 37
	SvcUpdateStatLong        = 38
	SvcMuzzleFlash           = 39
	SvcUpdateUserInfo        = 40
	SvcDownload              = 41
	SvcPlayerInfo            = 42
	SvcNails                 = 43
	SvcChokeCount            = 44
	SvcModelList             = 45
	SvcSoundList             = 46
	SvcPacketEntities        = 47
	SvcDeltaPacketEntities   = 48
	SvcMaxSpeed              = 49
	SvcEntGravity            = 50
	SvcSetInfo               = 51
	SvcServerInfo            = 52
	SvcUpdatePL              = 53
	SvcNails2                = 54
	SvcFTEModelListShort     = 60
	SvcFTESpawnBaseline2     = 66
	SvcFTESpawnStatic2       = 21
)

// Delta flags for player info (DF_*)
const (
	DFOrigin      = 1 << 0  // Origin X present
	DFOriginY     = 1 << 1  // Origin Y present
	DFOriginZ     = 1 << 2  // Origin Z present
	DFAngles      = 1 << 3  // Angle pitch present
	DFAnglesY     = 1 << 4  // Angle yaw present
	DFAnglesZ     = 1 << 5  // Angle roll present
	DFEffects     = 1 << 6  // Effects byte present
	DFSkinNum     = 1 << 7  // Skin number present
	DFDead        = 1 << 8  // Player is dead
	DFGIB         = 1 << 9  // Player is gibbed
	DFWeaponFrame = 1 << 10 // Weapon frame present
	DFModel       = 1 << 11 // Model index present
)

// Print levels
const (
	PrintLow    = 0
	PrintMedium = 1
	PrintHigh   = 2
	PrintChat   = 3
)

// Stat indices
const (
	StatHealth       = 0
	StatFrags        = 1
	StatWeapon       = 2
	StatAmmo         = 3
	StatArmor        = 4
	StatWeaponFrame  = 5
	StatShells       = 6
	StatNails        = 7
	StatRockets      = 8
	StatCells        = 9
	StatActiveWeapon = 10
	StatTotalSecrets = 11
	StatTotalMonsters= 12
	StatSecrets      = 13
	StatMonsters     = 14
	StatItems        = 15
	StatViewHeight   = 16
	StatTime         = 17
)

// Item flags
const (
	ITShotgun         = 1 << 0
	ITSuperShotgun    = 1 << 1
	ITNailgun         = 1 << 2
	ITSuperNailgun    = 1 << 3
	ITGrenadeLauncher = 1 << 4
	ITRocketLauncher  = 1 << 5
	ITLightning       = 1 << 6
	ITSuperLightning  = 1 << 7
	ITShells          = 1 << 8
	ITNails           = 1 << 9
	ITRockets         = 1 << 10
	ITCells           = 1 << 11
	ITAxe             = 1 << 12
	ITArmor1          = 1 << 13 // Green armor
	ITArmor2          = 1 << 14 // Yellow armor
	ITArmor3          = 1 << 15 // Red armor
	ITSuperHealth     = 1 << 16
	ITKey1            = 1 << 17
	ITKey2            = 1 << 18
	ITInvisibility    = 1 << 19
	ITInvulnerability = 1 << 20
	ITSuit            = 1 << 21
	ITQuad            = 1 << 22
)

// Protocol extensions
const (
	ProtocolVersionFTE  = 0x58455446 // 'FTEX'
	ProtocolVersionFTE2 = 0x32455446 // 'FTE2'
	ProtocolVersionMVD1 = 0x3144564D // 'MVD1'
	ProtocolVersion     = 28
)

// FTE protocol extension flags
const (
	FTEPextTrans             = 0x00000008
	FTEPextHLBSP             = 0x00000200
	FTEPextModelDbl          = 0x00001000
	FTEPextEntityDbl         = 0x00002000
	FTEPextEntityDbl2        = 0x00004000
	FTEPextFloatCoords       = 0x00008000
	FTEPextSpawnStatic2      = 0x00400000
	FTEPextColourMod         = 0x00080000
	FTEPext256PacketEntities = 0x01000000
	FTEPextChunkedDownloads  = 0x20000000
)

// MVD protocol extension flags
const (
	MVDPext1FloatCoords     = 1 << 0
	MVDPext1HighLagTeleport = 1 << 1
	MVDPext1HiddenMessages  = 1 << 5
)

// Hidden message types
const (
	MVDHiddenAntilagPosition          = 0x0000
	MVDHiddenUserCmd                  = 0x0001
	MVDHiddenUserCmdWeapons           = 0x0002
	MVDHiddenDemoInfo                 = 0x0003
	MVDHiddenCommentaryTrack          = 0x0004
	MVDHiddenCommentaryData           = 0x0005
	MVDHiddenCommentaryTextSegment    = 0x0006
	MVDHiddenDmgDone                  = 0x0007
	MVDHiddenUserCmdWeaponsSS         = 0x0008
	MVDHiddenUserCmdWeaponInstruction = 0x0009
	MVDHiddenPausedDuration           = 0x000A
	MVDHiddenDemoStartTimestampMs     = 0x000B
	MVDHiddenExtended                 = 0xFFFF
)

// MaxClients is the maximum number of players
const MaxClients = 32

// Vec3 represents a 3D vector
type Vec3 [3]float32

// Angle3 represents 3 angles (pitch, yaw, roll)
type Angle3 [3]float32

// MessageHeader represents a demo message header
type MessageHeader struct {
	TimeDelta   uint8
	MessageType uint8
	PlayerNum   int // For dem_single and dem_stats
}

// DemoMessage represents a parsed demo message
type DemoMessage struct {
	Header     MessageHeader
	PlayerMask uint32 // For dem_multiple
	Payload    []byte
	Time       float64 // Cumulative time in seconds
}

// PlayerState represents the state of a player at a point in time
type PlayerState struct {
	PlayerNum   int
	Flags       uint16
	Frame       uint8
	Origin      Vec3
	Angles      Angle3
	ModelIndex  int
	SkinNum     int
	Effects     uint8
	WeaponFrame uint8
	IsDead      bool
	IsGib       bool
	Time        float64
}

// PlayerInfo represents player metadata
type PlayerInfo struct {
	Slot        int
	UserID      int
	Name        string
	Team        string
	TopColor    int
	BottomColor int
	Auth        string // *auth login from userinfo (set by mvdsv for authenticated players)
	Spectator   bool
	Frags       int
	Ping        int
	PL          int // Packet loss
}

// ServerData represents server initialization data
type ServerData struct {
	ProtocolVersion   int
	FTEExtensions     uint32
	FTE2Extensions    uint32
	MVD1Extensions    uint32
	ServerCount       int
	GameDir           string
	ServerTime        float32
	LevelName         string
	MapFile           string // BSP filename from model list (e.g., "maps/dm2.bsp")
	Gravity           float32
	StopSpeed         float32
	MaxSpeed          float32
	SpectatorMaxSpeed float32
	Accelerate        float32
	AirAccelerate     float32
	WaterAccelerate   float32
	Friction          float32
	WaterFriction     float32
	EntGravity        float32
}

// PrintMessage represents a print event
type PrintMessage struct {
	Level   int
	Message string
	Time    float64
}

// FragEvent represents a frag (kill) detected from print messages
type FragEvent struct {
	Killer     string
	Victim     string
	WeaponCode string
	Time       float64
	IsSuicide  bool
	IsTeamKill bool
}

// DamageEvent represents damage dealt (from hidden messages)
type DamageEvent struct {
	Attacker  int
	Victim    int
	Damage    int
	DeathType int  // Weapon/death type that caused the damage
	IsSplash  bool
	Time      float64
}

// Death types (from KTX deathtype.h)
const (
	DtNone       = 0
	DtAxe        = 1
	DtSG         = 2
	DtSSG        = 3
	DtNG         = 4
	DtSNG        = 5
	DtGL         = 6
	DtRL         = 7
	DtLGBeam     = 8
	DtLGDischarge = 9
	DtLGDischargeSelf = 10
	DtHook       = 11
	DtChangeLevel = 12
	DtLava       = 13
	DtSlime      = 14
	DtWater      = 15
	DtFall       = 16
	DtStomp      = 17
	DtTele1      = 18
	DtTele2      = 19
	DtTele3      = 20
	DtTele4      = 21
	DtExploBox   = 22
	DtLaser      = 23
	DtFireball   = 24
	DtSquish     = 25
	DtTriggerHurt = 26
	DtSuicide    = 27
	DtUnknown    = 28
)

// DeathTypeToWeapon converts a death type to weapon/damage source name
func DeathTypeToWeapon(dt int) string {
	switch dt {
	case DtAxe:
		return "axe"
	case DtSG:
		return "sg"
	case DtSSG:
		return "ssg"
	case DtNG:
		return "ng"
	case DtSNG:
		return "sng"
	case DtGL:
		return "gl"
	case DtRL:
		return "rl"
	case DtLGBeam, DtLGDischarge, DtLGDischargeSelf:
		return "lg"
	case DtStomp:
		return "stomp"
	case DtTele1, DtTele2, DtTele3, DtTele4:
		return "tele"
	case DtSquish:
		return "squish"
	case DtExploBox:
		return "explobox"
	default:
		return "unknown"
	}
}

// IsEnvironmentalDamage returns true if the death type is environmental/self-inflicted
func IsEnvironmentalDamage(dt int) bool {
	switch dt {
	case DtLava, DtSlime, DtWater, DtFall, DtTriggerHurt, DtSuicide:
		return true
	default:
		return false
	}
}

// EnvironmentalDamageType returns the environmental damage category
func EnvironmentalDamageType(dt int) string {
	switch dt {
	case DtLava:
		return "lava"
	case DtSlime:
		return "slime"
	case DtWater:
		return "drown"
	case DtFall:
		return "fall"
	case DtTriggerHurt:
		return "trigger"
	case DtSuicide:
		return "suicide"
	case DtSquish:
		return "squish"
	default:
		return ""
	}
}

// Stats represents player statistics
type Stats struct {
	Health       int
	Armor        int
	Shells       int
	Nails        int
	Rockets      int
	Cells        int
	ActiveWeapon int
	Items        int
}

// Demo represents a parsed MVD demo
type Demo struct {
	ServerData    *ServerData
	Players       [MaxClients]*PlayerInfo
	PlayerStates  []PlayerState
	PrintMessages []PrintMessage
	FragEvents    []FragEvent
	DamageEvents  []DamageEvent
	Duration      float64
	Models        []string
	Sounds        []string
}

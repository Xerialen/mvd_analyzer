package parser

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// PlayerPositionEvent is emitted when a player position is updated.
//
// Time is float64 seconds (the derived ergonomic view); TimeMs is the
// canonical wire-native value in integer milliseconds — consumers that
// persist this into the result schema or compare against other ms
// timestamps must use TimeMs to avoid the float-precision drift that
// caused spurious spawn/death-boundary crossings.
type PlayerPositionEvent struct {
	PlayerNum int
	Origin    [3]float32 // X, Y, Z world coordinates
	// Angles is the raw angle16 wire value for [pitch, yaw, roll] — the
	// exact 2-byte short the server wrote, kept losslessly (we do not
	// narrow to float degrees). Decode to degrees with
	// float(uint16(v)) * 360/65536; values land in [0,360), so a pitch
	// > 180 means looking up. Roll is always 0 (the server zeroes it).
	Angles [3]int16
	Time   float64
	TimeMs int32
}

func (e *PlayerPositionEvent) EventType() EventType { return EventPlayerInfo }
func (e *PlayerPositionEvent) EventTime() float64   { return e.Time }

// parsePlayerInfo parses svc_playerinfo message and emits position events
func (p *Parser) parsePlayerInfo(r *mvd.BufferReader, time float64, timeMs int32, floatCoords bool) error {
	playerNum, err := r.ReadByte()
	if err != nil {
		return err
	}

	// Bounds check
	if playerNum >= mvd.MaxClients {
		return skipPlayerInfoRemainder(r, floatCoords)
	}

	flags, err := r.ReadUint16()
	if err != nil {
		return err
	}

	// Skip frame byte
	if err := r.Skip(1); err != nil {
		return err
	}

	// Get stored position for this player (for delta updates)
	origin := p.playerPositions[playerNum]

	// Read origin components (delta encoded - only present if flag is set)
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFOrigin<<i) != 0 {
			var coord float32
			if floatCoords {
				coord, err = r.ReadFloatCoord()
			} else {
				coord, err = r.ReadCoord()
			}
			if err != nil {
				return err
			}
			origin[i] = coord
		}
	}

	// Store updated position
	p.playerPositions[playerNum] = origin

	// Read angle components as the raw angle16 wire short — lossless;
	// bypasses ReadAngle16's float-degree conversion so the exact value
	// the server wrote survives into the result schema.
	var angles [3]int16
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFAngles<<i) != 0 {
			raw, rerr := r.ReadUint16()
			if rerr != nil {
				return rerr
			}
			angles[i] = int16(raw)
		}
	}

	// Skip remaining optional fields
	if flags&mvd.DFModel != 0 {
		if err := r.Skip(1); err != nil {
			return err
		}
	}
	if flags&mvd.DFSkinNum != 0 {
		if err := r.Skip(1); err != nil {
			return err
		}
	}
	if flags&mvd.DFEffects != 0 {
		if err := r.Skip(1); err != nil {
			return err
		}
	}
	if flags&mvd.DFWeaponFrame != 0 {
		if err := r.Skip(1); err != nil {
			return err
		}
	}

	// Only emit position event if we have valid position data
	// (skip if all coordinates are zero - likely uninitialized)
	if origin[0] != 0 || origin[1] != 0 || origin[2] != 0 {
		if err := p.emit(&PlayerPositionEvent{
			PlayerNum: int(playerNum),
			Origin:    origin,
			Angles:    angles,
			Time:      time,
			TimeMs:    timeMs,
		}); err != nil {
			return err
		}
	}

	// DF_DEAD / DF_GIB drive the primary DeathEvent / SpawnEvent path.
	// svc_playerinfo is broadcast for every player on every frame, so
	// this catches transitions the stat-based detector misses when the
	// dem_stats block is addressed to a different player.
	isDead := flags&(mvd.DFDead|mvd.DFGIB) != 0
	if !p.playerSeenInfo[playerNum] {
		p.playerSeenInfo[playerNum] = true
		if isDead {
			// First sample for this slot already shows dead — no prior
			// alive state to transition from, so don't fabricate a
			// DeathEvent. Pre-seed the dedup state so the next alive
			// frame fires a SpawnEvent.
			p.playerDeadKnown[playerNum] = true
			p.playerDead[playerNum] = true
			return nil
		}
		// First sample alive — synthesise a SpawnEvent so analytics has
		// a starting boundary for the player. Deduped against stats.go
		// in case StatHealth has already fired.
		return p.maybeEmitSpawn(int(playerNum), time, timeMs)
	}
	if isDead != p.playerDead[playerNum] {
		if isDead {
			return p.maybeEmitDeath(int(playerNum), time, timeMs)
		}
		return p.maybeEmitSpawn(int(playerNum), time, timeMs)
	}
	return nil
}

// skipPlayerInfoRemainder skips the rest of a playerinfo message after reading player num
func skipPlayerInfoRemainder(r *mvd.BufferReader, floatCoords bool) error {
	flags, err := r.ReadUint16()
	if err != nil {
		return err
	}
	r.Skip(1) // frame

	// Origin components
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFOrigin<<i) != 0 {
			if floatCoords {
				r.Skip(4)
			} else {
				r.Skip(2)
			}
		}
	}
	// Angle components
	for i := 0; i < 3; i++ {
		if flags&(mvd.DFAngles<<i) != 0 {
			r.Skip(2) // angle16
		}
	}
	if flags&mvd.DFModel != 0 {
		r.Skip(1)
	}
	if flags&mvd.DFSkinNum != 0 {
		r.Skip(1)
	}
	if flags&mvd.DFEffects != 0 {
		r.Skip(1)
	}
	if flags&mvd.DFWeaponFrame != 0 {
		r.Skip(1)
	}
	return nil
}

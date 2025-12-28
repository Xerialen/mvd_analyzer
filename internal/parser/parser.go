// Package parser handles parsing of MVD network message payloads.
package parser

import (
	"io"

	"github.com/mvd-analyzer/internal/mvd"
)

// Event represents a parsed game event
type Event interface {
	EventType() EventType
	EventTime() float64
}

// EventType identifies the type of event
type EventType int

const (
	EventServerData EventType = iota
	EventUserInfo
	EventPrint
	EventStatUpdate
	EventFragUpdate
	EventPlayerInfo
	EventDamage
	EventDemoInfo
)

// Handler is called for each parsed event
type Handler func(event Event) error

// Parser parses network message payloads
type Parser struct {
	decoder     *mvd.Decoder
	serverData  *mvd.ServerData
	players     [mvd.MaxClients]*mvd.PlayerInfo
	playerStats [mvd.MaxClients]*mvd.Stats
	handlers    []Handler
	floatCoords bool
}

// NewParser creates a new parser
func NewParser(decoder *mvd.Decoder) *Parser {
	p := &Parser{
		decoder: decoder,
	}
	// Initialize player stats
	for i := range p.playerStats {
		p.playerStats[i] = &mvd.Stats{}
	}
	return p
}

// OnEvent registers an event handler
func (p *Parser) OnEvent(h Handler) {
	p.handlers = append(p.handlers, h)
}

// ServerData returns the parsed server data (available after parsing starts)
func (p *Parser) ServerData() *mvd.ServerData {
	return p.serverData
}

// Players returns the player info array
func (p *Parser) Players() [mvd.MaxClients]*mvd.PlayerInfo {
	return p.players
}

// PlayerStats returns the player stats array
func (p *Parser) PlayerStats() [mvd.MaxClients]*mvd.Stats {
	return p.playerStats
}

// emit sends an event to all handlers
func (p *Parser) emit(event Event) error {
	for _, h := range p.handlers {
		if err := h(event); err != nil {
			return err
		}
	}
	return nil
}

// Parse processes all messages from the decoder
func (p *Parser) Parse() error {
	for {
		msg, err := p.decoder.NextMessage()
		if err != nil {
			if err == mvd.ErrEndOfDemo || err == io.EOF {
				return nil // Normal end
			}
			return err
		}

		if err := p.parseMessage(msg); err != nil {
			return err
		}
	}
}

// parseMessage handles a single demo message
func (p *Parser) parseMessage(msg *mvd.DemoMessage) error {
	if msg.Payload == nil || len(msg.Payload) == 0 {
		return nil
	}

	// Check for hidden messages
	if msg.IsHiddenMessage() {
		return p.parseHiddenMessage(msg)
	}

	// Parse network message payload
	return p.parseNetworkMessage(msg)
}

// parseNetworkMessage parses svc_* commands in the payload
func (p *Parser) parseNetworkMessage(msg *mvd.DemoMessage) error {
	r := mvd.NewBufferReader(msg.Payload)

	for !r.EOF() {
		cmd, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		switch cmd {
		case mvd.SvcServerData:
			if err := p.parseServerData(r, msg.Time); err != nil {
				// Non-fatal, continue with next message
				return nil
			}

		case mvd.SvcUpdateUserInfo:
			if err := p.parseUserInfo(r, msg.Time); err != nil {
				return nil
			}

		case mvd.SvcPrint:
			if err := p.parsePrint(r, msg.Time); err != nil {
				return nil
			}

		case mvd.SvcUpdateStat:
			if err := p.parseUpdateStat(r, msg.Time, msg.Header.PlayerNum); err != nil {
				return nil
			}

		case mvd.SvcUpdateStatLong:
			if err := p.parseUpdateStatLong(r, msg.Time, msg.Header.PlayerNum); err != nil {
				return nil
			}

		case mvd.SvcUpdateFrags:
			if err := p.parseUpdateFrags(r, msg.Time); err != nil {
				return nil
			}

		case mvd.SvcDisconnect:
			// Read disconnect message
			message, _ := r.ReadString()
			if message == "EndOfDemo" {
				return mvd.ErrEndOfDemo
			}

		default:
			// Skip unknown commands - if we can't determine size, skip rest of payload
			if err := skipCommand(r, cmd, p.floatCoords); err != nil {
				// Can't skip this command - bail on this payload, but continue parsing
				return nil
			}
		}
	}

	return nil
}

// parseHiddenMessage parses hidden messages (dem_multiple with player_mask=0)
func (p *Parser) parseHiddenMessage(msg *mvd.DemoMessage) error {
	// Hidden messages contain structured metadata
	r := mvd.NewBufferReader(msg.Payload)
	time := msg.Time

	for r.Remaining() > 0 {
		// Read block length (4 bytes)
		blockLen, err := r.ReadUint32()
		if err != nil {
			return nil // End of data
		}
		if blockLen < 2 || blockLen > 10000 {
			return nil // Invalid block (sanity check)
		}

		// Read type ID (2 bytes)
		typeID, err := r.ReadUint16()
		if err != nil {
			return nil
		}

		// blockLen is the length of the data AFTER the typeID (not including it)
		dataLen := int(blockLen)

		// Parse based on type
		switch typeID {
		case mvd.MVDHiddenDmgDone:
			// parseHiddenDamage handles the damage record and skips extra bytes
			if err := p.parseHiddenDamage(r, time, dataLen); err != nil {
				return nil // Stop on error
			}
		case mvd.MVDHiddenDemoInfo:
			// Parse embedded JSON demoinfo
			if err := p.parseHiddenDemoInfo(r, time, dataLen); err != nil {
				return nil
			}
		default:
			// Skip unknown hidden message types
			if dataLen > 0 {
				if err := r.Skip(dataLen); err != nil {
					return nil
				}
			}
		}
	}

	return nil
}

// parseHiddenDamage parses mvdhidden_dmgdone (0x0007)
// Format: single 8-byte record: <short: flags|deathtype> <short: attacker> <short: victim> <short: damage>
// Note: Each block contains exactly one damage record (8 bytes)
func (p *Parser) parseHiddenDamage(r *mvd.BufferReader, time float64, dataLen int) error {
	// Read exactly one damage record (8 bytes)
	if dataLen < 8 {
		return r.Skip(dataLen)
	}

	// Read flags and death type
	flagsAndType, err := r.ReadUint16()
	if err != nil {
		return err
	}

	// Read attacker entity number
	attackerEnt, err := r.ReadUint16()
	if err != nil {
		return err
	}

	// Read victim entity number
	victimEnt, err := r.ReadUint16()
	if err != nil {
		return err
	}

	// Read damage amount
	damage, err := r.ReadInt16()
	if err != nil {
		return err
	}

	// Skip any extra bytes in this block
	if dataLen > 8 {
		r.Skip(dataLen - 8)
	}

	// Extract splash damage flag (bit 15)
	const splashDamageFlag = 1 << 15
	isSplash := (flagsAndType & splashDamageFlag) != 0
	deathType := int(flagsAndType &^ splashDamageFlag)

	// Convert entity numbers to player numbers (entities are 1-indexed, players are 0-indexed)
	attackerPlayer := int(attackerEnt) - 1
	victimPlayer := int(victimEnt) - 1

	// Only emit if valid player numbers
	if attackerPlayer >= 0 && attackerPlayer < mvd.MaxClients &&
		victimPlayer >= 0 && victimPlayer < mvd.MaxClients &&
		damage > 0 {
		return p.emit(&DamageEvent{
			Attacker:  attackerPlayer,
			Victim:    victimPlayer,
			Damage:    int(damage),
			DeathType: deathType,
			IsSplash:  isSplash,
			Time:      time,
		})
	}

	return nil
}

// parseHiddenDemoInfo parses mvdhidden_demoinfo (0x0003)
// Format: <short: block_number> <bytes: json_content>
// JSON may be split across multiple blocks
func (p *Parser) parseHiddenDemoInfo(r *mvd.BufferReader, time float64, dataLen int) error {
	if dataLen < 2 {
		return r.Skip(dataLen)
	}

	// Read block number
	blockNum, err := r.ReadUint16()
	if err != nil {
		return err
	}

	// Read JSON content (remaining bytes)
	contentLen := dataLen - 2
	if contentLen <= 0 {
		return nil
	}

	content := make([]byte, contentLen)
	for i := 0; i < contentLen; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		content[i] = b
	}

	return p.emit(&DemoInfoEvent{
		BlockNum: int(blockNum),
		Content:  content,
		Time:     time,
	})
}

// skipCommand attempts to skip an unknown command
// Returns error if we can't determine the size
func skipCommand(r *mvd.BufferReader, cmd byte, floatCoords bool) error {
	switch cmd {
	case mvd.SvcNop:
		return nil
	case mvd.SvcBad:
		return nil
	case mvd.SvcSound:
		return skipSound(r, floatCoords)
	case mvd.SvcStuffText:
		_, err := r.ReadString()
		return err
	case mvd.SvcSetAngle:
		return r.Skip(3) // 3 angles (bytes)
	case mvd.SvcLightStyle:
		_, err := r.ReadByte()
		if err != nil {
			return err
		}
		_, err = r.ReadString()
		return err
	case mvd.SvcUpdatePing:
		return r.Skip(2) // player + ping byte
	case mvd.SvcUpdateEnterTime:
		return r.Skip(5) // player + float
	case mvd.SvcSetPause:
		return r.Skip(1)
	case mvd.SvcCenterPrint:
		_, err := r.ReadString()
		return err
	case mvd.SvcSpawnBaseline:
		return skipSpawnBaseline(r)
	case mvd.SvcSpawnStatic:
		return skipSpawnStatic(r)
	case mvd.SvcTempEntity:
		return skipTempEntity(r, floatCoords)
	case mvd.SvcKilledMonster:
		return nil
	case mvd.SvcFoundSecret:
		return nil
	case mvd.SvcIntermission:
		return r.Skip(12) // 3 coords + 3 angles
	case mvd.SvcFinale:
		_, err := r.ReadString()
		return err
	case mvd.SvcCDTrack:
		return r.Skip(1)
	case mvd.SvcSmallKick:
		return nil
	case mvd.SvcBigKick:
		return nil
	case mvd.SvcMuzzleFlash:
		return r.Skip(2)
	case mvd.SvcDownload:
		return skipDownload(r)
	case mvd.SvcPlayerInfo:
		return skipPlayerInfo(r, floatCoords)
	case mvd.SvcNails, mvd.SvcNails2:
		return skipNails(r, cmd == mvd.SvcNails2)
	case mvd.SvcChokeCount:
		return r.Skip(1)
	case mvd.SvcModelList:
		return skipModelList(r)
	case mvd.SvcSoundList:
		return skipSoundList(r)
	case mvd.SvcPacketEntities:
		return skipPacketEntities(r, floatCoords)
	case mvd.SvcDeltaPacketEntities:
		return skipDeltaPacketEntities(r, floatCoords)
	case mvd.SvcMaxSpeed:
		return r.Skip(4) // float
	case mvd.SvcEntGravity:
		return r.Skip(4) // float
	case mvd.SvcSetInfo:
		_, err := r.ReadByte() // player
		if err != nil {
			return err
		}
		_, err = r.ReadString() // key
		if err != nil {
			return err
		}
		_, err = r.ReadString() // value
		return err
	case mvd.SvcServerInfo:
		_, err := r.ReadString() // key
		if err != nil {
			return err
		}
		_, err = r.ReadString() // value
		return err
	case mvd.SvcUpdatePL:
		return r.Skip(2) // player + pl byte
	case mvd.SvcSpawnStaticSound:
		if floatCoords {
			return r.Skip(17) // 3 floats + byte + byte + byte
		}
		return r.Skip(11) // 3 shorts + byte + byte + byte
	default:
		// Unknown command - can't determine size
		return io.EOF
	}
}

// Skip functions for complex commands
func skipSound(r *mvd.BufferReader, floatCoords bool) error {
	channel, err := r.ReadUint16()
	if err != nil {
		return err
	}
	if channel&0x8000 != 0 {
		r.Skip(1) // volume
	}
	if channel&0x4000 != 0 {
		r.Skip(1) // attenuation
	}
	r.Skip(1) // sound_num
	if floatCoords {
		return r.Skip(12) // 3 floats
	}
	return r.Skip(6) // 3 shorts
}

func skipSpawnBaseline(r *mvd.BufferReader) error {
	return r.Skip(10) // Simplified - actual size varies
}

func skipSpawnStatic(r *mvd.BufferReader) error {
	return r.Skip(13) // Simplified
}

func skipTempEntity(r *mvd.BufferReader, floatCoords bool) error {
	teType, err := r.ReadByte()
	if err != nil {
		return err
	}
	// Size varies by type - simplified handling
	switch teType {
	case 0, 1, 2, 3, 4, 7, 8, 10, 11: // Point-based
		if floatCoords {
			return r.Skip(12)
		}
		return r.Skip(6)
	case 5, 6, 9, 13: // Beam-based
		return r.Skip(16)
	default:
		if floatCoords {
			return r.Skip(12)
		}
		return r.Skip(6)
	}
}

func skipDownload(r *mvd.BufferReader) error {
	size, err := r.ReadInt16()
	if err != nil {
		return err
	}
	r.Skip(1) // percent
	if size > 0 {
		return r.Skip(int(size))
	}
	return nil
}

func skipPlayerInfo(r *mvd.BufferReader, floatCoords bool) error {
	_, err := r.ReadByte() // player num
	if err != nil {
		return err
	}
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

func skipNails(r *mvd.BufferReader, isNails2 bool) error {
	count, err := r.ReadByte()
	if err != nil {
		return err
	}
	bytesPerNail := 6
	if isNails2 {
		bytesPerNail = 7
	}
	return r.Skip(int(count) * bytesPerNail)
}

func skipModelList(r *mvd.BufferReader) error {
	r.Skip(1) // start index
	for {
		s, err := r.ReadString()
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
	}
	return r.Skip(1) // next index
}

func skipSoundList(r *mvd.BufferReader) error {
	r.Skip(1) // start index
	for {
		s, err := r.ReadString()
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
	}
	return r.Skip(1) // next index
}

func skipPacketEntities(r *mvd.BufferReader, floatCoords bool) error {
	// This is complex - simplified version that may not work for all demos
	for {
		word, err := r.ReadUint16()
		if err != nil {
			return err
		}
		if word == 0 {
			break
		}
		// Skip entity data based on flags
		r.Skip(1) // Simplified - actual size varies with flags
	}
	return nil
}

func skipDeltaPacketEntities(r *mvd.BufferReader, floatCoords bool) error {
	r.Skip(1) // from frame
	return skipPacketEntities(r, floatCoords)
}

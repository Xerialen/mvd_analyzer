// Package parser handles parsing of MVD network message payloads.
package parser

import (
	"io"

	"github.com/mvd-analyzer/mvd-reader/mvd"
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
	EventIntermission
	EventStuffText
	EventCenterPrint
	EventServerInfo
	EventDeath
	EventSpawn
	EventItemSpawn
	EventItemState
	EventBackpackDropHint
	EventItemPickupHint
	EventBackpackPickupHint
	EventItemPickupPrint
	EventBackpackPickupPrint
	EventDemoStartTimestamp
	EventPausedDuration
)

// IntermissionEvent is emitted when the server enters intermission
// (svc_intermission, cmd 30). KTX-style demos send this when the timelimit
// or fraglimit is hit and the scoreboard camera takes over; downstream
// analyzers use it to stop sampling player state.
type IntermissionEvent struct {
	Time float64
}

func (e *IntermissionEvent) EventType() EventType { return EventIntermission }
func (e *IntermissionEvent) EventTime() float64   { return e.Time }

// StuffTextEvent is emitted for svc_stufftext (cmd 9). The server pushes
// console commands into the client this way — at connection time it sends
// `fullserverinfo "\key\value\..."` (the complete cvar dump), and during
// gameplay it sends `//ktx ...` style hints, weapon-stat tickers, and
// downloadable map / sound hints.
type StuffTextEvent struct {
	Command string
	Time    float64
}

func (e *StuffTextEvent) EventType() EventType { return EventStuffText }
func (e *StuffTextEvent) EventTime() float64   { return e.Time }

// CenterPrintEvent is emitted for svc_centerprint (cmd 26). KTX uses this
// during the match countdown to render the full match settings table
// (Mode / Spawnmodel / Antilag / Timelimit / etc) on every connected
// client's HUD. The countdown text is the cleanest source of structured
// match settings in a demo.
type CenterPrintEvent struct {
	Message string
	Time    float64
}

func (e *CenterPrintEvent) EventType() EventType { return EventCenterPrint }
func (e *CenterPrintEvent) EventTime() float64   { return e.Time }

// ServerInfoEvent is emitted for svc_serverinfo (cmd 52), which is a
// single-key/value serverinfo update sent mid-game (status changes,
// matchtag, fpd flags, mode, etc). The initial bulk serverinfo is sent
// via `fullserverinfo` stufftext, not via this command.
type ServerInfoEvent struct {
	Key   string
	Value string
	Time  float64
}

func (e *ServerInfoEvent) EventType() EventType { return EventServerInfo }
func (e *ServerInfoEvent) EventTime() float64   { return e.Time }

// maxHiddenBlockSize caps the length of a single hidden-message block
// (dem_multiple with player_mask=0). The largest legitimate block in the
// wild is the demoinfo JSON dump, which fits comfortably under 10 KB; any
// larger value is treated as corruption rather than a real block.
const maxHiddenBlockSize = 10000

// Handler is called for each parsed event
type Handler func(event Event) error

// Parser parses network message payloads
type Parser struct {
	decoder         *mvd.Decoder
	serverData      *mvd.ServerData
	players         [mvd.MaxClients]*mvd.PlayerInfo
	playerStats     [mvd.MaxClients]*mvd.Stats
	playerPositions [mvd.MaxClients][3]float32 // Last known position per player (for delta updates)
	// Per-player dead/alive bookkeeping for DeathEvent / SpawnEvent
	// emission. Two signals feed it: the StatHealth edge detector in
	// stats.go (>0 ↔ ≤0) and the DF_DEAD bit on every svc_playerinfo
	// in position.go. The two are deduplicated against playerDead /
	// playerDeadKnown so whichever fires first wins; the other becomes
	// a no-op. playerSeenInfo separately tracks "have we received a
	// svc_playerinfo for this slot yet" so the first sample doesn't
	// fabricate a DeathEvent for a player who joined the demo already
	// dead (no prior alive state to transition from).
	playerDead      [mvd.MaxClients]bool
	playerDeadKnown [mvd.MaxClients]bool
	playerSeenInfo  [mvd.MaxClients]bool
	// matchStarted flips on the first svc_print whose text matches one
	// of the canonical match-start phrases (see matchStartedFromPrint).
	// It gates the obituary-derived DeathEvent path in
	// tryEmitObituaryDeath: telefrag obits that fire at the *exact*
	// wire time of the start print arrive in the event stream before
	// the start print itself, so the analyzer's timing gate drops them
	// and the resulting parser-side dedup state silently suppresses
	// the still-to-arrive stat-based DeathEvent. Gating obit emission
	// on this flag keeps warmup obits silent and lets the dedup state
	// flow normally once the match is live.
	matchStarted   bool
	handlers       []Handler
	floatCoords    bool
	fteExtensions  uint32 // FTE protocol extension flags
	diagnosticMode bool
	warnings       []Warning

	// Entity state tracking — fills from svc_modellist, svc_spawnbaseline,
	// and svc_packetentities / svc_deltapacketentities so the parser
	// itself can emit ItemSpawnEvent / ItemStateEvent for every pickup
	// and respawn without downstream analyzers having to reconstruct
	// entity state. See entities.go for the decoder.
	modelList            []string
	baselines            map[int]*EntityState
	currentEntities      map[int]*EntityState
	spawnedItems         map[int]string // ent -> kind, set once per item
	lastEntityPacketTime float64        // time of the packet we're currently processing
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
		if err := p.ParseOne(); err != nil {
			if err == io.EOF {
				return nil // Normal end
			}
			return err
		}
	}
}

// ParseOne reads and processes exactly one demo message, invoking the
// registered OnEvent handlers for any events emitted by that message.
// Returns io.EOF at a clean end of stream. This is the primitive a
// pull-style events.Source iterator builds on; Parse() is just a loop
// over ParseOne until io.EOF.
func (p *Parser) ParseOne() error {
	msg, err := p.decoder.NextMessage()
	if err != nil {
		if err == mvd.ErrEndOfDemo {
			return io.EOF
		}
		return err
	}
	return p.parseMessage(msg)
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
				p.warn(msg.Time, "parse_error", "svc_serverdata: %v", err)
				return nil
			}

		case mvd.SvcUpdateUserInfo:
			if err := p.parseUserInfo(r, msg.Time); err != nil {
				p.warn(msg.Time, "parse_error", "svc_updateuserinfo: %v", err)
				return nil
			}

		case mvd.SvcSetInfo:
			if err := p.parseSetInfo(r, msg.Time); err != nil {
				p.warn(msg.Time, "parse_error", "svc_setinfo: %v", err)
				return nil
			}

		case mvd.SvcPrint:
			target := -1
			if msg.Header.MessageType == mvd.DemSingle {
				target = msg.Header.PlayerNum
			}
			if err := p.parsePrint(r, msg.Time, msg.TimeMs, target); err != nil {
				p.warn(msg.Time, "parse_error", "svc_print: %v", err)
				return nil
			}

		case mvd.SvcUpdateStat:
			if err := p.parseUpdateStat(r, msg.Time, msg.TimeMs, msg.Header.PlayerNum); err != nil {
				p.warn(msg.Time, "parse_error", "svc_updatestat: %v", err)
				return nil
			}

		case mvd.SvcUpdateStatLong:
			if err := p.parseUpdateStatLong(r, msg.Time, msg.TimeMs, msg.Header.PlayerNum); err != nil {
				p.warn(msg.Time, "parse_error", "svc_updatestatlong: %v", err)
				return nil
			}

		case mvd.SvcUpdateFrags:
			if err := p.parseUpdateFrags(r, msg.Time); err != nil {
				p.warn(msg.Time, "parse_error", "svc_updatefrags: %v", err)
				return nil
			}

		case mvd.SvcPlayerInfo:
			if err := p.parsePlayerInfo(r, msg.Time, msg.TimeMs, p.floatCoords); err != nil {
				p.warn(msg.Time, "parse_error", "svc_playerinfo: %v", err)
				return nil
			}

		case mvd.SvcModelList:
			if err := p.parseModelList(r); err != nil {
				p.warn(msg.Time, "parse_error", "svc_modellist: %v", err)
				return nil
			}

		case mvd.SvcDisconnect:
			message, _ := r.ReadString()
			if message == "EndOfDemo" {
				return mvd.ErrEndOfDemo
			}

		case mvd.SvcIntermission:
			// 3 short coords (6) + 3 byte angles (3) = 9 bytes camera pose.
			// We don't need the pose but we do need to signal intermission to
			// downstream analyzers so they can stop sampling player state.
			if err := r.Skip(9); err != nil {
				p.warn(msg.Time, "parse_error", "svc_intermission: %v", err)
				return nil
			}
			if err := p.emit(&IntermissionEvent{Time: msg.Time}); err != nil {
				return err
			}

		case mvd.SvcStuffText:
			// Stuffed console command — at t=0 includes `fullserverinfo "..."`
			// (complete cvar dump), and during gameplay carries `//ktx ...`
			// hints, weapon-stat tickers, and download requests.
			s, err := r.ReadString()
			if err != nil {
				p.warn(msg.Time, "parse_error", "svc_stufftext: %v", err)
				return nil
			}
			if err := p.emit(&StuffTextEvent{Command: s, Time: msg.Time}); err != nil {
				return err
			}
			if err := p.tryEmitBackpackDropHint(s, msg.Time); err != nil {
				return err
			}
			if err := p.tryEmitItemPickupHint(s, msg.Time); err != nil {
				return err
			}
			if err := p.tryEmitBackpackPickupHint(s, msg.Time); err != nil {
				return err
			}

		case mvd.SvcCenterPrint:
			// HUD center text — KTX renders the match settings table here
			// during the countdown.
			s, err := r.ReadString()
			if err != nil {
				p.warn(msg.Time, "parse_error", "svc_centerprint: %v", err)
				return nil
			}
			if err := p.emit(&CenterPrintEvent{Message: s, Time: msg.Time}); err != nil {
				return err
			}

		case mvd.SvcServerInfo:
			// Single-key serverinfo update (status, matchtag, fpd, ...).
			// Bulk serverinfo arrives via the `fullserverinfo` stufftext
			// command at connection time.
			k, err := r.ReadString()
			if err != nil {
				p.warn(msg.Time, "parse_error", "svc_serverinfo key: %v", err)
				return nil
			}
			v, err := r.ReadString()
			if err != nil {
				p.warn(msg.Time, "parse_error", "svc_serverinfo value: %v", err)
				return nil
			}
			if err := p.emit(&ServerInfoEvent{Key: k, Value: v, Time: msg.Time}); err != nil {
				return err
			}

		case mvd.SvcSpawnBaseline:
			if err := p.parseSpawnBaseline(r, msg.Time, p.floatCoords); err != nil {
				p.warn(msg.Time, "parse_error", "svc_spawnbaseline: %v", err)
				return nil
			}

		case mvd.SvcFTESpawnBaseline2:
			if err := p.parseSpawnBaseline2(r, msg.Time, p.floatCoords); err != nil {
				p.warn(msg.Time, "parse_error", "svc_fte_spawnbaseline2: %v", err)
				return nil
			}

		case mvd.SvcPacketEntities:
			p.lastEntityPacketTime = msg.Time
			if err := p.parsePacketEntities(r, false, p.floatCoords, p.fteExtensions); err != nil {
				p.warn(msg.Time, "parse_error", "svc_packetentities: %v", err)
				return nil
			}

		case mvd.SvcDeltaPacketEntities:
			p.lastEntityPacketTime = msg.Time
			if err := p.parsePacketEntities(r, true, p.floatCoords, p.fteExtensions); err != nil {
				p.warn(msg.Time, "parse_error", "svc_deltapacketentities: %v", err)
				return nil
			}

		case mvd.SvcFTEModelListShort:
			if err := p.parseModelList(r); err != nil {
				p.warn(msg.Time, "parse_error", "svc_fte_modellistshort: %v", err)
				return nil
			}

		default:
			if cmd == mvd.SvcTempEntity && p.diagnosticMode {
				teType, err := skipTempEntityDiag(r, p.floatCoords)
				if err != nil {
					p.warn(msg.Time, "unknown_te", "unknown temp entity type %d, %d bytes remaining in payload abandoned", teType, r.Remaining())
					return nil
				}
			} else if err := skipCommand(r, cmd, p.floatCoords, p.fteExtensions); err != nil {
				p.warn(msg.Time, "unknown_svc", "%s (cmd %d), %d bytes remaining in payload abandoned",
					SvcName(cmd), cmd, r.Remaining())
				return nil
			}
		}
	}

	return nil
}

// parseHiddenMessage parses hidden messages (dem_multiple with player_mask=0).
//
// Hidden messages are a sequence of length-prefixed blocks. The function
// reports two distinct failure modes through warn() so they're separable in
// the diagnostic feed:
//
//   - "parse_error" → garbage we can't recover from (truncated block header,
//     out-of-range length, sub-parser failure). We stop parsing this hidden
//     message and return; subsequent hidden messages still parse normally.
//   - graceful EOF when r.Remaining() drops to 0 between blocks is the
//     expected end of the message and is NOT logged.
func (p *Parser) parseHiddenMessage(msg *mvd.DemoMessage) error {
	r := mvd.NewBufferReader(msg.Payload)
	time := msg.Time

	// Malformed paused-duration block workaround. mvdsv's
	// SV_MVDWritePausedTimeToStreams (sv_demo.c:559) hand-writes the
	// mvdhidden_paused_duration (0x000A) block WITHOUT the leading 4-byte
	// mvdhidden_block_header_t.length field every other hidden block carries
	// (cf. sv_user.c:4187-4190). The dem_multiple payload is therefore a bare
	// [type_id:u16][duration:byte] (exactly 3 bytes) instead of the standard
	// [length:u32][type_id:u16][payload]. The block loop below would read the
	// type_id as a truncated length and bail, so detect that exact shape here.
	// A standard single block is >= 6 bytes (4+2 header), so len==3 is
	// unambiguous; a correctly-framed future build falls through to the
	// MVDHiddenPausedDuration case in the loop instead.
	if len(msg.Payload) == 3 &&
		uint16(msg.Payload[0])|uint16(msg.Payload[1])<<8 == mvd.MVDHiddenPausedDuration {
		return p.emit(&PausedDurationEvent{DurationMs: int(msg.Payload[2]), Time: time})
	}

	for r.Remaining() > 0 {
		// Read block length (4 bytes). EOF here mid-stream means the
		// final block header was truncated — that's a parse error, not a
		// clean end (the loop condition would have caught a clean end).
		blockLen, err := r.ReadUint32()
		if err != nil {
			p.warn(time, "parse_error", "hidden block: truncated length header (%v)", err)
			return nil
		}
		// blockLen counts the bytes after the type_id. 1 is legitimate (a
		// single-byte payload, e.g. a correctly-framed paused_duration block);
		// 0 is a degenerate empty block we refuse.
		if blockLen < 1 || blockLen > maxHiddenBlockSize {
			p.warn(time, "parse_error", "hidden block with invalid length %d", blockLen)
			return nil
		}

		// Read type ID (2 bytes)
		typeID, err := r.ReadUint16()
		if err != nil {
			p.warn(time, "parse_error", "hidden block typeID read failed: %v", err)
			return nil
		}

		// blockLen is the length of the data AFTER the typeID (not including it)
		dataLen := int(blockLen)

		// Parse based on type
		switch typeID {
		case mvd.MVDHiddenDmgDone:
			if err := p.parseHiddenDamage(r, time, dataLen); err != nil {
				p.warn(time, "parse_error", "hidden dmgdone: %v", err)
				return nil
			}
		case mvd.MVDHiddenDemoInfo:
			if err := p.parseHiddenDemoInfo(r, time, dataLen); err != nil {
				p.warn(time, "parse_error", "hidden demoinfo: %v", err)
				return nil
			}
		case mvd.MVDHiddenDemoStartTimestampMs:
			if err := p.parseHiddenDemoStart(r, time, dataLen); err != nil {
				p.warn(time, "parse_error", "hidden demo_start_timestamp_ms: %v", err)
				return nil
			}
		case mvd.MVDHiddenPausedDuration:
			// Correctly-framed paused-duration block (standard length-prefixed
			// form): dem_multiple payload [u32 length=1][u16 0x000A][byte]. This
			// is what QW-Group/mvdsv PR #210 emits once merged; the bare,
			// header-less form current mvdsv writes is handled up front in
			// parseHiddenMessage. Both decode to the same PausedDurationEvent.
			if err := p.parseHiddenPausedDuration(r, time, dataLen); err != nil {
				p.warn(time, "parse_error", "hidden paused_duration: %v", err)
				return nil
			}
		default:
			p.warn(time, "unknown_hidden", "unknown hidden message type 0x%04x, %d bytes skipped", typeID, dataLen)
			if dataLen > 0 {
				if err := r.Skip(dataLen); err != nil {
					p.warn(time, "parse_error", "hidden block 0x%04x: skip past end of payload (%v)", typeID, err)
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

	// Emit whenever the victim is a player and damage was dealt. KTX sends
	// mvdhidden_dmgdone when either attacker or victim is a player
	// (ktx/src/combat.c:810), so world/environmental damage-taken (lava,
	// fall, trigger, drowning) arrives with a non-player attacker — edict 0
	// (worldspawn) or a non-client entity. Those records are real
	// damage-taken signal, so we surface them with Attacker == -1 ("world")
	// rather than dropping them. The player→player path is unchanged.
	if victimPlayer >= 0 && victimPlayer < mvd.MaxClients && damage > 0 {
		attacker := attackerPlayer
		if attacker < 0 || attacker >= mvd.MaxClients {
			attacker = -1 // world / non-player inflictor
		}
		return p.emit(&DamageEvent{
			Attacker:  attacker,
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

// parseHiddenPausedDuration parses a length-prefixed mvdhidden_paused_duration
// (0x000A) block: a single byte of real wall-clock milliseconds elapsed during
// one paused idle frame. This handles the standard-framed form emitted by
// QW-Group/mvdsv PR #210 (length=1, one body byte); the bare, header-less form
// current mvdsv actually emits is decoded up front in parseHiddenMessage.
func (p *Parser) parseHiddenPausedDuration(r *mvd.BufferReader, time float64, dataLen int) error {
	if dataLen < 1 {
		return r.Skip(dataLen)
	}
	dur, err := r.ReadByte()
	if err != nil {
		return err
	}
	if dataLen > 1 {
		if err := r.Skip(dataLen - 1); err != nil {
			return err
		}
	}
	return p.emit(&PausedDurationEvent{DurationMs: int(dur), Time: time})
}

// parseHiddenDemoStart parses mvdhidden_demo_start_timestamp_ms (0x000B).
//
// The payload is the wall-clock time the server opened the MVD file, as Unix
// epoch milliseconds, ULEB128 varint-encoded (7 data bits per byte, high bit =
// continuation) — NOT a fixed-width uint64. mvdsv writes it once after the
// initial gamestate flush via Sys_TimestampMilliseconds(); a ~2026 value is 6
// bytes. Ref: QW-Group/mvdsv SV_MVDEmbedStartTimestamp (src/sv_demo_misc.c).
//
// This is the only sub-second-accurate demo-start anchor; the serverinfo
// `epoch` cvar carries the same instant truncated to whole seconds. Absent on
// demos recorded before mvdsv added the block.
func (p *Parser) parseHiddenDemoStart(r *mvd.BufferReader, time float64, dataLen int) error {
	if dataLen <= 0 {
		return nil
	}

	// Read the whole block so the reader stays aligned for the next block.
	body, err := r.ReadBytes(dataLen)
	if err != nil {
		return err
	}

	return p.emit(&DemoStartTimestampEvent{UnixMs: int64(decodeULEB128(body)), Time: time})
}

// decodeULEB128 decodes an unsigned LEB128 varint: low-order group first, 7
// data bits per byte, high bit set on every byte except the last.
func decodeULEB128(b []byte) uint64 {
	var v uint64
	var shift uint
	for _, x := range b {
		v |= uint64(x&0x7f) << shift
		if x&0x80 == 0 {
			break
		}
		shift += 7
	}
	return v
}

// skipCommand attempts to skip an unknown command
// Returns error if we can't determine the size
func skipCommand(r *mvd.BufferReader, cmd byte, floatCoords bool, fteExt uint32) error {
	switch cmd {
	case mvd.SvcNop:
		return nil
	case mvd.SvcBad:
		return nil
	case mvd.SvcSound:
		return skipSound(r, floatCoords)
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
		// [byte] player + [short] ping = 3 bytes.
		// Ref: ezquake cl_parse.c case svc_updateping.
		return r.Skip(3)
	case mvd.SvcUpdateEnterTime:
		return r.Skip(5) // player + float
	case mvd.SvcSetPause:
		return r.Skip(1)
	case mvd.SvcSpawnBaseline:
		// svc_spawnbaseline has a 2-byte entity number prefix before the baseline body.
		// Ref: ezquake cl_parse.c case svc_spawnbaseline — MSG_ReadShort() + CL_ParseBaseline().
		if err := r.Skip(2); err != nil {
			return err
		}
		return skipSpawnBaseline(r, floatCoords)
	case mvd.SvcSpawnStatic:
		// svc_spawnstatic has no prefix — CL_ParseStatic calls CL_ParseBaseline directly.
		return skipSpawnStatic(r, floatCoords)
	case mvd.SvcTempEntity:
		return skipTempEntity(r, floatCoords)
	case mvd.SvcKilledMonster:
		return nil
	case mvd.SvcFoundSecret:
		return nil
	case mvd.SvcDamage:
		// [byte] armor [byte] blood [vec3] from — coords are short in QW
		// standard protocol, float if FTE_PEXT_FLOATCOORDS was negotiated.
		// Ref: qwprot protocol.h (svc_damage = 19), ezquake cl_view.c V_ParseDamage.
		if floatCoords {
			return r.Skip(14) // 1 + 1 + 3*4
		}
		return r.Skip(8) // 1 + 1 + 3*2
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
		return skipPacketEntities(r, floatCoords, fteExt)
	case mvd.SvcDeltaPacketEntities:
		return skipDeltaPacketEntities(r, floatCoords, fteExt)
	case mvd.SvcMaxSpeed:
		return r.Skip(4) // float
	case mvd.SvcEntGravity:
		return r.Skip(4) // float
	case mvd.SvcSetInfo:
		// Handled in parseNetworkMessage main switch; this fallback is unused.
		_, err := r.ReadByte()
		if err != nil {
			return err
		}
		_, err = r.ReadString()
		if err != nil {
			return err
		}
		_, err = r.ReadString()
		return err
	case mvd.SvcUpdatePL:
		return r.Skip(2) // player + pl byte
	case mvd.SvcSpawnStaticSound:
		// 3 coords + sound_num(1) + vol(1) + atten(1).
		// Ref: ezquake cl_parse.c CL_ParseStaticSound.
		if floatCoords {
			return r.Skip(15) // 3*4 + 3
		}
		return r.Skip(9) // 3*2 + 3
	case mvd.SvcFTESpawnBaseline2:
		// Extended baseline: 2-byte flag word + entity delta
		w, err := r.ReadUint16()
		if err != nil {
			return err
		}
		return skipEntityDelta(r, w, floatCoords, fteExt)
	case mvd.SvcFTESpawnStatic2:
		// Extended static: 2-byte flag word + entity delta
		w, err := r.ReadUint16()
		if err != nil {
			return err
		}
		return skipEntityDelta(r, w, floatCoords, fteExt)
	case mvd.SvcFTEModelListShort:
		return skipModelList(r) // same format as regular model list
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

func skipSpawnBaseline(r *mvd.BufferReader, floatCoords bool) error {
	// model(1) + frame(1) + colormap(1) + skin(1) + 3*(coord + angle)
	r.Skip(4) // model, frame, colormap, skin
	for i := 0; i < 3; i++ {
		if floatCoords {
			r.Skip(4) // float coord
		} else {
			r.Skip(2) // short coord
		}
		r.Skip(1) // angle byte
	}
	return nil
}

func skipSpawnStatic(r *mvd.BufferReader, floatCoords bool) error {
	return skipSpawnBaseline(r, floatCoords)
}

// skipTempEntity skips a svc_temp_entity payload. The byte layout depends on
// the temp-entity type and whether the protocol negotiated float coordinates.
//
// Reference: ezquake `cl_tent.c::CL_ParseTEnt` and mvdsv `cl_parse.c`. The QW
// wire formats are:
//
//	TE_SPIKE/SUPERSPIKE/EXPLOSION/TAREXPLOSION/WIZSPIKE/KNIGHTSPIKE/
//	  LAVASPLASH/TELEPORT/LIGHTNINGBLOOD: 3 coords             (6 / 12 bytes)
//	TE_GUNSHOT, TE_BLOOD:                 byte count + 3 coords (7 / 13 bytes)
//	TE_LIGHTNING1/2/3 (beams):            short ent + 6 coords  (14 / 26 bytes)
//
// The previous implementation lumped TE_GUNSHOT into the plain-coord group,
// dropped TE_BLOOD into the default 6-byte branch, mis-sized beams as 16 bytes,
// and treated TE_LIGHTNINGBLOOD as a beam. Each of those was a 1-2 byte drift
// per occurrence; in a busy frame the accumulated drift would walk the parser
// straight into a byte that happened to look like svc_updatestatlong, at which
// point downstream code saw armor=172620004 and the team-average graph
// autoscaled to garbage.
//
// Unknown TE types deliberately bail (io.EOF) rather than guessing a length —
// silent drift is much worse than dropping the rest of the message.
// skipTempEntityDiag is like skipTempEntity but returns the TE type on unknown types
// so the caller can emit a diagnostic warning. Returns (teType, error).
func skipTempEntityDiag(r *mvd.BufferReader, floatCoords bool) (byte, error) {
	teType, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	coordSize := 2
	if floatCoords {
		coordSize = 4
	}
	switch teType {
	case 0, 1, 3, 4, 7, 8, 10, 11, 13:
		return teType, r.Skip(3 * coordSize)
	case 2, 12:
		return teType, r.Skip(1 + 3*coordSize)
	case 5, 6, 9:
		return teType, r.Skip(2 + 6*coordSize)
	default:
		return teType, io.EOF
	}
}

func skipTempEntity(r *mvd.BufferReader, floatCoords bool) error {
	teType, err := r.ReadByte()
	if err != nil {
		return err
	}
	coordSize := 2
	if floatCoords {
		coordSize = 4
	}
	switch teType {
	case 0, 1, 3, 4, 7, 8, 10, 11, 13:
		// 3 coords
		return r.Skip(3 * coordSize)
	case 2, 12:
		// byte count + 3 coords
		return r.Skip(1 + 3*coordSize)
	case 5, 6, 9:
		// beam: short entity + 3 coords (start) + 3 coords (end)
		return r.Skip(2 + 6*coordSize)
	default:
		return io.EOF
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

func skipPacketEntities(r *mvd.BufferReader, floatCoords bool, fteExt uint32) error {
	for {
		word, err := r.ReadUint16()
		if err != nil {
			return err
		}
		if word == 0 {
			break // end marker
		}
		if err := skipEntityDelta(r, word, floatCoords, fteExt); err != nil {
			return err
		}
	}
	return nil
}

func skipDeltaPacketEntities(r *mvd.BufferReader, floatCoords bool, fteExt uint32) error {
	r.Skip(1) // from sequence number
	return skipPacketEntities(r, floatCoords, fteExt)
}

// Entity update flag bits (from protocol.h)
const (
	uOrigin1  = 1 << 9
	uOrigin2  = 1 << 10
	uOrigin3  = 1 << 11
	uAngle2   = 1 << 12
	uFrame    = 1 << 13
	uMoreBits = 1 << 15
	// Low byte flags (read if uMoreBits set)
	uAngle1      = 1 << 0
	uAngle3      = 1 << 1
	uModel       = 1 << 2
	uColormap    = 1 << 3
	uSkin        = 1 << 4
	uEffects     = 1 << 5
	uFTEEvenMore = 1 << 7
	// FTE morebits flags
	uFTETrans     = 1 << 1
	uFTEModelDbl  = 1 << 3
	uFTEYetMore   = 1 << 7
	uFTEColourMod = 1 << 10 // bit 2 of second byte (after shift by 8)
)

// skipEntityDelta skips variable-length entity delta data for a given flag word.
// Mirrors CL_ParseDelta from ezquake cl_ents.c.
func skipEntityDelta(r *mvd.BufferReader, word uint16, floatCoords bool, fteExt uint32) error {
	bits := int(word)
	bits &= ^511 // clear entity number bits 0-8

	// Step 1: If U_MOREBITS, read low-order flag byte
	var lowFlags int
	if bits&uMoreBits != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		lowFlags = int(b)
		bits |= lowFlags
	}

	// Step 2: FTE extensions
	var morebits int
	if lowFlags&uFTEEvenMore != 0 && fteExt != 0 {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		morebits = int(b)
		if morebits&uFTEYetMore != 0 {
			b2, err := r.ReadByte()
			if err != nil {
				return err
			}
			morebits |= int(b2) << 8
		}
	}

	// Step 3: Read fields in CL_ParseDelta order
	if bits&uModel != 0 {
		if morebits&uFTEModelDbl != 0 {
			// U_MODEL set + U_FTE_MODELDBL: modelindex = byte + 256 (just 1 byte)
			r.Skip(1)
		} else {
			r.Skip(1) // modelindex byte
		}
	} else if morebits&uFTEModelDbl != 0 {
		// !U_MODEL + U_FTE_MODELDBL: modelindex = short (2 bytes)
		r.Skip(2)
	}

	if bits&uFrame != 0 {
		r.Skip(1) // frame byte
	}
	if bits&uColormap != 0 {
		r.Skip(1) // colormap byte
	}
	if bits&uSkin != 0 {
		r.Skip(1) // skin byte
	}
	if bits&uEffects != 0 {
		r.Skip(1) // effects byte
	}

	// Origins: 2 bytes (short coord) or 4 bytes (float coord)
	coordSize := 2
	if floatCoords {
		coordSize = 4
	}
	if bits&uOrigin1 != 0 {
		r.Skip(coordSize)
	}
	if lowFlags&uAngle1 != 0 {
		r.Skip(1) // angle byte
	}
	if bits&uOrigin2 != 0 {
		r.Skip(coordSize)
	}
	if bits&uAngle2 != 0 {
		r.Skip(1) // angle byte
	}
	if bits&uOrigin3 != 0 {
		r.Skip(coordSize)
	}
	if lowFlags&uAngle3 != 0 {
		r.Skip(1) // angle byte
	}

	// U_SOLID: no data

	// FTE transparency
	if morebits&uFTETrans != 0 && fteExt&mvd.FTEPextTrans != 0 {
		r.Skip(1)
	}

	// FTE colour mod (3 bytes RGB)
	if morebits&int(uFTEColourMod) != 0 && fteExt&mvd.FTEPextColourMod != 0 {
		r.Skip(3)
	}

	return nil
}

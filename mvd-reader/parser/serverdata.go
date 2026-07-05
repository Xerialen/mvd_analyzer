package parser

import (
	"github.com/mvd-analyzer/mvd-reader/mvd"
)

// ServerDataEvent is emitted when server data is parsed
type ServerDataEvent struct {
	Data *mvd.ServerData
	Time float64
}

func (e *ServerDataEvent) EventType() EventType { return EventServerData }
func (e *ServerDataEvent) EventTime() float64   { return e.Time }

// parseServerData parses svc_serverdata message
func (p *Parser) parseServerData(r *mvd.BufferReader, time float64) error {
	sd := &mvd.ServerData{}
	ext := &mvd.Extensions{}

	// Read protocol extensions until we hit PROTOCOL_VERSION (28)
	for {
		version, err := r.ReadUint32()
		if err != nil {
			return err
		}

		if version == mvd.ProtocolVersion {
			// Standard protocol version - done reading extensions
			sd.ProtocolVersion = int(version)
			break
		}

		// Read extension flags
		flags, err := r.ReadUint32()
		if err != nil {
			return err
		}

		switch version {
		case mvd.ProtocolVersionFTE:
			ext.FTE = flags
			sd.FTEExtensions = flags
		case mvd.ProtocolVersionFTE2:
			ext.FTE2 = flags
			sd.FTE2Extensions = flags
		case mvd.ProtocolVersionMVD1:
			ext.MVD1 = flags
			sd.MVD1Extensions = flags
		}
	}

	// Update decoder with extensions
	p.decoder.SetExtensions(ext)
	p.floatCoords = p.decoder.FloatCoords()
	p.fteExtensions = ext.FTE

	// Read server count
	count, err := r.ReadUint32()
	if err != nil {
		return err
	}
	sd.ServerCount = int(count)

	// Read game directory
	gameDir, err := r.ReadString()
	if err != nil {
		return err
	}
	sd.GameDir = gameDir

	// Read server time (float)
	serverTime, err := r.ReadFloat32()
	if err != nil {
		return err
	}
	sd.ServerTime = serverTime

	// Read level name
	levelName, err := r.ReadString()
	if err != nil {
		return err
	}
	sd.LevelName = levelName

	// Read movement variables (10 floats). Propagate the first read error
	// rather than silently zeroing the physics — a truncated serverdata that
	// left e.g. MaxSpeed=0 would feed downstream analysis bogus values.
	for _, mv := range []*float32{
		&sd.Gravity, &sd.StopSpeed, &sd.MaxSpeed, &sd.SpectatorMaxSpeed,
		&sd.Accelerate, &sd.AirAccelerate, &sd.WaterAccelerate, &sd.Friction,
		&sd.WaterFriction, &sd.EntGravity,
	} {
		v, err := r.ReadFloat32()
		if err != nil {
			return err
		}
		*mv = v
	}

	p.serverData = sd

	// Emit event
	return p.emit(&ServerDataEvent{Data: sd, Time: time})
}

// parseModelList decodes svc_modellist / svc_fte_modellistshort. The
// first model in the first chunk is the map BSP (used for the
// ServerData.MapFile shortcut); every model gets appended to the
// parser's model-index table so the entity-state decoder can look up
// model paths when classifying items.
//
// Wire format (ezquake-source/src/cl_parse.c:1722-1815): 1-byte
// start index, NUL-terminated strings until "" terminator, 1-byte
// continuation index. Split packets are rare in recorded demos but
// the protocol allows them, so we respect `start` as the starting
// offset within p.modelList.
func (p *Parser) parseModelList(r *mvd.BufferReader) error {
	start, err := r.ReadByte()
	if err != nil {
		return err
	}
	if p.modelList == nil {
		// Index 0 is reserved for the null model.
		p.modelList = []string{""}
	}
	firstIdx := int(start) + 1
	for len(p.modelList) < firstIdx {
		p.modelList = append(p.modelList, "")
	}
	idx := firstIdx
	firstModel := (idx == 1)
	for {
		s, err := r.ReadString()
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
		for len(p.modelList) < idx+1 {
			p.modelList = append(p.modelList, "")
		}
		p.modelList[idx] = s
		if firstModel && p.serverData != nil {
			p.serverData.MapFile = s
			firstModel = false
		}
		idx++
	}
	_, err = r.ReadByte()
	return err
}

// parseSoundList decodes svc_soundlist / svc_fte_soundlistshort. Each
// precached sound path is appended to the parser's sound-index table so
// the svc_sound decoder can resolve a wire sound_num back to its path
// (e.g. "weapons/rocket1i.wav") — which is how the shots analyzer maps a
// fire sound to the weapon that produced it.
//
// Wire format mirrors svc_modellist (ezquake-source/src/cl_parse.c
// case svc_soundlist): 1-byte start index, NUL-terminated strings until
// the "" terminator, 1-byte continuation index. Index 0 is reserved for
// the null sound, so a sound_num of N indexes p.soundList[N].
func (p *Parser) parseSoundList(r *mvd.BufferReader) error {
	start, err := r.ReadByte()
	if err != nil {
		return err
	}
	if p.soundList == nil {
		// Index 0 is reserved for the null sound.
		p.soundList = []string{""}
	}
	firstIdx := int(start) + 1
	for len(p.soundList) < firstIdx {
		p.soundList = append(p.soundList, "")
	}
	idx := firstIdx
	for {
		s, err := r.ReadString()
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
		for len(p.soundList) < idx+1 {
			p.soundList = append(p.soundList, "")
		}
		p.soundList[idx] = s
		idx++
	}
	_, err = r.ReadByte()
	return err
}

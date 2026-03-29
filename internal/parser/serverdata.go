package parser

import (
	"github.com/mvd-analyzer/internal/mvd"
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

	// Read movement variables (10 floats)
	gravity, _ := r.ReadFloat32()
	sd.Gravity = gravity

	stopSpeed, _ := r.ReadFloat32()
	sd.StopSpeed = stopSpeed

	maxSpeed, _ := r.ReadFloat32()
	sd.MaxSpeed = maxSpeed

	specMaxSpeed, _ := r.ReadFloat32()
	sd.SpectatorMaxSpeed = specMaxSpeed

	accelerate, _ := r.ReadFloat32()
	sd.Accelerate = accelerate

	airAccel, _ := r.ReadFloat32()
	sd.AirAccelerate = airAccel

	waterAccel, _ := r.ReadFloat32()
	sd.WaterAccelerate = waterAccel

	friction, _ := r.ReadFloat32()
	sd.Friction = friction

	waterFriction, _ := r.ReadFloat32()
	sd.WaterFriction = waterFriction

	entGravity, _ := r.ReadFloat32()
	sd.EntGravity = entGravity

	p.serverData = sd

	// Emit event
	return p.emit(&ServerDataEvent{Data: sd, Time: time})
}

// parseModelList parses svc_modellist to extract the map file (first model)
func (p *Parser) parseModelList(r *mvd.BufferReader) error {
	// Skip start index
	if _, err := r.ReadByte(); err != nil {
		return err
	}

	// First model is always the map BSP file
	firstModel := true
	for {
		s, err := r.ReadString()
		if err != nil {
			return err
		}
		if s == "" {
			break
		}

		// First model is the map file (e.g., "maps/dm2.bsp")
		if firstModel && p.serverData != nil {
			p.serverData.MapFile = s
			firstModel = false
		}
	}

	// Skip next index byte
	r.Skip(1)
	return nil
}

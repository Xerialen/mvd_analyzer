package mapgeom

// Full-map solid mesh extraction for the WebGL 3D map view.
//
// Build()/buildRegions above produce the per-loc *floor* plan the 2D map and
// the legacy Canvas-2.5D view render. The WebGL renderer instead wants the
// whole worldspawn shell — floors, walls and ceilings together — as one
// triangle soup it can Phong-shade. BuildMesh3D produces exactly that: every
// solid model-0 face, fan-triangulated into a flat XYZ float list, minus the
// faces that are rendered by other means or would wall the camera out:
//
//   - liquid '*' faces  → drawn separately from MapRegions.Liquids (translucent)
//   - trigger faces     → invisible touch volumes
//   - sky faces         → the skybox; keeping it would enclose the orbit camera
//
// Submodels (lifts/doors) are NOT included here — they move at runtime and are
// posed by the renderer from MapRegions.SubModels + the demo mover stream.
//
// Output is a compact little-endian binary blob (see EncodeMesh3D) written to
// maps3d/<map>.bin, kept out of the per-loc JSON because the full shell is
// several times larger than the floor plan and parses far faster as a typed
// array than as JSON numbers.

import (
	"encoding/binary"
	"math"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// Mesh3DVersion is the binary format version written into the M3D1 header.
const Mesh3DVersion = 1

// mesh3DMagic tags the binary blob so a stale/short read fails loudly.
var mesh3DMagic = [4]byte{'M', '3', 'D', '1'}

// Bounds3D is the axis-aligned box covering all emitted mesh vertices.
type Bounds3D struct {
	MinX, MinY, MinZ float32
	MaxX, MaxY, MaxZ float32
}

// Mesh3D is the full worldspawn solid shell: Tris is a flat XYZ list, 9 floats
// per triangle, in world units (Quake Z-up — no axis remap).
type Mesh3D struct {
	Map     string
	Bounds  Bounds3D
	Tris    []float32
	Faces   int // solid faces triangulated
	Skipped int // faces skipped (liquid/trigger/sky/degenerate ring)
}

// BuildMesh3D triangulates every solid worldspawn face of b into one mesh.
func BuildMesh3D(mapName string, b *bsp.BSP) Mesh3D {
	m := Mesh3D{Map: mapName}
	if b == nil || len(b.Models) == 0 {
		return m
	}

	world := b.Models[0]
	firstFace := int(world.FirstFace)
	endFace := firstFace + int(world.NumFaces)
	if firstFace < 0 {
		firstFace = 0
	}
	if endFace > len(b.Faces) {
		endFace = len(b.Faces)
	}

	bounds := Bounds3D{
		MinX: math.MaxFloat32, MinY: math.MaxFloat32, MinZ: math.MaxFloat32,
		MaxX: -math.MaxFloat32, MaxY: -math.MaxFloat32, MaxZ: -math.MaxFloat32,
	}
	hasBounds := false
	var degen int

	for faceIdx := firstFace; faceIdx < endFace; faceIdx++ {
		face := b.Faces[faceIdx]
		if int(face.PlaneID) >= len(b.Planes) {
			m.Skipped++
			continue
		}
		// Skip faces rendered by other means or that would enclose the camera.
		tex := strings.ToLower(b.FaceTexName(face))
		if (strings.HasPrefix(tex, "*") && !strings.Contains(tex, "tele")) ||
			strings.Contains(tex, "trigger") ||
			strings.HasPrefix(tex, "sky") {
			m.Skipped++
			continue
		}
		ring, ok := assembleRing(b, face)
		if !ok || len(ring) < 3 {
			m.Skipped++
			continue
		}
		before := len(m.Tris)
		m.Tris = fanTriangulate(m.Tris, ring, &degen)
		if len(m.Tris) == before {
			continue // wholly degenerate face
		}
		m.Faces++
		for _, v := range ring {
			if v.X < bounds.MinX {
				bounds.MinX = v.X
			}
			if v.X > bounds.MaxX {
				bounds.MaxX = v.X
			}
			if v.Y < bounds.MinY {
				bounds.MinY = v.Y
			}
			if v.Y > bounds.MaxY {
				bounds.MaxY = v.Y
			}
			if v.Z < bounds.MinZ {
				bounds.MinZ = v.Z
			}
			if v.Z > bounds.MaxZ {
				bounds.MaxZ = v.Z
			}
			hasBounds = true
		}
	}

	if hasBounds {
		m.Bounds = bounds
	}
	return m
}

// EncodeMesh3D serialises m to the little-endian M3D1 blob:
//
//	[0:4]   magic "M3D1"
//	[4:8]   uint32 version
//	[8:12]  uint32 triangle count
//	[12:36] 6 × float32 bounds (minX,minY,minZ,maxX,maxY,maxZ)
//	[36:]   triCount*9 × float32 vertex positions (x,y,z per vertex)
func EncodeMesh3D(m Mesh3D) []byte {
	triCount := len(m.Tris) / 9
	buf := make([]byte, 0, 36+len(m.Tris)*4)
	buf = append(buf, mesh3DMagic[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(Mesh3DVersion))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(triCount))
	for _, f := range []float32{
		m.Bounds.MinX, m.Bounds.MinY, m.Bounds.MinZ,
		m.Bounds.MaxX, m.Bounds.MaxY, m.Bounds.MaxZ,
	} {
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
	}
	for _, f := range m.Tris {
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
	}
	return buf
}

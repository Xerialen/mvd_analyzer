// Package mapgeom turns a parsed Quake 1 BSP into per-loc walkable-floor
// polygon sets suitable for the viewer's mini-map.
//
// The extractor only keeps faces whose plane normal points "up enough"
// to be treated as a floor (Z >= floorNormalZ). Each floor face is then
// assigned to the nearest loc by plain 3D Euclidean distance, matching
// ezQuake's TP_LocationName exactly (see
// ezquake-source/src/teamplay_locfiles.c). Faces are fan-triangulated
// and emitted as flat float32 triangle lists that the frontend can
// render with a trivial ctx.beginPath/moveTo/lineTo/fill loop.
//
// Faces that cannot be matched to a named loc (because no loc file is
// loaded, the loc list is empty, or the nearest loc's normalized name
// is empty) are routed into a reserved "unnamed" bucket with key
// UnnamedRegionKey. The unnamed bucket is always emitted last in
// result.Locs so the frontend can draw it as a neutral backdrop
// beneath the named loc regions.
package mapgeom

import (
	"sort"

	"github.com/mvd-analyzer/internal/bsp"
	"github.com/mvd-analyzer/internal/loc"
)

const (
	// floorNormalZ is the minimum Z-component for a plane to count as
	// walkable floor (~45° from horizontal — matches Q1's floor
	// heuristic closely enough for visualization).
	floorNormalZ = 0.7

	// ceilingMaxAboveLoc drops a face whose centroid is more than this
	// far above its nearest loc point. Loc points sit at player-eye
	// positions the mapper cared about, so a face significantly above
	// the closest one is almost always unreachable roof/ceiling
	// detail. Since a region is anchored by many loc points (one per
	// playable sub-area), stairs and lifts connecting levels stay
	// covered as long as each level has its own loc point.
	ceilingMaxAboveLoc float32 = 128.0
)

// UnnamedRegionKey is the reserved bucket name for floor faces that
// could not be assigned to a named loc. It is the empty string so it
// cannot collide with any NormalizeLocationName output (which returns
// "" only for empty input, and real loc entries are always non-empty).
// The frontend detects this entry by name === "" and draws it as a
// neutral backdrop beneath the named loc regions.
const UnnamedRegionKey = ""

// Bounds is the axis-aligned XY rectangle covering all emitted triangle
// vertices for a map.
type Bounds struct {
	MinX float32 `json:"minX"`
	MaxX float32 `json:"maxX"`
	MinY float32 `json:"minY"`
	MaxY float32 `json:"maxY"`
}

// LocRegion is the per-loc output record. Tris is a flat list of XY
// pairs in world units, 6 floats per triangle. Name is the normalized
// loc key (matching the JS side's processLocationGroups keying).
type LocRegion struct {
	Name string    `json:"name"`
	Z    float32   `json:"z"`
	Tris []float32 `json:"tris"`
}

// MapRegions is the JSON output root.
type MapRegions struct {
	Map     string      `json:"map"`
	Version int         `json:"version"`
	Bounds  Bounds      `json:"bounds"`
	Locs    []LocRegion `json:"locs"`
}

// Stats carries per-map counters for CLI verbose logging.
type Stats struct {
	FacesTotal   int
	FacesKept    int
	FacesDropped int // ring assembly or geometry drops (not Z-reject)
	FacesUnnamed int // kept but routed into the unnamed backdrop bucket
	FacesCeiling int // kept-but-filtered-as-ceiling-detail
	Locs         int
	Triangles    int
}

// Build extracts floor geometry from bsp, assigns each floor face to the
// nearest loc in finder, and groups them into per-loc triangle lists.
func Build(mapName string, b *bsp.BSP, finder *loc.Finder) (*MapRegions, Stats) {
	var stats Stats

	result := &MapRegions{
		Map:     mapName,
		Version: 1,
	}

	if b == nil || len(b.Models) == 0 {
		return result, stats
	}

	var locPoints []loc.Location
	if finder != nil {
		locPoints = finder.Locations()
	}

	// Only iterate worldspawn faces (model 0). Skipping other models
	// avoids claiming door/platform brush faces which move at runtime.
	world := b.Models[0]
	firstFace := int(world.FirstFace)
	endFace := firstFace + int(world.NumFaces)
	if firstFace < 0 {
		firstFace = 0
	}
	if endFace > len(b.Faces) {
		endFace = len(b.Faces)
	}

	type keptFace struct {
		ring [][2]float32 // XY only (Z kept separately)
		z    float32
	}

	// Group keptFaces by normalized loc name.
	groups := make(map[string][]keptFace)

	for faceIdx := firstFace; faceIdx < endFace; faceIdx++ {
		stats.FacesTotal++
		face := b.Faces[faceIdx]

		// Reject faces whose plane is not upward-facing enough.
		if int(face.PlaneID) >= len(b.Planes) {
			continue
		}
		plane := b.Planes[face.PlaneID]
		normal := plane.Normal
		if face.Side == 1 {
			normal = negate(normal)
		}
		if normal.Z < floorNormalZ {
			continue
		}

		// Assemble ring by walking surfedges → edges → vertices.
		ring3D, ok := assembleRing(b, face)
		if !ok || len(ring3D) < 3 {
			continue
		}

		// Per-face centroid (in world units) and average Z.
		var cx, cy, cz float32
		for _, p := range ring3D {
			cx += p.X
			cy += p.Y
			cz += p.Z
		}
		inv := 1.0 / float32(len(ring3D))
		cx *= inv
		cy *= inv
		cz *= inv

		// Find nearest loc by plain 3D Euclidean squared distance,
		// matching ezQuake's TP_LocationName (teamplay_locfiles.c).
		// Faces with no reachable loc (no finder loaded, empty loc
		// list, or empty normalized name) fall through into the
		// unnamed backdrop bucket.
		key := UnnamedRegionKey
		if len(locPoints) > 0 {
			bestIdx := 0
			bestScore := float32(1e30)
			for i, lp := range locPoints {
				dx := cx - lp.X
				dy := cy - lp.Y
				dz := cz - lp.Z
				score := dx*dx + dy*dy + dz*dz
				if i == 0 || score < bestScore {
					bestScore = score
					bestIdx = i
				}
			}
			// Drop faces sitting well above the nearest loc point —
			// they're almost certainly unreachable roof/ceiling
			// detail. Regions with real vertical range (stairs,
			// lifts) stay covered because each level gets its own
			// loc point, and the nearest loc of a face on that level
			// sits close to it in Z.
			if cz-locPoints[bestIdx].Z > ceilingMaxAboveLoc {
				stats.FacesCeiling++
				continue
			}
			if k := NormalizeLocationName(locPoints[bestIdx].Name); k != "" {
				key = k
			}
		}

		if key == UnnamedRegionKey {
			stats.FacesUnnamed++
		}
		stats.FacesKept++

		ring2D := make([][2]float32, len(ring3D))
		for i, p := range ring3D {
			ring2D[i] = [2]float32{p.X, p.Y}
		}
		groups[key] = append(groups[key], keptFace{ring: ring2D, z: cz})
	}

	// Produce stable output: sort named loc keys alphabetically, then
	// append the unnamed backdrop bucket last so the frontend can draw
	// it underneath the named regions.
	keys := make([]string, 0, len(groups))
	hasUnnamed := false
	for k := range groups {
		if k == UnnamedRegionKey {
			hasUnnamed = true
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if hasUnnamed {
		keys = append(keys, UnnamedRegionKey)
	}

	bounds := Bounds{MinX: 1e30, MaxX: -1e30, MinY: 1e30, MaxY: -1e30}
	hasBounds := false

	for _, k := range keys {
		faces := groups[k]

		// Median Z across this group's faces for the "z" hint.
		zs := make([]float32, len(faces))
		for i, f := range faces {
			zs[i] = f.z
		}
		sort.Slice(zs, func(i, j int) bool { return zs[i] < zs[j] })
		medianZ := zs[len(zs)/2]

		// Fan-triangulate every face ring into a flat list.
		var tris []float32
		for _, f := range faces {
			for i := 1; i+1 < len(f.ring); i++ {
				a := f.ring[0]
				b := f.ring[i]
				c := f.ring[i+1]
				tris = append(tris,
					a[0], a[1],
					b[0], b[1],
					c[0], c[1],
				)
				if !hasBounds {
					bounds.MinX, bounds.MaxX = a[0], a[0]
					bounds.MinY, bounds.MaxY = a[1], a[1]
					hasBounds = true
				}
				for _, p := range [3][2]float32{a, b, c} {
					if p[0] < bounds.MinX {
						bounds.MinX = p[0]
					}
					if p[0] > bounds.MaxX {
						bounds.MaxX = p[0]
					}
					if p[1] < bounds.MinY {
						bounds.MinY = p[1]
					}
					if p[1] > bounds.MaxY {
						bounds.MaxY = p[1]
					}
				}
				stats.Triangles++
			}
		}

		if len(tris) == 0 {
			continue
		}
		result.Locs = append(result.Locs, LocRegion{
			Name: k,
			Z:    medianZ,
			Tris: tris,
		})
	}
	stats.Locs = len(result.Locs)

	if hasBounds {
		result.Bounds = bounds
	}
	return result, stats
}

// assembleRing walks face.NumEdges surfedges starting at face.FirstEdge,
// resolves each through the edge table, and returns the polygon ring as
// a list of Vec3 in world units. For each surfedge the ring vertex is
// edge.V[0] when the surfedge index is positive and edge.V[1] when it
// is negative (Quake's standard winding convention).
func assembleRing(b *bsp.BSP, face bsp.Face) ([]bsp.Vec3, bool) {
	n := int(face.NumEdges)
	if n < 3 {
		return nil, false
	}
	first := int(face.FirstEdge)
	if first < 0 || first+n > len(b.Surfedges) {
		return nil, false
	}
	ring := make([]bsp.Vec3, 0, n)
	for i := 0; i < n; i++ {
		se := b.Surfedges[first+i]
		var vi uint32
		switch {
		case se > 0:
			if int(se) >= len(b.Edges) {
				return nil, false
			}
			vi = b.Edges[se].V[0]
		case se < 0:
			idx := int(-se)
			if idx >= len(b.Edges) {
				return nil, false
			}
			vi = b.Edges[idx].V[1]
		default:
			// se == 0 references the sentinel edge; treat as forward.
			vi = b.Edges[0].V[0]
		}
		if int(vi) >= len(b.Vertices) {
			return nil, false
		}
		ring = append(ring, b.Vertices[vi])
	}
	return ring, true
}

func negate(v bsp.Vec3) bsp.Vec3 {
	return bsp.Vec3{X: -v.X, Y: -v.Y, Z: -v.Z}
}


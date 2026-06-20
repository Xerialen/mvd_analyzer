// Package mapgeom turns a parsed Quake 1 BSP into per-loc walkable-floor
// polygon sets suitable for the viewer's mini-map and its 3D floor model.
//
// Faces whose plane normal points "up enough" (Z >= floorNormalZ) are
// floors; vertical-ish faces (|Z| < floorNormalZ) are walls and downward
// faces (ceiling undersides) are skipped. Walls are still classified (for
// the diagnostic counters) but no longer emitted — they only fed the
// removed occluding "solid" render. Each floor face is then
// assigned to the nearest loc by plain 3D Euclidean distance, matching
// ezQuake's TP_LocationName exactly (see
// ezquake-source/src/teamplay_locfiles.c). Faces are fan-triangulated
// and emitted as flat float32 triangle lists (x,y,z per vertex — 9
// floats per triangle since version 2) that the frontend can project
// and render with a trivial ctx.beginPath/moveTo/lineTo/fill loop.
//
// Faces that cannot be matched to a named loc (because no loc file is
// loaded, the loc list is empty, or the nearest loc's normalized name
// is empty) are routed into a reserved "unnamed" bucket with key
// UnnamedRegionKey. The unnamed bucket is always emitted last in
// result.Locs so the frontend can draw it as a neutral backdrop
// beneath the named loc regions.
package mapgeom

import (
	"math"
	"sort"
	"strings"

	"github.com/mvd-analyzer/mvd-analytics/mapclip"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
	"github.com/mvd-analyzer/mvd-analytics/loc"
)

const (
	// floorNormalZ is the minimum Z-component for a plane to count as
	// walkable floor (~45° from horizontal — matches Q1's floor
	// heuristic closely enough for visualization).
	floorNormalZ = 0.7

	// playerOriginAboveFloor is the Z offset between the floor a
	// standing player rests on and that player's origin (mins.z = -24
	// in standard Quake 1). Loc points are recorded at cl.simorg
	// (ezquake-source/src/teamplay_locfiles.c:316), so a walkable
	// floor's loc sits ~24 above the floor's centroid. The ceiling
	// filter compares like-for-like by adding this offset to face Z
	// before testing against the loc-derived cap.
	playerOriginAboveFloor float32 = 24.0

	// ceilingMaxAboveLoc drops a face whose centroid is more than this
	// far above its 3D-nearest loc point, *after* projecting the face
	// up by playerOriginAboveFloor so face Z and loc Z share a frame.
	// Loc points sit at player-origin positions the mapper cared
	// about, so a face significantly above the closest one is almost
	// always unreachable roof/ceiling detail. Since a region is
	// anchored by many loc points (one per playable sub-area), stairs
	// and lifts connecting levels stay covered as long as each level
	// has its own loc point.
	ceilingMaxAboveLoc float32 = 128.0

	// minTriArea is the smallest triangle area (world units²) the fan
	// triangulation keeps. A face ring with a redundant collinear
	// mid-edge vertex fan-triangulates into zero/near-zero-area slivers:
	// invisible in the viewer, and poison for the WASM edit mode, where
	// triPlane returns null on them so they survive as stray, unpickable,
	// undeletable lines/points once the surrounding patch is removed.
	// Real floor/wall triangles are orders of magnitude larger, so this
	// threshold only ever catches degenerate output.
	minTriArea float32 = 0.5
)

// Params are the tunable extraction thresholds. The zero value of any
// field means "use the package default" (see the constants above), so
// callers — e.g. the viewer's geometry edit mode, which feeds these
// from form inputs over the WASM bridge — can set only what they want
// to override.
type Params struct {
	// FloorNormalZ: minimum plane-normal Z for a face to count as
	// floor; faces with |normal Z| below it are walls. 0.7 ≈ 45°.
	FloorNormalZ float32 `json:"floorNormalZ,omitempty"`
	// CeilingMaxAboveLoc: drop faces whose centroid sits more than
	// this far above their 3D-nearest loc point (roof/ceiling cap).
	CeilingMaxAboveLoc float32 `json:"ceilingMaxAboveLoc,omitempty"`
	// PlayerOriginAboveFloor: standing-player origin height used to
	// put face Z and loc Z in the same frame for the ceiling cap.
	PlayerOriginAboveFloor float32 `json:"playerOriginAboveFloor,omitempty"`
	// PruneXYTol: max XY distance from a usage sample to a floor polygon
	// for the sample to count as "touching" it (BuildPruned only).
	// Default mapclip.FootprintReach (24).
	PruneXYTol float32 `json:"pruneXYTol,omitempty"`
	// PruneZTol: max |faceZ − sampleZ| for a usage sample to count as on
	// a floor polygon (BuildPruned only). Larger for slope-heavy maps.
	PruneZTol float32 `json:"pruneZTol,omitempty"`
}

// defaultPruneZTol is the BuildPruned vertical match tolerance: how far a
// recorded floor-contact Z may sit from the candidate face's plane Z and
// still count as a touch. 16 absorbs slope/trace slack without bridging
// to a distinctly different level (the nearest real floor below a stack
// is typically ≥ 64 away).
const defaultPruneZTol float32 = 16.0

// DefaultParams returns the thresholds the committed corpus is built with.
func DefaultParams() Params {
	return Params{
		FloorNormalZ:           floorNormalZ,
		CeilingMaxAboveLoc:     ceilingMaxAboveLoc,
		PlayerOriginAboveFloor: playerOriginAboveFloor,
		PruneXYTol:             mapclip.FootprintReach,
		PruneZTol:              defaultPruneZTol,
	}
}

// withDefaults fills zero-valued fields with the package defaults.
func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.FloorNormalZ == 0 {
		p.FloorNormalZ = d.FloorNormalZ
	}
	if p.CeilingMaxAboveLoc == 0 {
		p.CeilingMaxAboveLoc = d.CeilingMaxAboveLoc
	}
	if p.PlayerOriginAboveFloor == 0 {
		p.PlayerOriginAboveFloor = d.PlayerOriginAboveFloor
	}
	if p.PruneXYTol == 0 {
		p.PruneXYTol = d.PruneXYTol
	}
	if p.PruneZTol == 0 {
		p.PruneZTol = d.PruneZTol
	}
	return p
}

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

// LocRegion is the per-loc output record. Tris is a flat list of XYZ
// vertices in world units, 9 floats per triangle (version 2; version 1
// emitted XY-only, 6 floats per triangle). Z is the median floor height
// across the region's faces — a region-level hint kept for consumers
// that don't need per-vertex height. Name is the normalized loc key
// (matching the JS side's processLocationGroups keying).
type LocRegion struct {
	Name string    `json:"name"`
	Z    float32   `json:"z"`
	Tris []float32 `json:"tris"`
}

// GeometryVersion is written to MapRegions.Version. Version 2 carries
// per-vertex Z in Tris (9 floats/triangle) so the viewer can render the
// floor plan in 3D; version 1 files were XY-only (6 floats/triangle).
// Version 3 added the optional Walls list (vertical faces) for the viewer's
// occluding "solid" 3D mode; that mode was removed, so walls are no longer
// emitted (the field is kept only to parse existing v3 files).
// Version 4 adds the optional Liquids and SubModels meshes (water/slime/
// lava volumes and brush-model lifts/doors) and the Pruned provenance
// block. All readers stay presence-based: a v4 reader handles v1–v3 files
// (missing fields read as empty), and v1–v3 readers ignore the new
// fields, so a mixed-version corpus is fine in production.
const GeometryVersion = 4

// LiquidMesh is one liquid volume's triangle soup. Liquids come from the
// engine's turbulent '*' textures (excluding teleporters) at any
// orientation — the side faces give the pool its volume — so they are not
// split per loc and never run the floor/ceiling heuristics. Tris layout
// matches LocRegion.Tris (9 floats/triangle).
type LiquidMesh struct {
	Kind string    `json:"kind"` // "water" | "slime" | "lava"
	Tris []float32 `json:"tris"`
}

// SubModelMesh is the static geometry of one brush model (Models[ID],
// ID>=1) — lifts, doors, plats, trains. ID is the brush-model index the
// MVD's "*ID" entities and the result's MoverStream.SubModel reference.
// Vertices are at the model's authored (rest) position; the viewer offsets
// them by the demo-streamed pose origin. Tris layout matches
// LocRegion.Tris.
type SubModelMesh struct {
	ID   int       `json:"id"`
	Tris []float32 `json:"tris"`
}

// PruneInfo records how a usage-pruned corpus file was produced, so a
// reader can tell a pruned map (floor faces no player touched were
// dropped) from a full one. Absent (nil) on unpruned files. Filled only
// by the offline `mapgen -demos` path, never by Build.
type PruneInfo struct {
	Demos        int `json:"demos"`        // demos analyzed for floor usage
	Points       int `json:"points"`       // floor-usage sample points collected
	FacesDropped int `json:"facesDropped"` // floor faces removed as never-touched
}

// MapRegions is the JSON output root. Walls (a flat vertical-face triangle
// list) is retained only so readers can still parse the v3 files that carried
// it; the generator no longer populates it (the occluding "solid" render it
// fed was removed), so it is omitted from fresh output. Liquids, SubModels and
// Pruned are v4 additions and omitted when empty.
type MapRegions struct {
	Map       string         `json:"map"`
	Version   int            `json:"version"`
	Bounds    Bounds         `json:"bounds"`
	Locs      []LocRegion    `json:"locs"`
	Walls     []float32      `json:"walls,omitempty"`
	Liquids   []LiquidMesh   `json:"liquids,omitempty"`
	SubModels []SubModelMesh `json:"submodels,omitempty"`
	Pruned    *PruneInfo     `json:"pruned,omitempty"`
}


// Stats carries per-map counters for CLI verbose logging.
type Stats struct {
	FacesTotal   int
	FacesKept    int
	FacesDropped int // ring assembly or geometry drops (not Z-reject)
	FacesUnnamed   int // kept but routed into the unnamed backdrop bucket
	FacesCeiling   int // kept-but-filtered-as-ceiling-detail
	WallsKept      int // vertical faces classified as walls (not stored)
	Locs           int
	Triangles      int
	WallTris       int // wall triangles classified (not stored)
	DegenerateTris int // fan triangles dropped for sub-minTriArea area
	LiquidFaces    int // worldspawn '*' faces routed into Liquids
	LiquidTris     int // triangles emitted across all LiquidMesh
	SubModelMeshes int // brush-model meshes emitted into SubModels
	SubModelTris   int // triangles emitted across all SubModelMesh
	FacesPruned    int // floor faces dropped by usage pruning (BuildPruned)
}

// Build extracts floor geometry from bsp with the default thresholds,
// assigns each floor face to the nearest loc in finder, and groups them
// into per-loc triangle lists.
func Build(mapName string, b *bsp.BSP, finder *loc.Finder) (*MapRegions, Stats) {
	return BuildParams(mapName, b, finder, DefaultParams())
}

// BuildParams is Build with caller-supplied thresholds (zero fields fall
// back to the defaults).
func BuildParams(mapName string, b *bsp.BSP, finder *loc.Finder, params Params) (*MapRegions, Stats) {
	return buildRegions(mapName, b, finder, params.withDefaults(), nil)
}

// BuildPruned is BuildParams plus offline usage-based floor pruning: a
// floor face that no recorded floor-contact sample in usage lands on
// (within params.PruneXYTol / PruneZTol) is dropped, and result.Pruned
// records the provenance. Walls, liquids and submodels are never pruned.
// A nil usage behaves exactly like BuildParams.
func BuildPruned(mapName string, b *bsp.BSP, finder *loc.Finder, params Params, usage *FloorUsage) (*MapRegions, Stats) {
	return buildRegions(mapName, b, finder, params.withDefaults(), usage)
}

func buildRegions(mapName string, b *bsp.BSP, finder *loc.Finder, p Params, usage *FloorUsage) (*MapRegions, Stats) {
	var stats Stats

	result := &MapRegions{
		Map:     mapName,
		Version: GeometryVersion,
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
		ring []bsp.Vec3 // full XYZ vertices
		z    float32    // centroid Z (used for the region median-Z hint)
	}

	// Group keptFaces by normalized loc name.
	groups := make(map[string][]keptFace)

	// Liquid triangles accumulated per kind ("water"/"slime"/"lava").
	liquidTris := make(map[string][]float32)

	for faceIdx := firstFace; faceIdx < endFace; faceIdx++ {
		stats.FacesTotal++
		face := b.Faces[faceIdx]

		if int(face.PlaneID) >= len(b.Planes) {
			continue
		}

		// Liquid faces (engine '*' textures, excluding teleporters) mark a
		// water/slime/lava volume, not walkable floor. Route every
		// orientation — the side faces give the pool depth — into the
		// Liquids mesh and skip loc assignment and the ceiling cap. When
		// the BSP has no texture lump FaceTexName returns "" and the face
		// falls through to the floor/wall path (pre-v4 behaviour).
		if texName := strings.ToLower(b.FaceTexName(face)); strings.HasPrefix(texName, "*") && !strings.Contains(texName, "tele") {
			ring3D, ok := assembleRing(b, face)
			if !ok || len(ring3D) < 3 {
				continue
			}
			stats.LiquidFaces++
			kind := liquidKind(texName)
			before := len(liquidTris[kind])
			liquidTris[kind] = fanTriangulate(liquidTris[kind], ring3D, &stats.DegenerateTris)
			stats.LiquidTris += (len(liquidTris[kind]) - before) / 9
			continue
		}

		plane := b.Planes[face.PlaneID]
		normal := plane.Normal
		if face.Side == 1 {
			normal = negate(normal)
		}
		// Classify by normal: upward-facing enough → floor; vertical-ish
		// → wall (classified for diagnostics but not stored); downward →
		// ceiling underside, never visible from the analysis camera.
		isFloor := normal.Z >= p.FloorNormalZ
		isWall := !isFloor && normal.Z > -p.FloorNormalZ
		if !isFloor && !isWall {
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
			// Drop faces sitting well above their 3D-nearest loc —
			// they're almost certainly unreachable roof/ceiling
			// detail. Project the face up by playerOriginAboveFloor
			// so we compare loc Z to loc Z (loc points record player
			// origin = floor + 24). Walls share the cap (with the same
			// +24, kept for consistency): it trims skybox rims and
			// roof parapets the same way it trims roof floors.
			if cz+p.PlayerOriginAboveFloor-locPoints[bestIdx].Z > p.CeilingMaxAboveLoc {
				stats.FacesCeiling++
				continue
			}
			if k := NormalizeLocationName(locPoints[bestIdx].Name); k != "" {
				key = k
			}
		}

		if isWall {
			// Counted for the verbose diagnostics, but not stored: walls
			// only served the removed occluding "solid" render.
			stats.WallsKept++
			for i := 1; i+1 < len(ring3D); i++ {
				if triArea(ring3D[0], ring3D[i], ring3D[i+1]) < minTriArea {
					stats.DegenerateTris++
					continue
				}
				stats.WallTris++
			}
			continue
		}

		// Usage pruning (BuildPruned only): drop a floor face that no
		// recorded player floor-contact sample lands on. Uses the original
		// (non-side-negated) plane so planeZ is evaluated correctly.
		if usage != nil && !usage.matches(ring3D, plane, p.PruneXYTol, p.PruneZTol) {
			stats.FacesPruned++
			continue
		}

		if key == UnnamedRegionKey {
			stats.FacesUnnamed++
		}
		stats.FacesKept++

		groups[key] = append(groups[key], keptFace{ring: ring3D, z: cz})
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
				if triArea(a, b, c) < minTriArea {
					stats.DegenerateTris++
					continue
				}
				tris = append(tris,
					a.X, a.Y, a.Z,
					b.X, b.Y, b.Z,
					c.X, c.Y, c.Z,
				)
				if !hasBounds {
					bounds.MinX, bounds.MaxX = a.X, a.X
					bounds.MinY, bounds.MaxY = a.Y, a.Y
					hasBounds = true
				}
				for _, p := range [3]bsp.Vec3{a, b, c} {
					if p.X < bounds.MinX {
						bounds.MinX = p.X
					}
					if p.X > bounds.MaxX {
						bounds.MaxX = p.X
					}
					if p.Y < bounds.MinY {
						bounds.MinY = p.Y
					}
					if p.Y > bounds.MaxY {
						bounds.MaxY = p.Y
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

	// Walls are no longer emitted (they only fed the removed "solid" render),
	// so result.Walls stays nil. Bounds already excluded them; liquids and
	// submodels are likewise excluded (and movers shift at runtime, so their
	// rest pose is not a stable bound).

	// Liquids: emit one mesh per kind in a stable order.
	for _, kind := range []string{"water", "slime", "lava"} {
		if tris := liquidTris[kind]; len(tris) > 0 {
			result.Liquids = append(result.Liquids, LiquidMesh{Kind: kind, Tris: tris})
		}
	}

	// SubModels: every brush model (Models[1:]) — lifts, doors, plats,
	// trains. All orientations are kept (a lift needs its sides); only
	// trigger-textured faces (invisible touch volumes) are skipped. Empty
	// models (e.g. trigger-only brushes) are not emitted.
	for modelIdx := 1; modelIdx < len(b.Models); modelIdx++ {
		m := b.Models[modelIdx]
		mf := int(m.FirstFace)
		me := mf + int(m.NumFaces)
		if mf < 0 {
			mf = 0
		}
		if me > len(b.Faces) {
			me = len(b.Faces)
		}
		var tris []float32
		for fi := mf; fi < me; fi++ {
			face := b.Faces[fi]
			if int(face.PlaneID) >= len(b.Planes) {
				continue
			}
			if strings.Contains(strings.ToLower(b.FaceTexName(face)), "trigger") {
				continue
			}
			ring3D, ok := assembleRing(b, face)
			if !ok || len(ring3D) < 3 {
				continue
			}
			before := len(tris)
			tris = fanTriangulate(tris, ring3D, &stats.DegenerateTris)
			stats.SubModelTris += (len(tris) - before) / 9
		}
		if len(tris) > 0 {
			result.SubModels = append(result.SubModels, SubModelMesh{ID: modelIdx, Tris: tris})
		}
	}
	stats.SubModelMeshes = len(result.SubModels)

	if usage != nil {
		result.Pruned = &PruneInfo{
			Demos:        usage.demos,
			Points:       usage.Points(),
			FacesDropped: stats.FacesPruned,
		}
	}

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

// fanTriangulate appends ring's non-degenerate fan triangles (9 floats
// each) to dst and returns the grown slice; degenerate slivers below
// minTriArea are skipped and counted into *degen. Used for liquid and
// submodel meshes (the floor and wall loops inline the same guard because
// they also track per-triangle bounds/stats).
func fanTriangulate(dst []float32, ring []bsp.Vec3, degen *int) []float32 {
	for i := 1; i+1 < len(ring); i++ {
		a, b, c := ring[0], ring[i], ring[i+1]
		if triArea(a, b, c) < minTriArea {
			*degen++
			continue
		}
		dst = append(dst,
			a.X, a.Y, a.Z,
			b.X, b.Y, b.Z,
			c.X, c.Y, c.Z,
		)
	}
	return dst
}

// liquidKind classifies a lowercased '*' texture name into the liquid kind
// the viewer tints. Engine convention: substrings "lava"/"slime"; anything
// else turbulent is water (*water*, *04mwat*, plain "*").
func liquidKind(name string) string {
	switch {
	case strings.Contains(name, "lava"):
		return "lava"
	case strings.Contains(name, "slime"):
		return "slime"
	default:
		return "water"
	}
}

// triArea returns the area of triangle a,b,c — half the magnitude of the
// cross product of its two edges. Collinear vertices yield ~0, which is
// how a face ring with a redundant mid-edge vertex shows up after fan
// triangulation (see minTriArea).
func triArea(a, b, c bsp.Vec3) float32 {
	ux, uy, uz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	vx, vy, vz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
	cx := uy*vz - uz*vy
	cy := uz*vx - ux*vz
	cz := ux*vy - uy*vx
	return 0.5 * float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz)))
}


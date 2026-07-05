package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/mvd-analyzer/mvd-analytics/loc"
	"github.com/mvd-analyzer/mvd-analytics/mapents"
	"github.com/mvd-analyzer/mvd-analytics/mapgen/bsp"
)

// emitEntities parses a BSP's entity lump, classifies the entities we
// surface, names each by its nearest loc (disambiguated), and writes the
// per-map corpus JSON under outDir.
//
// Point entities (items, spawnpoints, teleport destinations) carry their
// position in the entity origin and are emitted directly. Brush entities
// (buttons, teleport sources, doors) need bmodel bbox resolution from the
// BSP models lump, resolved via brushPlacement below.
func emitEntities(path, name string, finder *loc.Finder, outDir string, verbose bool) error {
	ents, err := bsp.ReadEntities(path)
	if err != nil {
		return fmt.Errorf("read entities: %w", err)
	}
	// Submodel bboxes place brush entities (button/teleportSrc/door).
	// Best-effort: on a read error, brush entities just stay unplaced.
	models, _ := bsp.ReadModelBounds(path)

	out := make([]mapents.MapEntity, 0, len(ents))
	var unplaced int
	for _, e := range ents {
		etype, kind, ok := mapents.Classify(e.Classname, e.Spawnflags)
		if !ok {
			continue
		}
		me := mapents.MapEntity{
			Type:       etype,
			Class:      e.Classname,
			Kind:       kind,
			Spawnflags: e.Spawnflags,
		}
		if mapents.IsPointEntity(etype) {
			me.X, me.Y, me.Z = e.Origin[0], e.Origin[1], e.Origin[2]
		} else {
			// Brush entity: anchor at the submodel bbox centre and carry
			// the volume as Bounds.
			c, b, ok := brushPlacement(e, models)
			if !ok {
				unplaced++
				continue
			}
			me.X, me.Y, me.Z = c[0], c[1], c[2]
			me.Bounds = b
		}
		switch etype {
		case mapents.TypeTeleportDst:
			me.TargetName = e.RawKeys["targetname"]
		case mapents.TypeTeleportSrc:
			me.Target = e.RawKeys["target"]
		}
		if finder != nil {
			me.Loc = finder.FindNearest(me.X, me.Y, me.Z)
		}
		out = append(out, me)
	}

	assignNames(out)
	sortEntities(out)

	doc := mapents.MapEntities{Map: name, Version: mapents.CorpusVersion, Entities: out}
	data, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return fmt.Errorf("marshal entities: %w", err)
	}
	data = append(data, '\n')
	outPath := filepath.Join(outDir, name+".json")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "  ents %s: entities=%d unplaced-brush=%d bytes=%d\n",
			name, len(out), unplaced, len(data))
	}
	return nil
}

// brushPlacement resolves a brush entity's `model "*N"` reference to the
// Nth submodel's bbox, returning the box centre (anchor point) and the
// box itself (trigger/door volume), both offset by any entity origin.
// ok is false when the model key is missing/not a submodel ref or the
// index is out of range (e.g. models lump unreadable).
func brushPlacement(e bsp.Entity, models []bsp.ModelBounds) ([3]float32, *mapents.Bounds, bool) {
	m := e.RawKeys["model"]
	if len(m) < 2 || m[0] != '*' {
		return [3]float32{}, nil, false
	}
	idx, err := strconv.Atoi(m[1:])
	if err != nil || idx < 0 || idx >= len(models) {
		return [3]float32{}, nil, false
	}
	mb := models[idx]
	off := e.Origin
	min := [3]float32{mb.Mins.X + off[0], mb.Mins.Y + off[1], mb.Mins.Z + off[2]}
	max := [3]float32{mb.Maxs.X + off[0], mb.Maxs.Y + off[1], mb.Maxs.Z + off[2]}
	c := [3]float32{(min[0] + max[0]) / 2, (min[1] + max[1]) / 2, (min[2] + max[2]) / 2}
	return c, &mapents.Bounds{Min: min, Max: max}, true
}

// assignNames sets a human label on every entity: its nearest loc name
// (or a kind/type fallback when no loc file exists), disambiguated with
// `-1`/`-2` suffixes within each (type, kind, base-name) group so two
// RLs at "low" become "low-1"/"low-2" while an RL and a teleport that
// share a loc both stay "low" (the type/kind fields separate them).
func assignNames(ents []mapents.MapEntity) {
	type key struct{ t, k, b string }
	bases := make([]string, len(ents))
	groups := map[key][]int{}
	for i := range ents {
		bases[i] = baseName(ents[i])
		gk := key{ents[i].Type, ents[i].Kind, bases[i]}
		groups[gk] = append(groups[gk], i)
	}
	for gk, idxs := range groups {
		if len(idxs) == 1 {
			ents[idxs[0]].Name = bases[idxs[0]]
			continue
		}
		sort.Slice(idxs, func(a, b int) bool { return lessPos(ents[idxs[a]], ents[idxs[b]]) })
		for n, i := range idxs {
			ents[i].Name = fmt.Sprintf("%s-%d", gk.b, n+1)
		}
	}
}

func baseName(e mapents.MapEntity) string {
	if e.Loc != "" {
		return e.Loc
	}
	if e.Kind != "" {
		return e.Kind
	}
	switch e.Type {
	case mapents.TypeSpawn:
		return "spawn"
	case mapents.TypeTeleportDst:
		return "tele-dst"
	case mapents.TypeTeleportSrc:
		return "tele-src"
	case mapents.TypeButton:
		return "button"
	case mapents.TypeDoor:
		return "door"
	}
	return e.Type
}

// sortEntities orders the slice deterministically (type, kind, name,
// then position) so regenerated corpora diff cleanly.
func sortEntities(ents []mapents.MapEntity) {
	sort.Slice(ents, func(a, b int) bool {
		if ents[a].Type != ents[b].Type {
			return ents[a].Type < ents[b].Type
		}
		if ents[a].Kind != ents[b].Kind {
			return ents[a].Kind < ents[b].Kind
		}
		if ents[a].Name != ents[b].Name {
			return ents[a].Name < ents[b].Name
		}
		return lessPos(ents[a], ents[b])
	})
}

func lessPos(a, b mapents.MapEntity) bool {
	if a.X != b.X {
		return a.X < b.X
	}
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.Z < b.Z
}

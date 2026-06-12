//go:build !(js && wasm)

// Package mapbsp is the single source of raw per-map BSP bytes at
// analyze time. Both the visibility-aware loc finder (locvis) and the
// floor-height clip hull (mapclip) pull the same provisioned .bsp
// through here, so a deployment only has to ship BSPs once and both
// features light up (or degrade) together.
//
// BSPs are best-effort: a missing file is not an error, it just means
// the dependent feature falls back (locvis → V1 Euclidean nearest;
// mapclip → no floor column).
package mapbsp

import (
	"os"
	"path/filepath"

	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// dirOverride, if non-empty, takes precedence over $MVDA_BSP_DIR and
// ./bsps. Tests set it via SetDir.
var dirOverride string

// SetDir points LoadBytes at an on-disk directory of BSP files. Pass ""
// to revert to the env-var / cwd lookup. Native-only; WASM routes
// through the host fetchBspSync regardless.
func SetDir(dir string) { dirOverride = dir }

// LoadBytes returns the raw bytes of a map's BSP, or nil if none is
// found. Lookup order: SetDir, $MVDA_BSP_DIR, ./bsps. The map name is
// normalised with the same rules as the loc corpus so aliases resolve
// consistently.
func LoadBytes(mapName string) []byte {
	base := loc.NormalizeMapName(mapName)
	for _, dir := range []string{dirOverride, os.Getenv("MVDA_BSP_DIR"), "bsps"} {
		if dir == "" {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(dir, base+".bsp")); err == nil {
			return data
		}
	}
	return nil
}

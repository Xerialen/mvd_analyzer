//go:build !(js && wasm)

package mapbsp

import (
	"os"
	"path/filepath"
)

// dirOverride, if non-empty, takes precedence over $MVDA_BSP_DIR and
// ./bsps. Tests set it via SetDir. Guarded by cacheMu (mapbsp.go):
// LoadBytes snapshots it under the lock and passes it to readBSP, so a
// concurrent SetDir can't tear the read.
var dirOverride string

// SetDir points LoadBytes at an on-disk directory of BSP files. Pass ""
// to revert to the env-var / cwd lookup. Native-only; WASM routes
// through the host fetchBspSync regardless. Drops the memoised cache
// entry and bumps the generation so an in-flight load from the old
// directory discards its store instead of re-caching stale bytes.
func SetDir(dir string) {
	cacheMu.Lock()
	dirOverride = dir
	cacheMu.Unlock()
	invalidateCache()
}

// currentDir returns the override directory. Caller holds cacheMu.
func currentDir() string {
	return dirOverride
}

// readBSP reads the BSP for the already-normalised base name, or nil if
// no directory has it. Lookup order: the snapshotted SetDir override,
// $MVDA_BSP_DIR, ./bsps.
func readBSP(overrideDir, base string) []byte {
	for _, dir := range []string{overrideDir, os.Getenv("MVDA_BSP_DIR"), "bsps"} {
		if dir == "" {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(dir, base+".bsp")); err == nil {
			return data
		}
	}
	return nil
}

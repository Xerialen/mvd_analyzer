//go:build js && wasm

package mapbsp

import "github.com/mvd-analyzer/mvd-analytics/internal/jshost"

// SetDir is a no-op on WASM (the host owns BSP delivery via fetchBspSync);
// kept so callers don't need build-tagged code. The cache is keyed by map
// name and the host source can't change under it, so nothing to invalidate.
func SetDir(string) {}

// currentDir has no meaning on WASM; the host resolves delivery. Caller
// holds cacheMu.
func currentDir() string { return "" }

// readBSP pulls a map's BSP from the JS host via the shared jshost bridge
// (host callback fetchBspSync). The override dir is unused on WASM.
// Returns nil when the host has no such file (404) or the bridge isn't
// installed — the dependent feature then degrades gracefully.
func readBSP(_, base string) []byte {
	data, err := jshost.FetchSync("fetchBspSync", base)
	if err != nil {
		return nil
	}
	return data
}

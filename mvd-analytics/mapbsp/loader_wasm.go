//go:build js && wasm

package mapbsp

import (
	"syscall/js"

	"github.com/mvd-analyzer/mvd-analytics/loc"
)

// SetDir is a no-op on WASM (the host owns BSP delivery via
// fetchBspSync); kept so callers don't need build-tagged code.
func SetDir(string) {}

// LoadBytes pulls a map's BSP from the JS host via the synchronous
// fetchBspSync(name) -> Uint8Array | null callback worker.js installs.
// Returns nil when the host has no such file (404) or hasn't installed
// the callback — the dependent feature then degrades gracefully.
func LoadBytes(mapName string) []byte {
	base := loc.NormalizeMapName(mapName)
	fn := js.Global().Get("fetchBspSync")
	if fn.IsUndefined() || fn.Type() != js.TypeFunction {
		return nil
	}
	res := fn.Invoke(base)
	if res.IsNull() || res.IsUndefined() || res.Type() != js.TypeObject {
		return nil
	}
	length := res.Length()
	if length <= 0 {
		return nil
	}
	data := make([]byte, length)
	if n := js.CopyBytesToGo(data, res); n != length {
		return nil
	}
	return data
}

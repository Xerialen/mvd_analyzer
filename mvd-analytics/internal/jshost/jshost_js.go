//go:build js && wasm

// Package jshost bridges the WASM analytics loaders to the JS host's
// synchronous fetch callbacks (fetchLocSync / fetchMapEntsSync /
// fetchBspSync) that mvd-web's worker.js installs. Each callback performs
// a synchronous XHR — permitted inside a Web Worker — against the map's
// loc / entity / BSP file and returns a Uint8Array (or, for backward
// compatibility, a string).
//
// loc, mapents and mapbsp each used to re-implement the same
// look-up-global-fn → type-check → Invoke → CopyBytesToGo bridge with
// subtly different legacy-string handling; this package is the single
// implementation they now share.
package jshost

import (
	"fmt"
	"syscall/js"
)

// FetchSync invokes the named global JS function with arg and returns the
// raw bytes it produced:
//
//   - Uint8Array return: copied byte-for-byte (preserving bytes
//     0x80–0xFF that .loc item shorthands depend on).
//   - string return: accepted for backward compatibility (bytes ≥ 0x80
//     may already have been mangled to U+FFFD, but callers that only need
//     ASCII are fine).
//
// It returns (nil, nil) when the host reports "no such file" — a
// null/undefined return or an empty result — so callers can distinguish
// "absent" (nil, nil) from "bridge unavailable / short read" (nil, error)
// and pick their own policy (loc/mapents treat absent as an error; mapbsp
// treats it as best-effort degradation).
func FetchSync(fnName, arg string) ([]byte, error) {
	fn := js.Global().Get(fnName)
	if fn.IsUndefined() || fn.Type() != js.TypeFunction {
		return nil, fmt.Errorf("jshost: %s not available", fnName)
	}
	res := fn.Invoke(arg)
	if res.IsNull() || res.IsUndefined() {
		return nil, nil
	}
	switch res.Type() {
	case js.TypeObject:
		length := res.Length()
		if length <= 0 {
			return nil, nil
		}
		data := make([]byte, length)
		if n := js.CopyBytesToGo(data, res); n != length {
			return nil, fmt.Errorf("jshost: short read from %s (%d/%d)", fnName, n, length)
		}
		return data, nil
	case js.TypeString:
		text := res.String()
		if text == "" {
			return nil, nil
		}
		return []byte(text), nil
	default:
		return nil, fmt.Errorf("jshost: unexpected return type from %s", fnName)
	}
}

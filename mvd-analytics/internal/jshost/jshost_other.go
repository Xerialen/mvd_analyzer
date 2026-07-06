//go:build !(js && wasm)

package jshost

import "fmt"

// FetchSync is a stub on non-WASM platforms. The JS host bridge only
// exists in the browser worker; native loaders read the embedded corpus
// or on-disk files directly and never call this. It exists so the package
// compiles under `go build ./...` on every platform.
func FetchSync(fnName, arg string) ([]byte, error) {
	return nil, fmt.Errorf("jshost: FetchSync unavailable on non-wasm build")
}

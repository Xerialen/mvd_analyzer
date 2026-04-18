//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"syscall/js"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwdemo/mvdfile"
)

func analyze(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return errorJSON("missing data argument")
	}

	filename := "demo.mvd"
	if len(args) >= 2 {
		filename = args[1].String()
	}

	// Copy Uint8Array from JS to Go
	jsData := args[0]
	length := jsData.Get("length").Int()
	data := make([]byte, length)
	js.CopyBytesToGo(data, jsData)

	// Handle gzip decompression
	reader, err := mvdfile.NewReader(bytes.NewReader(data))
	if err != nil {
		return errorJSON(err.Error())
	}
	defer reader.Close()

	// Run analysis pipeline
	registry := analyzer.NewDefaultRegistry()
	result, err := registry.AnalyzeReader(reader, filename)
	if err != nil {
		return errorJSON(err.Error())
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		return errorJSON(err.Error())
	}

	return string(jsonBytes)
}

func errorJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// Set at build time via -ldflags.
var (
	GitHash   = "dev"
	GitTag    = "dev"
	BuildDate = "unknown"
)

func main() {
	js.Global().Set("analyzeMVD", js.FuncOf(analyze))
	js.Global().Set("wasmVersion", map[string]interface{}{
		"hash": GitHash,
		"tag":  GitTag,
		"date": BuildDate,
	})
	// Block forever to keep WASM instance alive
	select {}
}

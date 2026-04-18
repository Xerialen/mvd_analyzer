// One-off golden-output harness for the MVD analyzer refactor.
// Usage: go run /tmp/mvd_golden/golden.go <demos_dir> <out_dir>
// Run from inside the mvd_analyzer repo so the module resolves.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvd-analyzer/qwanalytics/analyzer"
	"github.com/mvd-analyzer/qwdemo/mvdfile"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: golden <demos_dir> <out_dir>")
		os.Exit(2)
	}
	demosDir := os.Args[1]
	outDir := os.Args[2]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entries, err := os.ReadDir(demosDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".mvd") && !strings.HasSuffix(name, ".mvd.gz") {
			continue
		}
		path := filepath.Join(demosDir, name)
		fmt.Println("analyzing:", name)
		if err := run(path, filepath.Join(outDir, name+".json")); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
			os.Exit(1)
		}
	}
}

func run(in, outJSON string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := mvdfile.NewReader(f)
	if err != nil {
		return err
	}
	defer r.Close()
	registry := analyzer.NewDefaultRegistry()
	result, err := registry.AnalyzeReader(r, filepath.Base(in))
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outJSON, b, 0o644)
}

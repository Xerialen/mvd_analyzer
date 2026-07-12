# Findings log

## 2026-07-12 — upstream synchronization candidate (schema v57)

The complete impact assessment, corpus manifest, commands, and output hashes
are recorded in [`../UPSTREAM_SYNC_EVALUATION.md`](../UPSTREAM_SYNC_EVALUATION.md)
and [`../evidence/upstream-sync/README.md`](../evidence/upstream-sync/README.md).

### Verification evidence

Focused real-CLI subprocess test:

```text
=== RUN   TestDecisionFlagsEndToEnd
--- PASS: TestDecisionFlagsEndToEnd (12.57s)
PASS
ok github.com/mvd-analyzer/mvd-analytics/cmd/qw-analyze 12.590s
```

Required repository gate after the final test addition:

```text
go test ./mvd-reader/... ./mvd-analytics/... ./mvd-api/... ./mvd-mcp/... ./mvd-web/...
go: warning: "./mvd-web/..." matched no packages
ok github.com/mvd-analyzer/mvd-analytics/cmd/qw-analyze 13.714s
ok github.com/mvd-analyzer/mvd-api (cached)
ok github.com/mvd-analyzer/mvd-mcp (cached)
```

All listed packages passed. The web module has no Go packages; its WASM/static
build was verified separately with `make build` and produced the expected
viewer assets, including the optional full BSP shell.

### Self-correction surfaced

The first full test run detected that the OpenAPI timeline artifact schema did
not yet declare `playerSlots`. The schema was corrected and the full gate then
passed. A spec review also found that decision flags were only covered below
the actual CLI boundary; `TestDecisionFlagsEndToEnd` now exercises flag
parsing, demo processing, final JSON attachment, inference, and KDLOG
precedence through the real command.

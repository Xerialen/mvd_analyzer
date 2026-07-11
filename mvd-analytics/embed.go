// Package mvdanalytics (the module root) exposes repo-level assets that
// other modules embed-and-serve. go:embed cannot reach across module
// directories, so files living at this root are exported here.
package mvdanalytics

import _ "embed"

// ResultSchemaMD is the authoritative field-level Result reference
// (RESULT_SCHEMA.md), embedded so mvd-api can serve it standalone at
// /docs/result-schema without a GitHub round trip.
//
//go:embed RESULT_SCHEMA.md
var ResultSchemaMD []byte

package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	_ "embed"

	mvdanalytics "github.com/mvd-analyzer/mvd-analytics"
)

// This file serves the machine-readable API description and its browsable
// viewer: GET /openapi.yaml (the hand-authored OpenAPI 3.1 spec, pinned to
// the code by the drift tests in openapi_test.go) and GET /docs (a small
// shell page loading the vendored RapiDoc web component — no CDN, no
// external requests). All three assets are embedded, so the endpoints are
// pure byte-serving with content-hash ETags.
//
// The routes are auth-exempt (authExempt): the spec is the public contract
// and must be readable before a client has a key. If a CSP is ever added
// to this server, /docs needs script-src 'self' (RapiDoc is a module
// script + shadow DOM; the shell has no inline script).

//go:embed openapi/openapi.yaml
var openapiSpec []byte

//go:embed openapi/docs.html
var docsHTML []byte

//go:embed openapi/rapidoc-min.js
var rapidocJS []byte

//go:embed openapi/result-schema.html
var resultSchemaHTML []byte

//go:embed openapi/marked.min.js
var markedJS []byte

// contentETag is a strong validator derived from the bytes themselves.
// The spec/viewer change with deploys, not with the schema version alone
// (wording edits don't bump the schema), so a schemaVersion-keyed ETag
// would 304 stale content.
func contentETag(kind string, body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"%s-%x"`, kind, sum[:6])
}

var (
	openapiSpecETag      = contentETag("openapi", openapiSpec)
	docsHTMLETag         = contentETag("docs", docsHTML)
	rapidocJSETag        = contentETag("rapidoc", rapidocJS)
	resultSchemaHTMLETag = contentETag("schema", resultSchemaHTML)
	resultSchemaMDETag   = contentETag("schemamd", mvdanalytics.ResultSchemaMD)
	markedJSETag         = contentETag("marked", markedJS)
)

// serveEmbedded writes one embedded asset with revalidation. max-age is an
// hour (not immutable — a redeploy changes the content under the same URL;
// the ETag keeps revalidation cheap).
func serveEmbedded(w http.ResponseWriter, r *http.Request, contentType, etag string, body []byte) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleOpenAPISpec: GET /openapi.yaml — the OpenAPI 3.1 description of
// this server.
func (s *server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "application/yaml", openapiSpecETag, openapiSpec)
}

// handleDocs: GET /docs (and /docs/) — the browsable API reference.
func (s *server) handleDocs(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "text/html; charset=utf-8", docsHTMLETag, docsHTML)
}

// handleDocsAsset: GET /docs/rapidoc-min.js — the vendored viewer bundle.
func (s *server) handleDocsAsset(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "text/javascript; charset=utf-8", rapidocJSETag, rapidocJS)
}

// handleResultSchema: GET /docs/result-schema — RESULT_SCHEMA.md (the
// field-level Result reference) rendered as a standalone page, so the
// deep contract is reachable from /docs without a GitHub round trip.
// The markdown itself is embedded from the mvd-analytics module root.
func (s *server) handleResultSchema(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "text/html; charset=utf-8", resultSchemaHTMLETag, resultSchemaHTML)
}

// handleResultSchemaMD: GET /docs/result-schema.md — the raw markdown
// the page renders (also the machine-readable form).
func (s *server) handleResultSchemaMD(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "text/markdown; charset=utf-8", resultSchemaMDETag, mvdanalytics.ResultSchemaMD)
}

// handleMarkedAsset: GET /docs/marked.min.js — the vendored markdown
// renderer the result-schema page uses.
func (s *server) handleMarkedAsset(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "text/javascript; charset=utf-8", markedJSETag, markedJS)
}

package main

// Golden-response validation: the teeth behind the full field-level
// response schemas in openapi/openapi.yaml. A committed golden-corpus
// Result is served through the REAL router (fakeStore + httptest), and
// every JSON response — success and error, across the shape-changing
// param variants — must validate against the spec's schema for that
// operation/status. OpenAPI 3.1 schemas ARE JSON Schema 2020-12, which
// github.com/google/jsonschema-go implements; gopkg.in/yaml.v3 reads the
// spec. Both are TEST-ONLY imports — the shipped binary embeds the spec
// as bytes and links neither.
//
// A coverage sweep at the end fails if a documented JSON 200 gains no
// validation case, so new endpoints must be added to the case table.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"gopkg.in/yaml.v3"
)

// specDocument holds the parsed spec with schema refs rewritten to $defs
// form so any response schema + components.schemas is a self-contained
// JSON Schema 2020-12 document.
type specDocument struct {
	doc      map[string]any
	defs     map[string]any
	resolved map[string]*jsonschema.Resolved
}

var (
	specDocOnce sync.Once
	specDocVal  *specDocument
	specDocErr  error
)

func loadSpecDocument() (*specDocument, error) {
	var raw any
	if err := yaml.Unmarshal(openapiSpec, &raw); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	js, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("yaml->json: %w", err)
	}
	// Rewrite schema refs so each response schema can be resolved as a
	// standalone document with components.schemas as its $defs.
	js = bytes.ReplaceAll(js, []byte("#/components/schemas/"), []byte("#/$defs/"))
	var doc map[string]any
	if err := json.Unmarshal(js, &doc); err != nil {
		return nil, err
	}
	comps, _ := doc["components"].(map[string]any)
	defs, _ := comps["schemas"].(map[string]any)
	if len(defs) == 0 {
		return nil, fmt.Errorf("no components.schemas found")
	}
	return &specDocument{doc: doc, defs: defs, resolved: map[string]*jsonschema.Resolved{}}, nil
}

func specDoc(t *testing.T) *specDocument {
	t.Helper()
	specDocOnce.Do(func() { specDocVal, specDocErr = loadSpecDocument() })
	if specDocErr != nil {
		t.Fatalf("loading openapi.yaml: %v", specDocErr)
	}
	return specDocVal
}

// responseSchema returns the resolved JSON schema for (path, method,
// status), following a components.responses $ref if present. ok=false when
// the response deliberately has no JSON content (204, 304, non-JSON).
func (sd *specDocument) responseSchema(t *testing.T, path, method, status string) (*jsonschema.Resolved, bool) {
	t.Helper()
	key := method + " " + path + " " + status
	if r, hit := sd.resolved[key]; hit {
		return r, r != nil
	}

	pathItem, _ := sd.doc["paths"].(map[string]any)[path].(map[string]any)
	if pathItem == nil {
		t.Fatalf("spec has no path %q", path)
	}
	op, _ := pathItem[strings.ToLower(method)].(map[string]any)
	if op == nil {
		t.Fatalf("spec has no operation %s %s", method, path)
	}
	resp, _ := op["responses"].(map[string]any)[status].(map[string]any)
	if resp == nil {
		t.Fatalf("spec %s %s documents no %q response", method, path, status)
	}
	if ref, _ := resp["$ref"].(string); ref != "" {
		name, ok := strings.CutPrefix(ref, "#/components/responses/")
		if !ok {
			t.Fatalf("unexpected response $ref %q", ref)
		}
		comps := sd.doc["components"].(map[string]any)
		resp, _ = comps["responses"].(map[string]any)[name].(map[string]any)
		if resp == nil {
			t.Fatalf("unresolvable response component %q", name)
		}
	}
	content, _ := resp["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	if schema == nil {
		sd.resolved[key] = nil
		return nil, false
	}

	standalone := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		standalone[k] = v
	}
	standalone["$defs"] = sd.defs
	js, err := json.Marshal(standalone)
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(js, &s); err != nil {
		t.Fatalf("schema for %s %s %s does not parse as JSON Schema: %v", method, path, status, err)
	}
	res, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("schema for %s %s %s does not resolve: %v", method, path, status, err)
	}
	sd.resolved[key] = res
	return res, true
}

// goldenResult loads the committed golden-corpus Result the validation
// server serves. The round-trip (Result -> golden JSON -> Result) must
// preserve the load-bearing sections — assert that up front so a lossy
// unmarshal fails loudly instead of validating empty bodies.
const goldenLabel = "4on4_osams_ra_230426_dm3"

var (
	goldenOnce sync.Once
	goldenVal  *result.Result
	goldenErr  error
)

func goldenResult(t *testing.T) *result.Result {
	t.Helper()
	goldenOnce.Do(func() {
		data, err := os.ReadFile("../mvd-analytics/testdata/golden/" + goldenLabel + ".json")
		if err != nil {
			goldenErr = err
			return
		}
		var r result.Result
		if err := json.Unmarshal(data, &r); err != nil {
			goldenErr = fmt.Errorf("golden unmarshal: %w", err)
			return
		}
		goldenVal = &r
	})
	if goldenErr != nil {
		t.Fatalf("loading golden corpus result: %v", goldenErr)
	}
	for name, missing := range map[string]bool{
		"frags":            goldenVal.Frags == nil,
		"damage":           goldenVal.Damage == nil,
		"items":            goldenVal.Items == nil,
		"timelineAnalysis": goldenVal.TimelineAnalysis == nil,
		"streams":          goldenVal.Streams == nil,
		"opening":          goldenVal.Opening == nil,
		"demoInfo":         goldenVal.DemoInfo == nil,
		"aim":              goldenVal.Aim == nil,
		"shots":            goldenVal.Shots == nil,
		"mapEntities":      goldenVal.MapEntities == nil,
	} {
		if missing {
			t.Fatalf("golden %s round-trip lost section %q — the validation server would serve an empty body", goldenLabel, name)
		}
	}
	return goldenVal
}

// allFieldCodes is every fields= selector, to exercise the widest
// stream-slice / state-at / buckets shapes.
const allFieldCodes = "h,a,at,li,pos,view,hgt,lq,vel,rl,lg,gl,ssg,sng,q,pe,r,sh,nl,rk,cl,sp,d"

type validationCase struct {
	name   string
	method string // "" = GET
	url    string // concrete request URL (path + query)
	path   string // spec path pattern
	status int
}

func validationCases() []validationCase {
	cases := []validationCase{
		{name: "health", url: "/healthz", path: "/healthz", status: 200},
		{name: "version", url: "/v1/version", path: "/v1/version", status: 200},
		{name: "auth-check", url: "/v1/auth/check", path: "/v1/auth/check", status: 204},
		{name: "artifacts-manifest", url: "/v1/artifacts", path: "/v1/artifacts", status: 200},
		{name: "graph", url: "/v1/graph", path: "/v1/graph", status: 200},
		{name: "load", method: "POST", url: "/v1/demos/gameId:42", path: "/v1/demos/{id}", status: 200},

		{name: "overview", url: "/v1/demos/gameId:42/overview", path: "/v1/demos/{id}/overview", status: 200},
		{name: "demoinfo", url: "/v1/demos/gameId:42/demoinfo", path: "/v1/demos/{id}/demoinfo", status: 200},
		{name: "metadata", url: "/v1/demos/gameId:42/metadata", path: "/v1/demos/{id}/metadata", status: 200},

		{name: "frags", url: "/v1/demos/gameId:42/frags", path: "/v1/demos/{id}/frags", status: 200},
		{name: "frags-summary", url: "/v1/demos/gameId:42/frags?summary=1", path: "/v1/demos/{id}/frags", status: 200},
		{name: "frags-filtered", url: "/v1/demos/gameId:42/frags?weapon=rl,lg&from=60&to=300", path: "/v1/demos/{id}/frags", status: 200},
		{name: "damage", url: "/v1/demos/gameId:42/damage", path: "/v1/demos/{id}/damage", status: 200},
		{name: "damage-summary", url: "/v1/demos/gameId:42/damage?summary=1", path: "/v1/demos/{id}/damage", status: 200},
		{name: "damage-filtered", url: "/v1/demos/gameId:42/damage?weapon=rl&from=60&to=300", path: "/v1/demos/{id}/damage", status: 200},
		{name: "shots", url: "/v1/demos/gameId:42/shots", path: "/v1/demos/{id}/shots", status: 200},
		{name: "aim", url: "/v1/demos/gameId:42/aim", path: "/v1/demos/{id}/aim", status: 200},
		{name: "aim-summary", url: "/v1/demos/gameId:42/aim?summary=1", path: "/v1/demos/{id}/aim", status: 200},
		{name: "aim-windowed", url: "/v1/demos/gameId:42/aim?from=60&to=300", path: "/v1/demos/{id}/aim", status: 200},

		{name: "loc-graph", url: "/v1/demos/gameId:42/loc-graph", path: "/v1/demos/{id}/loc-graph", status: 200},
		{name: "chat", url: "/v1/demos/gameId:42/chat", path: "/v1/demos/{id}/chat", status: 200},
		{name: "backpacks", url: "/v1/demos/gameId:42/backpacks", path: "/v1/demos/{id}/backpacks", status: 200},
		{name: "items", url: "/v1/demos/gameId:42/items", path: "/v1/demos/{id}/items", status: 200},
		{name: "items-summary", url: "/v1/demos/gameId:42/items?summary=1", path: "/v1/demos/{id}/items", status: 200},
		{name: "items-windowed", url: "/v1/demos/gameId:42/items?from=0&to=60&kinds=armor,mega", path: "/v1/demos/{id}/items", status: 200},
		// Empty result: {"items": []} matches both union branches — pins
		// the anyOf (not oneOf) spelling of the Items schema.
		{name: "items-empty", url: "/v1/demos/gameId:42/items?items=nosuchitem", path: "/v1/demos/{id}/items", status: 200},
		{name: "weapon-pickups", url: "/v1/demos/gameId:42/weapon-pickups", path: "/v1/demos/{id}/weapon-pickups", status: 200},
		{name: "weapon-pickups-world", url: "/v1/demos/gameId:42/weapon-pickups?source=world&weapon=rl", path: "/v1/demos/{id}/weapon-pickups", status: 200},

		{name: "buckets-columnar", url: "/v1/demos/gameId:42/buckets?windowMs=5000", path: "/v1/demos/{id}/buckets", status: 200},
		{name: "buckets-row", url: "/v1/demos/gameId:42/buckets?windowMs=5000&layout=row", path: "/v1/demos/{id}/buckets", status: 200},
		{name: "buckets-allfields", url: "/v1/demos/gameId:42/buckets?windowMs=30000&fields=" + allFieldCodes + "&includeTeam=1", path: "/v1/demos/{id}/buckets", status: 200},
		{name: "buckets-locindex-reducers", url: "/v1/demos/gameId:42/buckets?windowMs=10000&loc=index&reducers=h=min,a=last", path: "/v1/demos/{id}/buckets", status: 200},
		{name: "events", url: "/v1/demos/gameId:42/events", path: "/v1/demos/{id}/events", status: 200},
		{name: "events-locindex", url: "/v1/demos/gameId:42/events?loc=index&from=0&to=120", path: "/v1/demos/{id}/events", status: 200},
		{name: "stream-slice", url: "/v1/demos/gameId:42/stream-slice?from=10&to=20&fields=" + allFieldCodes, path: "/v1/demos/{id}/stream-slice", status: 200},
		// state-at rejects sp/d (no point-in-time meaning), so its widest
		// field set is allFieldCodes minus those two.
		{name: "state-at", url: "/v1/demos/gameId:42/state-at?time=30&fields=" + strings.TrimSuffix(allFieldCodes, ",sp,d"), path: "/v1/demos/{id}/state-at", status: 200},
		{name: "err-state-at-sp", url: "/v1/demos/gameId:42/state-at?time=30&fields=sp", path: "/v1/demos/{id}/state-at", status: 400},
		{name: "state-at-locindex", url: "/v1/demos/gameId:42/state-at?time=30&loc=index", path: "/v1/demos/{id}/state-at", status: 200},
		{name: "los", url: "/v1/demos/gameId:42/los", path: "/v1/demos/{id}/los", status: 200},
		{name: "projectiles", url: "/v1/demos/gameId:42/streams/projectiles", path: "/v1/demos/{id}/streams/projectiles", status: 200},
		{name: "beams", url: "/v1/demos/gameId:42/streams/beams", path: "/v1/demos/{id}/streams/beams", status: 200},
		{name: "nails", url: "/v1/demos/gameId:42/streams/nails", path: "/v1/demos/{id}/streams/nails", status: 200},
		{name: "loc-trails", url: "/v1/demos/gameId:42/loc-trails?minDwellMs=500", path: "/v1/demos/{id}/loc-trails", status: 200},
		{name: "loc-table", url: "/v1/demos/gameId:42/loc-table", path: "/v1/demos/{id}/loc-table", status: 200},
		{name: "region-control", url: "/v1/demos/gameId:42/region-control?windowMs=5000", path: "/v1/demos/{id}/region-control", status: 200},
		{name: "airgibs", url: "/v1/demos/gameId:42/airgibs", path: "/v1/demos/{id}/airgibs", status: 200},

		{name: "map-entities", url: "/v1/maps/dm3/entities", path: "/v1/maps/{map}/entities", status: 200},
		{name: "map-entities-filtered", url: "/v1/maps/dm3/entities?types=item&kinds=armor,weapon", path: "/v1/maps/{map}/entities", status: 200},

		// Error paths — every body must validate against ErrorEnvelope.
		{name: "err-invalid-demo-id", url: "/v1/demos/bogus/overview", path: "/v1/demos/{id}/overview", status: 400},
		{name: "err-invalid-param", url: "/v1/demos/gameId:42/frags?from=abc", path: "/v1/demos/{id}/frags", status: 400},
		{name: "err-unknown-field", url: "/v1/demos/gameId:42/state-at?time=1&fields=loc", path: "/v1/demos/{id}/state-at", status: 400},
		{name: "err-missing-param", url: "/v1/demos/gameId:42/state-at", path: "/v1/demos/{id}/state-at", status: 400},
		{name: "err-demo-not-found", url: "/v1/demos/gameId:999/overview", path: "/v1/demos/{id}/overview", status: 404},
		{name: "err-artifact-unknown", url: "/v1/demos/gameId:42/artifacts/nope", path: "/v1/demos/{id}/artifacts/{name}", status: 404},
		{name: "err-artifact-params", url: "/v1/demos/gameId:42/artifacts/frag?x=1", path: "/v1/demos/{id}/artifacts/{name}", status: 400},
		{name: "err-map-unknown", url: "/v1/maps/nosuchmap/entities", path: "/v1/maps/{map}/entities", status: 404},
		{name: "err-geometry-unconfigured", url: "/v1/maps/dm3/geometry", path: "/v1/maps/{map}/geometry", status: 404},
		{name: "err-demoinfo-unavailable", url: "/v1/demos/gameId:43/demoinfo", path: "/v1/demos/{id}/demoinfo", status: 422},
		{name: "err-frags-unavailable", url: "/v1/demos/gameId:43/frags", path: "/v1/demos/{id}/frags", status: 422},
		{name: "err-region-control-unavailable", url: "/v1/demos/gameId:43/region-control", path: "/v1/demos/{id}/region-control", status: 422},
		{name: "err-opening-unavailable", url: "/v1/demos/gameId:43/artifacts/opening", path: "/v1/demos/{id}/artifacts/{name}", status: 422},
	}
	// Every servable artifact through the generic accessor.
	for _, m := range analyzer.ArtifactManifest() {
		if !m.Servable {
			continue
		}
		cases = append(cases, validationCase{
			name:   "artifact-" + m.Name,
			url:    "/v1/demos/gameId:42/artifacts/" + m.Name,
			path:   "/v1/demos/{id}/artifacts/{name}",
			status: 200,
		})
	}
	return cases
}

func TestOpenAPIGoldenResponsesValidate(t *testing.T) {
	sd := specDoc(t)
	res := goldenResult(t)
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": res,
		// gameId:43 is a well-formed but capability-empty Result for the
		// 422 error paths.
		"gameId:43": {SchemaVersion: result.CurrentSchemaVersion},
	}}
	srv := newTestServer(t, store)
	defer srv.Close()

	covered := map[string]bool{}
	for _, tc := range validationCases() {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequest(method, srv.URL+tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("%s %s = %d, want %d; body: %.300s", method, tc.url, resp.StatusCode, tc.status, body)
			}
			if tc.status == 200 {
				covered[method+" "+tc.path] = true
			}
			schema, hasJSON := sd.responseSchema(t, tc.path, method, fmt.Sprintf("%d", tc.status))
			if !hasJSON {
				if len(body) > 0 {
					t.Fatalf("spec documents no JSON body for %s %s %d but got %d bytes", method, tc.path, tc.status, len(body))
				}
				return
			}
			var instance any
			if err := json.Unmarshal(body, &instance); err != nil {
				t.Fatalf("response is not JSON: %v; body: %.300s", err, body)
			}
			if err := schema.Validate(instance); err != nil {
				t.Errorf("response does not validate against the spec schema:\n%v", err)
			}
		})
	}

	// Coverage sweep: every documented JSON 200 must have at least one
	// validation case above (non-JSON and portal-less ops excluded), so a
	// new endpoint cannot land schema-unvalidated.
	t.Run("coverage", func(t *testing.T) {
		exempt := map[string]bool{
			"GET /openapi.yaml":           true, // yaml, not JSON
			"GET /docs":                   true, // html
			"GET /docs/result-schema":     true, // html
			"GET /v1/auth/check":          true, // 204, validated above but no 200
			"GET /v1/maps/{map}/geometry": true, // needs -maps-dir; 404 path validated above
		}
		for op := range specPaths(t) {
			if exempt[op] {
				continue
			}
			if !covered[op] {
				t.Errorf("no 200 validation case exercises %q — add one to validationCases()", op)
			}
		}
	})
}

var _ = httptest.NewServer // silence unused-import drift if the helper moves

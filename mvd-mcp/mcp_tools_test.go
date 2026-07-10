package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestInputSchemasHaveNoNullUnions guards the array/map filter params against
// the jsonschema-go regression that broke them in production: a nilable Go
// slice/map reflects to a ["null", X] type union, which several MCP clients
// coerce to a string, silently disabling filters like players/fields/weapon.
// addTool/stripNullTypes collapse those unions; this pins the result.
func TestInputSchemasHaveNoNullUnions(t *testing.T) {
	for name, s := range map[string]*jsonschema.Schema{
		"getBuckets":       inputSchema[GetBucketsInput](), // players, fields, reducers(map)
		"getEvents":        inputSchema[GetEventsInput](),  // players, types
		"getFrags":         inputSchema[GetFragsInput](),   // players, weapon
		"getItems":         inputSchema[GetItemsInput](),   // items, players, kinds
		"getWeaponPickups": inputSchema[GetWeaponPickupsInput](),
	} {
		assertNoNullTypes(t, name, s)
	}

	// Spot-check the concrete shape: players is a plain array of strings.
	pl := inputSchema[GetEventsInput]().Properties["players"]
	if pl == nil || pl.Type != "array" || len(pl.Types) != 0 || pl.Items == nil || pl.Items.Type != "string" {
		t.Errorf("getEvents.players is not a clean string array: %+v", pl)
	}
	// The reducers map is a plain object.
	if r := inputSchema[GetBucketsInput]().Properties["reducers"]; r == nil || r.Type != "object" || len(r.Types) != 0 {
		t.Errorf("getBuckets.reducers is not a clean object: %+v", r)
	}
}

func assertNoNullTypes(t *testing.T, path string, s *jsonschema.Schema) {
	t.Helper()
	if s == nil {
		return
	}
	for _, tp := range s.Types {
		if tp == "null" {
			t.Errorf("%s: schema still carries a \"null\" type union: %v", path, s.Types)
		}
	}
	for k, p := range s.Properties {
		assertNoNullTypes(t, path+"."+k, p)
	}
	assertNoNullTypes(t, path+".items", s.Items)
	assertNoNullTypes(t, path+".additionalProperties", s.AdditionalProperties)
	for i, p := range s.PrefixItems {
		assertNoNullTypes(t, path+".prefixItems", p)
		_ = i
	}
}

// fakeBackend implements MCPBackend with canned responses, so the
// tool-registration tests don't need an HTTP server.
type fakeBackend struct {
	loadErr error
}

func (f *fakeBackend) LoadDemo(_ context.Context, in LoadDemoInput) (*LoadDemoOutput, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if in.GameID == 0 && in.SHA256 == "" {
		return nil, errors.New("exactly one of gameId or sha256 must be set")
	}
	return &LoadDemoOutput{
		DemoID:        "sha:" + strings.Repeat("a", 64),
		SHA256:        strings.Repeat("a", 64),
		FromCache:     true,
		SchemaVersion: 7,
	}, nil
}

func (f *fakeBackend) GetOverview(_ context.Context, _ GetOverviewInput) (any, error) {
	return map[string]any{"schemaVersion": 7, "map": "dm6", "duration": 600.0}, nil
}

func (f *fakeBackend) GetBuckets(_ context.Context, _ GetBucketsInput) (any, error) {
	return map[string]any{"windowMs": 50, "buckets": []any{}}, nil
}
func (f *fakeBackend) GetEvents(_ context.Context, _ GetEventsInput) (any, error) {
	return map[string]any{"events": []any{}}, nil
}
func (f *fakeBackend) GetStreamSlice(_ context.Context, _ GetStreamSliceInput) (any, error) {
	return map[string]any{"players": []any{}}, nil
}
func (f *fakeBackend) GetStateAt(_ context.Context, in GetStateAtInput) (any, error) {
	return map[string]any{"t": in.Time, "players": map[string]any{}}, nil
}
func (f *fakeBackend) GetLocTrails(_ context.Context, _ GetLocTrailsInput) (any, error) {
	return map[string]any{"players": []any{}}, nil
}
func (f *fakeBackend) GetLocTable(_ context.Context, _ GetLocTableInput) (any, error) {
	return map[string]any{"locTable": []any{"", "ra", "ya"}}, nil
}
func (f *fakeBackend) GetRegionControl(_ context.Context, _ GetRegionControlInput) (any, error) {
	return map[string]any{"regions": []any{}, "stats": map[string]any{}}, nil
}
func (f *fakeBackend) GetDemoInfo(_ context.Context, _ GetDemoInfoInput) (any, error) {
	return map[string]any{"version": 3, "mode": "4on4", "players": []any{}}, nil
}
func (f *fakeBackend) GetMetadata(_ context.Context, _ GetMetadataInput) (any, error) {
	return map[string]any{"matchSettings": map[string]any{"mode": "4on4"}}, nil
}
func (f *fakeBackend) GetFrags(_ context.Context, _ GetFragsInput) (any, error) {
	return map[string]any{"totalFrags": 165, "byWeapon": map[string]any{"rl": 100}}, nil
}
func (f *fakeBackend) GetDamage(_ context.Context, _ GetDamageInput) (any, error) {
	return map[string]any{"totalDamage": 50000, "byWeapon": map[string]any{"rl": 30000}}, nil
}
func (f *fakeBackend) GetAim(_ context.Context, _ GetAimInput) (any, error) {
	return map[string]any{"players": []any{map[string]any{"player": "gore", "mode": "team"}}}, nil
}
func (f *fakeBackend) GetLocGraph(_ context.Context, _ GetLocGraphInput) (any, error) {
	return map[string]any{"locs": []any{}, "edges": []any{}}, nil
}
func (f *fakeBackend) GetChat(_ context.Context, _ GetChatInput) (any, error) {
	return []any{}, nil
}
func (f *fakeBackend) GetBackpacks(_ context.Context, _ GetBackpacksInput) (any, error) {
	return []any{}, nil
}
func (f *fakeBackend) GetItems(_ context.Context, _ GetItemsInput) (any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (f *fakeBackend) GetMapEntitiesByMap(_ context.Context, _ GetMapEntitiesByMapInput) (any, error) {
	return map[string]any{"entities": []any{}}, nil
}
func (f *fakeBackend) GetWeaponPickups(_ context.Context, _ GetWeaponPickupsInput) (any, error) {
	return []any{}, nil
}
func (f *fakeBackend) ListArtifacts(_ context.Context, _ ListArtifactsInput) (any, error) {
	return map[string]any{"schemaVersion": 49, "artifacts": []any{
		map[string]any{"name": "frag", "servable": true, "resultKey": "frags"},
	}}, nil
}
func (f *fakeBackend) GetArtifact(_ context.Context, in GetArtifactInput) (any, error) {
	if in.Name == "" {
		return nil, errors.New("name required")
	}
	return map[string]any{in.Name: map[string]any{}}, nil
}

// fakeSearcher is the default no-op searcher for backend-focused tests.
type fakeSearcher struct {
	out any
	err error
}

func (f *fakeSearcher) Search(_ context.Context, _ SearchGamesInput) (any, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.out == nil {
		return map[string]any{"limit": 20, "offset": 0, "count": 0, "games": []any{}}, nil
	}
	return f.out, nil
}

// testMCPSession spins up an MCP server with the given backend on an
// in-memory transport and returns a connected client session ready
// for tools/list / tools/call.
func testMCPSession(t *testing.T, backend MCPBackend) *mcp.ClientSession {
	return testMCPSessionWith(t, backend, &fakeSearcher{})
}

func testMCPSessionWith(t *testing.T, backend MCPBackend, sr searcher) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	srv := mcp.NewServer(&mcp.Implementation{Name: "mvd-mcp-test", Version: "test"}, nil)
	registerTools(srv, backend, sr)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(context.Background(), serverTransport)
	}()
	t.Cleanup(func() {
		select {
		case err := <-serverErrCh:
			if err != nil {
				t.Logf("mcp server exited with: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Logf("mcp server did not exit within 2s")
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "mvd-mcp-test-client", Version: "test"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestMCP_ListTools(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{
		"searchGames", "loadDemo",
		"getOverview", "getDemoInfo", "getMetadata", "getFrags", "getDamage",
		"getAim", "getLocGraph", "getChat",
		"getBackpacks", "getItems", "getMapEntitiesByMap", "getWeaponPickups",
		"getBuckets", "getEvents", "getStreamSlice", "getStateAt",
		"getLocTrails", "getLocTable", "getRegionControl",
		"listArtifacts", "getArtifact",
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		// Every tool is read-only (analytics, no user-facing mutation) so
		// clients can reduce per-call approval prompts.
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q missing readOnlyHint annotation", tool.Name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tools; want %d (names=%v)", len(got), len(want), toolNames(res.Tools))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

func toolNames(tools []*mcp.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func TestMCP_LoadDemo_RequiresIdentifier(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "loadDemo",
		Arguments: map[string]any{}, // neither gameId nor sha256
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected isError=true for missing identifier")
	}
}

func TestMCP_LoadDemo_HappyPath(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "loadDemo",
		Arguments: map[string]any{"gameId": 42},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("isError=true; expected success. content=%+v", res.Content)
	}
	var out LoadDemoOutput
	mustDecodeStructured(t, res, &out)
	if out.SHA256 == "" {
		t.Errorf("SHA256 empty in load output: %+v", out)
	}
	if !strings.HasPrefix(out.DemoID, "sha:") {
		t.Errorf("DemoID = %q; expected sha: prefix", out.DemoID)
	}
}

func TestMCP_GetOverview(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "getOverview",
		Arguments: map[string]any{"demoId": "gameId:42"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("isError=true; expected success. content=%+v", res.Content)
	}
	var out map[string]any
	mustDecodeStructured(t, res, &out)
	if out["map"] != "dm6" {
		t.Errorf("Map = %v; want dm6", out["map"])
	}
}

func TestMCP_GetStateAt(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "getStateAt",
		Arguments: map[string]any{
			"demoId": "gameId:42", "time": 15.0, "players": []any{"bps"}, "fields": []any{"h", "a"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("isError=true; content=%+v", res.Content)
	}
	if res.StructuredContent == nil {
		t.Errorf("StructuredContent missing")
	}
}

func TestMCP_LoadDemo_BackendError(t *testing.T) {
	sess := testMCPSession(t, &fakeBackend{loadErr: errors.New("demo not found")})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "loadDemo",
		Arguments: map[string]any{"gameId": 9999},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected isError=true on backend error")
	}
}

// mustDecodeStructured re-marshals a tool result's StructuredContent
// into the typed Out for assertion.
func mustDecodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("StructuredContent is nil")
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode structured content into %T: %v (data=%s)", out, err, string(data))
	}
}

// TestMCP_ErrorSurfacesAPIMessage: a REST 4xx body must reach the MCP
// caller verbatim inside the isError text content — self-describing
// errors (the enumerated field-code list, invalid params) are how an
// agent recovers in one turn. Guards the full stack: real proxy
// backend -> httptest mvd-api returning the standard error envelope ->
// registerTools -> real go-sdk client session.
func TestMCP_ErrorSurfacesAPIMessage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"invalid_param","message":"unknown field code loc; valid codes: li (location), h (health)"}}`))
	}))
	defer api.Close()
	b := newProxyBackend(api.URL, "", 5*time.Second)
	sess := testMCPSession(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "getStateAt",
		Arguments: map[string]any{"demoId": "gameId:42", "time": 0.3, "fields": []string{"loc"}},
	})
	if err != nil {
		t.Fatalf("CallTool must not fail at the protocol level: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected isError=true, got %+v", res)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "unknown field code loc") || !strings.Contains(text, "valid codes") {
		t.Errorf("error text must carry the API message verbatim, got %q", text)
	}
}

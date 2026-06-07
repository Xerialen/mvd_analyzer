package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mvd-analyzer/mvd-api/internal/democache"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// fakeStore implements demoStore for handler tests without touching
// disk or the hub.
type fakeStore struct {
	byID map[string]*result.Result
	err  error
}

func (f *fakeStore) GetResult(_ context.Context, id democache.DemoID) (*result.Result, democache.CacheMeta, error) {
	if f.err != nil {
		return nil, democache.CacheMeta{}, f.err
	}
	r, ok := f.byID[id.String()]
	if !ok {
		return nil, democache.CacheMeta{}, democache.ErrDemoNotFound
	}
	return r, democache.CacheMeta{
		SHA256:        strings.Repeat("a", 64),
		FromCache:     true,
		SchemaVersion: result.CurrentSchemaVersion,
	}, nil
}

// stubResult builds a minimal but well-formed *Result so handlers
// have something to query.
func stubResult() *result.Result {
	return &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		FilePath:      "stub.mvd.gz",
		Match: &result.MatchResult{
			Map:      "dm6",
			GameDir:  "qw",
			Duration: 600000,
			Players: []result.PlayerStat{
				{Name: "bps", Team: "blue", Frags: 35},
				{Name: "milton", Team: "blue", Frags: 28},
				{Name: "valla", Team: "red", Frags: 30},
			},
			Teams: []result.TeamStat{
				{Name: "blue", Frags: 63},
				{Name: "red", Frags: 30},
			},
		},
		Streams: &result.Streams{
			Global: result.GlobalStream{MatchStart: 0, MatchEnd: 600000},
			Players: []result.PlayerStream{
				{Name: "bps", Team: "blue",
					Health: []result.ChangeI16{{T: 0, V: 100}, {T: 10000, V: 50}, {T: 20000, V: 100}},
					Armor:  []result.ChangeI16{{T: 0, V: 0}, {T: 5000, V: 100}},
					RL:     []result.Interval{{Start: 5000, End: 60000}},
				},
			},
		},
		TimelineAnalysis: &result.TimelineAnalysisResult{
			LocTable: []string{"", "ra", "ya", "rl"},
		},
		Metadata: &result.MetadataResult{
			MatchSettings: &result.MatchSettings{Mode: "Team", Matchtag: "testcup"},
		},
		Messages: &result.MessagesResult{
			Events: []result.MatchEvent{
				{Time: 10000, Type: "chat", Player: "bps", Team: "blue", Message: "gl hf", MessageClean: "gl hf"},
				{Time: 20000, Type: "teamsay", Player: "milton", Team: "blue", Message: "watch RA"},
				{Time: 30000, Type: "frag", Player: "bps", Victim: "valla", Weapon: "rl"},
				{Time: 590000, Type: "chat", Player: "valla", Team: "red", Message: "gg"},
			},
		},
		DemoInfo: &result.DemoInfoResult{
			Version: 3,
			Mode:    "4on4",
			Players: []result.DemoInfoPlayer{
				{Name: "bps", Team: "blue"},
				{Name: "valla", Team: "red"},
			},
		},
		Backpacks: []result.BackpackDrop{
			{Time: 100000, Player: "bps", Team: "blue", Weapon: "rl", EntNum: 17},
			{Time: 200000, Player: "valla", Team: "red", Weapon: "lg", EntNum: 23},
		},
		Items: &result.ItemsResult{
			Items: []result.ItemTimeline{
				{Name: "ra", Kind: "ra", EntNum: 9, Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 20000, TakenBy: "bps", Team: "blue", RespawnAt: 40000},
				}},
				{Name: "mh_1", Kind: "mh", EntNum: 11, Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 35000, TakenBy: "valla", Team: "red"},
				}},
				{Name: "ya_1", Kind: "ya", EntNum: 12, Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 10000, TakenBy: "bps", Team: "blue", RespawnAt: 30000},
				}},
				{Name: "ya_2", Kind: "ya", EntNum: 13, Phases: []result.ItemPhase{
					{AvailableFrom: 0, TakenAt: 15000, TakenBy: "valla", Team: "red", RespawnAt: 35000},
				}},
			},
		},
		WeaponPickups: []result.WeaponPickup{
			{Time: 5000, Player: "bps", Team: "blue", Weapon: "rl", Source: "world", Kills: 3},
			{Time: 100000, Player: "milton", Team: "blue", Weapon: "rl", Source: "backpack", BackpackEnt: 17, Dropper: "bps", Kills: 1},
		},
		Errors: []string{"itemAnalyzer: respawn before pickup"},
	}
}

func newTestServer(t *testing.T, store demoStore) *httptest.Server {
	t.Helper()
	return newTestServerMaps(t, store, "")
}

func newTestServerMaps(t *testing.T, store demoStore, mapsDir string) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(newRouter(store, logger, mapsDir))
}

// --- /healthz, /v1/version ---

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/healthz", 200)
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
	if resp["schemaVersion"].(float64) != float64(result.CurrentSchemaVersion) {
		t.Errorf("schemaVersion mismatch")
	}
}

func TestVersion(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/version", 200)
	for _, k := range []string{"hash", "tag", "buildDate"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("missing key %q in version response", k)
		}
	}
}

// --- error mapping ---

func TestInvalidDemoID(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/banana/overview")
	if status != 400 {
		t.Errorf("status = %d; want 400", status)
	}
	var env errorEnvelope
	_ = json.Unmarshal(resp, &env)
	if env.Error.Code != "invalid_demo_id" {
		t.Errorf("code = %q; want invalid_demo_id (body=%s)", env.Error.Code, string(resp))
	}
}

func TestDemoNotFound(t *testing.T) {
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{}})
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/overview")
	if status != 404 {
		t.Errorf("status = %d; want 404 (body=%s)", status, string(resp))
	}
	var env errorEnvelope
	_ = json.Unmarshal(resp, &env)
	if env.Error.Code != "demo_not_found" {
		t.Errorf("code = %q; want demo_not_found", env.Error.Code)
	}
}

func TestHubUpstreamError(t *testing.T) {
	srv := newTestServer(t, &fakeStore{err: democache.ErrHubUpstream})
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/overview")
	if status != 502 {
		t.Errorf("status = %d; want 502 (body=%s)", status, string(resp))
	}
	if !errors.Is(democache.ErrHubUpstream, democache.ErrHubUpstream) {
		t.Fatal("sanity: ErrHubUpstream lost identity")
	}
}

// --- happy paths ---

func storeWithStub() *fakeStore {
	return &fakeStore{byID: map[string]*result.Result{"gameId:42": stubResult()}}
}

func TestLoad(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/demos/gameId:42", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Cache") == "" {
		t.Errorf("X-Cache header missing")
	}
	if resp.Header.Get("X-Schema-Version") != fmt.Sprintf("%d", result.CurrentSchemaVersion) {
		t.Errorf("X-Schema-Version = %q, want %d", resp.Header.Get("X-Schema-Version"), result.CurrentSchemaVersion)
	}
	if resp.Header.Get("ETag") == "" {
		t.Errorf("ETag missing")
	}
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if m["demoId"] == nil {
		t.Errorf("demoId missing in load response: %s", string(body))
	}
	if m["schemaVersion"].(float64) != float64(result.CurrentSchemaVersion) {
		t.Errorf("schemaVersion mismatch")
	}
}

func TestOverview(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/overview", 200)
	if resp["map"] != "dm6" {
		t.Errorf("map = %v", resp["map"])
	}
	if resp["matchEnd"].(float64) != 600000.0 {
		t.Errorf("matchEnd = %v (want 600000 ms in schema v8)", resp["matchEnd"])
	}
	if resp["mode"] != "Team" {
		t.Errorf("mode = %v", resp["mode"])
	}
	players, _ := resp["players"].([]any)
	if len(players) != 3 {
		t.Errorf("len(players) = %d; want 3", len(players))
	}
	teams, _ := resp["teams"].([]any)
	if len(teams) != 2 {
		t.Errorf("len(teams) = %d; want 2", len(teams))
	}
	errs, _ := resp["errors"].([]any)
	if len(errs) != 1 || errs[0] != "itemAnalyzer: respawn before pickup" {
		t.Errorf("errors = %v; want the one stub analyzer error", resp["errors"])
	}
}

// TestOverviewTiming checks that BuildOverview surfaces the wall-clock anchor
// (streams.global) in its `timing` block — the v23 exposure that lets a
// REST/MCP consumer map game time to real time without fetching streams.
func TestOverviewTiming(t *testing.T) {
	r := &result.Result{
		Streams: &result.Streams{
			Global: result.GlobalStream{
				MatchStart:          0,
				MatchEnd:            43035,
				DemoOffset:          10125,
				DemoStartUnixMs:     1780756716100,
				DemoStartAccuracyMs: 1,
				Pauses: []result.TimelinePause{
					{AtMs: 18340, DurationMs: 6641},
					{AtMs: 28074, DurationMs: 6558},
				},
			},
		},
	}
	ov := BuildOverview(r)
	if ov.Timing == nil {
		t.Fatal("Timing block missing")
	}
	if ov.Timing.DemoOffset != 10125 || ov.Timing.DemoStartUnixMs != 1780756716100 ||
		ov.Timing.DemoStartAccuracyMs != 1 || len(ov.Timing.Pauses) != 2 {
		t.Errorf("Timing = %+v", ov.Timing)
	}

	// No wall-clock source → no timing block.
	if got := BuildOverview(&result.Result{Streams: &result.Streams{}}); got.Timing != nil {
		t.Errorf("Timing should be omitted when no anchor present, got %+v", got.Timing)
	}
}

func TestOverviewOmitsErrorsWhenClean(t *testing.T) {
	clean := stubResult()
	clean.Errors = nil
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": clean}})
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/overview", 200)
	if _, present := resp["errors"]; present {
		t.Errorf("errors key should be omitted when the analysis is clean, got %v", resp["errors"])
	}
}

// TestBuckets_DefaultIsColumn pins that omitting layout returns the
// column-major shape (count + players, no row-major "buckets" array).
func TestBuckets_DefaultIsColumn(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/buckets?windowMs=1000&fields=h,a", 200)
	if int(resp["windowMs"].(float64)) != 1000 {
		t.Errorf("windowMs = %v; want 1000", resp["windowMs"])
	}
	if _, ok := resp["count"].(float64); !ok {
		t.Errorf("default layout should be columnar (missing count): %v", resp["count"])
	}
	if _, ok := resp["buckets"]; ok {
		t.Errorf("default (column) layout must not carry a row-major 'buckets' key")
	}
}

func TestBuckets_RowLayout(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/buckets?windowMs=1000&fields=h,a&layout=row", 200)
	if int(resp["windowMs"].(float64)) != 1000 {
		t.Errorf("windowMs = %v; want 1000", resp["windowMs"])
	}
	if _, ok := resp["buckets"].([]any); !ok {
		t.Errorf("layout=row buckets not an array: %T", resp["buckets"])
	}
}

func TestBuckets_BadParam(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/buckets?windowMs=banana")
	if status != 400 {
		t.Errorf("status = %d; want 400 (body=%s)", status, string(resp))
	}
}

func TestBuckets_ColumnLayout(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/buckets?windowMs=1000&fields=h,a&layout=column", 200)
	if int(resp["windowMs"].(float64)) != 1000 {
		t.Errorf("windowMs = %v; want 1000", resp["windowMs"])
	}
	count := int(resp["count"].(float64))
	if count != 600 {
		t.Errorf("count = %d; want 600", count)
	}
	if _, ok := resp["buckets"]; ok {
		t.Errorf("column layout must not carry a row-major 'buckets' key")
	}
	players, ok := resp["players"].(map[string]any)
	if !ok {
		t.Fatalf("players not an object: %T", resp["players"])
	}
	bps, ok := players["bps"].(map[string]any)
	if !ok {
		t.Fatalf("player bps missing: %v", players)
	}
	h, ok := bps["h"].([]any)
	if !ok {
		t.Fatalf("bps.h not an array: %T", bps["h"])
	}
	if len(h) != int(bps["n"].(float64)) {
		t.Errorf("h length %d != n %v", len(h), bps["n"])
	}
}

func TestBuckets_BadLayout(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/buckets?layout=banana")
	if status != 400 {
		t.Errorf("status = %d; want 400 (body=%s)", status, string(resp))
	}
}

func TestEvents_Default(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/events", 200)
	if _, ok := resp["events"].([]any); !ok && resp["events"] != nil {
		// view.Events returns {events: []} when no events; nil/absent is also acceptable
		t.Errorf("events shape unexpected: %T", resp["events"])
	}
}

func TestStreamSlice(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/stream-slice?from=0&to=30&fields=h,a", 200)
	if _, ok := resp["players"].([]any); !ok && resp["players"] != nil {
		t.Errorf("players shape unexpected: %T", resp["players"])
	}
}

func TestStateAt_MissingTime(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/state-at?players=bps")
	if status != 400 {
		t.Errorf("status = %d; want 400 (body=%s)", status, string(resp))
	}
}

func TestStateAt_HappyPath(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/state-at?time=15&fields=h,a&players=bps", 200)
	if resp["t"].(float64) != 15 {
		t.Errorf("t = %v; want 15", resp["t"])
	}
	players, _ := resp["players"].(map[string]any)
	if _, ok := players["bps"]; !ok {
		t.Errorf("bps state missing")
	}
}

func TestLocTrails(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/loc-trails?players=bps")
	if status != 200 {
		t.Errorf("status = %d; want 200 (body=%s)", status, string(resp))
	}
}

func TestLocTable(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/loc-table", 200)
	table, _ := resp["locTable"].([]any)
	want := []string{"", "ra", "ya", "rl"}
	if len(table) != len(want) {
		t.Fatalf("locTable len = %d, want %d (%v)", len(table), len(want), resp["locTable"])
	}
	for i, w := range want {
		if table[i] != w {
			t.Fatalf("locTable[%d] = %v, want %q", i, table[i], w)
		}
	}
}

func TestLocParam_Invalid(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	_, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/buckets?loc=banana")
	if status != 400 {
		t.Errorf("loc=banana status = %d; want 400", status)
	}
}

func TestDemoInfo(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/demoinfo", 200)
	if resp["mode"] != "4on4" {
		t.Errorf("mode = %v", resp["mode"])
	}
	players, _ := resp["players"].([]any)
	if len(players) != 2 {
		t.Errorf("len(players) = %d; want 2", len(players))
	}
}

func TestDemoInfo_Unavailable(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no DemoInfo
	}}
	srv := newTestServer(t, store)
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/demoinfo")
	if status != 422 {
		t.Errorf("status = %d; want 422 (%s)", status, resp)
	}
}

func TestChat_All(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/chat")
	if status != 200 {
		t.Fatalf("status = %d (%s)", status, body)
	}
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	// 3 chat/teamsay events (frag is filtered out by default types).
	if len(arr) != 3 {
		t.Errorf("len = %d; want 3 (body=%s)", len(arr), body)
	}
}

func TestChat_PlayerFilter(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, _ := getRaw(t, srv.URL+"/v1/demos/gameId:42/chat?players=bps")
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	if len(arr) != 1 || arr[0]["player"] != "bps" {
		t.Errorf("expected only bps; got %s", body)
	}
}

func TestChat_TimeWindow(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, _ := getRaw(t, srv.URL+"/v1/demos/gameId:42/chat?from=15&to=100")
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	// only the teamsay at t=20 is in [15, 100].
	if len(arr) != 1 || arr[0]["type"] != "teamsay" {
		t.Errorf("expected only the teamsay; got %s", body)
	}
}

func TestChat_TypesFilter(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, _ := getRaw(t, srv.URL+"/v1/demos/gameId:42/chat?types=teamsay")
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	if len(arr) != 1 || arr[0]["type"] != "teamsay" {
		t.Errorf("expected one teamsay; got %s", body)
	}
}

func TestBackpacks(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, _ := getRaw(t, srv.URL+"/v1/demos/gameId:42/backpacks")
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	if len(arr) != 2 {
		t.Errorf("len = %d; want 2", len(arr))
	}

	// weapon=lg filter
	body, _ = getRaw(t, srv.URL+"/v1/demos/gameId:42/backpacks?weapon=lg")
	_ = json.Unmarshal(body, &arr)
	if len(arr) != 1 || arr[0]["weapon"] != "lg" {
		t.Errorf("weapon=lg filter failed: %s", body)
	}
}

func TestItems_Filters(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()

	count := func(query string) int {
		t.Helper()
		resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/items"+query, 200)
		items, _ := resp["items"].([]any)
		return len(items)
	}

	// items= is case-insensitive and matches the kind token, so the
	// documented display vocabulary (RA, MH) resolves to the lowercase
	// instances ra, mh_1.
	if got := count("?items=RA"); got != 1 {
		t.Errorf("items=RA: got %d, want 1 (ra)", got)
	}
	if got := count("?items=mh"); got != 1 {
		t.Errorf("items=mh: got %d, want 1 (mh_1)", got)
	}
	// A bare kind token matches every instance of that type.
	if got := count("?items=YA"); got != 2 {
		t.Errorf("items=YA: got %d, want 2 (ya_1, ya_2)", got)
	}
	// A suffixed instance name matches just that one.
	if got := count("?items=ya_1"); got != 1 {
		t.Errorf("items=ya_1: got %d, want 1", got)
	}

	// kinds= matches the derived category.
	if got := count("?kinds=armor"); got != 3 {
		t.Errorf("kinds=armor: got %d, want 3 (ra, ya_1, ya_2)", got)
	}
	if got := count("?kinds=mega"); got != 1 {
		t.Errorf("kinds=mega: got %d, want 1 (mh_1)", got)
	}
	if got := count("?kinds=powerup"); got != 0 {
		t.Errorf("kinds=powerup: got %d, want 0", got)
	}
	// A raw kind token is also accepted by kinds=.
	if got := count("?kinds=ya"); got != 2 {
		t.Errorf("kinds=ya: got %d, want 2", got)
	}

	// players= keeps only phases taken by the named player. valla took
	// mh_1 and ya_2; the ra/ya_1 phases (taken by bps) drop out.
	if got := count("?players=valla"); got != 2 {
		t.Errorf("players=valla: got %d items, want 2 (mh_1, ya_2)", got)
	}
}

func TestWeaponPickups_SourceFilter(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, _ := getRaw(t, srv.URL+"/v1/demos/gameId:42/weapon-pickups?source=backpack")
	var arr []map[string]any
	_ = json.Unmarshal(body, &arr)
	if len(arr) != 1 || arr[0]["source"] != "backpack" {
		t.Errorf("source=backpack: %s", body)
	}
}

func TestRegionControl_Unavailable(t *testing.T) {
	// Stub demo has TimelineAnalysis but no RegionControl.
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	_, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/region-control")
	if status != 422 {
		t.Errorf("status = %d; want 422", status)
	}
}

// --- HTTP cache semantics ---

func TestETag_304(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	// First request to learn the ETag.
	resp1, err := http.Get(srv.URL + "/v1/demos/gameId:42/overview")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp1.Header.Get("ETag")
	resp1.Body.Close()
	if etag == "" {
		t.Fatal("ETag missing on first response")
	}

	// Second with If-None-Match.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/demos/gameId:42/overview", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 304 {
		t.Errorf("expected 304, got %d", resp2.StatusCode)
	}
}

// --- helpers ---

func getJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s: status %d; want %d (body=%s)", url, resp.StatusCode, wantStatus, string(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("GET %s: decode: %v (body=%s)", url, err, string(body))
	}
	return m
}

func getRaw(t *testing.T, url string) ([]byte, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

func damageResult() *result.Result {
	return &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		Damage: &result.DamageResult{
			TotalDamage: 300,
			ByWeapon:    map[string]int{"rl": 200, "sg": 100},
			ByPlayer: map[string]*result.PlayerDamage{
				"alpha": {Given: 200, Taken: 50, EWep: 120, EnemyVsRL: 120, ByWeapon: map[string]int{"rl": 200}},
				"bravo": {Given: 100, Taken: 200, ByWeapon: map[string]int{"sg": 100}},
			},
			Matrix: []result.DamagePair{
				{Attacker: "alpha", Victim: "bravo", Damage: 200, ByWeapon: map[string]int{"rl": 200}},
				{Attacker: "bravo", Victim: "alpha", Damage: 100, ByWeapon: map[string]int{"sg": 100}},
			},
			Events: []result.DamageEntry{
				{Time: 1000, Attacker: "alpha", Victim: "bravo", Weapon: "rl", Damage: 200, VictimWep: "rl"},
				{Time: 2000, Attacker: "bravo", Victim: "alpha", Weapon: "sg", Damage: 100},
			},
			Telefrags: []result.PositionalKill{
				{Time: 1500, Attacker: "alpha", Victim: "bravo"},
			},
			Stomps: []result.PositionalKill{
				{Time: 1700, Attacker: "bravo", Victim: "alpha"},
			},
		},
	}
}

func TestDamage_Unavailable(t *testing.T) {
	// Stub demo has no Damage section.
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	_, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/damage")
	if status != 422 {
		t.Errorf("status = %d; want 422", status)
	}
}

func TestDamage_FullAndFilters(t *testing.T) {
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": damageResult()}})
	defer srv.Close()

	// Unfiltered: full result.
	full := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage", 200)
	if full["totalDamage"].(float64) != 300 {
		t.Errorf("totalDamage = %v, want 300", full["totalDamage"])
	}
	if bw, _ := full["byPlayer"].(map[string]any); len(bw) != 2 {
		t.Errorf("byPlayer count = %d, want 2", len(bw))
	}

	// players=alpha narrows byPlayer + matrix + events to alpha's interactions.
	pf := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?players=alpha", 200)
	bp, _ := pf["byPlayer"].(map[string]any)
	if len(bp) != 1 || bp["alpha"] == nil {
		t.Errorf("players filter byPlayer = %v, want only alpha", bp)
	}
	mtx, _ := pf["matrix"].([]any)
	if len(mtx) != 2 { // alpha->bravo and bravo->alpha both involve alpha
		t.Errorf("players filter matrix = %d rows, want 2", len(mtx))
	}

	// Telefrags ride along, filtered by player and kept out of weapon damage.
	if tf, _ := full["telefrags"].([]any); len(tf) != 1 {
		t.Errorf("full telefrags = %d, want 1", len(tf))
	}
	if tf, _ := pf["telefrags"].([]any); len(tf) != 1 {
		t.Errorf("players=alpha telefrags = %d, want 1 (alpha telefragged bravo)", len(tf))
	}

	// weapon=rl narrows byWeapon + events, and excludes telefrags (not a weapon).
	wf := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapon=rl", 200)
	bw, _ := wf["byWeapon"].(map[string]any)
	if len(bw) != 1 || bw["rl"] == nil {
		t.Errorf("weapon filter byWeapon = %v, want only rl", bw)
	}
	ev, _ := wf["events"].([]any)
	if len(ev) != 1 {
		t.Errorf("weapon filter events = %d, want 1 (the rl hit)", len(ev))
	}
	if tf, present := wf["telefrags"]; present && tf != nil {
		if arr, _ := tf.([]any); len(arr) != 0 {
			t.Errorf("weapon=rl telefrags = %d, want 0 (telefrag is not weapon damage)", len(arr))
		}
	}
	// weapon=tele retrieves telefrags specifically (and excludes stomps).
	tw := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapon=tele", 200)
	if tf, _ := tw["telefrags"].([]any); len(tf) != 1 {
		t.Errorf("weapon=tele telefrags = %d, want 1", len(tf))
	}
	if st, present := tw["stomps"]; present && st != nil {
		if arr, _ := st.([]any); len(arr) != 0 {
			t.Errorf("weapon=tele stomps = %d, want 0", len(arr))
		}
	}
	// Stomps ride along on the full result and under weapon=stomp.
	if st, _ := full["stomps"].([]any); len(st) != 1 {
		t.Errorf("full stomps = %d, want 1", len(st))
	}
	sw := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapon=stomp", 200)
	if st, _ := sw["stomps"].([]any); len(st) != 1 {
		t.Errorf("weapon=stomp stomps = %d, want 1", len(st))
	}
}

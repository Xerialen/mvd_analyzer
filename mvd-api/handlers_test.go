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

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
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

// EnsureLOS runs the (idempotent) LOS pass on the stored Result, mirroring the
// real store: a BSP-less stub computes to empty and latches Streams.LOSComputed
// so a second /los request is a no-op.
func (f *fakeStore) EnsureLOS(ctx context.Context, id democache.DemoID) (*result.Result, democache.CacheMeta, error) {
	res, meta, err := f.GetResult(ctx, id)
	if err != nil {
		return nil, meta, err
	}
	analyzer.ComputeLOS(res)
	return res, meta, nil
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

// TestShotStreamEndpoints exercises the three on-demand spatial-stream
// endpoints; the fake store returns whatever streams the result carries.
func TestShotStreamEndpoints(t *testing.T) {
	r := stubResult()
	r.Streams.Projectiles = &result.ProjectileStreams{
		Weapon: []string{"rl"}, Spawn: []int32{1000}, End: []int32{1500},
		Sx: []float32{1}, Sy: []float32{2}, Sz: []float32{3},
		Ex: []float32{4}, Ey: []float32{5}, Ez: []float32{6},
	}
	r.Streams.Beams = &result.BeamStreams{
		T: []int32{2000}, Sx: []float32{1}, Sy: []float32{2}, Sz: []float32{3},
		Ex: []float32{4}, Ey: []float32{5}, Ez: []float32{6},
	}
	r.Streams.Nails = &result.ProjectileStreams{
		Weapon: []string{"nail"}, Spawn: []int32{3000}, End: []int32{3100},
		Sx: []float32{1}, Sy: []float32{2}, Sz: []float32{3},
		Ex: []float32{4}, Ey: []float32{5}, Ez: []float32{6},
	}
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": r}})
	defer srv.Close()

	for _, c := range []struct{ path, key string }{
		{"/v1/demos/gameId:42/streams/projectiles", "projectiles"},
		{"/v1/demos/gameId:42/streams/beams", "beams"},
		{"/v1/demos/gameId:42/streams/nails", "nails"},
	} {
		resp := getJSON(t, srv.URL+c.path, 200)
		if resp[c.key] == nil {
			t.Errorf("%s: %q missing/null, body=%v", c.path, c.key, resp)
		}
	}
}

// TestShotStreamEndpoints_Absent returns 200 with a null stream when the demo
// has none (the stub has no rockets).
func TestShotStreamEndpoints_Absent(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/streams/projectiles", 200)
	if resp["projectiles"] != nil {
		t.Errorf("expected null projectiles, got %v", resp["projectiles"])
	}
}

// fragDamageStore returns a store whose demo carries a small frag + damage
// log, for exercising the /frags and /damage filter params.
func fragDamageStore() *fakeStore {
	r := stubResult()
	r.Frags = &result.FragResult{
		TotalFrags: 2,
		ByWeapon:   map[string]int{"rl": 2},
		ByPlayer: map[string]*result.PlayerFrags{
			"bps":    {Kills: 1, Deaths: 1, ByWeapon: map[string]int{"rl": 1}},
			"milton": {Kills: 1, Deaths: 1, ByWeapon: map[string]int{"rl": 1}},
		},
		Frags: []result.FragEntry{
			{Time: 10000, Killer: "bps", Victim: "milton", Weapon: "rl"},
			{Time: 20000, Killer: "milton", Victim: "bps", Weapon: "rl"},
		},
	}
	r.Damage = &result.DamageResult{
		TotalDamage: 200,
		ByWeapon:    map[string]int{"rl": 200},
		ByPlayer: map[string]*result.PlayerDamage{
			"bps":    {Given: 100, Taken: 100, ByWeapon: map[string]int{"rl": 100}},
			"milton": {Given: 100, Taken: 100, ByWeapon: map[string]int{"rl": 100}},
		},
		Matrix: []result.DamagePair{
			{Attacker: "bps", Victim: "milton", Damage: 100, ByWeapon: map[string]int{"rl": 100}},
			{Attacker: "milton", Victim: "bps", Damage: 100, ByWeapon: map[string]int{"rl": 100}},
		},
		Events: []result.DamageEntry{
			{Time: 10000, Attacker: "bps", Victim: "milton", Weapon: "rl", Damage: 100, VictimWep: "rl"},
			{Time: 20000, Attacker: "milton", Victim: "bps", Weapon: "rl", Damage: 100, VictimWep: "rl"},
		},
	}
	return &fakeStore{byID: map[string]*result.Result{"gameId:42": r}}
}

func TestFragsParams_WindowAndSummary(t *testing.T) {
	srv := newTestServer(t, fragDamageStore())
	defer srv.Close()

	// summary drops the frags log but keeps aggregates.
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/frags?summary=1", 200)
	if resp["frags"] != nil {
		t.Errorf("summary should drop frags log, got %v", resp["frags"])
	}
	if int(resp["totalFrags"].(float64)) != 2 {
		t.Errorf("totalFrags = %v, want 2 (stored, summary keeps authoritative)", resp["totalFrags"])
	}

	// window from=15s keeps only the t=20s frag; aggregates recompute to 1.
	resp = getJSON(t, srv.URL+"/v1/demos/gameId:42/frags?from=15", 200)
	if int(resp["totalFrags"].(float64)) != 1 {
		t.Errorf("from=15: totalFrags = %v, want 1", resp["totalFrags"])
	}

	// malformed from is a clean 400 invalid_param.
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/frags?from=banana")
	if status != 400 {
		t.Errorf("from=banana: status = %d, want 400 (body=%s)", status, string(body))
	}
}

func TestDamageParams_MatrixWhenFiltered(t *testing.T) {
	srv := newTestServer(t, fragDamageStore())
	defer srv.Close()

	// Filtered by a player => matrix must be populated (not null), and Events
	// recomputed. bps is attacker/victim in both hits, so both survive.
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?players=bps", 200)
	if resp["matrix"] == nil {
		t.Errorf("filtered damage must populate matrix, got null")
	}
	if int(resp["totalDamage"].(float64)) != 200 {
		t.Errorf("totalDamage = %v, want 200", resp["totalDamage"])
	}

	// malformed to is a clean 400.
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/damage?to=banana")
	if status != 400 {
		t.Errorf("to=banana: status = %d, want 400 (body=%s)", status, string(body))
	}
}

// boundedDamageStore carries a demo with a full v54 bounded family (gameId:42)
// and one whose bounded reconstruction was skipped (gameId:99), for exercising
// the dmg= family selection.
func boundedDamageStore() *fakeStore {
	full := stubResult()
	full.Damage = &result.DamageResult{
		Dmg:         "both",
		BoundedMode: "standard",
		TotalDamage: 200,
		ByWeapon:    map[string]int{"rl": 200},
		ByPlayer: map[string]*result.PlayerDamage{
			"bps": {Given: 100, Taken: 100, ByWeapon: map[string]int{"rl": 100},
				Bounded: &result.PlayerDamage{Given: 80, Taken: 90, ByWeapon: map[string]int{"rl": 80}}},
			"milton": {Given: 100, Taken: 100, ByWeapon: map[string]int{"rl": 100},
				Bounded: &result.PlayerDamage{Given: 85, Taken: 88, ByWeapon: map[string]int{"rl": 85}}},
		},
		Matrix: []result.DamagePair{
			{Attacker: "bps", Victim: "milton", Damage: 100, ByWeapon: map[string]int{"rl": 100}},
		},
		Events: []result.DamageEntry{
			{Time: 10000, Attacker: "bps", Victim: "milton", Weapon: "rl", Damage: 100, Bounded: intp(80), VictimWep: "rl"},
		},
	}
	skipped := stubResult()
	skipped.Damage = &result.DamageResult{
		BoundedMode: "skipped:midair",
		ByWeapon:    map[string]int{},
		ByPlayer:    map[string]*result.PlayerDamage{},
	}
	return &fakeStore{byID: map[string]*result.Result{
		"gameId:42": full,
		"gameId:99": skipped,
	}}
}

func intp(i int) *int { return &i }

// TestDamageParams_DmgFamily pins the dmg= family selection and its
// default resolution (resolved once in handleDamage): an unset dmg is now
// `bounded` for BOTH the summary and the full log.
func TestDamageParams_DmgFamily(t *testing.T) {
	srv := newTestServer(t, boundedDamageStore())
	defer srv.Close()

	base := srv.URL + "/v1/demos/gameId:42/damage"

	// Default, full log: dmg resolves to bounded — materialized into the raw
	// field names (bps.given comes from the nest, 80), dmg echo "bounded", no
	// per-player bounded nest, and no summary-only boundedSource on the full log.
	resp := getJSON(t, base, 200)
	if resp["dmg"] != "bounded" {
		t.Errorf("full default: dmg = %v, want bounded", resp["dmg"])
	}
	bps := resp["byPlayer"].(map[string]any)["bps"].(map[string]any)
	if int(bps["given"].(float64)) != 80 {
		t.Errorf("full default (bounded): bps.given = %v, want 80 (materialized)", bps["given"])
	}
	if _, ok := bps["bounded"]; ok {
		t.Errorf("full default (bounded): byPlayer.bps.bounded nest should be dropped")
	}
	if _, ok := resp["boundedSource"]; ok {
		t.Errorf("full-log response must not carry boundedSource (summary-only)")
	}

	// Default, summary: dmg resolves to bounded — materialized, events dropped,
	// and boundedSource present (the stub's demoInfo players carry no dmg block,
	// so the figures stay reconstructed).
	resp = getJSON(t, base+"?summary=1", 200)
	if resp["dmg"] != "bounded" {
		t.Errorf("summary default: dmg = %v, want bounded", resp["dmg"])
	}
	if resp["boundedSource"] != "reconstructed" {
		t.Errorf("summary default: boundedSource = %v, want reconstructed", resp["boundedSource"])
	}
	bps = resp["byPlayer"].(map[string]any)["bps"].(map[string]any)
	if int(bps["given"].(float64)) != 80 {
		t.Errorf("summary default (bounded): bps.given = %v, want 80 (materialized)", bps["given"])
	}

	// Explicit both on the full log keeps the bounded nest.
	resp = getJSON(t, base+"?dmg=both", 200)
	if resp["dmg"] != "both" {
		t.Errorf("dmg=both: dmg = %v, want both", resp["dmg"])
	}
	bps = resp["byPlayer"].(map[string]any)["bps"].(map[string]any)
	if bps["bounded"] == nil {
		t.Errorf("dmg=both: byPlayer.bps.bounded nest missing")
	}

	// Explicit raw on the full log strips the bounded additions (no dmg echo,
	// no per-player bounded nest).
	resp = getJSON(t, base+"?dmg=raw", 200)
	if _, ok := resp["dmg"]; ok {
		t.Errorf("dmg=raw: dmg should be absent, got %v", resp["dmg"])
	}
	bps = resp["byPlayer"].(map[string]any)["bps"].(map[string]any)
	if _, ok := bps["bounded"]; ok {
		t.Errorf("dmg=raw: byPlayer.bps.bounded should be stripped")
	}

	// Explicit bounded materializes: dmg echo "bounded", per-player figures come
	// from the nest (given 80), and the nest itself is dropped.
	resp = getJSON(t, base+"?dmg=bounded", 200)
	if resp["dmg"] != "bounded" {
		t.Errorf("dmg=bounded: dmg = %v, want bounded", resp["dmg"])
	}
	bps = resp["byPlayer"].(map[string]any)["bps"].(map[string]any)
	if int(bps["given"].(float64)) != 80 {
		t.Errorf("dmg=bounded: bps.given = %v, want 80 (materialized)", bps["given"])
	}
	if _, ok := bps["bounded"]; ok {
		t.Errorf("dmg=bounded: byPlayer.bps.bounded nest should be dropped")
	}

	// Unknown dmg is a clean 400 invalid_param.
	body, status := getRaw(t, base+"?dmg=nope")
	if status != 400 {
		t.Errorf("dmg=nope: status = %d, want 400 (body=%s)", status, string(body))
	}

	// dmg=bounded on a skipped:* demo is a 422 bounded_unavailable.
	body, status = getRaw(t, srv.URL+"/v1/demos/gameId:99/damage?dmg=bounded")
	if status != 422 {
		t.Fatalf("skipped dmg=bounded: status = %d, want 422 (body=%s)", status, string(body))
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("422 body decode: %v (body=%s)", err, string(body))
	}
	if env.Error.Code != "bounded_unavailable" {
		t.Errorf("422 code = %q, want bounded_unavailable", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "skipped:midair") {
		t.Errorf("422 message should name the boundedMode, got %q", env.Error.Message)
	}

	// Both/bounded on the skipped:* demo still serve (raw path unaffected).
	if _, status := getRaw(t, srv.URL+"/v1/demos/gameId:99/damage?dmg=both"); status != 200 {
		t.Errorf("skipped dmg=both: status = %d, want 200", status)
	}

	// A DEFAULTED request (no dmg param) on the skipped:* demo falls back to raw
	// instead of 422: 200, no dmg echo, and boundedMode explains the absence.
	resp = getJSON(t, srv.URL+"/v1/demos/gameId:99/damage", 200)
	if _, ok := resp["dmg"]; ok {
		t.Errorf("skipped defaulted: dmg should be absent (raw fallback), got %v", resp["dmg"])
	}
	if resp["boundedMode"] != "skipped:midair" {
		t.Errorf("skipped defaulted: boundedMode = %v, want skipped:midair", resp["boundedMode"])
	}
	// The defaulted summary on the skipped demo also falls back to raw.
	resp = getJSON(t, srv.URL+"/v1/demos/gameId:99/damage?summary=1", 200)
	if _, ok := resp["dmg"]; ok {
		t.Errorf("skipped defaulted summary: dmg should be absent (raw fallback), got %v", resp["dmg"])
	}
	if _, ok := resp["boundedSource"]; ok {
		t.Errorf("skipped defaulted summary: boundedSource should be absent (raw fallback)")
	}
}

// TestTimeBoundParams_Rejected400 pins the from/to/time validation: NaN, Inf,
// negatives, and values whose millisecond form overflows int32 must be a clean
// 400 invalid_param, not a silent all-filtered 200 (the bad float→int32
// conversion secToMs would otherwise perform).
func TestTimeBoundParams_Rejected400(t *testing.T) {
	srv := newTestServer(t, fragDamageStore())
	defer srv.Close()

	bad := []string{
		"frags?from=-1",
		"frags?to=-0.5",
		"frags?from=NaN",
		"frags?from=Inf",
		"frags?from=1e12", // ms overflows int32
		"damage?to=1e12",
	}
	for _, q := range bad {
		body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/"+q)
		if status != 400 {
			t.Errorf("%s: status = %d, want 400 (body=%s)", q, status, string(body))
		}
	}

	// A valid large-but-representable bound is still accepted (200).
	if _, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/frags?from=100"); status != 200 {
		t.Errorf("from=100: status = %d, want 200", status)
	}
}

func newTestServer(t *testing.T, store demoStore) *httptest.Server {
	t.Helper()
	return newTestServerMaps(t, store, "")
}

func newTestServerMaps(t *testing.T, store demoStore, mapsDir string) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(newRouter(store, logger, mapsDir, nil, nil))
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
	// A POST is not a cacheable resource: no ETag / Cache-Control (nit).
	if got := resp.Header.Get("ETag"); got != "" {
		t.Errorf("POST must not carry ETag, got %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "" {
		t.Errorf("POST must not carry Cache-Control, got %q", got)
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

func TestLOS(t *testing.T) {
	store := storeWithStub()
	srv := newTestServer(t, store)
	defer srv.Close()
	// The stub has no DemoInfo/BSP, so LOS computes to empty — but the
	// endpoint must still be wired, return 200, echo the players, and mark
	// the pass computed so a second request is a no-op.
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/los", 200)
	players, ok := resp["players"].([]any)
	if !ok || len(players) == 0 {
		t.Fatalf("expected players array, got %v", resp["players"])
	}
	p0 := players[0].(map[string]any)
	if p0["name"] != "bps" {
		t.Errorf("players[0].name = %v, want bps", p0["name"])
	}
	if _, has := p0["los"]; has {
		t.Errorf("expected no los on a BSP-less stub, got %v", p0["los"])
	}
	if !store.byID["gameId:42"].Streams.LOSComputed {
		t.Errorf("LOSComputed should latch after the first /los request")
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

// The position-derived field codes (view / hgt / lq / vel, schema
// v31/v32) must flow through the REST layer end-to-end: mvd-api passes
// `fields` straight to the view layer, so this guards that the API
// surfaces them (and still 400s an unknown code).
func TestStreamSlice_ViewVelocityFields(t *testing.T) {
	res := stubResult()
	res.Streams.Players[0].Position = &result.PositionTrack{
		T:   []int32{0, 100, 200, 300},
		X:   []float32{0, 10, 20, 30},
		Y:   []float32{0, 0, 0, 0},
		Z:   []float32{0, 0, 0, 0},
		VP:  []int16{0, 100, 200, 300},
		VYa: []int16{0, -100, -200, -300},
		VX:  []float32{100, 100, 100, 100},
		VY:  []float32{0, 0, 0, 0},
		VZ:  []float32{0, 0, 0, 0},
	}
	store := &fakeStore{byID: map[string]*result.Result{"gameId:42": res}}
	srv := newTestServer(t, store)
	defer srv.Close()

	// stream-slice: view + vel each project into their own sibling track,
	// and pos stays absent when not requested (clean break).
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/stream-slice?from=0&to=0.4&fields=view,vel&players=bps", 200)
	players, _ := resp["players"].([]any)
	if len(players) == 0 {
		t.Fatal("stream-slice returned no players")
	}
	p0 := players[0].(map[string]any)
	if _, ok := p0["view"]; !ok {
		t.Errorf("stream-slice missing view track: %v", p0)
	}
	if _, ok := p0["vel"]; !ok {
		t.Errorf("stream-slice missing vel track: %v", p0)
	}
	if _, ok := p0["pos"]; ok {
		t.Errorf("pos should be absent when only view/vel requested: %v", p0)
	}

	// state-at: view + vel surface as point objects on the player.
	st := getJSON(t, srv.URL+"/v1/demos/gameId:42/state-at?time=0.1&fields=view,vel&players=bps", 200)
	sp := st["players"].(map[string]any)["bps"].(map[string]any)
	if _, ok := sp["view"]; !ok {
		t.Errorf("state-at missing view: %v", sp)
	}
	if _, ok := sp["vel"]; !ok {
		t.Errorf("state-at missing vel: %v", sp)
	}

	// buckets accept the codes too (columnar default).
	getJSON(t, srv.URL+"/v1/demos/gameId:42/buckets?windowMs=100&fields=vel&players=bps", 200)

	// An unknown field code is rejected with 400 (no silent pass-through).
	if _, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/stream-slice?from=0&to=1&fields=bogus"); status != 400 {
		t.Errorf("unknown field status = %d; want 400", status)
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

func TestAirgibs(t *testing.T) {
	r := &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		TimelineAnalysis: &result.TimelineAnalysisResult{
			Airgibs: []result.AirgibEvent{{
				Time: 60000, Attacker: "bps", Victim: "milton", Height: 120, Damage: 110,
			}},
		},
	}
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": r}})
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/airgibs")
	if status != 200 {
		t.Fatalf("status = %d (%s)", status, body)
	}
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(arr) != 1 || arr[0]["attacker"] != "bps" {
		t.Errorf("airgibs = %s; want one bps hit", body)
	}
}

func TestAirgibs_EmptyWithoutBSP(t *testing.T) {
	// TimelineAnalysis present but no airgibs (no clip hull → no heights):
	// an empty list, not an error.
	r := &result.Result{
		SchemaVersion:    result.CurrentSchemaVersion,
		TimelineAnalysis: &result.TimelineAnalysisResult{},
	}
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": r}})
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/airgibs")
	if status != 200 || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("status = %d, body = %q; want 200 []", status, body)
	}
}

func TestAirgibs_Unavailable(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no TimelineAnalysis
	}}
	srv := newTestServer(t, store)
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/airgibs")
	if status != 422 {
		t.Errorf("status = %d; want 422 (%s)", status, resp)
	}
}

func TestShots(t *testing.T) {
	r := &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "bps", Weapon: "lg", Source: "beam", Hit: true, Victims: []string{"milton"}},
				{Time: 1500, Player: "milton", Weapon: "rl", Source: "sound"},
			},
			ByPlayer: []result.PlayerShots{{Player: "bps", Total: 1,
				ByWeapon: []result.WeaponShots{{Weapon: "lg", Shots: 1, Hits: 1, Accuracy: 1}}}},
		},
	}
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": r}})
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/shots", 200)
	if shots, _ := resp["shots"].([]any); len(shots) != 2 {
		t.Fatalf("len(shots) = %d; want 2 (%v)", len(shots), resp)
	}
	if byPlayer, _ := resp["byPlayer"].([]any); len(byPlayer) != 1 {
		t.Fatalf("len(byPlayer) = %d; want 1 (%v)", len(byPlayer), resp)
	}
}

func TestShots_Unavailable(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no Shots
	}}
	srv := newTestServer(t, store)
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/shots")
	if status != 422 {
		t.Errorf("status = %d; want 422 (%s)", status, resp)
	}
}

func TestAim(t *testing.T) {
	r := &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		Aim: &result.AimResult{Players: []result.PlayerAim{{
			Player: "bps", Team: "blue", Mode: "duel",
			Crosshair: &result.CrosshairSamples{
				T: []int32{1000}, Weapon: []string{"lg"},
				DYaw: []float32{1}, DPitch: []float32{-1},
				NYaw: []float32{0.5}, NPitch: []float32{-0.5},
				Dist: []float32{800}, Hit: []bool{true}, Target: []string{"milton"},
			},
		}}},
	}
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": r}})
	defer srv.Close()
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/aim", 200)
	if players, _ := resp["players"].([]any); len(players) != 1 {
		t.Fatalf("len(players) = %d; want 1 (%v)", len(players), resp)
	}
}

func TestAim_Unavailable(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no Aim
	}}
	srv := newTestServer(t, store)
	defer srv.Close()
	resp, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/aim")
	if status != 422 {
		t.Errorf("status = %d; want 422 (%s)", status, resp)
	}
}

// aimParamsFixture is TestAim's result with a second player and the raw
// Shots/Streams the windowed recompute reads.
func aimParamsFixture() *result.Result {
	track := func(name string, x float64) result.PlayerStream {
		return result.PlayerStream{
			Name: name,
			Position: &result.PositionTrack{
				T: []int32{0, 5000}, X: []float32{float32(x), float32(x)},
				Y: []float32{0, 0}, Z: []float32{0, 0}, VP: []int16{0, 0}, VYa: []int16{0, 0},
			},
		}
	}
	return &result.Result{
		SchemaVersion: result.CurrentSchemaVersion,
		Shots: &result.ShotsResult{
			Shots: []result.Shot{
				{Time: 1000, Player: "bps", Weapon: "lg", Hit: false},
				{Time: 20000, Player: "bps", Weapon: "lg", Hit: true},
			},
			ByPlayer: []result.PlayerShots{{Player: "bps"}, {Player: "milton"}},
		},
		Streams: &result.Streams{Players: []result.PlayerStream{track("bps", 0), track("milton", 1000)}},
		Aim: &result.AimResult{Players: []result.PlayerAim{
			{
				Player: "bps", Team: "blue", Mode: "duel",
				Weapons:   []result.WeaponAim{{Weapon: "lg", Shots: 2, Hits: 1}},
				Crosshair: &result.CrosshairSamples{T: []int32{1000, 20000}, Weapon: []string{"lg", "lg"}},
				LGRamp:    &result.LGRampSamples{Since: []int32{0, 0}},
			},
			{
				Player: "milton", Team: "red", Mode: "duel",
				Weapons:   []result.WeaponAim{{Weapon: "sg", Shots: 3}},
				Crosshair: &result.CrosshairSamples{T: []int32{500}},
			},
		}},
	}
}

// TestAimParams exercises the summary / players / from / malformed-from query
// params on the aim endpoint.
func TestAimParams(t *testing.T) {
	srv := newTestServer(t, &fakeStore{byID: map[string]*result.Result{"gameId:42": aimParamsFixture()}})
	defer srv.Close()

	// summary drops the crosshair + lgRamp blocks, keeps weapons.
	resp := getJSON(t, srv.URL+"/v1/demos/gameId:42/aim?summary=1", 200)
	players, _ := resp["players"].([]any)
	if len(players) != 2 {
		t.Fatalf("summary players = %d, want 2 (%v)", len(players), resp)
	}
	p0, _ := players[0].(map[string]any)
	if _, hasCH := p0["crosshair"]; hasCH {
		t.Errorf("summary kept crosshair block: %v", p0)
	}
	if _, hasRamp := p0["lgRamp"]; hasRamp {
		t.Errorf("summary kept lgRamp block: %v", p0)
	}
	if _, hasW := p0["weapons"]; !hasW {
		t.Errorf("summary dropped weapons: %v", p0)
	}

	// players=bps (no window) selects the stored bps aim only.
	resp = getJSON(t, srv.URL+"/v1/demos/gameId:42/aim?players=bps", 200)
	players, _ = resp["players"].([]any)
	if len(players) != 1 {
		t.Fatalf("players=bps returned %d players, want 1", len(players))
	}
	if p, _ := players[0].(map[string]any); p["player"] != "bps" {
		t.Errorf("players=bps returned %v, want bps", p["player"])
	}

	// from=15 recomputes: only bps's t=20s lg fire survives → 1 shot.
	resp = getJSON(t, srv.URL+"/v1/demos/gameId:42/aim?from=15", 200)
	players, _ = resp["players"].([]any)
	if len(players) != 1 {
		t.Fatalf("from=15 returned %d players, want 1 (only bps fired in window)", len(players))
	}
	p, _ := players[0].(map[string]any)
	weapons, _ := p["weapons"].([]any)
	if len(weapons) != 1 {
		t.Fatalf("from=15 bps weapons = %v, want 1 row", p["weapons"])
	}
	if w, _ := weapons[0].(map[string]any); w["shots"] != float64(1) {
		t.Errorf("from=15 windowed lg shots = %v, want 1", w["shots"])
	}

	// malformed from is a clean 400 invalid_param.
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/aim?from=banana")
	if status != 400 {
		t.Errorf("from=banana: status = %d, want 400 (body=%s)", status, string(body))
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

// --- CORS (F17) ---

func TestCORS_Preflight(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()

	paths := []string{
		"/v1/demos/gameId:42/overview",
		"/v1/maps/dm6/entities",
		"/v1/demos/gameId:42/artifacts/frag",
	}
	for _, path := range paths {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("OPTIONS %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("OPTIONS %s: status %d; want 204", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("OPTIONS %s: Allow-Origin = %q; want *", path, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
			t.Errorf("OPTIONS %s: Allow-Methods = %q; want it to include GET", path, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
			t.Errorf("OPTIONS %s: Allow-Headers = %q; want it to include Authorization", path, got)
		}
		if resp.Header.Get("Access-Control-Max-Age") == "" {
			t.Errorf("OPTIONS %s: missing Access-Control-Max-Age", path)
		}
		// requestID now wraps outside CORS, so even a preflight short-circuit
		// carries an id (FIX 4).
		if resp.Header.Get("X-Request-Id") == "" {
			t.Errorf("OPTIONS %s: preflight missing X-Request-Id", path)
		}
	}
}

func TestCORS_ActualGET(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/demos/gameId:42/overview", nil)
	req.Header.Set("Origin", "https://example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q; want *", got)
	}
	expose := resp.Header.Get("Access-Control-Expose-Headers")
	for _, h := range []string{"ETag", "X-Cache", "X-Schema-Version", "X-Request-Id"} {
		if !strings.Contains(expose, h) {
			t.Errorf("Expose-Headers %q missing %q", expose, h)
		}
	}
}

// --- 5xx hygiene + request id (F19) ---

func TestInternalError_GenericBodyWithRequestID(t *testing.T) {
	// An unclassified store error must not leak its text (it can embed cache
	// paths / upstream URLs); the client gets a generic body + the request id
	// and the real error goes to the log only.
	const secret = "write tier-1: /home/ops/.cache/qw-mvd/mvd/ab/deadbeef.mvd.gz: no space left"
	srv := newTestServer(t, &fakeStore{err: errors.New(secret)})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/demos/gameId:42/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d; want 500 (body=%s)", resp.StatusCode, string(body))
	}
	id := resp.Header.Get("X-Request-Id")
	if id == "" {
		t.Errorf("missing X-Request-Id header")
	}
	if strings.Contains(string(body), secret) || strings.Contains(string(body), "/home/ops") {
		t.Errorf("500 body leaked internal error text: %s", string(body))
	}
	if id != "" && !strings.Contains(string(body), id) {
		t.Errorf("500 body %q does not cite request id %q", string(body), id)
	}
}

// TestInternalError_ArtifactRoute pins the same hygiene on the generic
// artifact surface: a lazy-artifact store failure serves the generic 500,
// not the underlying error text.
func TestInternalError_ArtifactRoute(t *testing.T) {
	const secret = "compute los: bsp mmap failed at /var/lib/mvd/maps/dm3.bsp"
	srv := newTestServer(t, &fakeStore{err: errors.New(secret)})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/demos/gameId:42/artifacts/los")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d; want 500 (body=%s)", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), "/var/lib/mvd") {
		t.Errorf("artifact 500 body leaked internal error text: %s", string(body))
	}
	if id := resp.Header.Get("X-Request-Id"); id == "" || !strings.Contains(string(body), id) {
		t.Errorf("artifact 500 body %q does not cite request id %q", string(body), id)
	}
}

// TestUnavailable_NoETag pins the nit: a 422 error body must not carry the
// ETag that setCacheHeaders / setArtifactCacheHeaders set on the success
// path before the availability check runs (writeError strips it). Covers
// both the curated endpoint and the generic artifact endpoint.
func TestUnavailable_NoETag(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no DemoInfo → 422
	}}
	srv := newTestServer(t, store)
	defer srv.Close()

	for _, path := range []string{"/v1/demos/gameId:42/demoinfo", "/v1/demos/gameId:42/artifacts/demoinfo"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 422 {
			t.Fatalf("GET %s: status = %d; want 422", path, resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != "" {
			t.Errorf("GET %s: 422 response must not carry ETag, got %q", path, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: 422 Cache-Control = %q; want no-store", path, got)
		}
	}
}

// TestLOS_NoStreams pins the nit: a demo with Streams == nil must return
// {"players":[]}, not {"players":null} — on both the curated /los and the
// generic /artifacts/los route (they share losBody).
func TestLOS_NoStreams(t *testing.T) {
	store := &fakeStore{byID: map[string]*result.Result{
		"gameId:42": {SchemaVersion: result.CurrentSchemaVersion}, // no Streams
	}}
	srv := newTestServer(t, store)
	defer srv.Close()

	for _, path := range []string{"/v1/demos/gameId:42/los", "/v1/demos/gameId:42/artifacts/los"} {
		body, status := getRaw(t, srv.URL+path)
		if status != 200 {
			t.Fatalf("GET %s: status = %d; want 200 (body=%s)", path, status, body)
		}
		var out struct {
			Players *[]any `json:"players"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("GET %s: decode: %v", path, err)
		}
		if out.Players == nil {
			t.Errorf("GET %s: players is null; want []", path)
		} else if len(*out.Players) != 0 {
			t.Errorf("GET %s: players = %v; want empty", path, *out.Players)
		}
	}
}

// TestWeaponPickups_SourceValidated: source is an enum like loc/layout —
// a typo 400s instead of silently matching nothing.
func TestWeaponPickups_SourceValidated(t *testing.T) {
	srv := newTestServer(t, storeWithStub())
	defer srv.Close()
	body, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/weapon-pickups?source=backpak")
	if status != 400 {
		t.Fatalf("status = %d, want 400 for a bad source (%s)", status, body)
	}
	if !strings.Contains(string(body), "invalid_param") || !strings.Contains(string(body), "backpak") {
		t.Errorf("error must name the code and the bad value: %s", body)
	}
	for _, ok := range []string{"world", "backpack", "unknown", "WORLD", ""} {
		_, status := getRaw(t, srv.URL+"/v1/demos/gameId:42/weapon-pickups?source="+ok)
		if status != 200 {
			t.Errorf("source=%q: status = %d, want 200", ok, status)
		}
	}
}

// TestWeaponsAlias: phase 16.2 renamed the singular `weapon` CSV param to
// `weapons`; the old spelling stays accepted as a legacy alias and the
// canonical name wins when both are present.
func TestWeaponsAlias(t *testing.T) {
	srv := newTestServer(t, fragDamageStore())
	defer srv.Close()

	canonical := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapons=rl", 200)
	legacy := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapon=rl", 200)
	cb, _ := canonical["byWeapon"].(map[string]any)
	lb, _ := legacy["byWeapon"].(map[string]any)
	if len(cb) != 1 || cb["rl"] == nil {
		t.Errorf("weapons=rl byWeapon = %v, want only rl", cb)
	}
	if fmt.Sprintf("%v", cb) != fmt.Sprintf("%v", lb) {
		t.Errorf("weapons= and weapon= disagree: %v vs %v", cb, lb)
	}

	// Canonical wins when both are present.
	both := getJSON(t, srv.URL+"/v1/demos/gameId:42/damage?weapons=rl&weapon=tele", 200)
	bb, _ := both["byWeapon"].(map[string]any)
	if len(bb) != 1 || bb["rl"] == nil {
		t.Errorf("weapons=rl&weapon=tele byWeapon = %v, want only rl (weapons wins)", bb)
	}
}

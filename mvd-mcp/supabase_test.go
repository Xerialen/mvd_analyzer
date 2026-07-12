package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSupabase stands in for hub.quakeworld.nu Supabase. It captures
// the request URL so tests can assert on filter construction.
type fakeSupabase struct {
	srv      *httptest.Server
	lastURL  string
	respCode int
	respBody string
}

func newFakeSupabase(body string) *fakeSupabase {
	f := &fakeSupabase{respCode: 200, respBody: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastURL = r.URL.String()
		if r.Header.Get("apikey") == "" {
			http.Error(w, "missing apikey", 401)
			return
		}
		w.WriteHeader(f.respCode)
		w.Write([]byte(f.respBody))
	}))
	return f
}

func (f *fakeSupabase) Close() { f.srv.Close() }

func newTestSupabaseClient(srvURL string) *supabaseClient {
	c := newSupabaseClient(5 * time.Second)
	c.baseURL = srvURL
	return c
}

func TestSearch_NoFilters_DefaultsApplied(t *testing.T) {
	fs := newFakeSupabase(`[]`)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output = %T; want map[string]any", out)
	}
	if m["limit"].(int) != 20 {
		t.Errorf("default limit = %v; want 20", m["limit"])
	}
	if !strings.Contains(fs.lastURL, "limit=20") {
		t.Errorf("missing limit=20 in URL: %s", fs.lastURL)
	}
	if !strings.Contains(fs.lastURL, "order=timestamp.desc") {
		t.Errorf("missing order=timestamp.desc in URL: %s", fs.lastURL)
	}
	if !strings.Contains(fs.lastURL, "select=") {
		t.Errorf("missing select clause in URL: %s", fs.lastURL)
	}
}

func TestSearch_LimitCapped(t *testing.T) {
	fs := newFakeSupabase(`[]`)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{Limit: 999})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := out.(map[string]any)
	if m["limit"].(int) != 100 {
		t.Errorf("limit not capped: %v", m["limit"])
	}
}

func TestSearch_AllFilters(t *testing.T) {
	fs := newFakeSupabase(`[]`)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	in := SearchGamesInput{
		Players:  []string{"bps", "valla"},
		Teams:    []string{"Die", "okkis"},
		Map:      "dm6",
		Mode:     "4on4",
		Matchtag: "qwsl",
		From:     "2025-01-01",
		To:       "2025-12-31",
		Limit:    50,
		Offset:   100,
	}
	if _, err := c.Search(context.Background(), in); err != nil {
		t.Fatalf("Search: %v", err)
	}
	u := fs.lastURL

	checks := []string{
		"limit=50",
		"offset=100",
		// FTS encoded twice once for the value once for QueryEscape of "'"
		// — we just sanity-check the filter name appears for each player.
		"players_fts=fts.",
		"team_names=cs.",
		"map=eq.dm6",
		"mode=eq.4on4",
		"matchtag=ilike.",
		"timestamp=gte.2025-01-01",
		"timestamp=lte.2025-12-31",
	}
	for _, c := range checks {
		if !strings.Contains(u, c) {
			t.Errorf("URL missing %q: %s", c, u)
		}
	}
	// Each player should generate a filter.
	if strings.Count(u, "players_fts=fts.") != 2 {
		t.Errorf("expected 2 players_fts filters, got %d (%s)", strings.Count(u, "players_fts=fts."), u)
	}
	if strings.Count(u, "team_names=cs.") != 2 {
		t.Errorf("expected 2 team_names filters, got %d (%s)", strings.Count(u, "team_names=cs."), u)
	}
}

func TestSearch_PassesThroughGames(t *testing.T) {
	body := `[{"id":12345,"timestamp":"2025-06-01T10:00:00","mode":"4on4","map":"dm6","teams":[{"name":"Die","score":89},{"name":"okkis","score":76}],"players":["bps","milton","valla","vegetius"],"demo_sha256":"abc","demo_source_url":"https://example.com/x.mvd.gz"}]`
	fs := newFakeSupabase(body)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{Mode: "4on4"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	m := out.(map[string]any)
	if m["count"].(int) != 1 {
		t.Errorf("count = %v; want 1", m["count"])
	}
	games := m["games"].([]any)
	if len(games) != 1 {
		t.Fatalf("len(games) = %d; want 1", len(games))
	}
	row := games[0].(map[string]any)
	if row["id"].(float64) != 12345 {
		t.Errorf("id = %v; want 12345", row["id"])
	}
	if row["map"] != "dm6" {
		t.Errorf("map = %v; want dm6", row["map"])
	}
}

func TestSearch_PostgrestError(t *testing.T) {
	fs := newFakeSupabase(`{"message":"boom"}`)
	fs.respCode = 500
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	_, err := c.Search(context.Background(), SearchGamesInput{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not mention status: %v", err)
	}
}

func TestSearch_AuthHeaderSent(t *testing.T) {
	var sawApiKey, sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawApiKey = r.Header.Get("apikey")
		sawAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestSupabaseClient(srv.URL)

	if _, err := c.Search(context.Background(), SearchGamesInput{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sawApiKey == "" {
		t.Errorf("apikey header missing")
	}
	if !strings.HasPrefix(sawAuth, "Bearer ") {
		t.Errorf("Authorization Bearer missing: %q", sawAuth)
	}
}

// End-to-end: searchGames tool call goes through registerTools to a
// fake searcher and returns shape-correct JSON.
func TestMCP_SearchGames(t *testing.T) {
	body := `[{"id":777,"map":"dm6","mode":"1on1","timestamp":"2025-04-15T20:00:00","teams":[],"players":["bps","milton"],"demo_sha256":"d3","demo_source_url":""}]`
	fs := newFakeSupabase(body)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	sess := testMCPSessionWith(t, &fakeBackend{}, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "searchGames",
		Arguments: map[string]any{
			"map": "dm6", "mode": "1on1", "players": []any{"bps"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("isError; content=%+v", res.Content)
	}
	var out map[string]any
	mustDecodeStructured(t, res, &out)
	if out["count"].(float64) != 1 {
		t.Errorf("count = %v; want 1", out["count"])
	}
}

// realRosterRow mirrors the live v1_games shape (verified against the
// hub 2026-07-10): players carry ping / color arrays / name_color /
// team_color / is_bot alongside name+team+frags.
const realRosterRow = `[{"id":7,"timestamp":"2026-07-01T10:00:00","mode":"4on4","map":"schloss",
"teams":[{"name":"red","score":89}],
"players":[{"name":"bps","ping":25,"team":"red","color":[4,4],"frags":31,"is_bot":false,"name_color":"wwww","team_color":"ww"}],
"demo_sha256":"abc","demo_source_url":"https://example.com/x.mvd.gz"}]`

func TestSearch_CompactRosterDefault(t *testing.T) {
	fs := newFakeSupabase(realRosterRow)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	row := out.(map[string]any)["games"].([]any)[0].(map[string]any)
	pl := row["players"].([]any)[0].(map[string]any)
	for _, gone := range []string{"ping", "color", "is_bot", "name_color", "team_color"} {
		if _, ok := pl[gone]; ok {
			t.Errorf("compact roster still carries %q", gone)
		}
	}
	if pl["name"] != "bps" || pl["team"] != "red" || pl["frags"].(float64) != 31 {
		t.Errorf("compact roster lost identity fields: %+v", pl)
	}
	if row["teams"] == nil {
		t.Error("teams must survive compaction")
	}
}

func TestSearch_RosterOptInVerbatim(t *testing.T) {
	fs := newFakeSupabase(realRosterRow)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{Roster: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	pl := out.(map[string]any)["games"].([]any)[0].(map[string]any)["players"].([]any)[0].(map[string]any)
	if pl["ping"].(float64) != 25 || pl["name_color"] != "wwww" {
		t.Errorf("roster:true must pass hub rows through verbatim, got %+v", pl)
	}
}

// TestSearch_TotalFromContentRange: the Prefer count header is sent and
// the PostgREST Content-Range total surfaces as `total`; a 206 partial
// page is success.
func TestSearch_TotalFromContentRange(t *testing.T) {
	var seenPrefer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPrefer = r.Header.Get("Prefer")
		w.Header().Set("Content-Range", "0-19/1234")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestSupabaseClient(srv.URL)

	out, err := c.Search(context.Background(), SearchGamesInput{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if seenPrefer != "count=exact" {
		t.Errorf("Prefer = %q, want count=exact", seenPrefer)
	}
	if total := out.(map[string]any)["total"]; total != 1234 {
		t.Errorf("total = %v, want 1234", total)
	}
}

// TestSearch_NoContentRangeOmitsTotal: a hub that ignores the count
// preference yields a response without `total` (never a wrong one).
func TestSearch_NoContentRangeOmitsTotal(t *testing.T) {
	fs := newFakeSupabase(`[]`)
	defer fs.Close()
	c := newTestSupabaseClient(fs.srv.URL)
	out, err := c.Search(context.Background(), SearchGamesInput{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, ok := out.(map[string]any)["total"]; ok {
		t.Error("total present without a Content-Range header")
	}
}

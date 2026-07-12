package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// hub.quakeworld.nu Supabase. The anon key is public — it's the same
// one shipped in the web bundle (mvd-web/static/app.js).
const (
	supabaseURL    = "https://ncsphkjfominimxztjip.supabase.co/rest/v1/v1_games"
	supabaseAPIKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Im5jc3Boa2pmb21pbmlteHp0amlwIiwicm9sZSI6ImFub24iLCJpYXQiOjE2OTY5Mzg1NjMsImV4cCI6MjAxMjUxNDU2M30.NN6hjlEW-qB4Og9hWAVlgvUdwrbBO13s8OkAJuBGVbo"

	// Fields the search returns — mirrors the web's SEARCH_SELECT.
	supabaseSearchSelect = "id,timestamp,mode,matchtag,map,teams,players,demo_sha256,demo_source_url"
)

// searcher is the interface searchGames tool depends on, so tests can
// inject an httptest-faked Supabase.
type searcher interface {
	Search(ctx context.Context, in SearchGamesInput) (any, error)
}

// supabaseClient queries hub.quakeworld.nu's PostgREST surface
// directly. mvd-mcp uses this from the MCP shim so the search path
// doesn't route through mvd-api — discovery is the hub's
// responsibility, not ours, and the data is already public.
type supabaseClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newSupabaseClient(timeout time.Duration) *supabaseClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &supabaseClient{
		baseURL: supabaseURL,
		apiKey:  supabaseAPIKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// Search runs a hub search with the given filters. Returns the raw
// PostgREST response as `[]any` of game rows (each row is a
// map[string]any with the SEARCH_SELECT fields).
func (s *supabaseClient) Search(ctx context.Context, in SearchGamesInput) (any, error) {
	parts := []string{
		"select=" + url.QueryEscape(supabaseSearchSelect),
		"order=timestamp.desc",
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	parts = append(parts, "limit="+strconv.Itoa(limit))
	if in.Offset > 0 {
		parts = append(parts, "offset="+strconv.Itoa(in.Offset))
	}

	for _, p := range in.Players {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// FTS with apostrophe quoting, AND'd via repeated filters
		// (PostgREST's default semantics for repeats on one column).
		parts = append(parts, "players_fts=fts.'"+url.QueryEscape(p)+"'")
	}
	for _, t := range in.Teams {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// cs (contains) on the team_names text[] column.
		parts = append(parts, "team_names=cs.{"+url.QueryEscape(t)+"}")
	}
	if in.Map != "" {
		parts = append(parts, "map=eq."+url.QueryEscape(in.Map))
	}
	if in.Mode != "" {
		parts = append(parts, "mode=eq."+url.QueryEscape(in.Mode))
	}
	if in.Matchtag != "" {
		parts = append(parts, "matchtag=ilike.%25"+url.QueryEscape(in.Matchtag)+"%25")
	}
	if in.From != "" {
		parts = append(parts, "timestamp=gte."+url.QueryEscape(in.From))
	}
	if in.To != "" {
		// Match the web's behaviour: include the full end day.
		parts = append(parts, "timestamp=lte."+url.QueryEscape(in.To+"T23:59:59"))
	}

	full := s.baseURL + "?" + strings.Join(parts, "&")
	req, err := http.NewRequestWithContext(ctx, "GET", full, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.apiKey)
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("accept-profile", "public")
	// Ask PostgREST for the total match count (Content-Range: 0-19/1234)
	// so pagination is honest: `count` is rows-in-this-page, `total` is
	// all matching rows. With a count preference PostgREST may answer
	// 206 Partial Content for a partial page — that is success here.
	req.Header.Set("Prefer", "count=exact")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub search: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("hub search: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var games []any
	if err := json.Unmarshal(body, &games); err != nil {
		return nil, fmt.Errorf("hub search: decode: %w", err)
	}
	if !in.Roster {
		compactRosters(games)
	}
	out := map[string]any{
		"limit":  limit,
		"offset": in.Offset,
		"count":  len(games),
		"games":  games,
	}
	if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
		out["total"] = total
	}
	return out, nil
}

// compactRosters projects each game row's players array down to
// {name, team, frags} in place. The verbatim hub rows carry per-player
// ping, color arrays, name_color, team_color and is_bot — detail an
// agent picking demos never reads and that multiplies the payload ~4x
// (roster:true opts back in). Non-object entries pass through verbatim.
func compactRosters(games []any) {
	for _, g := range games {
		row, ok := g.(map[string]any)
		if !ok {
			continue
		}
		players, ok := row["players"].([]any)
		if !ok {
			continue
		}
		compact := make([]any, 0, len(players))
		for _, pl := range players {
			pm, ok := pl.(map[string]any)
			if !ok {
				compact = append(compact, pl)
				continue
			}
			c := make(map[string]any, 3)
			for _, k := range []string{"name", "team", "frags"} {
				if v, ok := pm[k]; ok {
					c[k] = v
				}
			}
			compact = append(compact, c)
		}
		row["players"] = compact
	}
}

// parseContentRangeTotal extracts the total from a PostgREST
// Content-Range header ("0-19/1234", or "*/0" for an empty result).
// ok=false when the header is absent or the total is unknown ("/*").
func parseContentRangeTotal(cr string) (int, bool) {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 {
		return 0, false
	}
	total, err := strconv.Atoi(cr[i+1:])
	if err != nil {
		return 0, false
	}
	return total, true
}

var _ searcher = (*supabaseClient)(nil)

// Package hubfetch resolves and downloads MVD demos from
// hub.quakeworld.nu by game ID. It mirrors the fetch flow already used
// by the web frontend (mvd-web/static/app.js, the SUPABASE_URL / CDN
// path): query the Supabase v1_games endpoint for the demo's sha256 +
// source URL, then try the public CDN before falling back to the
// original recording server.
//
// The Supabase URL and anon key are public (already shipped in the
// browser bundle) and authenticate read-only access to the public
// game catalog. There is no token rotation concern.
//
// Its consumers are the golden test harness
// (mvd-analytics/analyzer/golden_test.go) and mvd-api (via
// mvd-api/internal/democache). It is intentionally small and has no
// dependency on the analyzer or result packages so it can be reused for
// ad-hoc tooling (e.g. a future cache-warming CLI).
package hubfetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SupabaseURL is the v1_games REST endpoint backing hub.quakeworld.nu.
// The anon key below is the same one shipped in mvd-web/static/app.js.
const (
	SupabaseURL    = "https://ncsphkjfominimxztjip.supabase.co/rest/v1/v1_games"
	SupabaseAPIKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Im5jc3Boa2pmb21pbmlteHp0amlwIiwicm9sZSI6ImFub24iLCJpYXQiOjE2OTY5Mzg1NjMsImV4cCI6MjAxMjUxNDU2M30.NN6hjlEW-qB4Og9hWAVlgvUdwrbBO13s8OkAJuBGVbo"
	CDNBase        = "https://d.quake.world"
)

// ErrNotFound is returned by Resolve when the hub has no row for the
// requested gameId (an empty result set). Callers should detect it with
// errors.Is rather than matching the message, so a hub outage whose body
// merely contains "not found" is not misclassified as a 404. See
// democache's classifyHubError.
var ErrNotFound = errors.New("not found")

// GameInfo is the minimal subset of the Supabase row that the
// downloader needs. The real schema has many more fields (teams, mode,
// timestamp, …) — leave them off here so we don't need to track
// schema drift for fields we never read.
type GameInfo struct {
	ID            int    `json:"id"`
	DemoSHA256    string `json:"demo_sha256"`
	DemoSourceURL string `json:"demo_source_url"`
}

// Client is a small wrapper around http.Client so tests can swap in
// httptest.Server URLs.  Defaults work for production: 30 s timeout,
// the public Supabase + CDN bases.
type Client struct {
	HTTP        *http.Client
	SupabaseURL string // overrides const for testing
	CDNBase     string // overrides const for testing
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		SupabaseURL: SupabaseURL,
		CDNBase:     CDNBase,
	}
}

// Resolve looks up game metadata by hub gameId. It returns the
// minimal info needed for download. The Supabase REST API answers
// `?id=eq.N` with a JSON array of rows; an empty array means the game
// does not exist.
func (c *Client) Resolve(gameID int) (*GameInfo, error) {
	q := url.Values{}
	q.Set("select", "id,demo_sha256,demo_source_url")
	q.Set("id", "eq."+strconv.Itoa(gameID))
	req, err := http.NewRequest("GET", c.SupabaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", SupabaseAPIKey)
	req.Header.Set("Authorization", "Bearer "+SupabaseAPIKey)
	req.Header.Set("accept-profile", "public")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase resolve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase resolve: status %d: %s", resp.StatusCode, string(body))
	}

	var rows []GameInfo
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("supabase resolve: decode: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("game %d %w", gameID, ErrNotFound)
	}
	return &rows[0], nil
}

// Download fetches the MVD bytes for a resolved game. It tries the
// public CDN first (path: <cdnBase>/<sha[:3]>/<sha>.mvd.gz) and falls
// back to demo_source_url if the CDN copy is missing or unreachable.
func (c *Client) Download(info *GameInfo) ([]byte, error) {
	if info == nil {
		return nil, errors.New("nil GameInfo")
	}

	// Path 1: CDN, when we have a sha to address it.
	if len(info.DemoSHA256) >= 3 {
		cdnURL := fmt.Sprintf("%s/%s/%s.mvd.gz", c.CDNBase, info.DemoSHA256[:3], info.DemoSHA256)
		if data, err := c.fetch(cdnURL); err == nil {
			return data, nil
		}
		// Fall through to source on CDN miss / error.
	}

	// Path 2: original recording server.
	if info.DemoSourceURL != "" {
		data, err := c.fetch(info.DemoSourceURL)
		if err != nil {
			return nil, fmt.Errorf("source download: %w", err)
		}
		return data, nil
	}

	return nil, errors.New("no download URL available (no sha256 and no demo_source_url)")
}

func (c *Client) fetch(u string) ([]byte, error) {
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// proxyBackend implements MCPBackend by forwarding every tool call to
// a running mvd-api. Uses stdlib http.Client; one retry on transient
// transport failures and 502/503/504 statuses.
type proxyBackend struct {
	baseURL string
	label   string
	http    *http.Client
}

// newProxyBackend constructs a proxy backend. Empty label is fine —
// no Authorization header is sent.
func newProxyBackend(baseURL, label string, timeout time.Duration) *proxyBackend {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &proxyBackend{
		baseURL: strings.TrimRight(baseURL, "/"),
		label:   label,
		http:    &http.Client{Timeout: timeout},
	}
}

// proxyErrorPayload mirrors mvd-api's error envelope.
type proxyErrorPayload struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// proxyError carries the wire error code so callers can format it for
// MCP tool result content.
type proxyError struct {
	Status int
	Code   string
	Body   string
}

func (e *proxyError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("api %d %s: %s", e.Status, e.Code, e.Body)
	}
	return fmt.Sprintf("api %d: %s", e.Status, e.Body)
}

// do performs an HTTP call with one retry on net errors and transient
// 5xx responses. Body is decoded into out on 2xx; non-2xx returns
// *proxyError. Pass *any for opaque pass-through.
func (p *proxyBackend) do(ctx context.Context, method, path string, query url.Values, out any) error {
	full := p.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	attempt := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, full, nil)
		if err != nil {
			return nil, err
		}
		if p.label != "" {
			req.Header.Set("Authorization", "Bearer "+p.label)
		}
		return p.http.Do(req)
	}

	resp, err := attempt()
	if shouldRetry(resp, err) {
		_ = drainBody(resp)
		time.Sleep(500 * time.Millisecond)
		resp, err = attempt()
	}
	if err != nil {
		return fmt.Errorf("proxy %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	}

	pe := &proxyError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	var env proxyErrorPayload
	if json.Unmarshal(body, &env) == nil {
		pe.Code = env.Error.Code
		if env.Error.Message != "" {
			pe.Body = env.Error.Message
		}
	}
	return pe
}

func shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		var netErr net.Error
		if errors.As(err, &netErr) {
			return true
		}
		return true
	}
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func drainBody(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// fetchOpaque is a small helper for view-shaped tool calls: decode the
// response body into a generic JSON value so the MCP SDK can re-serialise
// it. mvd-api owns the shape; we just pass it through.
func (p *proxyBackend) fetchOpaque(ctx context.Context, method, path string, q url.Values) (any, error) {
	var out any
	if err := p.do(ctx, method, path, q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// fetchOpaqueList is fetchOpaque for the mvd-api endpoints whose body is
// a top-level JSON array (/chat, /backpacks, /weapon-pickups). The MCP
// SDK requires a tool's structuredContent to be a JSON object, so a bare
// array fails validation ("expected record, received array"). We wrap it
// under `key` here, at the MCP boundary, rather than reshaping the REST
// contract — bare-array bodies are valid HTTP and the array-only
// constraint is the MCP layer's. An already-object body (defensive, e.g.
// a future shape change or an error envelope) passes through untouched.
func (p *proxyBackend) fetchOpaqueList(ctx context.Context, method, path string, q url.Values, key string) (any, error) {
	var out any
	if err := p.do(ctx, method, path, q, &out); err != nil {
		return nil, err
	}
	if _, isObject := out.(map[string]any); isObject {
		return out, nil
	}
	if out == nil {
		out = []any{}
	}
	return map[string]any{key: out}, nil
}

// --- MCPBackend impl ---

func (p *proxyBackend) LoadDemo(ctx context.Context, in LoadDemoInput) (*LoadDemoOutput, error) {
	id, err := loadDemoToPathID(in)
	if err != nil {
		return nil, err
	}
	var out LoadDemoOutput
	if err := p.do(ctx, "POST", "/v1/demos/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func loadDemoToPathID(in LoadDemoInput) (string, error) {
	switch {
	case in.GameID > 0 && in.SHA256 == "":
		return "gameId:" + strconv.Itoa(in.GameID), nil
	case in.SHA256 != "" && in.GameID == 0:
		return "sha:" + strings.ToLower(in.SHA256), nil
	default:
		return "", errors.New("exactly one of gameId or sha256 must be set")
	}
}

func (p *proxyBackend) GetOverview(ctx context.Context, in GetOverviewInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/overview", nil)
}

func (p *proxyBackend) GetDemoInfo(ctx context.Context, in GetDemoInfoInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/demoinfo", nil)
}

func (p *proxyBackend) GetMetadata(ctx context.Context, in GetMetadataInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/metadata", nil)
}

func (p *proxyBackend) GetFrags(ctx context.Context, in GetFragsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Weapon) > 0 {
		q.Set("weapon", strings.Join(in.Weapon, ","))
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/frags", q)
}

func (p *proxyBackend) GetDamage(ctx context.Context, in GetDamageInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Weapon) > 0 {
		q.Set("weapon", strings.Join(in.Weapon, ","))
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/damage", q)
}

func (p *proxyBackend) GetLocGraph(ctx context.Context, in GetLocGraphInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/loc-graph", nil)
}

func (p *proxyBackend) GetChat(ctx context.Context, in GetChatInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if in.StartTime != 0 {
		q.Set("from", strconv.FormatFloat(in.StartTime, 'f', -1, 64))
	}
	if in.EndTime != 0 {
		q.Set("to", strconv.FormatFloat(in.EndTime, 'f', -1, 64))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Types) > 0 {
		q.Set("types", strings.Join(in.Types, ","))
	}
	return p.fetchOpaqueList(ctx, "GET", "/v1/demos/"+in.DemoID+"/chat", q, "messages")
}

func (p *proxyBackend) GetBackpacks(ctx context.Context, in GetBackpacksInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if in.Weapon != "" {
		q.Set("weapon", in.Weapon)
	}
	return p.fetchOpaqueList(ctx, "GET", "/v1/demos/"+in.DemoID+"/backpacks", q, "backpacks")
}

func (p *proxyBackend) GetItems(ctx context.Context, in GetItemsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if len(in.Items) > 0 {
		q.Set("items", strings.Join(in.Items, ","))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Kinds) > 0 {
		q.Set("kinds", strings.Join(in.Kinds, ","))
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/items", q)
}

func (p *proxyBackend) GetMapEntitiesByMap(ctx context.Context, in GetMapEntitiesByMapInput) (any, error) {
	if in.Map == "" {
		return nil, errors.New("map required")
	}
	q := url.Values{}
	if len(in.Types) > 0 {
		q.Set("types", strings.Join(in.Types, ","))
	}
	if len(in.Kinds) > 0 {
		q.Set("kinds", strings.Join(in.Kinds, ","))
	}
	return p.fetchOpaque(ctx, "GET", "/v1/maps/"+url.PathEscape(in.Map)+"/entities", q)
}

func (p *proxyBackend) GetWeaponPickups(ctx context.Context, in GetWeaponPickupsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Weapon) > 0 {
		q.Set("weapon", strings.Join(in.Weapon, ","))
	}
	if in.Source != "" {
		q.Set("source", in.Source)
	}
	return p.fetchOpaqueList(ctx, "GET", "/v1/demos/"+in.DemoID+"/weapon-pickups", q, "pickups")
}

func (p *proxyBackend) GetBuckets(ctx context.Context, in GetBucketsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	// MCP default: 1 s windows. The REST API still defaults to 50 ms
	// when omitted, but for the typical MCP consumer 50 ms emits ~24K
	// buckets / 4on4 — far too verbose for an LLM context. Explicit
	// override (windowMs: 50) reaches the finer resolution.
	windowMs := in.WindowMs
	if windowMs <= 0 {
		windowMs = 1000
	}
	q.Set("windowMs", strconv.Itoa(windowMs))
	if in.StartTime != 0 {
		q.Set("from", strconv.FormatFloat(in.StartTime, 'f', -1, 64))
	}
	if in.EndTime != 0 {
		q.Set("to", strconv.FormatFloat(in.EndTime, 'f', -1, 64))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Fields) > 0 {
		q.Set("fields", strings.Join(in.Fields, ","))
	}
	if len(in.Reducers) > 0 {
		pairs := make([]string, 0, len(in.Reducers))
		for k, v := range in.Reducers {
			pairs = append(pairs, k+"="+v)
		}
		q.Set("reducers", strings.Join(pairs, ","))
	}
	if in.IncludeTeam {
		q.Set("includeTeam", "1")
	}
	if in.Loc != "" {
		q.Set("loc", in.Loc)
	}
	if in.Layout != "" {
		q.Set("layout", in.Layout)
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/buckets", q)
}

func (p *proxyBackend) GetEvents(ctx context.Context, in GetEventsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if in.StartTime != 0 {
		q.Set("from", strconv.FormatFloat(in.StartTime, 'f', -1, 64))
	}
	if in.EndTime != 0 {
		q.Set("to", strconv.FormatFloat(in.EndTime, 'f', -1, 64))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Types) > 0 {
		q.Set("types", strings.Join(in.Types, ","))
	}
	if in.Loc != "" {
		q.Set("loc", in.Loc)
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/events", q)
}

func (p *proxyBackend) GetStreamSlice(ctx context.Context, in GetStreamSliceInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if in.StartTime != 0 {
		q.Set("from", strconv.FormatFloat(in.StartTime, 'f', -1, 64))
	}
	if in.EndTime != 0 {
		q.Set("to", strconv.FormatFloat(in.EndTime, 'f', -1, 64))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Fields) > 0 {
		q.Set("fields", strings.Join(in.Fields, ","))
	}
	if in.Loc != "" {
		q.Set("loc", in.Loc)
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/stream-slice", q)
}

func (p *proxyBackend) GetStateAt(ctx context.Context, in GetStateAtInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	q.Set("time", strconv.FormatFloat(in.Time, 'f', -1, 64))
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if len(in.Fields) > 0 {
		q.Set("fields", strings.Join(in.Fields, ","))
	}
	if in.Loc != "" {
		q.Set("loc", in.Loc)
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/state-at", q)
}

func (p *proxyBackend) GetLocTrails(ctx context.Context, in GetLocTrailsInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	if in.StartTime != 0 {
		q.Set("from", strconv.FormatFloat(in.StartTime, 'f', -1, 64))
	}
	if in.EndTime != 0 {
		q.Set("to", strconv.FormatFloat(in.EndTime, 'f', -1, 64))
	}
	if len(in.Players) > 0 {
		q.Set("players", strings.Join(in.Players, ","))
	}
	if in.MinDwellMs > 0 {
		q.Set("minDwellMs", strconv.Itoa(in.MinDwellMs))
	}
	if in.Loc != "" {
		q.Set("loc", in.Loc)
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/loc-trails", q)
}

func (p *proxyBackend) GetLocTable(ctx context.Context, in GetLocTableInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/loc-table", nil)
}

func (p *proxyBackend) GetRegionControl(ctx context.Context, in GetRegionControlInput) (any, error) {
	if in.DemoID == "" {
		return nil, errors.New("demoId required")
	}
	q := url.Values{}
	// Same MCP-vs-REST default split as GetBuckets — 1 s buckets are
	// the right granularity for an LLM reading region-control state
	// strings; pass windowMs explicitly to override.
	windowMs := in.WindowMs
	if windowMs <= 0 {
		windowMs = 1000
	}
	q.Set("windowMs", strconv.Itoa(windowMs))
	return p.fetchOpaque(ctx, "GET", "/v1/demos/"+in.DemoID+"/region-control", q)
}

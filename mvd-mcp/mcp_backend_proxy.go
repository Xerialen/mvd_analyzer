package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// demoIDRe accepts exactly the two canonical demo-id forms mvd-api's
// ParseDemoID accepts: "gameId:N" and "sha:HEX" (64 hex). Anything else —
// in particular a value containing '/', '?', or '#' — is rejected before
// it can be spliced into a proxy URL path.
var demoIDRe = regexp.MustCompile(`^(gameId:\d+|sha:[0-9a-fA-F]{64})$`)

// artifactNameRe accepts the kebab-case artifact names the DAG uses
// (lowercase letters, digits, hyphens). Validated before splicing into the
// proxy path so a malformed name can't reroute the request; the mvd-api
// closed registry does the authoritative "is this servable" check (404).
var artifactNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// demoPath builds the "/v1/demos/<id><suffix>" proxy path for a
// model-supplied demoID. It validates the id against the canonical forms
// and PathEscapes it, so a malicious or malformed id (e.g.
// "gameId:42/frags?players=x") cannot reroute the request to a different
// endpoint (F5). suffix is a fixed, trusted path tail like "/overview" or
// "". PathEscape leaves ':' intact, so a valid id is unchanged.
func demoPath(demoID, suffix string) (string, error) {
	if demoID == "" {
		return "", errors.New("demoId required")
	}
	if !demoIDRe.MatchString(demoID) {
		return "", fmt.Errorf("invalid demoId %q (want gameId:N or sha:HEX)", demoID)
	}
	return "/v1/demos/" + url.PathEscape(demoID) + suffix, nil
}

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
		// Retry any transport error except a caller-driven cancel/timeout: a
		// retry there would just race the same dead deadline. We deliberately
		// do not distinguish net.Error from other errors — a non-net error out
		// of http.Client.Do (e.g. a redirect-policy or body-read failure) is
		// still worth one more attempt, and the retry budget is small.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
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

// query is a url.Values with conditional setters that mirror the REST
// param encoding: each setter no-ops on its zero value, so an unset MCP
// input stays out of the query string and the REST default applies. set
// writes unconditionally, for the few always-present params (state-at
// time, the defaulted windowMs). Build with query{}, then convert to
// url.Values when handing it to do/fetchOpaque.
type query url.Values

func (q query) set(key, val string) { q[key] = []string{val} }

// csv joins a set as CSV, matching the REST parseCSV surface.
func (q query) csv(key string, vals []string) {
	if len(vals) > 0 {
		q.set(key, strings.Join(vals, ","))
	}
}

// seconds encodes a match-relative time; 0 means "unset" (as every REST
// from/to defaults to the full window).
func (q query) seconds(key string, sec float64) {
	if sec != 0 {
		q.set(key, secStr(sec))
	}
}

// summaryDefaultTrue resolves the MCP-layer summary default (D1,
// PLAN-api-usability): a caller who says nothing gets summary=true —
// token-lean by default; REST keeps its full-log default. Returns the
// resolved value plus whether it was defaulted (a defaulted summary
// response gets a hint so agents can self-serve the full data).
func summaryDefaultTrue(v *bool) (val, defaulted bool) {
	if v == nil {
		return true, true
	}
	return *v, false
}

// withSummaryHint annotates a summary-by-default response with how to
// get the dropped detail. Only fires when the caller didn't ask for the
// summary explicitly, and only on object bodies.
func withSummaryHint(out any, add bool, dropped string) any {
	if !add {
		return out
	}
	if m, ok := out.(map[string]any); ok {
		m["hint"] = "summary=true is the MCP default; pass summary:false for " + dropped
	}
	return out
}

// intv encodes a non-zero integer.
func (q query) intv(key string, n int) {
	if n != 0 {
		q.set(key, strconv.Itoa(n))
	}
}

// str encodes a non-empty string.
func (q query) str(key, val string) {
	if val != "" {
		q.set(key, val)
	}
}

// boolean encodes a true flag as "1"; false stays out of the query string so
// the REST default (false) applies.
func (q query) boolean(key string, v bool) {
	if v {
		q.set(key, "1")
	}
}

func secStr(sec float64) string { return strconv.FormatFloat(sec, 'f', -1, 64) }

// --- MCPBackend impl ---

func (p *proxyBackend) LoadDemo(ctx context.Context, in LoadDemoInput) (*LoadDemoOutput, error) {
	id, err := loadDemoToPathID(in)
	if err != nil {
		return nil, err
	}
	var out LoadDemoOutput
	// PathEscape the constructed id: the sha branch below lowercases the
	// model-supplied SHA256 but does not validate it, so escape it before it
	// reaches the URL path (F5). PathEscape leaves ':' intact.
	if err := p.do(ctx, "POST", "/v1/demos/"+url.PathEscape(id), nil, &out); err != nil {
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
	path, err := demoPath(in.DemoID, "/overview")
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) GetDemoInfo(ctx context.Context, in GetDemoInfoInput) (any, error) {
	path, err := demoPath(in.DemoID, "/demoinfo")
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) GetMetadata(ctx context.Context, in GetMetadataInput) (any, error) {
	path, err := demoPath(in.DemoID, "/metadata")
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) GetFrags(ctx context.Context, in GetFragsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/frags")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("players", in.Players)
	q.csv("weapons", in.Weapons)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.boolean("summary", in.Summary)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetDamage(ctx context.Context, in GetDamageInput) (any, error) {
	path, err := demoPath(in.DemoID, "/damage")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("players", in.Players)
	q.csv("weapons", in.Weapons)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	summary, defaulted := summaryDefaultTrue(in.Summary)
	q.boolean("summary", summary)
	out, err := p.fetchOpaque(ctx, "GET", path, url.Values(q))
	return withSummaryHint(out, defaulted && summary, "the per-hit events log"), err
}

func (p *proxyBackend) GetAim(ctx context.Context, in GetAimInput) (any, error) {
	path, err := demoPath(in.DemoID, "/aim")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("players", in.Players)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	summary, defaulted := summaryDefaultTrue(in.Summary)
	q.boolean("summary", summary)
	out, err := p.fetchOpaque(ctx, "GET", path, url.Values(q))
	return withSummaryHint(out, defaulted && summary, "the per-fire crosshair + lgRamp arrays"), err
}

func (p *proxyBackend) GetLocGraph(ctx context.Context, in GetLocGraphInput) (any, error) {
	path, err := demoPath(in.DemoID, "/loc-graph")
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) GetChat(ctx context.Context, in GetChatInput) (any, error) {
	path, err := demoPath(in.DemoID, "/chat")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.csv("players", in.Players)
	q.csv("types", in.Types)
	return p.fetchOpaqueList(ctx, "GET", path, url.Values(q), "messages")
}

func (p *proxyBackend) GetBackpacks(ctx context.Context, in GetBackpacksInput) (any, error) {
	path, err := demoPath(in.DemoID, "/backpacks")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("players", in.Players)
	q.csv("weapons", in.Weapons)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	return p.fetchOpaqueList(ctx, "GET", path, url.Values(q), "backpacks")
}

func (p *proxyBackend) GetItems(ctx context.Context, in GetItemsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/items")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("items", in.Items)
	q.csv("players", in.Players)
	q.csv("kinds", in.Kinds)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	summary, defaulted := summaryDefaultTrue(in.Summary)
	q.boolean("summary", summary)
	out, err := p.fetchOpaque(ctx, "GET", path, url.Values(q))
	return withSummaryHint(out, defaulted && summary, "the full phase timeline"), err
}

func (p *proxyBackend) GetMapEntitiesByMap(ctx context.Context, in GetMapEntitiesByMapInput) (any, error) {
	if in.Map == "" {
		return nil, errors.New("map required")
	}
	q := query{}
	q.csv("types", in.Types)
	q.csv("kinds", in.Kinds)
	return p.fetchOpaque(ctx, "GET", "/v1/maps/"+url.PathEscape(in.Map)+"/entities", url.Values(q))
}

func (p *proxyBackend) GetWeaponPickups(ctx context.Context, in GetWeaponPickupsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/weapon-pickups")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.csv("players", in.Players)
	q.csv("weapons", in.Weapons)
	q.str("source", in.Source)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	return p.fetchOpaqueList(ctx, "GET", path, url.Values(q), "pickups")
}

func (p *proxyBackend) GetBuckets(ctx context.Context, in GetBucketsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/buckets")
	if err != nil {
		return nil, err
	}
	// MCP default: 5 s windows. The REST API still defaults to 50 ms when
	// omitted, but 50 ms emits ~24K buckets / 4on4 — far too verbose for an
	// LLM context — and even 1 s is ~1200 buckets per field per player on a
	// 20-min match. 5 s resolves everything a bucketed timeline answers
	// (trends, control; the shortest interesting run — a quad — is 30 s);
	// finer questions belong to getStateAt / getEvents / getStreamSlice.
	// Explicit override (windowMs: 1000, 50, ...) reaches finer resolution.
	windowMs := in.WindowMs
	if windowMs <= 0 {
		windowMs = 5000
	}
	q := query{}
	q.intv("windowMs", windowMs)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.csv("players", in.Players)
	q.csv("fields", in.Fields)
	if len(in.Reducers) > 0 {
		pairs := make([]string, 0, len(in.Reducers))
		for k, v := range in.Reducers {
			pairs = append(pairs, k+"="+v)
		}
		q.set("reducers", strings.Join(pairs, ","))
	}
	if in.IncludeTeam {
		q.set("includeTeam", "1")
	}
	q.str("loc", in.Loc)
	q.str("layout", in.Layout)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetEvents(ctx context.Context, in GetEventsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/events")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.csv("players", in.Players)
	q.csv("types", in.Types)
	q.str("loc", in.Loc)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetStreamSlice(ctx context.Context, in GetStreamSliceInput) (any, error) {
	// MCP-layer size guard (same family as the windowMs / summary
	// defaults): an unwindowed slice is native-rate change entries for
	// every requested field of every player over the whole match — the
	// biggest payload this service can emit, and never what an agent
	// wants blind. REST /stream-slice stays unwindowed for programs.
	if in.StartTime == 0 && in.EndTime == 0 {
		return nil, errors.New("stream-slice needs a time window at the MCP layer: pass startTime and/or endTime (match-relative seconds; keep windows tens of seconds). For whole-match overviews use getBuckets; for one instant use getStateAt")
	}
	path, err := demoPath(in.DemoID, "/stream-slice")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.csv("players", in.Players)
	q.csv("fields", in.Fields)
	q.str("loc", in.Loc)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetStateAt(ctx context.Context, in GetStateAtInput) (any, error) {
	path, err := demoPath(in.DemoID, "/state-at")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.set("time", secStr(in.Time)) // required — always sent, even for time=0
	q.csv("players", in.Players)
	q.csv("fields", in.Fields)
	q.str("loc", in.Loc)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetLocTrails(ctx context.Context, in GetLocTrailsInput) (any, error) {
	path, err := demoPath(in.DemoID, "/loc-trails")
	if err != nil {
		return nil, err
	}
	q := query{}
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	q.csv("players", in.Players)
	// MCP default: 250 ms dwell filter. Raw trails are dominated by
	// nearest-loc flicker at region boundaries; an agent reading a
	// residence list wants where the player WAS, not every boundary
	// graze. Explicit 0 opts back into the raw stream (REST default).
	minDwell := 250
	if in.MinDwellMs != nil {
		minDwell = *in.MinDwellMs
	}
	if minDwell > 0 {
		q.set("minDwellMs", strconv.Itoa(minDwell))
	}
	q.str("loc", in.Loc)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

func (p *proxyBackend) GetLocTable(ctx context.Context, in GetLocTableInput) (any, error) {
	path, err := demoPath(in.DemoID, "/loc-table")
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) ListArtifacts(ctx context.Context, _ ListArtifactsInput) (any, error) {
	out, err := p.fetchOpaque(ctx, "GET", "/v1/artifacts", nil)
	if err != nil {
		return nil, err
	}
	return compactArtifactManifest(out), nil
}

// compactArtifactManifest trims the REST manifest to what an MCP agent
// can act on: fetchable artifacts and the fields that matter for
// picking one (name, resultKey, cost, lazy, description). The
// requires/provides/mutates edges describe pipeline wiring — the dev
// story, told by ARTIFACTS.md and /v1/graph — and non-servable nodes
// cannot be fetched at all, so both are noise at this surface (same
// MCP-vs-REST split as the summary/windowMs defaults). Unexpected
// shapes pass through untouched.
func compactArtifactManifest(out any) any {
	m, ok := out.(map[string]any)
	if !ok {
		return out
	}
	arts, ok := m["artifacts"].([]any)
	if !ok {
		return out
	}
	compact := make([]any, 0, len(arts))
	for _, a := range arts {
		row, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if servable, _ := row["servable"].(bool); !servable {
			continue
		}
		c := make(map[string]any, 5)
		for _, k := range []string{"name", "resultKey", "cost", "lazy", "description"} {
			if v, ok := row[k]; ok {
				c[k] = v
			}
		}
		compact = append(compact, c)
	}
	m["artifacts"] = compact
	return m
}

func (p *proxyBackend) GetArtifact(ctx context.Context, in GetArtifactInput) (any, error) {
	if in.Name == "" {
		return nil, errors.New("name required")
	}
	if !artifactNameRe.MatchString(in.Name) {
		return nil, fmt.Errorf("invalid artifact name %q", in.Name)
	}
	path, err := demoPath(in.DemoID, "/artifacts/"+url.PathEscape(in.Name))
	if err != nil {
		return nil, err
	}
	return p.fetchOpaque(ctx, "GET", path, nil)
}

func (p *proxyBackend) GetRegionControl(ctx context.Context, in GetRegionControlInput) (any, error) {
	path, err := demoPath(in.DemoID, "/region-control")
	if err != nil {
		return nil, err
	}
	// Same MCP-vs-REST default split as GetBuckets — 5 s buckets keep the
	// per-region bucketStates strings readable (a 20-min match is 240
	// chars per region instead of 1200); pass windowMs explicitly to
	// override.
	windowMs := in.WindowMs
	if windowMs <= 0 {
		windowMs = 5000
	}
	q := query{}
	q.intv("windowMs", windowMs)
	q.seconds("from", in.StartTime)
	q.seconds("to", in.EndTime)
	return p.fetchOpaque(ctx, "GET", path, url.Values(q))
}

package main

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// ciGet returns a query value by case-insensitive key. The documented
// canonical spelling is camelCase (windowMs, minDwellMs, includeTeam), but
// windowms / WindowMs / etc. resolve too, so consumers never trip on the
// casing of a parameter name. The exact-case hit is preferred; otherwise
// the first key that matches case-insensitively wins.
func ciGet(q url.Values, key string) string {
	if v := q.Get(key); v != "" {
		return v
	}
	lk := strings.ToLower(key)
	for k, vs := range q {
		if len(vs) > 0 && strings.ToLower(k) == lk {
			return vs[0]
		}
	}
	return ""
}

// parseCSV splits a comma-separated query parameter, trimming spaces
// and dropping empty entries.
func parseCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseFloat parses a query-string number. Empty → default.
func parseFloat(q url.Values, key string, defaultVal float64) (float64, error) {
	v := ciGet(q, key)
	if v == "" {
		return defaultVal, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q", key, v)
	}
	return f, nil
}

// parseInt parses a query-string integer. Empty → default.
func parseInt(q url.Values, key string, defaultVal int) (int, error) {
	v := ciGet(q, key)
	if v == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q", key, v)
	}
	return n, nil
}

// parseBool parses 0/1 or true/false. Empty → false.
func parseBool(q url.Values, key string) bool {
	switch strings.ToLower(ciGet(q, key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseLocIndex reads ?loc=name|index. Empty or "name" → false
// (resolved loc names, the default); "index" → true (raw LocTable
// indices, decode via /loc-table). Any other value is an error.
func parseLocIndex(q url.Values) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(ciGet(q, "loc"))) {
	case "", "name", "names":
		return false, nil
	case "index", "indices", "li":
		return true, nil
	default:
		return false, fmt.Errorf("invalid loc=%q (want 'name' or 'index')", ciGet(q, "loc"))
	}
}

// parseLayout reads ?layout=row|column. Empty → "column" (the compact
// column-major ColumnarBuckets, the default); "row" → the bucket-major
// BucketsView. Any other value is an error.
func parseLayout(q url.Values) (string, error) {
	switch strings.ToLower(strings.TrimSpace(ciGet(q, "layout"))) {
	case "", "column", "columnar":
		return "column", nil
	case "row":
		return "row", nil
	default:
		return "", fmt.Errorf("invalid layout=%q (want 'row' or 'column')", ciGet(q, "layout"))
	}
}

// parseReducers parses a comma-separated list of "field=name" pairs.
// Empty → nil. Malformed → error.
func parseReducers(v string) (map[string]string, error) {
	if v == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(v, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 || eq == len(kv)-1 {
			return nil, fmt.Errorf("invalid reducer pair %q (want 'field=name')", kv)
		}
		out[strings.TrimSpace(kv[:eq])] = strings.TrimSpace(kv[eq+1:])
	}
	return out, nil
}

// qp is an error-accumulating reader over a request's url.Values. Each
// accessor no-ops once an error has been recorded, so a handler does all
// its reads into the view-options struct and then checks Err() once — the
// first malformed param wins, exactly as the old sequential parse-then-
// check chain did. Keys resolve case-insensitively (the accessors funnel
// through the same ciGet/parse* helpers used elsewhere), so the field
// order of the options literal determines which of several bad params is
// reported; keep it matching the historical read order.
type qp struct {
	q   url.Values
	err error
}

func newQP(q url.Values) *qp { return &qp{q: q} }

// Err returns the first param-read error, or nil. Handlers pass it to
// writeInvalidParam for the shared 400 invalid_param tail.
func (p *qp) Err() error { return p.err }

// maxSecBound is the largest match-relative seconds bound the view layer can
// represent: secToMs rounds sec*1000 to int32 ms, so a larger value would wrap
// under Go's implementation-defined out-of-range float→int32 conversion and
// silently filter everything with an HTTP 200 instead of erroring.
const maxSecBound = float64(math.MaxInt32) / 1000.0

// Sec reads a match-relative seconds bound (from/to/time; empty → def) and
// validates it. NaN/Inf, negatives, and values whose millisecond form
// overflows int32 are rejected here rather than reaching the view's secToMs,
// where the bad float→int32 conversion would produce a silent all-filtered 200.
// No-op after a prior error.
func (p *qp) Sec(key string, def float64) float64 {
	if p.err != nil {
		return def
	}
	v, err := parseFloat(p.q, key, def)
	if err != nil {
		p.err = err
		return def
	}
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		p.err = fmt.Errorf("invalid %s=%q (not a finite number)", key, ciGet(p.q, key))
		return def
	case v < 0:
		p.err = fmt.Errorf("invalid %s=%q (must be >= 0)", key, ciGet(p.q, key))
		return def
	case v > maxSecBound:
		p.err = fmt.Errorf("invalid %s=%q (exceeds the maximum match time)", key, ciGet(p.q, key))
		return def
	}
	return v
}

// Int reads an integer param (empty → def). No-op after a prior error.
func (p *qp) Int(key string, def int) int {
	if p.err != nil {
		return def
	}
	v, err := parseInt(p.q, key, def)
	if err != nil {
		p.err = err
	}
	return v
}

// CSV reads a comma-separated param. It cannot fail, so it never sets err.
func (p *qp) CSV(key string) []string { return parseCSV(ciGet(p.q, key)) }

// Bool reads a 0/1|true/false param. It cannot fail, so it never sets err.
func (p *qp) Bool(key string) bool { return parseBool(p.q, key) }

// LocIndex reads ?loc=name|index. No-op after a prior error.
func (p *qp) LocIndex() bool {
	if p.err != nil {
		return false
	}
	v, err := parseLocIndex(p.q)
	if err != nil {
		p.err = err
	}
	return v
}

// Layout reads ?layout=row|column (empty → "column"). No-op after error.
func (p *qp) Layout() string {
	if p.err != nil {
		return "column"
	}
	v, err := parseLayout(p.q)
	if err != nil {
		p.err = err
	}
	return v
}

// Reducers reads the "field=name,..." reducer-override param. No-op after
// a prior error.
func (p *qp) Reducers(key string) map[string]string {
	if p.err != nil {
		return nil
	}
	v, err := parseReducers(ciGet(p.q, key))
	if err != nil {
		p.err = err
	}
	return v
}

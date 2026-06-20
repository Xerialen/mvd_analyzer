package main

import (
	"fmt"
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

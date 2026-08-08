package interpolate

import (
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// rangeParams returns a 1-hour range (09:00–10:00 UTC on 2026-08-08) with a
// fixed 1-minute interval and no maxDataPoints recalculation, so macro outputs
// are deterministic.
func rangeParams() Params {
	return Params{
		TimeRange: backend.TimeRange{
			From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
		},
		Interval: time.Minute,
	}
}

func TestInterpolate_Macros(t *testing.T) {
	p := rangeParams()
	const (
		from = "2026-08-08 09:00:00"
		to   = "2026-08-08 10:00:00"
	)
	td := func(s string) string { return "TO_DATE('" + s + "','YYYY-MM-DD HH24:MI:SS')" }

	cases := []struct{ name, sql, want string }{
		{"from epoch ms", "SELECT $__from FROM DUAL", "SELECT 1786179600000 FROM DUAL"},
		{"to epoch ms", "SELECT $__to FROM DUAL", "SELECT 1786183200000 FROM DUAL"},
		{"timeFilter", "SELECT * FROM t WHERE $__timeFilter(at)",
			"SELECT * FROM t WHERE at >= " + td(from) + " AND at <= " + td(to)},
		{"timeFrom", "SELECT * FROM t WHERE $__timeFrom(at)",
			"SELECT * FROM t WHERE at >= " + td(from)},
		{"timeTo", "SELECT * FROM t WHERE $__timeTo(at)",
			"SELECT * FROM t WHERE at <= " + td(to)},
		{"unixEpochFilter", "SELECT * FROM t WHERE $__unixEpochFilter(ts)",
			"SELECT * FROM t WHERE ts >= 1786179600 AND ts <= 1786183200"},
		{"unixEpochGroup 5m", "SELECT $__unixEpochGroup(ts, 5m) AS b FROM t",
			"SELECT FLOOR(ts / 300) * 300 AS b FROM t"},
		{"unixEpochGroup quoted", "SELECT $__unixEpochGroup(ts, '5m') AS b FROM t",
			"SELECT FLOOR(ts / 300) * 300 AS b FROM t"},
		{"unixEpochGroup bare", "SELECT $__unixEpochGroup(ts, 300) AS b FROM t",
			"SELECT FLOOR(ts / 300) * 300 AS b FROM t"},
		{"interval", "SELECT $__interval FROM t", "SELECT 1m FROM t"},
		{"interval_ms", "SELECT $__interval_ms FROM t", "SELECT 60000 FROM t"},
		{"interval_s", "SELECT $__interval_s FROM t", "SELECT 60 FROM t"},
		{"time col", "SELECT $__time(at) FROM t", "SELECT at FROM t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Interpolate(tc.sql, p)
			if err != nil {
				t.Fatalf("Interpolate(%q): %v", tc.sql, err)
			}
			if got != tc.want {
				t.Errorf("Interpolate(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

func TestInterpolate_MacroErrors(t *testing.T) {
	p := rangeParams()
	cases := []struct{ name, sql, want string }{
		{"timeFilter no args", "SELECT * FROM t WHERE $__timeFilter", "unexpected number of arguments"},
		{"timeFilter too many", "SELECT * FROM t WHERE $__timeFilter(a, b)", "unexpected number of arguments"},
		{"timeGroup unsupported", "SELECT $__timeGroup(x, month) FROM t", "$__timeGroup is not supported for Oracle"},
		{"table unsupported", "SELECT * FROM $__table", "$__table is not supported"},
		{"column unsupported", "SELECT $__column FROM t", "$__column is not supported"},
		{"unixEpochGroup bad interval", "SELECT $__unixEpochGroup(ts, foo) FROM t", "invalid interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Interpolate(tc.sql, p)
			if err == nil {
				t.Fatalf("Interpolate(%q) = nil, want error", tc.sql)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestInterpolate_Guards(t *testing.T) {
	p := rangeParams()
	cases := []struct{ name, sql, want string }{
		{"variable in macro arg", "SELECT * FROM t WHERE $__timeFilter(${col})",
			"variables inside macro arguments"},
		{"unknown macro lowercase", "SELECT * FROM t WHERE $__timefilter(at)",
			"unknown macro"},
		{"unknown macro", "SELECT * FROM t WHERE $__bogus(at)",
			"unknown macro"},
		{"unterminated variable", "SELECT ${broken",
			"malformed variable"},
		{"bare dollar brace", "SELECT ${ FROM t",
			"malformed variable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Interpolate(tc.sql, p)
			if err == nil {
				t.Fatalf("Interpolate(%q) = nil, want error", tc.sql)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestInterpolate_Variables(t *testing.T) {
	scoped := map[string]any{
		"host":     []any{"web-1", "web-2"},
		"empty":    []any{},
		"name":     "web-1",
		"num":      "42",
		"dec":      "3.14",
		"neg":      "-5",
		"fraction": ".5",
		"exp":      "1e5",
		"evil":     "5) OR 1=1 --",
		"squote":   "O'Brien",
		"nilvar":   nil,
		"intv":     int64(7),
		"floatv":   float64(3.5),
		"truev":    true,
		"falsev":   false,
		"inject":   "$__timeFilter(ts)",
		"other":    "${other}",
	}
	// dec/neg/fraction/exp/evil exercise the numericVariables hint; num is
	// all-digits (bare even without the hint).
	numeric := map[string]bool{"num": true, "dec": true, "neg": true, "fraction": true, "exp": true, "evil": true}
	p := Params{ScopedVars: scoped, NumericVars: numeric}

	cases := []struct{ name, sql, want string }{
		{"plain string", "SELECT ${name}", "SELECT 'web-1'"},
		{"all-digits bare", "SELECT ${num}", "SELECT 42"},
		{"decimal bare with hint", "SELECT ${dec}", "SELECT 3.14"},
		{"negative bare with hint", "SELECT ${neg}", "SELECT -5"},
		{"fraction bare with hint", "SELECT ${fraction}", "SELECT .5"},
		{"exponent not bare", "SELECT ${exp}", "SELECT '1e5'"},
		{"injection quoted", "SELECT ${evil}", "SELECT '5) OR 1=1 --'"},
		{"quote escaped", "SELECT ${squote}", "SELECT 'O''Brien'"},
		{"nil to NULL", "SELECT ${nilvar}", "SELECT NULL"},
		{"csv array", "SELECT ${host:csv}", "SELECT 'web-1', 'web-2'"},
		{"csv empty to NULL", "SELECT ${empty:csv}", "SELECT NULL"},
		{"singlequote array", "SELECT ${host:singlequote}", "SELECT 'web-1', 'web-2'"},
		{"singlequote scalar", "SELECT ${name:singlequote}", "SELECT 'web-1'"},
		{"int64 bare", "SELECT ${intv}", "SELECT 7"},
		{"float64 bare", "SELECT ${floatv}", "SELECT 3.5"},
		{"bool true", "SELECT ${truev}", "SELECT 1"},
		{"bool false", "SELECT ${falsev}", "SELECT 0"},
		// A value that looks like a macro or variable is inserted as a quoted
		// string literal and never re-expanded (single-pass, §15).
		{"value with macro literal", "SELECT ${inject}", "SELECT '$__timeFilter(ts)'"},
		{"value with var literal", "SELECT ${other}", "SELECT '${other}'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Interpolate(tc.sql, p)
			if err != nil {
				t.Fatalf("Interpolate(%q): %v", tc.sql, err)
			}
			if got != tc.want {
				t.Errorf("Interpolate(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// TestInterpolate_NumericHintRequired: without the numericVariables hint (or an
// all-integer value) a fractional value stays quoted — the hint only permits
// *trying* bare, it never grants it (§8.2).
func TestInterpolate_NumericHintRequired(t *testing.T) {
	p := Params{ScopedVars: map[string]any{"dec": "3.14", "num": "42"}}
	got, err := Interpolate("SELECT ${dec}, ${num}", p)
	if err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	if want := "SELECT '3.14', 42"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInterpolate_VariableErrors(t *testing.T) {
	scoped := map[string]any{
		"host":   []any{"web-1"},
		"allvar": "$__all",
	}
	p := Params{ScopedVars: scoped}

	cases := []struct{ name, sql, want string }{
		{"unresolved", "SELECT ${missing} FROM DUAL", "unresolved variable: ${missing}"},
		{"all selected", "SELECT ${allvar}", "has 'All' selected"},
		{"array plain", "SELECT ${host}", "use ${host:csv} for multi-value"},
		{"unknown format", "SELECT ${host:raw}", "unsupported variable format :raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Interpolate(tc.sql, p)
			if err == nil {
				t.Fatalf("Interpolate(%q) = nil, want error", tc.sql)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

// TestInterpolate_AlertingContext is the F3 DoD: a query evaluated server-side
// (no scopedVars, Grafana's alert defaults IntervalMs=1000ms,
// maxDataPoints=43200) resolves $__timeFilter from the TimeRange and
// $__interval from the derived bucketing interval; a ${var} errors loudly
// instead of failing silently (§8.3).
func TestInterpolate_AlertingContext(t *testing.T) {
	p := Params{
		TimeRange: backend.TimeRange{
			From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 8, 8, 15, 0, 0, 0, time.UTC),
		},
		Interval:      time.Second,
		MaxDataPoints: 43200,
		// no ScopedVars — alert rules bypass the frontend
	}

	got, err := Interpolate("SELECT COUNT(*) FROM t WHERE $__timeFilter(at)", p)
	if err != nil {
		t.Fatalf("timeFilter: %v", err)
	}
	want := "SELECT COUNT(*) FROM t WHERE at >= TO_DATE('2026-08-08 09:00:00','YYYY-MM-DD HH24:MI:SS') AND at <= TO_DATE('2026-08-08 15:00:00','YYYY-MM-DD HH24:MI:SS')"
	if got != want {
		t.Errorf("timeFilter = %q, want %q", got, want)
	}

	got, err = Interpolate("SELECT $__interval AS step FROM t", p)
	if err != nil {
		t.Fatalf("interval: %v", err)
	}
	if got != "SELECT 1s AS step FROM t" {
		t.Errorf("interval = %q, want 'SELECT 1s AS step FROM t'", got)
	}

	if _, err := Interpolate("SELECT ${var} FROM t", p); err == nil || !strings.Contains(err.Error(), "unresolved variable") {
		t.Errorf("err = %v, want unresolved variable error", err)
	}
}

// TestInterpolate_Combined expands macros and variables together in one query.
func TestInterpolate_Combined(t *testing.T) {
	p := rangeParams()
	p.ScopedVars = map[string]any{"host": []any{"web-1", "web-2"}}

	sql := "SELECT $__interval AS interval, id FROM t WHERE $__timeFilter(at) AND host IN (${host:csv})"
	got, err := Interpolate(sql, p)
	if err != nil {
		t.Fatalf("Interpolate: %v", err)
	}
	want := "SELECT 1m AS interval, id FROM t WHERE at >= TO_DATE('2026-08-08 09:00:00','YYYY-MM-DD HH24:MI:SS') AND at <= TO_DATE('2026-08-08 10:00:00','YYYY-MM-DD HH24:MI:SS') AND host IN ('web-1', 'web-2')"
	if got != want {
		t.Errorf("combined = %q, want %q", got, want)
	}
}

func TestDeriveInterval(t *testing.T) {
	oneHour := backend.TimeRange{
		From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
	}
	sixHour := backend.TimeRange{
		From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 8, 15, 0, 0, 0, time.UTC),
	}
	thirtyMin := backend.TimeRange{
		From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC),
	}
	twentyFour := backend.TimeRange{
		From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		name     string
		interval time.Duration
		points   int64
		tr       backend.TimeRange
		want     time.Duration
	}{
		{"panel interval wins", 10 * time.Second, 100, oneHour, 36 * time.Second},
		{"alert 6h", time.Second, 43200, sixHour, time.Second},
		{"alert 24h", time.Second, 43200, twentyFour, 2 * time.Second},
		{"no maxDataPoints", 500 * time.Millisecond, 0, oneHour, 500 * time.Millisecond},
		{"zero interval", 0, 100, oneHour, 36 * time.Second},
		{"alert 30m", time.Second, 43200, thirtyMin, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveInterval(tc.interval, tc.points, tc.tr); got != tc.want {
				t.Errorf("deriveInterval(%v, %d, range) = %v, want %v", tc.interval, tc.points, got, tc.want)
			}
		})
	}
}

func TestParseIntervalSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"1m", 60, false},
		{"5m", 300, false},
		{"1h", 3600, false},
		{"1d", 86400, false},
		{"30s", 30, false},
		{"'5m'", 300, false},
		{"300", 300, false},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := parseIntervalSeconds(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseIntervalSeconds(%q) = %d, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseIntervalSeconds(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseIntervalSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

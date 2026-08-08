package interpolate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// deriveInterval computes the bucketing interval for the query (report §10.4).
// Grafana sends IntervalMs from the panel (dashboard) or its own defaults
// (alerting: 1000ms with maxDataPoints=43200). The formula
//
//	interval_ms = max(IntervalMs, range_ms / max(1, maxDataPoints))
//
// is the single shared source for SQL $__interval and, later, the Prometheus
// proxy step (F6/F7).
func deriveInterval(interval time.Duration, maxDataPoints int64, tr backend.TimeRange) time.Duration {
	rangeMs := tr.To.UnixMilli() - tr.From.UnixMilli()
	if rangeMs < 0 {
		rangeMs = 0
	}
	if maxDataPoints < 1 {
		return interval
	}
	calc := rangeMs / maxDataPoints
	if calc < 1 {
		calc = 1
	}
	derived := time.Duration(calc) * time.Millisecond
	if derived > interval {
		return derived
	}
	return interval
}

// intervalUnitRe matches an interval argument: an integer with an optional
// d/h/m/s suffix (a bare integer is seconds). Quotes are stripped first so the
// Grafana "5m" and a literal '5m' both work.
var intervalUnitRe = regexp.MustCompile(`^(\d+)([dhms]?)$`)

// parseIntervalSeconds converts an interval argument (e.g. "5m", "1h", "1d",
// "'30s'", "300") to seconds.
func parseIntervalSeconds(s string) (int64, error) {
	s = strings.Trim(strings.TrimSpace(s), "'\"")
	m := intervalUnitRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid interval %q — use e.g. 5m, 1h, 1d, or a bare seconds integer", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "d":
		return n * 86400, nil
	case "h":
		return n * 3600, nil
	case "m":
		return n * 60, nil
	case "s", "":
		return n, nil
	}
	return 0, fmt.Errorf("invalid interval unit %q", m[2])
}

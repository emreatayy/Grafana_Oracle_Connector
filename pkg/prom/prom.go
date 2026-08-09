// Package prom is the Prometheus proxy client (report §9). It is a thin, pure
// Go wrapper around Prometheus' HTTP API v1: it validates the configured base
// URL, issues query_range / query / status/buildinfo calls with optional
// basic/bearer auth, derives the step, and converts matrix/vector results into
// Grafana frames. It deliberately knows nothing about Grafana settings or the
// Oracle connection, so it is unit-testable against httptest fixtures.
package prom

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Client is a Prometheus HTTP API v1 client bound to one validated base URL.
// It is safe for concurrent use; every request is cancelled via ctx.
type Client struct {
	base   string
	http   *http.Client
	auth   string // precomputed Authorization header ("" = none)
	limit  int    // maxSeries-bound limit (0 = unset)
	legend string // series-name template ("" = default {{ __name__ }})
}

// Options configures the HTTP transport (report §9.1/§9.5).
type Options struct {
	AuthType     string // "none" | "basic" | "token"
	User         string // basic auth user
	Password     string // basic auth password
	Token        string // bearer token
	TLSInsecure  bool   // skip TLS certificate verification
	Timeout      time.Duration
	Limit        int    // optional Prometheus `limit` (max returned series; 0 = unset)
	LegendFormat string // series-name template ("" = default {{ __name__ }})
}

// ValidateURL trims whitespace and accepts only http/https URLs without
// embedded credentials (report §9.1). Returns the trimmed URL or an error.
func ValidateURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("prometheus URL is empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("prometheus URL is not parseable: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("prometheus URL must use http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("prometheus URL has no host")
	}
	if u.User != nil {
		return "", fmt.Errorf("prometheus URL must not embed credentials; configure them in the datasource settings")
	}
	return trimmed, nil
}

// NewClient builds a Client for a validated base URL. It returns an error when
// the URL fails ValidateURL, so a misconfigured datasource fails at instance
// construction, never mid-query.
func NewClient(baseURL string, opts Options) (*Client, error) {
	base, err := ValidateURL(baseURL)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.TLSInsecure}, // #nosec G402 — operator opt-in
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	return &Client{
		base:   strings.TrimRight(base, "/"),
		http:   &http.Client{Transport: tr, Timeout: opts.Timeout},
		auth:   AuthHeader(opts),
		limit:  opts.Limit,
		legend: opts.LegendFormat,
	}, nil
}

// AuthHeader precomputes the Authorization header for opts (report §9.1).
func AuthHeader(opts Options) string {
	switch opts.AuthType {
	case "basic":
		token := base64.StdEncoding.EncodeToString([]byte(opts.User + ":" + opts.Password))
		return "Basic " + token
	case "token":
		if opts.Token != "" {
			return "Bearer " + opts.Token
		}
	}
	return ""
}

// BuildInfo returns the Prometheus version banner for the health check
// (report §9.2 endpoint table). Empty string when the server omits it.
func (c *Client) BuildInfo(ctx context.Context) (string, error) {
	var resp buildInfoResponse
	if err := c.get(ctx, "/api/v1/status/buildinfo", nil, &resp); err != nil {
		return "", err
	}
	return resp.Data.Version, nil
}

// QueryRange issues GET /api/v1/query_range and returns the matrix as a wide
// time-series frame (report §9.2/§9.3): one time.Time field + one *float64
// field per series, labels on the fields. Zero series yield an empty frame.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*data.Frame, error) {
	params := url.Values{
		"query": {query},
		"start": {unixStr(start)},
		"end":   {unixStr(end)},
		"step":  {stepStr(step)},
	}
	c.applyLimit(&params)
	var resp queryResponse
	if err := c.get(ctx, "/api/v1/query_range", params, &resp); err != nil {
		return nil, err
	}
	return matrixToFrame(resp.Data, c.legend)
}

// QueryInstant issues GET /api/v1/query and returns the vector as a wide
// single-row frame for stat/gauge/table (report §9.4).
func (c *Client) QueryInstant(ctx context.Context, query string, at time.Time) (*data.Frame, error) {
	params := url.Values{"query": {query}}
	if !at.IsZero() {
		params.Set("time", unixStr(at))
	}
	c.applyLimit(&params)
	var resp queryResponse
	if err := c.get(ctx, "/api/v1/query", params, &resp); err != nil {
		return nil, err
	}
	return vectorToFrame(resp.Data, c.legend)
}

// get performs a GET against the API, decodes JSON into out, and maps HTTP
// status + Prometheus `status:"error"` payloads to readable errors (§9.5).
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	u := c.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("prometheus request: %w", err)
	}
	if auth := c.auth; auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("prometheus response read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("prometheus returned HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("prometheus response parse: %w", err)
	}
	return nil
}

// maxResponseBytes bounds a single Prometheus response (report §9.5). 64 MiB is
// generous for real series data while guarding against an unbounded download.
const maxResponseBytes = 64 << 20

func (c *Client) applyLimit(params *url.Values) {
	if c.limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", c.limit))
	}
}

func unixStr(t time.Time) string {
	return fmt.Sprintf("%d", t.Unix())
}

func stepStr(d time.Duration) string {
	if s := int64(d / time.Second); s >= 1 {
		return fmt.Sprintf("%d", s)
	}
	return "1"
}

// DeriveStep is the single step/interval derivation shared by SQL and
// Prometheus (§10.4): whole seconds, at least 1s. When maxDataPoints > 0 the
// step follows from the range (the alerting context — §10.4); otherwise the
// request's IntervalMs is used, floored at 1s.
func DeriveStep(start, end time.Time, maxDataPoints int64, interval time.Duration) time.Duration {
	var s int64
	if maxDataPoints > 0 {
		ms := end.Sub(start).Milliseconds() / max(1, maxDataPoints)
		s = ms / 1000
	} else {
		s = interval.Milliseconds() / 1000
	}
	if s < 1 {
		s = 1
	}
	return time.Duration(s) * time.Second
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// buildInfoResponse is the /api/v1/status/buildinfo payload subset we read.
type buildInfoResponse struct {
	Status string `json:"status"`
	Data   struct {
		Version string `json:"version"`
	} `json:"data"`
	Error     string `json:"error"`
	ErrorType string `json:"errorType"`
}

// queryResponse is the shared envelope for /api/v1/query and /query_range.
type queryResponse struct {
	Status    string    `json:"status"`
	Data      queryData `json:"data"`
	Error     string    `json:"error"`
	ErrorType string    `json:"errorType"`
}

type queryData struct {
	ResultType string            `json:"resultType"`
	Result     []json.RawMessage `json:"result"`
}

// matrixSeries / vectorSeries decode one result entry either way: a matrix has
// `values`, a vector has `value`.
type promSeries struct {
	Metric map[string]string `json:"metric"`
	Values []promSample      `json:"values"`
	Value  *promSample       `json:"value"`
}

type promSample [2]any // [unix-ts (float64), value (string)]

func (s *promSample) UnmarshalJSON(b []byte) error {
	var pair []any
	if err := json.Unmarshal(b, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("prometheus sample must be [timestamp, value]")
	}
	*s = promSample{pair[0], pair[1]}
	return nil
}

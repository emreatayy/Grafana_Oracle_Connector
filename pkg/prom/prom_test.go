package prom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

func TestValidateURL(t *testing.T) {
	valid := map[string]string{
		"http://localhost:9090":     "http://localhost:9090",
		"  https://prom.example/x ": "https://prom.example/x",
		"http://127.0.0.1:9090/":    "http://127.0.0.1:9090/",
	}
	for in, want := range valid {
		got, err := ValidateURL(in)
		if err != nil || got != want {
			t.Errorf("ValidateURL(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}

	invalid := []string{
		"", "  ", "ftp://prom.example", "localhost:9090", // no scheme
		"http://user:pass@prom.example", // embedded credentials
		"http:///no-host",
		"gopher://prom.example",
	}
	for _, in := range invalid {
		if got, err := ValidateURL(in); err == nil {
			t.Errorf("ValidateURL(%q) = (%q, nil), want error", in, got)
		}
	}
}

func TestAuthHeader(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"none", Options{AuthType: "none"}, ""},
		{"basic", Options{AuthType: "basic", User: "admin", Password: "pw"}, "Basic YWRtaW46cHc="},
		{"token", Options{AuthType: "token", Token: "abc"}, "Bearer abc"},
		{"empty token", Options{AuthType: "token", Token: ""}, ""},
		{"empty string", Options{}, ""},
	}
	for _, c := range cases {
		if got := AuthHeader(c.opts); got != c.want {
			t.Errorf("%s: AuthHeader = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDeriveStep(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	end := time.Unix(3600, 0).UTC() // 1 h

	if got := DeriveStep(start, end, 360, 15*time.Second); got != 10*time.Second {
		t.Errorf("maxDataPoints path: step = %v, want 10s", got)
	}
	// maxDataPoints wins over interval in the alerting context (§10.4).
	if got := DeriveStep(start, end, 360, 1*time.Second); got != 10*time.Second {
		t.Errorf("maxDataPoints path with small interval: step = %v, want 10s", got)
	}
	if got := DeriveStep(start, end, 0, 15000*time.Millisecond); got != 15*time.Second {
		t.Errorf("interval path: step = %v, want 15s", got)
	}
	// Floor at 1 s, never 0.
	if got := DeriveStep(start, end, 0, 500*time.Millisecond); got != 1*time.Second {
		t.Errorf("floor: step = %v, want 1s", got)
	}
	if got := DeriveStep(start, end, 1<<62, 0); got != 1*time.Second {
		t.Errorf("huge maxDataPoints floor: step = %v, want 1s", got)
	}
}

func TestFormatName(t *testing.T) {
	labels := map[string]string{"__name__": "up", "job": "prometheus", "instance": "localhost:9090"}
	cases := []struct{ tmpl, want string }{
		{"", "up"},
		{"{{ __name__ }}", "up"},
		{"{{job}} / {{ instance }}", "prometheus / localhost:9090"},
		{"host={{instance}}", "host=localhost:9090"},
		{"{{missing}}", ""},    // unknown label → empty
		{"a {{ missing", "up"}, // unbalanced braces → default
		{"literal", "literal"}, // no tokens → literal
	}
	for _, c := range cases {
		if got := FormatName(c.tmpl, labels); got != c.want {
			t.Errorf("FormatName(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
	// No __name__ → "series"
	if got := FormatName("", map[string]string{"job": "x"}); got != "series" {
		t.Errorf("FormatName(no name) = %q, want series", got)
	}
}

func newFixtureServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestQueryRange_MatrixFrame(t *testing.T) {
	const body = `{
	  "status": "success",
	  "data": {
	    "resultType": "matrix",
	    "result": [
	      {"metric": {"__name__": "up", "job": "prometheus"}, "values": [[100, "1"], [110, "0"]]},
	      {"metric": {"__name__": "up", "job": "node"}, "values": [[100, "1"], [110, "1"]]}
	    ]
	  }
	}`
	var gotQuery string
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("path = %q, want /api/v1/query_range", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("query")
		if got := r.URL.Query().Get("step"); got != "10" {
			t.Errorf("step = %q, want 10", got)
		}
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	c, err := NewClient(srv.URL, Options{Limit: 1000})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	frame, err := c.QueryRange(context.Background(), "up", time.Unix(100, 0), time.Unix(200, 0), 10*time.Second)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if gotQuery != "up" {
		t.Errorf("query param = %q, want up", gotQuery)
	}
	if frame.Meta == nil || frame.Meta.Type != data.FrameTypeTimeSeriesWide {
		t.Fatalf("meta = %+v, want TimeSeriesWide", frame.Meta)
	}
	// time + 2 value fields
	if len(frame.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	if frame.Fields[0].Name != "time" {
		t.Errorf("field 0 name = %q, want time", frame.Fields[0].Name)
	}
	// Series names come from __name__, which is stripped from labels.
	if got := frame.Fields[1].Name; got != "up" {
		t.Errorf("field 1 name = %q, want up", got)
	}
	if got := frame.Fields[1].Labels["job"]; got != "prometheus" {
		t.Errorf("field 1 labels = %v, want job=prometheus", frame.Fields[1].Labels)
	}
	if _, ok := frame.Fields[1].Labels["__name__"]; ok {
		t.Errorf("field 1 labels must not carry __name__")
	}
	if got := *frame.Fields[1].At(0).(*float64); got != 1 {
		t.Errorf("series 0 [0] = %v, want 1", got)
	}
	if got := *frame.Fields[1].At(1).(*float64); got != 0 {
		t.Errorf("series 0 [1] = %v, want 0", got)
	}
	if got := *frame.Fields[2].At(1).(*float64); got != 1 {
		t.Errorf("series 1 [1] = %v, want 1", got)
	}
}

func TestQueryRange_Empty(t *testing.T) {
	const body = `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	frame, err := c.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), 15*time.Second)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(frame.Fields) != 1 || frame.Fields[0].Name != "time" {
		t.Fatalf("empty matrix fields = %d (%v), want just time", len(frame.Fields), frame.Fields[0].Name)
	}
}

func TestQueryInstant_VectorFrame(t *testing.T) {
	const body = `{
	  "status": "success",
	  "data": {
	    "resultType": "vector",
	    "result": [
	      {"metric": {"__name__": "up", "instance": "a"}, "value": [100, "1"]},
	      {"metric": {"__name__": "up", "instance": "b"}, "value": [100, "0"]}
	    ]
	  }
	}`
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("path = %q, want /api/v1/query", r.URL.Path)
		}
		if got := r.URL.Query().Get("time"); got != "123" {
			t.Errorf("time = %q, want 123", got)
		}
		_, _ = w.Write([]byte(body))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	frame, err := c.QueryInstant(context.Background(), "up", time.Unix(123, 0))
	if err != nil {
		t.Fatalf("QueryInstant: %v", err)
	}
	if frame.Rows() != 1 {
		t.Fatalf("rows = %d, want 1", frame.Rows())
	}
	if len(frame.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	if got := *frame.Fields[1].At(0).(*float64); got != 1 {
		t.Errorf("a = %v, want 1", got)
	}
	if got := *frame.Fields[2].At(0).(*float64); got != 0 {
		t.Errorf("b = %v, want 0", got)
	}
	if got := frame.Fields[2].Labels["instance"]; got != "b" {
		t.Errorf("b labels = %v, want instance=b", frame.Fields[2].Labels)
	}
}

func TestQuery_Authorization(t *testing.T) {
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})
	c, err := NewClient(srv.URL, Options{AuthType: "token", Token: "secret"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.QueryInstant(context.Background(), "up", time.Time{}); err != nil {
		t.Fatalf("QueryInstant: %v", err)
	}
}

func TestQuery_HTTPError(t *testing.T) {
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid parameter \"step\""}`))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.QueryInstant(context.Background(), "up{", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("err = %v, want HTTP 400", err)
	}
}

// TestRawJSON_LabelValues exercises the metric-browser/variable passthrough
// (F7/F8, report §9.2): the raw envelope is returned unchanged and the
// Authorization header built from Options reaches the server.
func TestRawJSON_LabelValues(t *testing.T) {
	var gotPath, gotAuth string
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"success","data":["up","down"]}`))
	})
	c, err := NewClient(srv.URL, Options{AuthType: "token", Token: "s3cret"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	body, err := c.RawJSON(context.Background(), "/api/v1/label/__name__/values", nil)
	if err != nil {
		t.Fatalf("RawJSON: %v", err)
	}
	if gotPath != "/api/v1/label/__name__/values" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("auth = %q, want Bearer s3cret", gotAuth)
	}
	if !strings.Contains(string(body), `"up"`) {
		t.Errorf("body = %s, want the raw envelope", body)
	}
}

// TestRawJSON_SeriesParams forwards match[] selectors to /api/v1/series.
func TestRawJSON_SeriesParams(t *testing.T) {
	var gotRawQuery string
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	params := url.Values{"match[]": {"up", `up{job="x"}`}}
	_, err = c.RawJSON(context.Background(), "/api/v1/series", params)
	if err != nil {
		t.Fatalf("RawJSON: %v", err)
	}
	if !strings.Contains(gotRawQuery, "match%5B%5D=up") || !strings.Contains(gotRawQuery, "match%5B%5D=") {
		t.Errorf("raw query = %q, want the match[] params forwarded", gotRawQuery)
	}
}

// TestLabelValues returns the values of a label (F8 `label_values(<label>)`).
func TestLabelValues(t *testing.T) {
	var gotPath string
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":["prometheus","node"]}`))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	values, err := c.LabelValues(context.Background(), "job")
	if err != nil {
		t.Fatalf("LabelValues: %v", err)
	}
	if gotPath != "/api/v1/label/job/values" {
		t.Errorf("path = %q, want /api/v1/label/job/values", gotPath)
	}
	if len(values) != 2 || values[0] != "prometheus" || values[1] != "node" {
		t.Errorf("values = %v, want [prometheus node]", values)
	}
}

// TestSeries returns the series set matching a selector (F8
// `label_values(<metric>, <label>)`).
func TestSeries(t *testing.T) {
	var gotPath, gotMatch string
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query().Get("match[]")
		_, _ = w.Write([]byte(`{"status":"success","data":[
			{"__name__":"up","job":"prometheus","instance":"a"},
			{"__name__":"up","job":"prometheus","instance":"b"}
		]}`))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	series, err := c.Series(context.Background(), "up")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if gotPath != "/api/v1/series" {
		t.Errorf("path = %q, want /api/v1/series", gotPath)
	}
	if gotMatch != "up" {
		t.Errorf("match[] = %q, want up", gotMatch)
	}
	if len(series) != 2 || series[0]["job"] != "prometheus" {
		t.Errorf("series = %v, want 2 series with job=prometheus", series)
	}
}

func TestBuildInfo(t *testing.T) {
	srv := newFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status/buildinfo" {
			t.Errorf("path = %q, want buildinfo", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"3.5.0"}}`))
	})
	c, err := NewClient(srv.URL, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ver, err := c.BuildInfo(context.Background())
	if err != nil {
		t.Fatalf("BuildInfo: %v", err)
	}
	if ver != "3.5.0" {
		t.Errorf("version = %q, want 3.5.0", ver)
	}
}

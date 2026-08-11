package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
	"github.com/emreatayy/oracle-datasource/pkg/oracle"
)

// newTestDatasource builds an unconnected instance (NewDatasource does not
// connect eagerly). Uses a connector so session params are per-instance.
func newTestDatasource(t *testing.T) *Datasource {
	t.Helper()
	settings := &models.PluginSettings{
		OracleSettings: models.OracleSettings{
			Hostname:           "localhost",
			Port:               1521,
			Service:            "XEPDB1",
			User:               "SYSTEM",
			MaxOpenConns:       10,
			MaxIdleConns:       5,
			ConnMaxLifetimeSec: 1800,
			QueryTimeoutSec:    30,
			CommandTimeoutSec:  10,
			MaxRows:            100000,
			MaxSeries:          1000,
			SessionTimeZone:    "UTC",
		},
		Secrets: &models.SecretPluginSettings{O_Password: "pw"},
	}
	dsn, err := oracle.BuildDSN(settings)
	if err != nil {
		t.Fatalf("BuildDSN: %v", err)
	}
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	return &Datasource{settings: settings, db: db, session: oracle.SessionParams(settings.SessionTimeZone)}
}

func TestValidateReadOnly_Allowed(t *testing.T) {
	allowed := []string{
		"SELECT 1 FROM DUAL",
		"  select * from t",
		"\nWITH cte AS (SELECT 1 FROM DUAL) SELECT * FROM cte",
		"-- comment\nSELECT 1 FROM DUAL",
		"/* hint */ SELECT 1 FROM DUAL",
	}
	for _, sql := range allowed {
		if err := validateReadOnly(sql); err != nil {
			t.Errorf("validateReadOnly(%q) = %v, want nil", sql, err)
		}
	}
}

func TestValidateReadOnly_Rejected(t *testing.T) {
	rejected := []string{
		"",
		"   ",
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET x = 1",
		"DELETE FROM t",
		"DROP TABLE t",
		"ALTER TABLE t ADD c NUMBER",
		"BEGIN dbms_output.put_line('x'); END;",
		"-- hide\nINSERT INTO t VALUES (1)", // comment-hidden DML
		"/* hide */ DELETE FROM t",          // block-comment-hidden DML
		"CREATE TABLE t (c NUMBER)",
		"MERGE INTO t USING s ON (1=1) WHEN MATCHED THEN UPDATE SET x=1",
	}
	for _, sql := range rejected {
		if err := validateReadOnly(sql); err == nil {
			t.Errorf("validateReadOnly(%q) = nil, want rejection", sql)
		}
	}
}

func TestFirstKeyword(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"select 1", "SELECT"},
		{"  with x as (select 1) select * from x", "WITH"},
		{"-- c\nSELECT 1", "SELECT"},
		{"/* a */ /* b */ SELECT 1", "SELECT"},
		{"-- only comment", ""},
		{"/* unterminated", ""},
		{"   ", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := firstKeyword(tc.sql); got != tc.want {
			t.Errorf("firstKeyword(%q) = %q, want %q", tc.sql, got, tc.want)
		}
	}
}

func TestQuery_GuardRejectsBeforeExecution(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	// A DML statement must be rejected by the guard before any DB access.
	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"sql":"DELETE FROM important_table"}`),
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", dr.Status)
	}
	if !strings.Contains(dr.Error.Error(), "read-only guard") {
		t.Errorf("error = %q, want read-only guard message", dr.Error)
	}
}

// TestQuery_PrometheusDisabled proves the F6 fail-closed gate: a Prometheus-mode
// query with the proxy disabled is rejected before any connection attempt (the
// DB here is never reachable), never executed as SQL against Oracle.
func TestQuery_PrometheusDisabled(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"mode":"prometheus","promql":"up"}`),
		}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	dr := resp.Responses["A"]
	if dr.Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", dr.Status)
	}
	if !strings.Contains(dr.Error.Error(), "Prometheus proxy is disabled") {
		t.Errorf("error = %q, want disabled message", dr.Error)
	}
}

// TestQuery_PrometheusEmptyPromQL rejects a Prometheus-mode query with no PromQL
// even when the proxy is enabled.
func TestQuery_PrometheusEmptyPromQL(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"mode":"prometheus","promql":""}`),
		}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	if resp.Responses["A"].Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", resp.Responses["A"].Status)
	}
}

// TestQuery_PrometheusQueryRange routes a mode=prometheus query to the proxy and
// returns the matrix as a wide time-series frame (report §9.3), with the raw
// PromQL on ExecutedQueryString and the §10.4 step derived from maxDataPoints.
func TestQuery_PrometheusQueryRange(t *testing.T) {
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
	var path, step string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		step = r.URL.Query().Get("step")
		if got := r.URL.Query().Get("query"); got != "up" {
			t.Errorf("query param = %q, want up", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:         "A",
			JSON:          json.RawMessage(`{"mode":"prometheus","promql":"up"}`),
			TimeRange:     backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
			MaxDataPoints: 360,
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatalf("unexpected error: %v", dr.Error)
	}
	if len(dr.Frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(dr.Frames))
	}
	if path != "/api/v1/query_range" {
		t.Errorf("path = %q, want /api/v1/query_range", path)
	}
	if step != "10" {
		t.Errorf("step = %q, want 10", step)
	}
	frame := dr.Frames[0]
	if frame.Meta == nil || frame.Meta.ExecutedQueryString != "up" {
		t.Errorf("ExecutedQueryString = %+v, want up", frame.Meta)
	}
	if len(frame.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	if got := frame.Fields[1].Name; got != "up" {
		t.Errorf("field 1 name = %q, want up", got)
	}
}

// TestQuery_PrometheusInstant routes an instant (vector) Prometheus query
// through the proxy and returns the single-row wide frame (report §9.4).
func TestQuery_PrometheusInstant(t *testing.T) {
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
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if got := r.URL.Query().Get("time"); got != "123" {
			t.Errorf("time param = %q, want 123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:     "A",
			JSON:      json.RawMessage(`{"mode":"prometheus","promql":"up","instant":true}`),
			TimeRange: backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(123, 0)},
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatalf("unexpected error: %v", dr.Error)
	}
	if len(dr.Frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(dr.Frames))
	}
	if path != "/api/v1/query" {
		t.Errorf("path = %q, want /api/v1/query", path)
	}
	frame := dr.Frames[0]
	if frame.Rows() != 1 {
		t.Errorf("rows = %d, want 1", frame.Rows())
	}
	if len(frame.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	if got := *frame.Fields[1].At(0).(*float64); got != 1 {
		t.Errorf("series a = %v, want 1", got)
	}
}

// TestQuery_PromLabelValues_SingleArg serves a Grafana query variable
// `label_values(job)` (report §8.5): the single-argument form returns a
// one-column frame whose values become the dropdown options.
func TestQuery_PromLabelValues_SingleArg(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":["prometheus","node"]}`))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:     "A",
			JSON:      json.RawMessage(`{"mode":"prometheus","promql":"label_values(job)"}`),
			TimeRange: backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatalf("unexpected error: %v", dr.Error)
	}
	if gotPath != "/api/v1/label/job/values" {
		t.Errorf("path = %q, want /api/v1/label/job/values", gotPath)
	}
	if len(dr.Frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(dr.Frames))
	}
	frame := dr.Frames[0]
	if frame.Fields[0].Name != "job" || frame.Rows() != 2 {
		t.Fatalf("field = %s rows = %d, want job / 2", frame.Fields[0].Name, frame.Rows())
	}
	if got := frame.Fields[0].At(0).(string); got != "prometheus" {
		t.Errorf("first value = %q, want prometheus", got)
	}
}

// TestQuery_PromLabelValues_TwoArgs serves `label_values(up, job)`: distinct
// job values collected from the series matching `up`, sorted.
func TestQuery_PromLabelValues_TwoArgs(t *testing.T) {
	var gotPath, gotMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query().Get("match[]")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":[
			{"__name__":"up","job":"prometheus","instance":"a"},
			{"__name__":"up","job":"node","instance":"b"},
			{"__name__":"up","job":"prometheus","instance":"c"}
		]}`))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:     "A",
			JSON:      json.RawMessage(`{"mode":"prometheus","promql":"label_values(up, job)"}`),
			TimeRange: backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatalf("unexpected error: %v", dr.Error)
	}
	if gotPath != "/api/v1/series" {
		t.Errorf("path = %q, want /api/v1/series", gotPath)
	}
	if gotMatch != "up" {
		t.Errorf("match[] = %q, want up", gotMatch)
	}
	frame := dr.Frames[0]
	if frame.Rows() != 2 {
		t.Fatalf("rows = %d, want 2 (distinct jobs, sorted)", frame.Rows())
	}
	got := []string{frame.Fields[0].At(0).(string), frame.Fields[0].At(1).(string)}
	if got[0] != "node" || got[1] != "prometheus" {
		t.Errorf("values = %v, want [node prometheus] sorted", got)
	}
}

// TestQuery_PromLabelValues_NotIntercepted: a real PromQL expression that
// merely contains the word label_values is not intercepted — it must still go
// to /api/v1/query_range.
func TestQuery_PromLabelValues_NotIntercepted(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:         "A",
			JSON:          json.RawMessage(`{"mode":"prometheus","promql":"up + on(job) group_left label_values"}`),
			TimeRange:     backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
			MaxDataPoints: 360,
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	if err := resp.Responses["A"].Error; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/query_range" {
		t.Errorf("path = %q, want /api/v1/query_range (not intercepted)", gotPath)
	}
}

// TestQuery_PromLabelValues_ProxyDisabled: label_values is gated by promEnabled
// exactly like any Prometheus query — a clean 400, never touching Oracle.
func TestQuery_PromLabelValues_ProxyDisabled(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose() // promEnabled stays false

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"mode":"prometheus","promql":"label_values(job)"}`),
		}},
	}
	resp, err := ds.QueryData(context.Background(), req)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	dr := resp.Responses["A"]
	if dr.Error == nil {
		t.Fatal("expected an error, got none")
	}
	if !strings.Contains(dr.Error.Error(), "disabled") {
		t.Errorf("error = %v, want the disabled-proxy message", dr.Error)
	}
}

func TestQuery_EmptySQLRejected(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"sql":""}`),
		}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	if resp.Responses["A"].Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", resp.Responses["A"].Status)
	}
}

func TestQuery_InvalidJSON(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{oops`)}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	if resp.Responses["A"].Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", resp.Responses["A"].Status)
	}
}

// TestQueryModel_Annotation parses the F9 annotation marker (report §11): a
// dashboard annotation target carries `"annotation": true` and must be
// distinguished from a regular panel query.
func TestQueryModel_Annotation(t *testing.T) {
	var qm queryModel
	if err := json.Unmarshal(json.RawMessage(`{"sql":"SELECT ts FROM events","annotation":true}`), &qm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !qm.Annotation {
		t.Errorf("annotation = false, want true")
	}
	var plain queryModel
	if err := json.Unmarshal(json.RawMessage(`{"sql":"SELECT 1 FROM DUAL"}`), &plain); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plain.Annotation {
		t.Errorf("annotation = true for a plain query, want false")
	}
}

// TestQueryModel_Parse verifies the query model fields (report §4.3) and that
// the optional ones stay empty when absent.
func TestQueryModel_Parse(t *testing.T) {
	raw := json.RawMessage(`{"sql":"SELECT 1 FROM DUAL","format":"time_series","timeColumn":"ts","largeNumberMode":"float","scopedVars":{"host":["web-1"]},"mode":"prometheus","promql":"up","instant":true,"legendFormat":"{{job}}","step":"15s"}`)
	var qm queryModel
	if err := json.Unmarshal(raw, &qm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if qm.SQL != "SELECT 1 FROM DUAL" {
		t.Errorf("sql = %q", qm.SQL)
	}
	if qm.Format != "time_series" {
		t.Errorf("format = %q, want time_series", qm.Format)
	}
	if qm.TimeColumn != "ts" {
		t.Errorf("timeColumn = %q, want ts", qm.TimeColumn)
	}
	if qm.LargeNumberMode != "float" {
		t.Errorf("largeNumberMode = %q, want float", qm.LargeNumberMode)
	}
	if qm.Mode != "prometheus" {
		t.Errorf("mode = %q, want prometheus", qm.Mode)
	}
	if qm.PromQL != "up" {
		t.Errorf("promql = %q, want up", qm.PromQL)
	}
	if !qm.Instant {
		t.Errorf("instant = false, want true")
	}
	if qm.LegendFormat != "{{job}}" {
		t.Errorf("legendFormat = %q, want {{job}}", qm.LegendFormat)
	}
	if qm.Step != "15s" {
		t.Errorf("step = %q, want 15s", qm.Step)
	}
	host, ok := qm.ScopedVars["host"].([]any)
	if !ok || len(host) != 1 || host[0] != "web-1" {
		t.Errorf("scopedVars = %#v, want host=[\"web-1\"]", qm.ScopedVars)
	}

	var empty queryModel
	if err := json.Unmarshal(json.RawMessage(`{"sql":"SELECT 1"}`), &empty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if empty.Format != "" || empty.TimeColumn != "" || empty.LargeNumberMode != "" || empty.ScopedVars != nil || empty.Mode != "" || empty.PromQL != "" || empty.Instant || empty.LegendFormat != "" || empty.Step != "" {
		t.Errorf("optional fields not empty: %+v", empty)
	}
}

// TestQuery_InterpolationErrorBeforeConnection proves the F3 ordering: an
// unresolved ${var} fails with a readable interpolation error before any
// connection attempt (the DB here is never reachable).
func TestQuery_InterpolationErrorBeforeConnection(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"sql":"SELECT ${missing} FROM DUAL"}`),
		}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	dr := resp.Responses["A"]
	if dr.Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", dr.Status)
	}
	if !strings.Contains(dr.Error.Error(), "unresolved variable") {
		t.Errorf("error = %q, want unresolved variable message", dr.Error)
	}
}

func TestDispose_ClosesDB(t *testing.T) {
	ds := newTestDatasource(t)
	db := ds.db // Dispose nils the field; keep a reference to probe closedness.
	ds.Dispose()
	if err := db.Ping(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("db.Ping after Dispose = %v, want 'database is closed'", err)
	}
}

func TestDispose_GuardStopsQueries(t *testing.T) {
	ds := newTestDatasource(t)
	ds.Dispose()

	err := ds.ensureReady(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disposed") {
		t.Fatalf("ensureReady after Dispose = %v, want disposed error", err)
	}
}

// --- F7: CallResource (metric browser + variables) ---------------------------

// callResource sends one request through ds.CallResource and returns the
// response the sender received.
func callResource(ds *Datasource, req *backend.CallResourceRequest) *backend.CallResourceResponse {
	var resp *backend.CallResourceResponse
	sender := backend.CallResourceResponseSenderFunc(func(r *backend.CallResourceResponse) error {
		resp = r
		return nil
	})
	if err := ds.CallResource(context.Background(), req, sender); err != nil {
		return &backend.CallResourceResponse{Status: -1, Body: []byte(err.Error())}
	}
	return resp
}

// TestCallResource_PrometheusDisabled: the browser is gated by promEnabled the
// same way as queries — a disabled proxy gets a clean 400, never touching the
// (unreachable) database.
func TestCallResource_PrometheusDisabled(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	resp := callResource(ds, &backend.CallResourceRequest{
		Path: "prometheus/label/__name__/values",
	})
	if resp.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "disabled") {
		t.Errorf("body = %s, want disabled message", resp.Body)
	}
}

// TestCallResource_MetricBrowser proxies label/__name__/values and labels with
// the datasource's auth header, and returns the raw Prometheus envelope.
func TestCallResource_MetricBrowser(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"success","data":["up","down"]}`))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL
	ds.settings.PromAuthType = "token"
	ds.settings.Secrets.PromToken = "t0ken"

	resp := callResource(ds, &backend.CallResourceRequest{
		Path: "prometheus/label/__name__/values",
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.Status, resp.Body)
	}
	if gotPath != "/api/v1/label/__name__/values" {
		t.Errorf("proxied path = %q, want label/__name__/values", gotPath)
	}
	if gotAuth != "Bearer t0ken" {
		t.Errorf("auth = %q, want Bearer t0ken", gotAuth)
	}
	if !strings.Contains(string(resp.Body), `"up"`) {
		t.Errorf("body = %s, want the raw prometheus envelope", resp.Body)
	}
}

// TestCallResource_Series forwards only the match[] selector to /api/v1/series.
func TestCallResource_Series(t *testing.T) {
	var gotPath, gotMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query().Get("match[]")
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer srv.Close()

	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = srv.URL

	resp := callResource(ds, &backend.CallResourceRequest{
		Path: "prometheus/series",
		URL:  "http://irrelevant/prometheus/series?match[]=up%7Bjob%3D%22prometheus%22%7D",
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.Status, resp.Body)
	}
	if gotPath != "/api/v1/series" {
		t.Errorf("proxied path = %q", gotPath)
	}
	if gotMatch != `up{job="prometheus"}` {
		t.Errorf("match[] = %q, want the decoded selector", gotMatch)
	}
}

// TestCallResource_RejectsBadPaths: path traversal / control characters in a
// label name and unknown resources are rejected before reaching Prometheus.
func TestCallResource_RejectsBadPaths(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = "http://prom.example:9090" // never reached

	cases := []struct {
		path string
		want int
	}{
		{"prometheus/label/..%2Fetc/values", http.StatusBadRequest},
		{"prometheus/label/%2Fetc%2Fpasswd/values", http.StatusBadRequest},
		{"prometheus/label//values", http.StatusBadRequest},
		{"prometheus/unknown", http.StatusNotFound},
		{"other/label/__name__/values", http.StatusNotFound},
	}
	for _, c := range cases {
		resp := callResource(ds, &backend.CallResourceRequest{Path: c.path})
		if resp.Status != c.want {
			t.Errorf("path %q: status = %d, want %d", c.path, resp.Status, c.want)
		}
	}
}

// TestCallResource_MetadataTables_SilentEmpty: the F9 metadata endpoint degrades
// silently — an unconnected database (or a permission-less account) yields 200
// with an empty table list, never an error the editor would surface (report §14.3).
func TestCallResource_MetadataTables_SilentEmpty(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	for _, u := range []string{"", "metadata/tables", "metadata/tables?prefix=DU"} {
		resp := callResource(ds, &backend.CallResourceRequest{Path: "metadata/tables", URL: u})
		if resp.Status != http.StatusOK {
			t.Errorf("url %q: status = %d, want 200 (silent empty)", u, resp.Status)
		}
		if !strings.Contains(string(resp.Body), `"tables":[]`) {
			t.Errorf("url %q: body = %s, want empty tables", u, resp.Body)
		}
	}
}

// TestCallResource_MetadataColumns_SilentEmpty: same silent degradation for a
// column lookup when the database is unreachable.
func TestCallResource_MetadataColumns_SilentEmpty(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	resp := callResource(ds, &backend.CallResourceRequest{
		Path: "metadata/columns",
		URL:  "http://irrelevant/metadata/columns?table=GOX_F2",
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"columns":[]`) {
		t.Errorf("body = %s, want empty columns", resp.Body)
	}
}

// TestCallResource_MetadataColumns_RequiresTable: a column lookup without a
// table parameter is a client error, not a silent empty.
func TestCallResource_MetadataColumns_RequiresTable(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	for _, u := range []string{"", "http://irrelevant/metadata/columns?table=%20%20"} {
		resp := callResource(ds, &backend.CallResourceRequest{Path: "metadata/columns", URL: u})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("url %q: status = %d, want 400", u, resp.Status)
		}
	}
}

// TestCallResource_Metadata_UnknownPath: paths outside the metadata/ namespace
// still get a clean 404.
func TestCallResource_Metadata_UnknownPath(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	resp := callResource(ds, &backend.CallResourceRequest{Path: "metadata/foo"})
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.Status)
	}
}

// TestCallResource_ProxyError maps a Prometheus failure to a readable 502 whose
// text never echoes credentials.
func TestCallResource_ProxyError(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()
	ds.settings.PromEnabled = true
	ds.settings.PromURL = "http://no-such-prom.invalid:9090"
	ds.settings.PromAuthType = "token"
	ds.settings.Secrets.PromToken = "s3cret-token-xyz"

	resp := callResource(ds, &backend.CallResourceRequest{
		Path: "prometheus/label/__name__/values",
	})
	if resp.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.Status)
	}
	if strings.Contains(string(resp.Body), "s3cret-token-xyz") {
		t.Errorf("body leaks the token: %s", resp.Body)
	}
}

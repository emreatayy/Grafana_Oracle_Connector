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

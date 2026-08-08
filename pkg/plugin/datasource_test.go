package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

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

// TestQuery_PrometheusModeRejected proves the F4 fail-closed mode guard: a
// saved Prometheus-mode query is rejected before any connection attempt (the
// DB here is never reachable), never executed as SQL against Oracle.
func TestQuery_PrometheusModeRejected(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID: "A",
			JSON:  json.RawMessage(`{"mode":"prometheus","sql":"SELECT 1 FROM DUAL"}`),
		}},
	}
	resp, _ := ds.QueryData(context.Background(), req)
	dr := resp.Responses["A"]
	if dr.Status != backend.StatusBadRequest {
		t.Errorf("status = %v, want StatusBadRequest", dr.Status)
	}
	if !strings.Contains(dr.Error.Error(), "Prometheus mode is not implemented") {
		t.Errorf("error = %q, want Prometheus-not-implemented message", dr.Error)
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
	raw := json.RawMessage(`{"sql":"SELECT 1 FROM DUAL","format":"time_series","timeColumn":"ts","largeNumberMode":"float","scopedVars":{"host":["web-1"]},"mode":"prometheus"}`)
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
	host, ok := qm.ScopedVars["host"].([]any)
	if !ok || len(host) != 1 || host[0] != "web-1" {
		t.Errorf("scopedVars = %#v, want host=[\"web-1\"]", qm.ScopedVars)
	}

	var empty queryModel
	if err := json.Unmarshal(json.RawMessage(`{"sql":"SELECT 1"}`), &empty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if empty.Format != "" || empty.TimeColumn != "" || empty.LargeNumberMode != "" || empty.ScopedVars != nil || empty.Mode != "" {
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

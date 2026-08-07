package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestValuesToField(t *testing.T) {
	t.Run("empty column yields a nullable string field", func(t *testing.T) {
		f := valuesToField("c", []any{})
		if f.Len() != 0 {
			t.Errorf("len = %d, want 0", f.Len())
		}
	})

	t.Run("time column", func(t *testing.T) {
		ts := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
		f := valuesToField("t", []any{ts, nil, ts})
		if f.Len() != 3 {
			t.Errorf("len = %d, want 3", f.Len())
		}
	})

	t.Run("string column", func(t *testing.T) {
		f := valuesToField("s", []any{"a", nil, "c"})
		if f.Len() != 3 {
			t.Errorf("len = %d, want 3", f.Len())
		}
	})

	t.Run("mixed column stringifies", func(t *testing.T) {
		f := valuesToField("m", []any{"a", int64(42)})
		if f.Len() != 2 {
			t.Errorf("len = %d, want 2", f.Len())
		}
	})
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

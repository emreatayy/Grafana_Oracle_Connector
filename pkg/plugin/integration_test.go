package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	go_ora "github.com/sijms/go-ora/v2"

	"github.com/emreatayy/oracle-datasource/pkg/models"
	"github.com/emreatayy/oracle-datasource/pkg/oracle"
)

// Integration tests require a live Oracle and are skipped in CI unless
// ORACLE_TEST_DSN is set. Run against the verification harness database:
//
//	ORACLE_TEST_DSN='oracle://SYSTEM:verif_password@localhost:1521/XEPDB1' \
//	  go test ./pkg/plugin/ -run Integration -v
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ORACLE_TEST_DSN")
	if dsn == "" {
		t.Skip("ORACLE_TEST_DSN not set; skipping integration test")
	}
	return dsn
}

// newIntegrationDatasource builds a connected instance against the harness DB.
func newIntegrationDatasource(t *testing.T) *Datasource {
	t.Helper()
	dsn := integrationDSN(t)
	settings := &models.PluginSettings{
		OracleSettings: models.OracleSettings{
			ConnStr:            dsn,
			MaxOpenConns:       5,
			MaxIdleConns:       1,
			ConnMaxLifetimeSec: 300,
			QueryTimeoutSec:    30,
			MaxRows:            10000,
			MaxSeries:          1000,
			SessionTimeZone:    "UTC",
		},
	}
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	return &Datasource{settings: settings, db: db, session: oracle.SessionParams("UTC")}
}

// TestIntegration_Health connects and reads the version banner.
func TestIntegration_Health(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if res.Status != backend.HealthStatusOk {
		t.Fatalf("health status = %s (%q), want OK", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "Oracle") {
		t.Errorf("banner = %q, want Oracle version banner", res.Message)
	}
	t.Logf("connected to: %s", res.Message)
}

// TestIntegration_Health_BadPassword verifies the error is readable and does
// not leak the password.
func TestIntegration_Health_BadPassword(t *testing.T) {
	dsn := integrationDSN(t)
	dsn = strings.Replace(dsn, "verif_password", "wrong-password-xyz", 1)
	settings := &models.PluginSettings{
		OracleSettings: models.OracleSettings{
			ConnStr:            dsn,
			MaxOpenConns:       2,
			MaxIdleConns:       1,
			ConnMaxLifetimeSec: 300,
			QueryTimeoutSec:    5,
			SessionTimeZone:    "UTC",
		},
	}
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	ds := &Datasource{settings: settings, db: db, session: oracle.SessionParams("UTC")}
	defer ds.Dispose()

	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if res.Status != backend.HealthStatusError {
		t.Fatalf("health status = %s, want Error", res.Status)
	}
	if !strings.Contains(res.Message, "01017") {
		t.Errorf("message = %q, want mapped ORA-01017", res.Message)
	}
	if strings.Contains(res.Message, "wrong-password-xyz") {
		t.Errorf("message leaks the password: %q", res.Message)
	}
}

// TestIntegration_SimpleSelect verifies the read-only query path returns rows.
func TestIntegration_SimpleSelect(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	resp := ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"SELECT 1 AS one, 'hello' AS greeting, TO_CHAR(SYSDATE, 'YYYY') AS yr FROM DUAL"}`),
	})
	if resp.Error != nil {
		t.Fatalf("query error: %v", resp.Error)
	}
	if len(resp.Frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(resp.Frames))
	}
	frame := resp.Frames[0]
	if frame.Rows() != 1 {
		t.Errorf("rows = %d, want 1", frame.Rows())
	}
	if len(frame.Fields) != 3 {
		t.Errorf("fields = %d, want 3", len(frame.Fields))
	}
}

// TestIntegration_SessionTimeZone verifies the ALTER SESSION TIME_ZONE pin
// (report §5.4): DBTIMEZONE-dependent values must reflect the pinned zone.
func TestIntegration_SessionTimeZone(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	if err := ds.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	var tz string
	if err := ds.db.QueryRowContext(context.Background(),
		"SELECT SESSIONTIMEZONE FROM DUAL").Scan(&tz); err != nil {
		t.Fatalf("read session timezone: %v", err)
	}
	if tz != "+00:00" && tz != "UTC" && !strings.Contains(tz, "UTC") {
		t.Errorf("session timezone = %q, want UTC offset (session pinned)", tz)
	}
}

// TestIntegration_CancelLongQuery is the F1 DoD: a heavy query must return
// promptly when its context is cancelled (the mechanism Grafana uses when a
// panel is closed or an alert is stopped).
func TestIntegration_CancelLongQuery(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	// A statement that will not finish quickly.
	heavy := `SELECT COUNT(*) FROM (
		SELECT 1 FROM DUAL CONNECT BY LEVEL <= 1000000000
	)`

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	resp := ds.query(ctx, backend.DataQuery{RefID: "A", JSON: json.RawMessage(`{"sql":` + mustJSON(heavy) + `}`)})
	elapsed := time.Since(start)

	// Must return well inside the 30s query timeout and not hang forever.
	if elapsed > 10*time.Second {
		t.Fatalf("query did not cancel; took %v", elapsed)
	}
	// Either an error surfaced or the frame is present — the point is we did
	// not block. A cancelled server-side query normally yields an error.
	t.Logf("cancelled after %v; response error = %v", elapsed, resp.Error)
}

// TestIntegration_ReadOnlyGuard verifies DML is rejected before execution.
func TestIntegration_ReadOnlyGuard(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	resp := ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"UPDATE dual SET dummy = 'x' WHERE 1=0"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected read-only guard error, got nil")
	}
	if !strings.Contains(resp.Error.Error(), "read-only guard") {
		t.Errorf("error = %q, want read-only guard message", resp.Error)
	}
}

// mustJSON quotes a string as a JSON string literal.
func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ensureF2Table creates the F2 type-mapping test table (drop-first for
// idempotency). DDL/DML run directly on the pool — the plugin's read-only guard
// applies only to ds.query.
func ensureF2Table(t *testing.T, ds *Datasource, ctx context.Context) {
	t.Helper()
	_, _ = ds.db.ExecContext(ctx, `DROP TABLE gox_f2`)
	if _, err := ds.db.ExecContext(ctx, `CREATE TABLE gox_f2 (
		id    NUMBER(10),
		ratio NUMBER(10,2),
		label VARCHAR2(20),
		big   NUMBER(20),
		at    DATE,
		notes CLOB
	)`); err != nil {
		t.Fatalf("create gox_f2: %v", err)
	}
	t.Cleanup(func() { _, _ = ds.db.ExecContext(ctx, `DROP TABLE gox_f2`) })
}

// TestIntegration_TypedColumns is the F2 DoD: a real Oracle result set must
// come back as a typed frame — int64/float64/string/time fields, NULL preserved,
// a large NUMBER kept at full precision in table format, CLOB read as text, and
// the §7 time-series rules applied (duplicate time kept, NULL time skipped).
func TestIntegration_TypedColumns(t *testing.T) {
	ds := newIntegrationDatasource(t)
	// Register dispose as a cleanup (not defer): t.Cleanup runs LIFO, so
	// ensureF2Table's DROP cleanup runs before the pool is closed.
	t.Cleanup(ds.Dispose)
	ctx := context.Background()
	ensureF2Table(t, ds, ctx)

	at := time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC)
	insert := func(id int64, ratio, label, big, when, notes any) {
		t.Helper()
		if _, err := ds.db.ExecContext(ctx,
			`INSERT INTO gox_f2 (id, ratio, label, big, at, notes) VALUES (:1, :2, :3, :4, :5, :6)`,
			id, ratio, label, big, when, notes); err != nil {
			t.Fatalf("insert id=%d: %v", id, err)
		}
	}
	insert(42, 3.14, "web-1", "12345678901234567890", at, "clob-note") // full row
	insert(7, nil, nil, nil, nil, nil)                                 // all-NULL row
	insert(8, 1.0, "web-2", "2", at, nil)                               // duplicate time with id=42
	insert(9, 2.0, "web-3", "3", nil, nil)                              // NULL time → skipped in ts

	t.Run("table typed", func(t *testing.T) {
		sql := "SELECT id, ratio, label, big, at, notes FROM gox_f2 ORDER BY id"
		resp := ds.query(ctx, backend.DataQuery{RefID: "A",
			JSON: json.RawMessage(`{"sql":` + mustJSON(sql) + `,"format":"table"}`)})
		if resp.Error != nil {
			t.Fatalf("query error: %v", resp.Error)
		}
		frame := resp.Frames[0]

		wantTypes := []data.FieldType{
			data.FieldTypeNullableInt64,
			data.FieldTypeNullableFloat64,
			data.FieldTypeNullableString,
			data.FieldTypeNullableString, // big NUMBER → string in table (ADR-8)
			data.FieldTypeNullableTime,
			data.FieldTypeNullableString, // CLOB → string
		}
		if len(frame.Fields) != len(wantTypes) {
			t.Fatalf("fields = %d, want %d", len(frame.Fields), len(wantTypes))
		}
		for i, ft := range wantTypes {
			if frame.Fields[i].Type() != ft {
				t.Errorf("field %s type = %v, want %v", frame.Fields[i].Name, frame.Fields[i].Type(), ft)
			}
		}
		// ORDER BY id: rows are 7 (all NULL), 8, 9, 42 — the full row is last.
		if got := *frame.Fields[0].At(3).(*int64); got != 42 {
			t.Errorf("id[3] = %d, want 42", got)
		}
		if got := *frame.Fields[1].At(3).(*float64); got != 3.14 {
			t.Errorf("ratio[3] = %v, want 3.14", got)
		}
		if !frame.Fields[1].NilAt(0) {
			t.Errorf("ratio[0] (NULL row) should be NULL")
		}
		if got := *frame.Fields[3].At(3).(*string); got != "12345678901234567890" {
			t.Errorf("big[3] = %q, want full precision preserved", got)
		}
		if got := *frame.Fields[5].At(3).(*string); got != "clob-note" {
			t.Errorf("notes[3] = %q, want CLOB text", got)
		}
		if frame.Meta == nil || frame.Meta.ExecutedQueryString != sql {
			t.Errorf("meta = %+v, want ExecutedQueryString", frame.Meta)
		}
	})

	t.Run("large number float in time series", func(t *testing.T) {
		resp := ds.query(ctx, backend.DataQuery{RefID: "A",
			JSON: json.RawMessage(`{"sql":"SELECT at AS t, big AS v FROM gox_f2 ORDER BY at","format":"auto"}`)})
		if resp.Error != nil {
			t.Fatalf("query error: %v", resp.Error)
		}
		frame := resp.Frames[0]
		if frame.Meta == nil || frame.Meta.Type != data.FrameTypeTimeSeriesWide {
			t.Fatalf("meta = %+v, want TimeSeriesWide", frame.Meta)
		}
		if len(frame.Fields) != 2 || frame.Fields[1].Type() != data.FieldTypeNullableFloat64 {
			t.Errorf("fields = %d (type %v), want [t, v *float64] (ts large-number float)",
				len(frame.Fields), frame.Fields[1].Type())
		}
		// ids 42 and 8 share `at` → duplicate time kept; ids 7 and 9 have NULL
		// `at` → skipped (§7.3.6/7.3.7).
		if frame.Rows() != 2 {
			t.Errorf("rows = %d, want 2 (duplicate time kept, NULL-time rows skipped)", frame.Rows())
		}
	})
}

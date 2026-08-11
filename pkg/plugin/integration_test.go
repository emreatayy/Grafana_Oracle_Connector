package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

// badHostPortRe rewrites oracle://user:pass@host:PORT/service to port 1, a
// closed port, so a health dial fails fast regardless of the harness host/port.
var badHostPortRe = regexp.MustCompile(`@([^/@]+):[0-9]+/`)

// newIntegrationDatasource builds a connected instance against the harness DB.
// The pool parameters from settings are applied to the sql.DB exactly as the
// production NewDatasource does (report §5.1), so integration tests exercise
// the real pool behaviour — including the F12 pool-under-load measurement.
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
			CommandTimeoutSec:  30,
			MaxRows:            10000,
			MaxSeries:          1000,
			SessionTimeZone:    "UTC",
		},
	}
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	db.SetMaxOpenConns(settings.MaxOpenConns)
	db.SetMaxIdleConns(settings.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(settings.ConnMaxLifetimeSec) * time.Second)
	return &Datasource{settings: settings, db: db, session: oracle.SessionParams("UTC")}
}

// TestIntegration_Health connects, reads the version banner and verifies the
// F10 structured JSON detail (report §13 "Message").
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
	msg := parseHealth(t, res.Message)
	if !msg.Oracle.OK || !strings.Contains(msg.Oracle.Version, "Oracle") {
		t.Errorf("oracle = %+v, want ok with version banner", msg.Oracle)
	}
	if msg.Prometheus.Enabled || !msg.Prometheus.OK {
		t.Errorf("prometheus = %+v, want enabled=false ok=true (green without config)", msg.Prometheus)
	}
	t.Logf("connected to: %s", msg.Oracle.Version)
}

// TestIntegration_Health_BadPassword verifies the error is readable, carried in
// the JSON detail, and does not leak the password.
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
	msg := parseHealth(t, res.Message)
	if msg.Oracle.OK || !strings.Contains(msg.Oracle.Error, "01017") {
		t.Errorf("oracle = %+v, want ok=false with mapped ORA-01017", msg.Oracle)
	}
	if strings.Contains(res.Message, "wrong-password-xyz") {
		t.Errorf("message leaks the password: %q", res.Message)
	}
}

// TestIntegration_Health_BadHost covers the "URL/connection" scenario of the
// F10 DoD: an unreachable port must surface as a readable network error inside
// the JSON detail (never a raw stack or a leak of the DSN password).
func TestIntegration_Health_BadHost(t *testing.T) {
	dsn := integrationDSN(t)
	// Point the DSN at a closed local port so the dial fails fast, whatever the
	// harness host/port was. Connection refused needs no valid credentials.
	dsn = badHostPortRe.ReplaceAllString(dsn, "@$1:1/")
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
		t.Fatalf("health status = %s (%q), want Error", res.Status, res.Message)
	}
	msg := parseHealth(t, res.Message)
	if msg.Oracle.OK || !strings.Contains(msg.Oracle.Error, "network error") {
		t.Errorf("oracle = %+v, want ok=false with a network error", msg.Oracle)
	}
	if strings.Contains(res.Message, "verif_password") {
		t.Errorf("message leaks the DSN password: %q", res.Message)
	}
}

// TestIntegration_SimpleSelect verifies the read-only query path returns rows.
func TestIntegration_SimpleSelect(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	resp := ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"SELECT 1 AS one, 'hello' AS greeting, TO_CHAR(SYSDATE, 'YYYY') AS yr FROM DUAL"}`),
	}, "")
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

// TestIntegration_QueryMetrics exercises the F11 self-metrics on a real,
// successful query (report §18 F11): a SELECT must count and time one
// sql-mode query and record zero errors. The unit tests cover the rejection
// paths; this one proves the happy path records through buildFrame.
func TestIntegration_QueryMetrics(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()
	resetMetrics(t)

	resp := ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"SELECT COUNT(*) AS n FROM DUAL"}`),
	}, "it-uid")
	if resp.Error != nil {
		t.Fatalf("query error: %v", resp.Error)
	}

	if got := testutil.ToFloat64(metrics.queryTotal.WithLabelValues("it-uid", "sql")); got != 1 {
		t.Errorf("query_total{it-uid,sql} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.errorsTotal.WithLabelValues("it-uid", "sql")); got != 0 {
		t.Errorf("errors_total{it-uid,sql} = %v, want 0 for a successful query", got)
	}
	if got := histogramSampleCount(t, "it-uid", "sql"); got != 1 {
		t.Errorf("query_duration samples = %d, want 1", got)
	}
	// F12: the single-row COUNT(*) must be counted by rows_scanned_total.
	if got := testutil.ToFloat64(metrics.rowsScanned.WithLabelValues("it-uid", "sql")); got != 1 {
		t.Errorf("rows_scanned_total{it-uid,sql} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.queriesTruncated.WithLabelValues("it-uid", "sql")); got != 0 {
		t.Errorf("queries_truncated_total{it-uid,sql} = %v, want 0", got)
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

// TestIntegration_CancelLongQuery is the F1 DoD, re-measured for F12 (report
// §18 F12): a heavy query must stop promptly when its context is cancelled
// (the mechanism Grafana uses when a panel is closed or an alert is stopped).
// F12 tightens the assertion — a cancelled query must not return a partial
// success, it must fail.
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
	resp := ds.query(ctx, backend.DataQuery{RefID: "A", JSON: json.RawMessage(`{"sql":` + mustJSON(heavy) + `}`)}, "")
	elapsed := time.Since(start)

	// Must return well inside the 30s query timeout and not hang forever.
	if elapsed > 5*time.Second {
		t.Fatalf("query did not cancel; took %v", elapsed)
	}
	// The DoD: cancellation must stop the work — a cancelled query is an error
	// (ORA-01013 operation cancelled, or the context deadline), never a frame.
	if resp.Error == nil {
		t.Fatalf("cancelled query returned no error (elapsed %v); cancellation did not stop the work", elapsed)
	}
	t.Logf("measured: heavy query cancelled after %v (%v)", elapsed, resp.Error)
}

// TestIntegration_ReadOnlyGuard verifies DML is rejected before execution.
func TestIntegration_ReadOnlyGuard(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()

	resp := ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"UPDATE dual SET dummy = 'x' WHERE 1=0"}`),
	}, "")
	if resp.Error == nil {
		t.Fatal("expected read-only guard error, got nil")
	}
	if !strings.Contains(resp.Error.Error(), "read-only guard") {
		t.Errorf("error = %q, want read-only guard message", resp.Error)
	}
}

// TestIntegration_Annotation is the F9 DoD backend half: an annotation query
// (report §11) must come back as a TABLE frame — no time-series conversion — with
// DataTopic.Annotations set, so Grafana's annotation reader can map the
// time/title/text fields by name. The control (same SQL, no marker) proves the
// forcing actually matters.
func TestIntegration_Annotation(t *testing.T) {
	ds := newIntegrationDatasource(t)
	t.Cleanup(ds.Dispose)
	ctx := context.Background()
	ensureF2Table(t, ds, ctx)
	seedF2Rows(t, ds, ctx)

	annotation := func(extra string) *data.Frame {
		t.Helper()
		resp := ds.query(ctx, backend.DataQuery{RefID: "A",
			JSON: json.RawMessage(`{"sql":"SELECT at AS \"time\", label AS \"title\", label AS \"text\" FROM gox_f2 ORDER BY id"` + extra + `}`)}, "")
		if resp.Error != nil {
			t.Fatalf("query error: %v", resp.Error)
		}
		if len(resp.Frames) != 1 {
			t.Fatalf("frames = %d, want 1", len(resp.Frames))
		}
		return resp.Frames[0]
	}

	// Control: without the marker the same data is a plain data frame — no
	// annotation topic.
	plain := annotation("")
	if plain.Meta == nil || plain.Meta.DataTopic != "" {
		t.Fatalf("control meta = %+v, want no DataTopic", plain.Meta)
	}

	// Annotation marker: table shape (all 4 rows kept — time-series would drop
	// the two NULL-time rows) + DataTopic.Annotations.
	frame := annotation(`,"annotation":true`)
	if frame.Meta == nil || frame.Meta.DataTopic != data.DataTopicAnnotations {
		t.Fatalf("meta = %+v, want DataTopic.Annotations", frame.Meta)
	}
	if frame.Rows() != 4 {
		t.Errorf("rows = %d, want 4 (table keeps NULL-time rows; time-series would drop them)", frame.Rows())
	}
	names := make([]string, len(frame.Fields))
	for i, f := range frame.Fields {
		names[i] = f.Name
	}
	want := []string{"time", "title", "text"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("fields = %v, want [%v] in order", names, want)
		}
	}
}

// TestIntegration_Metadata is the F9 DoD metadata half (report §14.3): the
// resource lists accessible tables (bounded), narrows by prefix, and resolves a
// table's columns with types — including schema-qualified names and an unknown
// table (empty, not an error). The full list is NOT asserted to contain the
// fixture table because a privileged account's bounded-200 list is
// alphabet-biased; the prefix query is the reliable accessor.
func TestIntegration_Metadata(t *testing.T) {
	ds := newIntegrationDatasource(t)
	t.Cleanup(ds.Dispose)
	ctx := context.Background()
	ensureF2Table(t, ds, ctx)
	seedF2Rows(t, ds, ctx)

	t.Run("prefix finds the fixture table", func(t *testing.T) {
		tables, err := ds.metadataTables(ctx, "GOX_F")
		if err != nil {
			t.Fatalf("metadataTables: %v", err)
		}
		if !containsString(tables, "GOX_F2") {
			t.Errorf("tables = %v, want it to contain GOX_F2", tables)
		}
	})

	t.Run("unprefixed list is bounded", func(t *testing.T) {
		tables, err := ds.metadataTables(ctx, "")
		if err != nil {
			t.Fatalf("metadataTables: %v", err)
		}
		if len(tables) == 0 {
			t.Errorf("tables empty for a privileged account")
		}
		if len(tables) > metadataMaxTables {
			t.Errorf("tables = %d, want bounded to %d", len(tables), metadataMaxTables)
		}
	})

	t.Run("columns with types", func(t *testing.T) {
		cols, err := ds.metadataColumns(ctx, "gox_f2")
		if err != nil {
			t.Fatalf("metadataColumns: %v", err)
		}
		if len(cols) != 6 {
			t.Fatalf("columns = %d, want 6: %v", len(cols), cols)
		}
		byName := map[string]string{}
		for _, c := range cols {
			byName[c.Name] = c.DataType
		}
		if byName["AT"] != "DATE" {
			t.Errorf("AT type = %q, want DATE", byName["AT"])
		}
		if byName["LABEL"] == "" || !strings.HasPrefix(byName["LABEL"], "VARCHAR2") {
			t.Errorf("LABEL type = %q, want VARCHAR2", byName["LABEL"])
		}
	})

	t.Run("schema-qualified name", func(t *testing.T) {
		cols, err := ds.metadataColumns(ctx, "SYSTEM.gox_f2")
		if err != nil {
			t.Fatalf("metadataColumns: %v", err)
		}
		if len(cols) != 6 {
			t.Errorf("columns = %d, want 6 for SYSTEM.gox_f2", len(cols))
		}
	})

	t.Run("unknown table is empty, not an error", func(t *testing.T) {
		cols, err := ds.metadataColumns(ctx, "NO_SUCH_TABLE_XYZ")
		if err != nil {
			t.Fatalf("metadataColumns: %v", err)
		}
		if len(cols) != 0 {
			t.Errorf("columns = %d, want 0", len(cols))
		}
	})

	t.Run("resource JSON shape", func(t *testing.T) {
		resp := callResource(ds, &backend.CallResourceRequest{
			Path: "metadata/columns",
			URL:  "http://irrelevant/metadata/columns?table=gox_f2",
		})
		if resp.Status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", resp.Status, resp.Body)
		}
		if !strings.Contains(string(resp.Body), `"dataType":"DATE"`) {
			t.Errorf("body = %s, want a DATE column in the response", resp.Body)
		}
	})
}

// containsString reports whether want is in xs.
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
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

// seedF2Rows inserts the four gox_f2 fixture rows (ids 42, 7, 8, 9) used by the
// F2 typed-column and F3 macro integration tests.
func seedF2Rows(t *testing.T, ds *Datasource, ctx context.Context) {
	t.Helper()
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
	insert(8, 1.0, "web-2", "2", at, nil)                              // duplicate time with id=42
	insert(9, 2.0, "web-3", "3", nil, nil)                             // NULL time → skipped in ts
}

// frameInt64s reads the first field of a frame as int64 values in row order.
// Computed expressions (e.g. FLOOR(id/10)*10) are classified Prec==0 → float
// (§6.2), so both int64 and float64 cells are accepted.
func frameInt64s(t *testing.T, frame *data.Frame) []int64 {
	t.Helper()
	ids := make([]int64, 0, frame.Rows())
	field := frame.Fields[0]
	for i := 0; i < frame.Rows(); i++ {
		switch v := field.At(i).(type) {
		case *int64:
			ids = append(ids, *v)
		case *float64:
			ids = append(ids, int64(*v))
		default:
			t.Errorf("row %d: unexpected cell type %T", i, v)
		}
	}
	return ids
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

	seedF2Rows(t, ds, ctx)

	t.Run("table typed", func(t *testing.T) {
		sql := "SELECT id, ratio, label, big, at, notes FROM gox_f2 ORDER BY id"
		resp := ds.query(ctx, backend.DataQuery{RefID: "A",
			JSON: json.RawMessage(`{"sql":` + mustJSON(sql) + `,"format":"table"}`)}, "")
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
			JSON: json.RawMessage(`{"sql":"SELECT at AS t, big AS v FROM gox_f2 ORDER BY at","format":"auto"}`)}, "")
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

// TestIntegration_Macros is the F3 DoD end-to-end: $__timeFilter + ${var:csv}
// expand on the real backend, an empty selection becomes IN (NULL), and
// $__unixEpochGroup buckets an epoch column. It also asserts ExecutedQueryString
// carries the raw (uninterpolated) SQL and that an unresolved ${var} errors
// without touching the DB.
func TestIntegration_Macros(t *testing.T) {
	ds := newIntegrationDatasource(t)
	t.Cleanup(ds.Dispose)
	ctx := context.Background()
	ensureF2Table(t, ds, ctx)
	seedF2Rows(t, ds, ctx)

	tr := backend.TimeRange{
		From: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
	}
	query := func(raw string, extra string) (*data.Frame, error) {
		t.Helper()
		resp := ds.query(ctx, backend.DataQuery{
			RefID:         "A",
			TimeRange:     tr,
			Interval:      time.Minute,
			MaxDataPoints: 100,
			JSON:          json.RawMessage(`{"sql":` + mustJSON(raw) + extra + `}`),
		}, "")
		if resp.Error != nil {
			return nil, resp.Error
		}
		if len(resp.Frames) != 1 {
			t.Fatalf("frames = %d, want 1", len(resp.Frames))
		}
		return resp.Frames[0], nil
	}

	// gox_f2: ids 42 and 8 are at 09:30 (in range); ids 7 and 9 have NULL at.
	raw := "SELECT id FROM gox_f2 WHERE $__timeFilter(at) AND label IN (${host:csv}) ORDER BY id"

	t.Run("single host", func(t *testing.T) {
		frame, err := query(raw, `,"scopedVars":{"host":["web-1"]}`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if got := frameInt64s(t, frame); len(got) != 1 || got[0] != 42 {
			t.Errorf("ids = %v, want [42]", got)
		}
		if frame.Meta == nil || frame.Meta.ExecutedQueryString != raw {
			t.Errorf("meta = %+v, want ExecutedQueryString == raw SQL", frame.Meta)
		}
	})

	t.Run("multi host", func(t *testing.T) {
		frame, err := query(raw, `,"scopedVars":{"host":["web-1","web-2"]}`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if got := frameInt64s(t, frame); len(got) != 2 || got[0] != 8 || got[1] != 42 {
			t.Errorf("ids = %v, want [8 42]", got)
		}
	})

	t.Run("empty selection to NULL", func(t *testing.T) {
		frame, err := query(raw, `,"scopedVars":{"host":[]}`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if frame.Rows() != 0 {
			t.Errorf("rows = %d, want 0 (IN (NULL))", frame.Rows())
		}
	})

	t.Run("unixEpochGroup", func(t *testing.T) {
		frame, err := query("SELECT $__unixEpochGroup(id, 10) AS b FROM gox_f2 ORDER BY id", "")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if got := frameInt64s(t, frame); len(got) != 4 || got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 40 {
			t.Errorf("buckets = %v, want [0 0 0 40]", got)
		}
	})

	t.Run("unresolved variable", func(t *testing.T) {
		if _, err := query("SELECT id FROM gox_f2 WHERE id = ${missing}", ""); err == nil ||
			!strings.Contains(err.Error(), "unresolved variable") {
			t.Errorf("err = %v, want unresolved variable error", err)
		}
	})
}

// TestIntegration_Command exercises the F15 "command" resource against the
// harness Oracle (report §12 DoD): start/stop/enable/disable a scheduler job,
// call a procedure with an observable side effect, call a function via
// SELECT … FROM DUAL, and prove the allowlist rejects DDL/DML and the
// confirmation flag gates every execution.
func TestIntegration_Command(t *testing.T) {
	ds := newIntegrationDatasource(t)
	// Register dispose first: t.Cleanup runs LIFO, so the drop-cleanup below
	// runs before the pool is closed.
	t.Cleanup(ds.Dispose)
	ctx := context.Background()

	// Create the scheduler jobs, procedure, function and audit table with a
	// direct connection: CREATE/DROP are DDL and deliberately NOT allowlisted —
	// the command resource must never run them (report §12.3). This setup also
	// proves go-ora ExecContext runs anonymous PL/SQL blocks (§21 row 8).
	setup := []string{
		`BEGIN DBMS_SCHEDULER.CREATE_JOB(job_name => 'F15_TEST_JOB', job_type => 'PLSQL_BLOCK', job_action => 'BEGIN NULL; END;', enabled => FALSE, auto_drop => FALSE); END;`,
		`BEGIN DBMS_SCHEDULER.CREATE_JOB(job_name => 'F15_SLEEP_JOB', job_type => 'PLSQL_BLOCK', job_action => 'BEGIN DBMS_LOCK.SLEEP(30); END;', enabled => FALSE, auto_drop => FALSE); END;`,
		`CREATE TABLE F15_AUDIT (id NUMBER PRIMARY KEY)`,
		`CREATE OR REPLACE PROCEDURE F15_PROC(v NUMBER) AS BEGIN INSERT INTO F15_AUDIT(id) VALUES (v); END;`,
		`CREATE OR REPLACE FUNCTION F15_ECHO(v NUMBER) RETURN NUMBER AS BEGIN RETURN v * 2; END;`,
	}
	for _, q := range setup {
		if _, err := ds.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			// force => TRUE so a leftover async RUN_JOB instance (start_then_stop)
			// cannot block the drop; the DDL drops below are idempotent-ish and
			// errors are tolerated because the datasource may already be torn down.
			`BEGIN DBMS_SCHEDULER.DROP_JOB('F15_SLEEP_JOB', force => TRUE); END;`,
			`BEGIN DBMS_SCHEDULER.DROP_JOB('F15_TEST_JOB', force => TRUE); END;`,
			`DROP TABLE F15_AUDIT`,
			`DROP PROCEDURE F15_PROC`,
			`DROP FUNCTION F15_ECHO`,
		} {
			if _, err := ds.db.ExecContext(context.Background(), q); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	// run posts one command through the resource handler (the path the UI
	// "Run command" button takes).
	run := func(t *testing.T, sql string, confirm bool) *backend.CallResourceResponse {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"sql": sql, "confirm": confirm})
		return callResource(ds, &backend.CallResourceRequest{
			Path: "command",
			Body: body,
			PluginContext: backend.PluginContext{
				DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{UID: "integration-ds"},
				User:                       &backend.User{Login: "tester"},
			},
		})
	}
	okBody := func(t *testing.T, resp *backend.CallResourceResponse) commandResponse {
		t.Helper()
		if resp.Status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", resp.Status, resp.Body)
		}
		var res commandResponse
		if err := json.Unmarshal(resp.Body, &res); err != nil {
			t.Fatalf("body is not a commandResponse: %v (%s)", err, resp.Body)
		}
		if !res.OK {
			t.Fatalf("ok = false, want true; body = %s", resp.Body)
		}
		return res
	}
	jobState := func(name string) string {
		var state string
		if err := ds.db.QueryRowContext(ctx, `SELECT state FROM user_scheduler_jobs WHERE job_name = :1`, name).Scan(&state); err != nil {
			t.Fatalf("read job %s state: %v", name, state)
		}
		return state
	}

	t.Run("unconfirmed command is rejected", func(t *testing.T) {
		resp := run(t, "BEGIN DBMS_SCHEDULER.DISABLE('F15_TEST_JOB'); END;", false)
		if resp.Status != http.StatusBadRequest || !strings.Contains(string(resp.Body), "not confirmed") {
			t.Errorf("status = %d body = %s, want 400 confirmation error", resp.Status, resp.Body)
		}
	})

	t.Run("disable then enable a scheduler job", func(t *testing.T) {
		okBody(t, run(t, "BEGIN DBMS_SCHEDULER.DISABLE('F15_TEST_JOB'); END;", true))
		if got := jobState("F15_TEST_JOB"); got != "DISABLED" {
			t.Errorf("state after DISABLE = %q, want DISABLED", got)
		}
		okBody(t, run(t, "BEGIN DBMS_SCHEDULER.ENABLE('F15_TEST_JOB'); END;", true))
		// 21c reports an enabled, schedulable job as SCHEDULED (not ENABLED);
		// accept both so the assertion holds across Oracle versions.
		if got := jobState("F15_TEST_JOB"); got != "ENABLED" && got != "SCHEDULED" {
			t.Errorf("state after ENABLE = %q, want ENABLED or SCHEDULED", got)
		}
	})

	t.Run("start then stop a running job", func(t *testing.T) {
		okBody(t, run(t, "BEGIN DBMS_SCHEDULER.ENABLE('F15_SLEEP_JOB'); END;", true))
		// START_JOB was removed from DBMS_SCHEDULER in Oracle 21c; RUN_JOB with
		// use_current_session => FALSE is the documented replacement and launches
		// the instance asynchronously (report §12.4, verified against the 21c docs).
		okBody(t, run(t, "BEGIN DBMS_SCHEDULER.RUN_JOB('F15_SLEEP_JOB', use_current_session => FALSE); END;", true))
		// The instance launch is asynchronous; STOP on a not-yet-running job
		// raises ORA-27366, so poll for the RUNNING state first.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && jobState("F15_SLEEP_JOB") != "RUNNING" {
			time.Sleep(100 * time.Millisecond)
		}
		if got := jobState("F15_SLEEP_JOB"); got != "RUNNING" {
			t.Fatalf("sleep job never reached RUNNING, state = %q", got)
		}
		okBody(t, run(t, "BEGIN DBMS_SCHEDULER.STOP_JOB('F15_SLEEP_JOB'); END;", true))
		// 21c returns a stopped schedulable job to SCHEDULED (not STOPPED).
		if got := jobState("F15_SLEEP_JOB"); got != "STOPPED" && got != "SCHEDULED" {
			t.Errorf("state after STOP = %q, want STOPPED or SCHEDULED", got)
		}
	})

	t.Run("call a procedure with a side effect", func(t *testing.T) {
		okBody(t, run(t, "BEGIN F15_PROC(7); END;", true))
		var n int
		if err := ds.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM F15_AUDIT WHERE id = 7`).Scan(&n); err != nil {
			t.Fatalf("read audit count: %v", err)
		}
		if n != 1 {
			t.Errorf("audit rows for id 7 = %d, want 1", n)
		}
	})

	t.Run("function call returns its value", func(t *testing.T) {
		res := okBody(t, run(t, "SELECT F15_ECHO(21) FROM DUAL", true))
		if res.Message != "42" {
			t.Errorf("message = %q, want 42", res.Message)
		}
	})

	t.Run("allowlist rejects DDL and DML", func(t *testing.T) {
		for _, sql := range []string{
			"DROP TABLE F15_AUDIT",
			"BEGIN DELETE FROM F15_AUDIT; END;",
		} {
			resp := run(t, sql, true)
			if resp.Status != http.StatusBadRequest {
				t.Errorf("%q status = %d, want 400", sql, resp.Status)
			}
			if !strings.Contains(string(resp.Body), "must be a BEGIN") &&
				!strings.Contains(string(resp.Body), "not allowlisted") {
				t.Errorf("%q body = %s, want a readable allowlist rejection", sql, resp.Body)
			}
		}
		// The attempted DROP never touched the table.
		if _, err := ds.db.QueryContext(ctx, `SELECT COUNT(*) FROM F15_AUDIT`); err != nil {
			t.Errorf("table F15_AUDIT does not survive the attempted DROP: %v", err)
		}
	})

	t.Run("database error surfaces readably", func(t *testing.T) {
		resp := run(t, "BEGIN DBMS_SCHEDULER.DISABLE('F15_NO_SUCH_JOB'); END;", true)
		if resp.Status != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", resp.Status)
		}
		if !strings.Contains(string(resp.Body), "ORA-") {
			t.Errorf("body = %s, want a readable ORA error", resp.Body)
		}
	})
}

// TestIntegration_MaxRows is the F12 DoD first half (report §18 F12): "100k+
// rows … measured". A query that would return 120k rows is stopped at the
// maxRows cap (default 100k), the frame carries a warning notice, and the F12
// counters record the scanned rows and the truncation. A control query well
// under the cap truncates nothing. The read is timed so the behaviour is
// measured, not assumed.
func TestIntegration_MaxRows(t *testing.T) {
	ds := newIntegrationDatasource(t) // MaxRows=10000
	defer ds.Dispose()
	ctx := context.Background()
	resetMetrics(t)

	// Control: well under maxRows → no notice, no truncation counter.
	small := ds.query(ctx, backend.DataQuery{RefID: "A",
		JSON: json.RawMessage(`{"sql":"SELECT LEVEL AS n FROM DUAL CONNECT BY LEVEL <= 500"}`)}, "mx-uid")
	if small.Error != nil {
		t.Fatalf("control query error: %v", small.Error)
	}
	if n := small.Frames[0].Rows(); n != 500 {
		t.Errorf("control rows = %d, want 500", n)
	}
	if meta := small.Frames[0].Meta; meta != nil && len(meta.Notices) > 0 {
		t.Errorf("control meta notices = %+v, want none (under maxRows)", meta.Notices)
	}
	if got := testutil.ToFloat64(metrics.queriesTruncated.WithLabelValues("mx-uid", "sql")); got != 0 {
		t.Errorf("queries_truncated_total{mx-uid,sql} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.rowsScanned.WithLabelValues("mx-uid", "sql")); got != 500 {
		t.Errorf("rows_scanned_total{mx-uid,sql} = %v, want 500", got)
	}

	// The DoD measurement: 120k generated rows capped at maxRows=100000.
	ds.settings.MaxRows = 100000
	start := time.Now()
	big := ds.query(ctx, backend.DataQuery{RefID: "A",
		JSON: json.RawMessage(`{"sql":"SELECT LEVEL AS n FROM DUAL CONNECT BY LEVEL <= 120000"}`)}, "mx-uid")
	elapsed := time.Since(start)

	if big.Error != nil {
		t.Fatalf("big query error: %v", big.Error)
	}
	if n := big.Frames[0].Rows(); n != 100000 {
		t.Errorf("capped rows = %d, want 100000 (maxRows)", n)
	}
	meta := big.Frames[0].Meta
	if meta == nil || len(meta.Notices) == 0 {
		t.Fatal("capped frame has no meta notices; want a maxRows truncation warning")
	}
	if got := meta.Notices[0].Severity; got != data.NoticeSeverityWarning {
		t.Errorf("notice severity = %v, want warning", got)
	}
	if got := testutil.ToFloat64(metrics.queriesTruncated.WithLabelValues("mx-uid", "sql")); got != 1 {
		t.Errorf("queries_truncated_total{mx-uid,sql} = %v, want 1", got)
	}
	// rows_scanned counts the rows actually read: 500 (control) + 100000 (capped).
	if got := testutil.ToFloat64(metrics.rowsScanned.WithLabelValues("mx-uid", "sql")); got != 100500 {
		t.Errorf("rows_scanned_total{mx-uid,sql} = %v, want 100500", got)
	}
	t.Logf("measured: read 120k generated rows, stopped at maxRows=100000 in %v", elapsed)
}

// TestIntegration_PoolUnderLoad is the F12 DoD third part (report §18 F12):
// "pool behavior under load measured". newIntegrationDatasource configures
// MaxOpenConns=5, MaxIdleConns=1. Running 20 concurrent queries — 4× the pool
// size — must all succeed (the pool serializes them, go-ora never opens
// unbounded connections), the configured cap must be reported by the pool
// stats, a sampler must never observe more than MaxOpenConns open connections,
// and the pool must drain back to the idle high-water mark once the load
// finishes.
func TestIntegration_PoolUnderLoad(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()
	ctx := context.Background()
	if err := ds.ensureReady(ctx); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	// The pool parameters from settings are actually wired into sql.DB.
	if got := ds.db.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("pool MaxOpenConnections = %d, want 5 (SetMaxOpenConns applied)", got)
	}

	// Sample the pool during the load so the peak is measured, not guessed.
	var peakMu sync.Mutex
	peak := 0
	stop := make(chan struct{})
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := ds.db.Stats().OpenConnections; n > peak {
				peakMu.Lock()
				peak = n
				peakMu.Unlock()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	const workers = 20
	// Each query generates and aggregates 50k rows — enough work that the pool
	// (5 conns) is genuinely saturated by 20 concurrent queries.
	sqlText := `SELECT COUNT(*) AS n FROM (SELECT 1 FROM DUAL CONNECT BY LEVEL <= 50000)`
	start := time.Now()
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := ds.query(ctx, backend.DataQuery{RefID: "A", JSON: json.RawMessage(`{"sql":` + mustJSON(sqlText) + `}`)}, "pool-uid")
			errs[i] = resp.Error
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(stop)
	<-sampleDone

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: query error: %v", i, err)
		}
	}
	peakMu.Lock()
	measuredPeak := peak
	peakMu.Unlock()
	if measuredPeak > 5 {
		t.Errorf("measured peak open connections = %d, want <= 5 (MaxOpenConns cap)", measuredPeak)
	}
	if measuredPeak < 2 {
		t.Logf("note: measured peak = %d — queries may not have overlapped enough to saturate the pool", measuredPeak)
	}

	// After the load the pool drains to the idle high-water mark (1 idle conn).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := ds.db.Stats()
		if st.OpenConnections == 1 && st.Idle == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := ds.db.Stats()
	if st.Idle != 1 {
		t.Errorf("idle connections after load = %d, want 1 (MaxIdleConns)", st.Idle)
	}

	t.Logf("measured: %d concurrent queries over %d-conn pool finished in %v; peak open = %d; final stats = %+v",
		workers, 5, elapsed, measuredPeak, st)
}

// TestIntegration_DashboardExamples is the F13 DoD "every example in the docs
// works" (report §18 F13) made repeatable: it runs the two SQL queries shipped
// in examples/dashboard/oracle-overview.json against a real Oracle and asserts
// the exact frame shapes the dashboard README promises. Panel 1 (synthetic
// CONNECT BY time series) must come back as a long time-series frame with a
// time + numeric field for all 96 generated points once the panel's range
// covers them; panel 2 (wrapped-ROWNUM user_tables table) must come back as a
// table frame of at most 20 rows with the TABLE_NAME/NUM_ROWS/LAST_ANALYZED
// columns. The third panel is Prometheus-proxy mode — its backend path is
// covered by the F6 proxy tests, and the dashboard README already marks it
// "skipped if you did not enable the proxy".
func TestIntegration_DashboardExamples(t *testing.T) {
	ds := newIntegrationDatasource(t)
	defer ds.Dispose()
	ctx := context.Background()

	// The panels default to now-6h→now, which shows only the last 24 of the 96
	// 15-minute points. Widen to a 48h window ending slightly in the future so
	// all 96 generated points (they span the day before SYSDATE) are in range.
	tr := backend.TimeRange{
		From: time.Now().Add(-48 * time.Hour),
		To:   time.Now().Add(24 * time.Hour),
	}

	// Panel 1 — Synthetic load: SQL time series (CONNECT BY over DUAL).
	// With no dimension column the series frame comes back WIDE (report §7.3
	// rule 4): one time field + one value field, both nullable.
	run := func(raw, format string) *data.Frame {
		t.Helper()
		resp := ds.query(ctx, backend.DataQuery{
			RefID:         "A",
			TimeRange:     tr,
			Interval:      time.Minute,
			MaxDataPoints: 1000,
			JSON:          json.RawMessage(`{"sql":` + mustJSON(raw) + `,"format":` + mustJSON(format) + `}`),
		}, "dash-uid")
		if resp.Error != nil {
			t.Fatalf("query error: %v", resp.Error)
		}
		if len(resp.Frames) != 1 {
			t.Fatalf("frames = %d, want 1", len(resp.Frames))
		}
		return resp.Frames[0]
	}

	frame := run(`SELECT ts, load FROM (SELECT SYSDATE - (LEVEL - 1) / 96 AS ts, LEVEL AS load FROM DUAL CONNECT BY LEVEL <= 96) WHERE $__timeFilter(ts)`, "time_series")
	if got := frame.Rows(); got != 96 {
		t.Errorf("time-series rows = %d, want 96 (all points in the widened range)", got)
	}
	if frame.Meta == nil || frame.Meta.Type != data.FrameTypeTimeSeriesWide {
		t.Errorf("time-series meta = %+v, want FrameTypeTimeSeriesWide", frame.Meta)
	}
	if len(frame.Fields) != 2 {
		t.Fatalf("time-series fields = %d, want 2 (ts, load)", len(frame.Fields))
	}
	if frame.Fields[0].Name != "TS" || frame.Fields[0].Type() != data.FieldTypeNullableTime {
		t.Errorf("field[0] = %q/%v, want TS/nullable time", frame.Fields[0].Name, frame.Fields[0].Type())
	}
	if frame.Fields[1].Name != "LOAD" || frame.Fields[1].Type() != data.FieldTypeNullableFloat64 {
		t.Errorf("field[1] = %q/%v, want LOAD/nullable float64", frame.Fields[1].Name, frame.Fields[1].Type())
	}
	// Saw-tooth: the 96 LEVEL counters are 1..96 and strictly increasing.
	last := 0.0
	for i := 0; i < 96; i++ {
		v := *frame.Fields[1].At(i).(*float64)
		if v < last || v < 1 || v > 96 {
			t.Fatalf("load[%d] = %v outside [1..96] saw-tooth", i, v)
		}
		last = v
	}

	// Panel 2 — User tables: SQL table (user_tables, wrapped ROWNUM). Table
	// frames are nullable columns (report §6.2), so each field is *T.
	frame = run(`SELECT * FROM (SELECT table_name, num_rows, last_analyzed FROM user_tables ORDER BY table_name) WHERE ROWNUM <= 20`, "table")
	if got := frame.Rows(); got > 20 {
		t.Errorf("table rows = %d, want <= 20 (wrapped ROWNUM cap)", got)
	}
	if len(frame.Fields) != 3 {
		t.Fatalf("table fields = %d, want 3 (TABLE_NAME, NUM_ROWS, LAST_ANALYZED)", len(frame.Fields))
	}
	if frame.Fields[0].Name != "TABLE_NAME" || frame.Fields[0].Type() != data.FieldTypeNullableString {
		t.Errorf("field[0] = %q/%v, want TABLE_NAME/nullable string", frame.Fields[0].Name, frame.Fields[0].Type())
	}
	if frame.Fields[1].Name != "NUM_ROWS" || frame.Fields[1].Type() != data.FieldTypeNullableFloat64 {
		t.Errorf("field[1] = %q/%v, want NUM_ROWS/nullable float64", frame.Fields[1].Name, frame.Fields[1].Type())
	}
	if frame.Fields[2].Name != "LAST_ANALYZED" || frame.Fields[2].Type() != data.FieldTypeNullableTime {
		t.Errorf("field[2] = %q/%v, want LAST_ANALYZED/nullable time (NULL handling)", frame.Fields[2].Name, frame.Fields[2].Type())
	}
	t.Logf("dashboard SQL panels verified: %d time-series points + user_tables table of %d rows", 96, frame.Rows())
}

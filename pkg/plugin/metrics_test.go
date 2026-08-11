package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// resetMetrics clears every label series so each test measures only its own
// queries. Tests in this package run sequentially (no t.Parallel), so resetting
// at test start gives deterministic counters.
func resetMetrics(t *testing.T) {
	t.Helper()
	metrics.queryTotal.Reset()
	metrics.errorsTotal.Reset()
	metrics.queryDuration.Reset()
	metrics.rowsScanned.Reset()
	metrics.queriesTruncated.Reset()
}

// gatheredFamilyNames returns the metric-family names on the process-wide
// prometheus.DefaultGatherer — exactly what the SDK diagnostics adapter exposes
// to Grafana (backend/serve.go).
func gatheredFamilyNames(t *testing.T) map[string]bool {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("DefaultGatherer.Gather: %v", err)
	}
	names := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	return names
}

// histogramSampleCount reads the observation count of the
// oracle_datasource_query_duration series matching the label pair.
func histogramSampleCount(t *testing.T, datasource, mode string) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("DefaultGatherer.Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "oracle_datasource_query_duration" {
			continue
		}
		for _, m := range mf.GetMetric() {
			gotDS, gotMode := "", ""
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "datasource":
					gotDS = l.GetValue()
				case "mode":
					gotMode = l.GetValue()
				}
			}
			if gotDS == datasource && gotMode == mode {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	t.Fatalf("no oracle_datasource_query_duration series for %s/%s", datasource, mode)
	return 0
}

// TestMetrics_RegisteredOnDefaultGatherer proves the F11 DoD plumbing: the
// metric set is registered on prometheus.DefaultRegisterer, so the SDK's
// diagnostics server (which gathers prometheus.DefaultGatherer) serves it to
// Grafana through the CollectMetrics RPC. The series must be visible to that
// gatherer after the first query.
func TestMetrics_RegisteredOnDefaultGatherer(t *testing.T) {
	resetMetrics(t)
	ds := newTestDatasource(t)
	defer ds.Dispose()

	// One guard-rejected query still counts and labels a series for every
	// family. The F12 counters (rows_scanned, queries_truncated) only gain a
	// series inside buildFrame — which the guard rejection never reaches — so
	// synthesize their {uid-1,sql} series at 0 to prove they are registered on
	// the same gatherer.
	ds.query(context.Background(), backend.DataQuery{
		RefID: "A",
		JSON:  json.RawMessage(`{"sql":"DELETE FROM important_table"}`),
	}, "uid-1")
	metrics.rowsScanned.WithLabelValues("uid-1", "sql").Add(0)
	metrics.queriesTruncated.WithLabelValues("uid-1", "sql").Add(0)

	names := gatheredFamilyNames(t)
	for _, want := range []string{
		"oracle_datasource_query_total",
		"oracle_datasource_errors_total",
		"oracle_datasource_query_duration",
		"oracle_datasource_rows_scanned_total",
		"oracle_datasource_queries_truncated_total",
	} {
		if !names[want] {
			t.Errorf("metric family %s not visible on the default gatherer (Grafana scrapes this)", want)
		}
	}
}

// TestQueryRecorder verifies the recorder's three effects: query_total counts
// every query, query_duration observes each completion, and errors_total only
// counts failed queries.
func TestQueryRecorder(t *testing.T) {
	resetMetrics(t)

	rec := newQueryRecorder("ds-9", "sql")
	rec.done(false)
	if got := testutil.ToFloat64(metrics.queryTotal.WithLabelValues("ds-9", "sql")); got != 1 {
		t.Errorf("query_total = %v, want 1", got)
	}
	if got := histogramSampleCount(t, "ds-9", "sql"); got != 1 {
		t.Errorf("query_duration samples = %d, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.errorsTotal.WithLabelValues("ds-9", "sql")); got != 0 {
		t.Errorf("errors_total = %v, want 0 for a successful query", got)
	}

	rec = newQueryRecorder("ds-9", "sql")
	rec.done(true)
	if got := testutil.ToFloat64(metrics.queryTotal.WithLabelValues("ds-9", "sql")); got != 2 {
		t.Errorf("query_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(metrics.errorsTotal.WithLabelValues("ds-9", "sql")); got != 1 {
		t.Errorf("errors_total = %v, want 1 for a failed query", got)
	}
	if got := histogramSampleCount(t, "ds-9", "sql"); got != 2 {
		t.Errorf("query_duration samples = %d, want 2", got)
	}
}

// TestQueryData_MetricsLabels proves the end-to-end label plumbing: the
// datasource UID from the request PluginContext and the per-query mode both
// reach the metric labels. Uses an unconnected datasource — the guard and the
// proxy gate reject before any DB touch, so no server is needed.
func TestQueryData_MetricsLabels(t *testing.T) {
	resetMetrics(t)
	ds := newTestDatasource(t)
	defer ds.Dispose()

	req := &backend.QueryDataRequest{
		PluginContext: backend.PluginContext{
			DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{UID: "uid-abc"},
		},
		Queries: []backend.DataQuery{
			{RefID: "A", JSON: json.RawMessage(`{"sql":"DELETE FROM important_table"}`)},
			{RefID: "B", JSON: json.RawMessage(`{"mode":"prometheus","promql":"up"}`)},
		},
	}
	if _, err := ds.QueryData(context.Background(), req); err != nil {
		t.Fatalf("QueryData: %v", err)
	}

	if got := testutil.ToFloat64(metrics.queryTotal.WithLabelValues("uid-abc", "sql")); got != 1 {
		t.Errorf("query_total{uid-abc,sql} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.errorsTotal.WithLabelValues("uid-abc", "sql")); got != 1 {
		t.Errorf("errors_total{uid-abc,sql} = %v, want 1 (guard rejection is a failed query)", got)
	}
	if got := testutil.ToFloat64(metrics.queryTotal.WithLabelValues("uid-abc", "prometheus")); got != 1 {
		t.Errorf("query_total{uid-abc,prometheus} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.errorsTotal.WithLabelValues("uid-abc", "prometheus")); got != 1 {
		t.Errorf("errors_total{uid-abc,prometheus} = %v, want 1 (proxy disabled)", got)
	}
}

// captureLogger is a minimal log.Logger that records every entry so tests can
// assert log hygiene (report §15, F11 DoD: no values/passwords in logs).
type captureLogger struct {
	mu   sync.Mutex
	logs []string
}

func (c *captureLogger) Debug(msg string, args ...interface{})    { c.record("debug", msg, args...) }
func (c *captureLogger) Info(msg string, args ...interface{})     { c.record("info", msg, args...) }
func (c *captureLogger) Warn(msg string, args ...interface{})     { c.record("warn", msg, args...) }
func (c *captureLogger) Error(msg string, args ...interface{})    { c.record("error", msg, args...) }
func (c *captureLogger) With(args ...interface{}) log.Logger      { return c }
func (c *captureLogger) Level() log.Level                         { return log.Debug }
func (c *captureLogger) FromContext(_ context.Context) log.Logger { return c }

func (c *captureLogger) record(level, msg string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fields := []string{level, msg}
	for _, a := range args {
		fields = append(fields, fmt.Sprintf("%v", a))
	}
	c.logs = append(c.logs, strings.Join(fields, " "))
}

// text returns the whole captured output (level + message + args per entry).
func (c *captureLogger) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.logs, "\n")
}

// captureDefaultLogger swaps the package log.DefaultLogger for the test and
// restores it afterwards. Safe because no test in this package runs in parallel.
func captureDefaultLogger(t *testing.T) *captureLogger {
	t.Helper()
	cl := &captureLogger{}
	orig := log.DefaultLogger
	log.DefaultLogger = cl
	t.Cleanup(func() { log.DefaultLogger = orig })
	return cl
}

// TestLogHygiene is the F11 DoD check on the query path: rejections and
// failures are logged at the right level, and the log entries never carry the
// SQL text or the configured password (report §15).
func TestLogHygiene(t *testing.T) {
	resetMetrics(t)
	cl := captureDefaultLogger(t)
	ds := newTestDatasource(t) // password "pw" in settings; DB unreachable

	// Guard rejection: Warn level, no full SQL, only the offending keyword.
	const badSQL = "DELETE FROM highly_secret_table"
	ds.query(context.Background(), backend.DataQuery{RefID: "A", JSON: json.RawMessage(`{"sql":"` + badSQL + `"}`)}, "uid-x")
	// Disposed datasource: Error level, no SQL, no password.
	ds.Dispose()
	ds.query(context.Background(), backend.DataQuery{RefID: "B", JSON: json.RawMessage(`{"sql":"SELECT * FROM customer"}`)}, "uid-x")

	text := cl.text()
	if !strings.Contains(text, "oracle query rejected") {
		t.Errorf("captured log missing the rejection entry:\n%s", text)
	}
	if strings.Contains(text, badSQL) {
		t.Errorf("guard-rejection log leaks the SQL %q:\n%s", badSQL, text)
	}
	if !strings.Contains(text, "oracle query failed") {
		t.Errorf("captured log missing the failure entry:\n%s", text)
	}
	if strings.Contains(text, "SELECT * FROM customer") {
		t.Errorf("failure log leaks the SQL:\n%s", text)
	}
	if strings.Contains(text, "pw") {
		t.Errorf("failure log leaks the datasource password:\n%s", text)
	}
}

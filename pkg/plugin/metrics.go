package plugin

// F11 — self-metrics (report §18 F11). The plugin exposes a small Prometheus
// metric set to Grafana: the SDK's diagnostics server gathers
// prometheus.DefaultGatherer (backend/serve.go) and Grafana serves the series
// at the per-plugin endpoint GET /metrics/plugins/emreatayy-oracle-datasource
// (Grafana's pluginMetricsEndpoint, backed by the CollectMetrics RPC) — the
// series are not merged into the server's own /metrics. The metrics are
// therefore registered on prometheus.DefaultRegisterer, once per plugin process
// (each backend plugin runs as its own child process).
//
// Labels are a fixed, bounded set — datasource UID and query mode — so every
// datasource and query path is distinguishable without unbounded cardinality
// (report §15). Values and passwords never appear in metric labels, help text
// or log messages. F12 adds two volume/limit counters (rows_scanned_total,
// queries_truncated_total) with the same labels — the observability half of the
// §18 F12 DoD.

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// metricsNamespace prefixes every metric the plugin exports
// (oracle_datasource_query_total, oracle_datasource_errors_total,
// oracle_datasource_query_duration — report §18 F11).
const metricsNamespace = "oracle_datasource"

// metricLabels identifies a query: the datasource UID and the query mode
// (sql | prometheus). Bounded by construction — one series per datasource×mode.
var metricLabels = []string{"datasource", "mode"}

// pluginMetrics is the self-metrics set (F11 core, F12 rows/truncation). The
// singleton is registered on prometheus.DefaultRegisterer by promauto at
// construction.
type pluginMetrics struct {
	queryTotal    *prometheus.CounterVec
	errorsTotal   *prometheus.CounterVec
	queryDuration *prometheus.HistogramVec
	// F12 (report §18 F12): rows_scanned_total counts the rows actually read
	// into a frame and queries_truncated_total counts the queries that hit the
	// maxRows limit — together they make the §18 F12 DoD ("100k+ rows … measured")
	// observable in Grafana on top of the F11 duration histogram.
	rowsScanned      *prometheus.CounterVec
	queriesTruncated *prometheus.CounterVec
}

// metrics is the process-wide metric set (report §18 F11).
var metrics = newPluginMetrics()

// newPluginMetrics builds and registers the metric set. promauto registers on
// the DefaultRegisterer, so the metrics are exposed to Grafana with no explicit
// wiring.
func newPluginMetrics() *pluginMetrics {
	reg := promauto.With(prometheus.DefaultRegisterer)
	return &pluginMetrics{
		queryTotal: reg.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "query_total",
			Help:      "Total number of datasource queries executed.",
		}, metricLabels),
		errorsTotal: reg.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "errors_total",
			Help:      "Total number of datasource query errors.",
		}, metricLabels),
		queryDuration: reg.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Name:      "query_duration",
			Help:      "Datasource query duration in seconds.",
			// Sub-second to 30s spans the fast DUAL probes and the slowest
			// time-series queries; everything above 30s lands in +Inf.
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, metricLabels),
		rowsScanned: reg.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "rows_scanned_total",
			Help:      "Total number of rows read into query frames.",
		}, metricLabels),
		queriesTruncated: reg.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "queries_truncated_total",
			Help:      "Total number of queries stopped early by the maxRows limit.",
		}, metricLabels),
	}
}

// queryRecorder times one query, counts it up front and finalises it exactly
// once. mode is the query-mode label (sql | prometheus), dsUID the datasource
// UID ("" when the request carries none).
type queryRecorder struct {
	dsUID   string
	mode    string
	started time.Time
}

// newQueryRecorder starts recording a query and increments query_total.
func newQueryRecorder(dsUID, mode string) *queryRecorder {
	metrics.queryTotal.WithLabelValues(dsUID, mode).Inc()
	return &queryRecorder{dsUID: dsUID, mode: mode, started: time.Now()}
}

// done records the duration and, when err is true, increments errors_total.
// It must be called exactly once, after the query has returned.
func (r *queryRecorder) done(err bool) {
	metrics.queryDuration.WithLabelValues(r.dsUID, r.mode).Observe(time.Since(r.started).Seconds())
	if err {
		metrics.errorsTotal.WithLabelValues(r.dsUID, r.mode).Inc()
	}
}

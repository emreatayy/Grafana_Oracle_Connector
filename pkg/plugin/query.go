package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"

	"github.com/emreatayy/oracle-datasource/pkg/convert"
	"github.com/emreatayy/oracle-datasource/pkg/interpolate"
	"github.com/emreatayy/oracle-datasource/pkg/prom"
)

// queryModel is the query payload sent by the frontend (report §4.3). F2 wires
// SQL, format, an optional explicit time column and a per-query large-number
// override. F3 adds scopedVars, the dashboard variables attached by
// applyTemplateVariables (empty for alert queries — §8.3). F4 adds mode, the
// per-query Oracle/Prometheus editor selection. F6 adds the Prometheus fields:
// promql, instant (query vs query_range), legendFormat and an optional step
// override. F9 adds annotation, set by the frontend when the query is a
// dashboard annotation target (report §11).
type queryModel struct {
	SQL             string         `json:"sql"`
	Format          string         `json:"format"`
	TimeColumn      string         `json:"timeColumn"`
	LargeNumberMode string         `json:"largeNumberMode"`
	ScopedVars      map[string]any `json:"scopedVars"`
	Mode            string         `json:"mode"`
	PromQL          string         `json:"promql"`
	Instant         bool           `json:"instant"`
	LegendFormat    string         `json:"legendFormat"`
	Step            string         `json:"step"`
	Annotation      bool           `json:"annotation"`
}

// readOnlyKeywords are the only words a read-only query may start with (ADR-7).
// Everything else is rejected before execution — fail closed, never guess.
var readOnlyKeywords = map[string]bool{
	"SELECT": true,
	"WITH":   true,
}

// labelValuesRe matches the Grafana query-variable syntax (report §8.5):
//
//	label_values(<label>)            -> every value of a label
//	label_values(<metric>, <label>)  -> a label's values among series matching a metric
//
// The regex is anchored so a real PromQL expression containing `label_values`
// as an identifier is never mis-detected — only a bare, whole-query call.
var labelValuesRe = regexp.MustCompile(`^\s*label_values\(\s*([a-zA-Z0-9_:]+)(?:\s*,\s*([a-zA-Z0-9_:]+)\s*)?\)\s*$`)

// query executes a single query:
//
//	guard(raw) → interpolate → guard(interpolated) → connect → timeout → execute
//
// (report §8, ADR-2, ADR-7). The interpolated SQL is kept in a local variable
// and never written back into qm.SQL, so ExecutedQueryString and any logging
// always carry the raw, author-written SQL (§7.5, §15). Interpolation runs
// before ensureReady so a macro/variable problem never pings the DB. dsUID is
// the datasource UID from the request PluginContext — it labels the F11
// self-metrics (report §18 F11).
func (d *Datasource) query(ctx context.Context, query backend.DataQuery, dsUID string) (resp backend.DataResponse) {
	var qm queryModel
	if err := json.Unmarshal(query.JSON, &qm); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest,
			fmt.Sprintf("json unmarshal: %v", err.Error()))
	}

	// F11: every query is counted, timed and labelled by datasource UID and
	// mode (report §18 F11); a failed query also bumps errors_total. The same
	// defer adds the log entry — the message is safeMessage-shaped (password-
	// redacted, §15) and never carries SQL or values. Client rejections (bad
	// request) are Warn-level diagnostics; real failures are Error.
	mode := queryMode(qm)
	rec := newQueryRecorder(dsUID, mode)
	defer func() {
		rec.done(resp.Error != nil)
		if resp.Error != nil {
			if resp.Status == backend.StatusBadRequest {
				log.DefaultLogger.Warn("oracle query rejected",
					"datasource", dsUID, "mode", mode, "refId", query.RefID, "error", resp.Error.Error())
			} else {
				log.DefaultLogger.Error("oracle query failed",
					"datasource", dsUID, "mode", mode, "refId", query.RefID, "error", resp.Error.Error())
			}
		} else {
			// Execution trace lives at Debug: a dashboard refreshes every few
			// seconds, so Info would drown the log. No SQL, values or query
			// content — just the identifiers (§15).
			log.DefaultLogger.Debug("oracle query executed",
				"datasource", dsUID, "mode", mode, "refId", query.RefID)
		}
	}()

	// F4/F6: the mode is the per-query editor selection (report §14.1). A
	// Prometheus-mode query takes the proxy path (F6); anything else runs SQL
	// against Oracle. The proxy is gated by promEnabled and the URL validation
	// in NewClient, so a misconfigured datasource fails cleanly, never touching
	// Oracle.
	if qm.Mode == "prometheus" {
		return d.promQuery(ctx, query, qm)
	}

	// F9: an annotation query (report §11) is consumed by Grafana's annotation
	// reader, which maps fields by name (time/title/text/tags) straight off a
	// table frame — a time-series conversion would reshape and corrupt it, so
	// force the table shape before scanning.
	if qm.Annotation {
		qm.Format = convert.FormatTable
	}

	// ADR-7: guard the raw SQL first (the interpolated form is re-guarded
	// below). Fail closed before any macro or variable expansion.
	if err := validateReadOnly(qm.SQL); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
	}

	sql, err := interpolate.Interpolate(qm.SQL, interpolate.Params{
		TimeRange:     query.TimeRange,
		Interval:      query.Interval,
		MaxDataPoints: query.MaxDataPoints,
		ScopedVars:    qm.ScopedVars,
		NumericVars:   d.settings.NumericVariableSet(),
	})
	if err != nil {
		// Interpolation errors are readable and never carry SQL (§15).
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
	}

	// Belt-and-suspenders: re-check the interpolated SQL in case expansion
	// moved an unsafe keyword to the front (§15).
	if err := validateReadOnly(sql); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
	}

	if err := d.ensureReady(ctx); err != nil {
		return d.queryError(err)
	}

	// Bound every query; the request context also propagates panel/alert
	// cancellation, so closing the panel stops a running query (report §5.1).
	qCtx := ctx
	if d.settings.QueryTimeoutSec > 0 {
		var cancel context.CancelFunc
		qCtx, cancel = context.WithTimeout(ctx, time.Duration(d.settings.QueryTimeoutSec)*time.Second)
		defer cancel()
	}

	rows, err := d.db.QueryContext(qCtx, sql)
	if err != nil {
		return d.queryError(err)
	}
	defer func() { _ = rows.Close() }()

	return d.buildFrame(rows, qm)
}

// promQuery executes a Prometheus-mode query through the proxy (report §9).
// It fails cleanly when the proxy is disabled or the base URL invalid — never
// touching Oracle — and otherwise issues query_range (matrix) or query
// (instant/vector) with the §10.4 step derivation.
func (d *Datasource) promQuery(ctx context.Context, query backend.DataQuery, qm queryModel) backend.DataResponse {
	if !d.settings.PromEnabled {
		return backend.ErrDataResponse(backend.StatusBadRequest,
			"Prometheus proxy is disabled: enable it in the datasource settings")
	}
	if strings.TrimSpace(qm.PromQL) == "" {
		return backend.ErrDataResponse(backend.StatusBadRequest,
			"prometheus mode requires a PromQL expression")
	}

	client, err := d.promClient(d.settings.MaxSeries)
	if err != nil {
		// URL/config problems are readable and carry no credentials (§9.1).
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
	}
	client.SetLegendFormat(qm.LegendFormat)

	// F8: a Grafana query-variable `label_values(...)` is intercepted here and
	// never reaches the PromQL parser (report §8.5).
	if frame, ok, lvErr := d.promLabelValues(ctx, qm.PromQL, client); ok {
		if lvErr != nil {
			return d.queryError(lvErr)
		}
		resp := backend.DataResponse{}
		resp.Frames = append(resp.Frames, frame)
		return resp
	}

	// Bound every query; the request context also propagates panel/alert
	// cancellation (report §9.5).
	qCtx := ctx
	if d.settings.QueryTimeoutSec > 0 {
		var cancel context.CancelFunc
		qCtx, cancel = context.WithTimeout(ctx, time.Duration(d.settings.QueryTimeoutSec)*time.Second)
		defer cancel()
	}

	var frame *data.Frame
	if qm.Instant {
		frame, err = client.QueryInstant(qCtx, qm.PromQL, query.TimeRange.To)
	} else {
		step, stepErr := promStep(query, qm)
		if stepErr != nil {
			return backend.ErrDataResponse(backend.StatusBadRequest, stepErr.Error())
		}
		frame, err = client.QueryRange(qCtx, qm.PromQL, query.TimeRange.From, query.TimeRange.To, step)
	}
	if err != nil {
		return d.queryError(err)
	}

	if qm.PromQL != "" {
		if frame.Meta == nil {
			frame.Meta = &data.FrameMeta{}
		}
		frame.Meta.ExecutedQueryString = qm.PromQL
	}
	resp := backend.DataResponse{}
	resp.Frames = append(resp.Frames, frame)
	return resp
}

// promLabelValues serves the Grafana query-variable syntax `label_values(...)`
// (report §8.5). The single-argument form lists every value of a label; the
// two-argument form lists a label's values among the series matching a metric
// (distinct, sorted). ok=false means the query is not a label_values call and
// the normal PromQL path runs unchanged. Proxy failures are returned as-is and
// surfaced with the standard credential-free error formatting.
func (d *Datasource) promLabelValues(ctx context.Context, promql string, client *prom.Client) (*data.Frame, bool, error) {
	m := labelValuesRe.FindStringSubmatch(strings.TrimSpace(promql))
	if m == nil {
		return nil, false, nil
	}
	// label_values(<label>) — m[1] is the label.
	if m[2] == "" {
		label := m[1]
		values, err := client.LabelValues(ctx, label)
		if err != nil {
			return nil, true, err
		}
		return labelValuesFrame(label, values), true, nil
	}
	// label_values(<metric>, <label>) — m[1] is the metric, m[2] the label.
	metric, label := m[1], m[2]
	series, err := client.Series(ctx, metric)
	if err != nil {
		return nil, true, err
	}
	seen := map[string]bool{}
	values := make([]string, 0)
	for _, s := range series {
		if v, ok := s[label]; ok && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	sort.Strings(values)
	return labelValuesFrame(label, values), true, nil
}

// labelValuesFrame builds the single-column frame a Grafana query variable
// reads: the first field's values become the dropdown options (report §8.5).
func labelValuesFrame(label string, values []string) *data.Frame {
	return data.NewFrame("label_values", data.NewField(label, nil, values))
}

// promStep derives the Prometheus query_range step (report §9.3/§10.4): an
// explicit per-query step wins, then maxDataPoints, then the request interval,
// floored at 1s.
func promStep(query backend.DataQuery, qm queryModel) (time.Duration, error) {
	if qm.Step != "" {
		d, err := time.ParseDuration(qm.Step)
		if err != nil {
			return 0, fmt.Errorf("invalid step %q: %v", qm.Step, err)
		}
		if d < time.Second {
			return time.Second, nil
		}
		return d.Truncate(time.Second), nil
	}
	return prom.DeriveStep(query.TimeRange.From, query.TimeRange.To, query.MaxDataPoints, query.Interval), nil
}

// validateReadOnly enforces the first-keyword guard: skip whitespace and SQL
// comments, then require the first identifier to be SELECT or WITH (ADR-7).
// This is a safety net, not a parser — anything unrecognized is rejected.
func validateReadOnly(sql string) error {
	keyword := firstKeyword(sql)
	if keyword == "" {
		return fmt.Errorf("empty query: provide a SELECT or WITH statement")
	}
	if !readOnlyKeywords[keyword] {
		return fmt.Errorf("read-only guard: %q is not allowed — only SELECT and WITH queries are permitted", keyword)
	}
	return nil
}

// firstKeyword returns the uppercased first word of sql after skipping leading
// whitespace and SQL comments (-- line and /* */ block), or "" if there is none.
func firstKeyword(sql string) string {
	s := strings.TrimSpace(sql)
	for s != "" {
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
			} else {
				return ""
			}
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
			} else {
				return ""
			}
		default:
			i := 0
			for i < len(s) && !isSpace(s[i]) {
				i++
			}
			return strings.ToUpper(s[:i])
		}
	}
	return ""
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// buildFrame reads a result set into a typed Grafana frame (report §6/§7).
// Columns are classified before scanning (convert.Resolve), each cell is
// scanned into an explicit typed destination (never []any — go-ora returns
// NUMBER as string there, regression #533), and the frame is shaped by the
// pure convert package. Rows are capped at the ADR-11 maxRows guard.
func (d *Datasource) buildFrame(rows *sql.Rows, qm queryModel) backend.DataResponse {
	resp := backend.DataResponse{}

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return d.queryError(err)
	}
	cols := make([]convert.ColumnInfo, len(colTypes))
	for i, ct := range colTypes {
		cols[i] = convert.InfoFromColumnType(ct)
	}

	// Per-query largeNumberMode overrides the datasource default (report §4.3);
	// either may be empty, in which case convert applies the ADR-8 per-format
	// default (string in table, float in time_series).
	large := d.settings.LargeNumberMode
	if qm.LargeNumberMode != "" {
		large = qm.LargeNumberMode
	}

	opt := convert.Options{
		Format:              qm.Format,
		TimeColumn:          qm.TimeColumn,
		MaxSeries:           d.settings.MaxSeries,
		LargeNumbers:        large,
		ExecutedQueryString: qm.SQL, // raw SQL before interpolation (report §7.5)
	}
	_, kinds := convert.Resolve(cols, opt)

	rowVals := make([][]any, 0, d.settings.MaxRows)
	for rows.Next() {
		if d.settings.MaxRows > 0 && len(rowVals) >= d.settings.MaxRows {
			break
		}
		dest := make([]any, len(kinds))
		for i, k := range kinds {
			dest[i] = convert.ScanDest(k)
		}
		if err := rows.Scan(dest...); err != nil {
			return d.queryError(err)
		}
		cell := make([]any, len(kinds))
		for i := range dest {
			cell[i] = convert.ValueOf(dest[i])
		}
		rowVals = append(rowVals, cell)
	}
	if err := rows.Err(); err != nil {
		return d.queryError(err)
	}

	frame, err := convert.BuildFrame(cols, rowVals, opt)
	if err != nil {
		return d.queryError(err)
	}
	// F9: mark annotation queries so Grafana treats the frame as annotation
	// events (DataTopic.Annotations, report §11) instead of panel data.
	if qm.Annotation {
		if frame.Meta == nil {
			frame.Meta = &data.FrameMeta{}
		}
		frame.Meta.DataTopic = data.DataTopicAnnotations
	}
	resp.Frames = append(resp.Frames, frame)
	return resp
}

// queryError formats a query failure without leaking credentials or SQL.
func (d *Datasource) queryError(err error) backend.DataResponse {
	return backend.ErrDataResponse(backend.StatusInternal, d.safeMessage(err))
}

// queryMode returns the F11 metric/log mode label for a query model:
// "prometheus" for Prometheus-mode queries, "sql" for everything else
// (report §18 F11).
func queryMode(qm queryModel) string {
	if qm.Mode == "prometheus" {
		return "prometheus"
	}
	return "sql"
}

package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
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
// override.
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
}

// readOnlyKeywords are the only words a read-only query may start with (ADR-7).
// Everything else is rejected before execution — fail closed, never guess.
var readOnlyKeywords = map[string]bool{
	"SELECT": true,
	"WITH":   true,
}

// query executes a single query:
//
//	guard(raw) → interpolate → guard(interpolated) → connect → timeout → execute
//
// (report §8, ADR-2, ADR-7). The interpolated SQL is kept in a local variable
// and never written back into qm.SQL, so ExecutedQueryString and any logging
// always carry the raw, author-written SQL (§7.5, §15). Interpolation runs
// before ensureReady so a macro/variable problem never pings the DB.
func (d *Datasource) query(ctx context.Context, query backend.DataQuery) backend.DataResponse {
	var qm queryModel
	if err := json.Unmarshal(query.JSON, &qm); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest,
			fmt.Sprintf("json unmarshal: %v", err.Error()))
	}

	// F4/F6: the mode is the per-query editor selection (report §14.1). A
	// Prometheus-mode query takes the proxy path (F6); anything else runs SQL
	// against Oracle. The proxy is gated by promEnabled and the URL validation
	// in NewClient, so a misconfigured datasource fails cleanly, never touching
	// Oracle.
	if qm.Mode == "prometheus" {
		return d.promQuery(ctx, query, qm)
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

	user, password, token := d.settings.PromCreds()
	client, err := prom.NewClient(d.settings.PromURL, prom.Options{
		AuthType:     d.settings.PromAuthType,
		User:         user,
		Password:     password,
		Token:        token,
		TLSInsecure:  d.settings.PromTlsSkipVerify,
		Timeout:      time.Duration(d.settings.QueryTimeoutSec) * time.Second,
		Limit:        d.settings.MaxSeries,
		LegendFormat: qm.LegendFormat,
	})
	if err != nil {
		// URL/config problems are readable and carry no credentials (§9.1).
		return backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
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
	resp.Frames = append(resp.Frames, frame)
	return resp
}

// queryError formats a query failure without leaking credentials or SQL.
func (d *Datasource) queryError(err error) backend.DataResponse {
	return backend.ErrDataResponse(backend.StatusInternal, d.safeMessage(err))
}

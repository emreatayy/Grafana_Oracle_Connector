package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// queryModel is the query payload sent by the frontend (report §4.3). F1 wires
// SQL only; mode/format/parameters arrive in later phases.
type queryModel struct {
	SQL string `json:"sql"`
}

// readOnlyKeywords are the only words a read-only query may start with (ADR-7).
// Everything else is rejected before execution — fail closed, never guess.
var readOnlyKeywords = map[string]bool{
	"SELECT": true,
	"WITH":   true,
}

// query executes a single query: guard → timeout → execute → frame.
func (d *Datasource) query(ctx context.Context, query backend.DataQuery) backend.DataResponse {
	var qm queryModel
	if err := json.Unmarshal(query.JSON, &qm); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest,
			fmt.Sprintf("json unmarshal: %v", err.Error()))
	}

	if err := validateReadOnly(qm.SQL); err != nil {
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

	rows, err := d.db.QueryContext(qCtx, qm.SQL)
	if err != nil {
		return d.queryError(err)
	}
	defer func() { _ = rows.Close() }()

	return d.buildFrame(rows, d.settings.MaxRows)
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

// buildFrame converts a result set into a provisional generic frame.
// TODO(F2): typed column mapping (report §6.x). Until then cells keep the
// go-ora default scan types — NUMBER arrives as string (go-ora regression #533,
// report §21 check 2) — and the transpose below yields string/time fields.
func (d *Datasource) buildFrame(rows *sql.Rows, maxRows int) backend.DataResponse {
	resp := backend.DataResponse{}

	cols, err := rows.Columns()
	if err != nil {
		return d.queryError(err)
	}

	// Row-major generic scan, capped at maxRows (ADR-11 cardinality guard).
	rowVals := make([][]any, 0, maxRows)
	for rows.Next() {
		if maxRows > 0 && len(rowVals) >= maxRows {
			break
		}
		cells := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = &cells[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return d.queryError(err)
		}
		rowVals = append(rowVals, cells)
	}
	if err := rows.Err(); err != nil {
		return d.queryError(err)
	}

	// Transpose to column fields. An empty result still yields columns so the
	// table panel renders a header.
	frame := data.NewFrame("oracle")
	for i, name := range cols {
		vals := make([]any, len(rowVals))
		for r := range rowVals {
			vals[r] = rowVals[r][i]
		}
		frame.Fields = append(frame.Fields, valuesToField(name, vals))
	}
	resp.Frames = append(resp.Frames, frame)
	return resp
}

// valuesToField converts a generic column into a concrete SDK field. The SDK
// does not accept []any (it panics), so F1 maps cells to two concrete shapes —
// time.Time for temporal columns, string for everything else (go-ora returns
// NUMBER as string in []any — regression #533). This is the provisional
// stand-in for the typed mapper; F2 replaces it entirely (report §6.x).
func valuesToField(name string, vals []any) *data.Field {
	if len(vals) == 0 {
		return data.NewField(name, nil, []*string{})
	}
	if allTime(vals) {
		times := make([]*time.Time, len(vals))
		for i, v := range vals {
			if v != nil {
				t := v.(time.Time)
				times[i] = &t
			}
		}
		return data.NewField(name, nil, times)
	}
	strs := make([]*string, len(vals))
	for i, v := range vals {
		if s, ok := cellString(v); ok {
			strs[i] = &s
		}
	}
	return data.NewField(name, nil, strs)
}

func allTime(vals []any) bool {
	for _, v := range vals {
		if v == nil {
			continue
		}
		if _, ok := v.(time.Time); !ok {
			return false
		}
	}
	return true
}

// cellString renders a driver cell as a string, or ok=false for NULL. time.Time
// is formatted with the session zone (RFC3339Nano keeps the offset); NLS
// formatting arrives with the typed mapper in F2.
func cellString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case []byte:
		return string(x), true
	case time.Time:
		return x.Format(time.RFC3339Nano), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		return fmt.Sprint(x), true
	}
}

// queryError formats a query failure without leaking credentials or SQL.
func (d *Datasource) queryError(err error) backend.DataResponse {
	return backend.ErrDataResponse(backend.StatusInternal, d.safeMessage(err))
}

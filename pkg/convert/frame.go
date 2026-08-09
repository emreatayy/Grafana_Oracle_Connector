package convert

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// defaultMaxSeries is the ADR-11 cardinality guard applied when Options.MaxSeries
// is not set (report §7.3 rule 5).
const defaultMaxSeries = 1000

// Options controls frame generation (report §7.1). Format is the requested
// format; TimeColumn optionally pins the time column by name; MaxSeries is the
// ADR-11 cardinality guard (0 → defaultMaxSeries); LargeNumbers is the
// datasource largeNumberMode setting ("" = ADR-8 per-format default);
// ExecutedQueryString is the raw SQL before interpolation (report §7.5).
type Options struct {
	Format              string
	TimeColumn          string
	MaxSeries           int
	LargeNumbers        string
	ExecutedQueryString string
}

// Resolve decides the final format and per-column kinds for a result set
// (report §7.2 + §6.2). The query path calls it before scanning so it can pick
// typed destinations; BuildFrame calls it again internally — it is pure and
// deterministic, so the two can never diverge.
func Resolve(cols []ColumnInfo, opt Options) (format string, kinds []Kind) {
	// Auto decision (§7.2) uses probe kinds where large NUMBER counts as a
	// value column regardless of the mode it will finally scan as.
	probe := classifyAll(cols, LargeNumberString)
	format = opt.Format
	if format == "" || format == FormatAuto {
		if hasTime(probe) && hasValueKind(probe) {
			format = FormatTimeSeries
		} else {
			format = FormatTable
		}
	}

	// Final kinds use the effective large-number mode for the chosen format.
	mode := EffectiveLargeMode(opt.LargeNumbers, format)
	return format, classifyAll(cols, mode)
}

// BuildFrame turns classified columns + a row-major result set into a Grafana
// frame per report §7. rows[r][c] holds a concrete Go value or nil for NULL.
func BuildFrame(cols []ColumnInfo, rows [][]any, opt Options) (*data.Frame, error) {
	format, kinds := Resolve(cols, opt)
	names := dedupeNames(cols)

	var frame *data.Frame
	var err error
	if format == FormatTimeSeries {
		frame, err = timeSeriesFrame(names, kinds, rows, opt)
	} else {
		frame = tableFrame(names, kinds, rows)
	}
	if err != nil {
		return nil, err
	}

	if opt.ExecutedQueryString != "" {
		if frame.Meta == nil {
			frame.Meta = &data.FrameMeta{}
		}
		frame.Meta.ExecutedQueryString = opt.ExecutedQueryString
	}
	return frame, nil
}

// classifyAll classifies every column under one large-number mode.
func classifyAll(cols []ColumnInfo, mode LargeNumberMode) []Kind {
	kinds := make([]Kind, len(cols))
	for i, c := range cols {
		kinds[i] = Classify(c, mode)
	}
	return kinds
}

func hasTime(kinds []Kind) bool {
	for _, k := range kinds {
		if k == KindTime {
			return true
		}
	}
	return false
}

func hasValueKind(kinds []Kind) bool {
	for _, k := range kinds {
		if IsValueKind(k) {
			return true
		}
	}
	return false
}

// dedupeNames disambiguates duplicate column names (report §7.5): col, col_1,
// col_2, … Also collision-safe when a column is literally named col_1.
func dedupeNames(cols []ColumnInfo) []string {
	names := make([]string, len(cols))
	used := make(map[string]bool, len(cols))
	for i, c := range cols {
		name := c.Name
		if used[name] {
			for n := 1; ; n++ {
				cand := fmt.Sprintf("%s_%d", name, n)
				if !used[cand] {
					name = cand
					break
				}
			}
		}
		used[name] = true
		names[i] = name
	}
	return names
}

// columnAt transposes a row-major result set into one column of values.
func columnAt(rows [][]any, c int) []any {
	col := make([]any, len(rows))
	for r := range rows {
		col[r] = rows[r][c]
	}
	return col
}

// tableFrame renders every column as a typed field (report §7.4): NULL is a
// real null, rows with NULL time are kept, and an empty result still yields
// typed fields so the table panel renders a header.
func tableFrame(names []string, kinds []Kind, rows [][]any) *data.Frame {
	frame := data.NewFrame("oracle")
	for i := range names {
		frame.Fields = append(frame.Fields, fieldFromColumn(names[i], kinds[i], columnAt(rows, i), nil))
	}
	return frame
}

// timeSeriesFrame shapes a time-series frame (report §7.3).
func timeSeriesFrame(names []string, kinds []Kind, rows [][]any, opt Options) (*data.Frame, error) {
	timeIdx := timeColumnIndex(names, kinds, opt.TimeColumn)
	if timeIdx < 0 {
		return nil, fmt.Errorf("time_series format requires a time column (DATE/TIMESTAMP); add one to the query or use table format")
	}

	var valueIdxs, strIdxs []int
	for i, k := range kinds {
		if i == timeIdx {
			continue
		}
		switch k {
		case KindFloat, KindInt, KindBool:
			valueIdxs = append(valueIdxs, i)
		case KindString:
			strIdxs = append(strIdxs, i)
		}
	}

	// §7.3 rule 7: rows with a NULL time are skipped in time series.
	filtered := filteredRows(rows, timeIdx)

	// Rule 3 — exactly 1 value column + ≥1 string column → long format.
	if len(valueIdxs) == 1 && len(strIdxs) > 0 {
		return longFrame(names, kinds, strIdxs, timeIdx, valueIdxs[0], filtered, opt)
	}

	// Rule 4 — wide format (ADR-6).
	if len(valueIdxs) == 0 {
		return nil, timeSeriesNoValueError(kinds)
	}
	return wideFrame(names, kinds, strIdxs, timeIdx, valueIdxs, filtered)
}

func timeColumnIndex(names []string, kinds []Kind, explicit string) int {
	if explicit != "" {
		for i, n := range names {
			if n == explicit {
				return i
			}
		}
	}
	for i, k := range kinds {
		if k == KindTime {
			return i
		}
	}
	return -1
}

func filteredRows(rows [][]any, timeIdx int) [][]any {
	filtered := make([][]any, 0, len(rows))
	for _, row := range rows {
		if row[timeIdx] != nil {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// timeSeriesNoValueError explains why time_series could not find a value
// column, hinting at the ADR-8 knob when the only numeric column is a large
// NUMBER left in string mode.
func timeSeriesNoValueError(kinds []Kind) error {
	for _, k := range kinds {
		if k == KindLargeNumber {
			return fmt.Errorf("time_series format requires a numeric value column; large NUMBER columns need largeNumberMode=float in time series, or use table format")
		}
	}
	return fmt.Errorf("time_series format requires a numeric value column; add one to the query or use table format")
}

// longFrame builds the LONG time-series format (report §7.3 rule 3): string
// columns become dimensions, then time, then the single value column. The
// frontend pivots long→wide; we enforce the ADR-11 cardinality guard on the
// distinct label combinations first.
func longFrame(names []string, kinds []Kind, strIdxs []int, timeIdx, valueIdx int, rows [][]any, opt Options) (*data.Frame, error) {
	maxSeries := opt.MaxSeries
	if maxSeries <= 0 {
		maxSeries = defaultMaxSeries
	}
	if series := distinctSeries(strIdxs, rows); series > maxSeries {
		return nil, fmt.Errorf("time_series cardinality: query returned %d series (maxSeries=%d); add a GROUP BY or filter to reduce it",
			series, maxSeries)
	}

	fields := make([]*data.Field, 0, len(strIdxs)+2)
	for _, i := range strIdxs {
		fields = append(fields, fieldFromColumn(names[i], KindString, columnAt(rows, i), nil))
	}
	fields = append(fields, fieldFromColumn(names[timeIdx], KindTime, columnAt(rows, timeIdx), nil))
	fields = append(fields, timeSeriesValueField(names[valueIdx], kinds[valueIdx], columnAt(rows, valueIdx), nil))

	frame := data.NewFrame("oracle", fields...)
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesLong}
	return frame, nil
}

// distinctSeries counts the distinct value combinations of the string
// dimension columns across rows (NULL/empty labels count as ""). The canonical
// key is length-prefixed so "a;b" cannot collide with "ab;".
func distinctSeries(strIdxs []int, rows [][]any) int {
	seen := make(map[string]struct{})
	for _, row := range rows {
		var b strings.Builder
		for _, i := range strIdxs {
			v, ok := row[i].(string)
			if !ok {
				v = ""
			}
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
			b.WriteByte(';')
		}
		seen[b.String()] = struct{}{}
	}
	return len(seen)
}

// wideFrame builds the WIDE time-series format (report §7.3 rule 4 / ADR-6): a
// single time field + one field per value column. String columns with a single
// distinct value are attached as labels on the value fields; the rest are
// appended as plain fields.
func wideFrame(names []string, kinds []Kind, strIdxs []int, timeIdx int, valueIdxs []int, rows [][]any) (*data.Frame, error) {
	var valueLabels data.Labels
	var extraStr []int
	for _, i := range strIdxs {
		if v, single := singleDistinctString(i, rows); single {
			if valueLabels == nil {
				valueLabels = data.Labels{}
			}
			valueLabels[names[i]] = v
		} else {
			extraStr = append(extraStr, i)
		}
	}

	fields := []*data.Field{
		fieldFromColumn(names[timeIdx], KindTime, columnAt(rows, timeIdx), nil),
	}
	for _, i := range valueIdxs {
		fields = append(fields, timeSeriesValueField(names[i], kinds[i], columnAt(rows, i), valueLabels))
	}
	for _, i := range extraStr {
		fields = append(fields, fieldFromColumn(names[i], KindString, columnAt(rows, i), nil))
	}

	frame := data.NewFrame("oracle", fields...)
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesWide}
	return frame, nil
}

// singleDistinctString returns the single distinct non-empty string value of
// column c across rows, or ok=false when the column has none or ≥2 distinct
// values (report §7.3 rule 4).
func singleDistinctString(c int, rows [][]any) (string, bool) {
	var first string
	sawFirst := false
	for _, row := range rows {
		v, ok := row[c].(string)
		if !ok || v == "" {
			continue
		}
		if !sawFirst {
			first, sawFirst = v, true
		} else if v != first {
			return "", false
		}
	}
	return first, sawFirst
}

// timeSeriesValueField builds a time-series value field. Grafana's alerting SSE
// and dataplane readers require time-series value columns to be float64 (report
// §10.1), so int and bool columns are coerced here regardless of the table-format
// typing (report §6.1 rules 1/4 keep int64/bool in tables). Large NUMBER columns
// already classify as float in time series (ADR-8), so they need no coercion.
func timeSeriesValueField(name string, kind Kind, col []any, labels data.Labels) *data.Field {
	switch kind {
	case KindFloat:
		out := make([]*float64, len(col))
		for i, v := range col {
			if n, ok := v.(float64); ok {
				out[i] = &n
			}
		}
		return data.NewField(name, labels, out)
	case KindInt:
		out := make([]*float64, len(col))
		for i, v := range col {
			if n, ok := v.(int64); ok {
				f := float64(n)
				out[i] = &f
			}
		}
		return data.NewField(name, labels, out)
	case KindBool:
		out := make([]*float64, len(col))
		for i, v := range col {
			if b, ok := v.(bool); ok {
				f := 0.0
				if b {
					f = 1.0
				}
				out[i] = &f
			}
		}
		return data.NewField(name, labels, out)
	default:
		// Not expected for time-series value columns; fall back to the generic
		// builder rather than failing the whole frame.
		return fieldFromColumn(name, kind, col, labels)
	}
}

// fieldFromColumn builds a concrete SDK field from a column of values
// (report §6.1 / §6.2). Values are concrete Go values or nil for NULL; the SDK
// requires typed slices ([]any panics). KindBytes is encoded as base64 because
// the SDK has no byte-vector field type (report §6.1 BLOB note).
func fieldFromColumn(name string, kind Kind, col []any, labels data.Labels) *data.Field {
	switch kind {
	case KindInt:
		out := make([]*int64, len(col))
		for i, v := range col {
			if n, ok := v.(int64); ok {
				out[i] = &n
			}
		}
		return data.NewField(name, labels, out)
	case KindFloat:
		out := make([]*float64, len(col))
		for i, v := range col {
			if n, ok := v.(float64); ok {
				out[i] = &n
			}
		}
		return data.NewField(name, labels, out)
	case KindLargeNumber, KindString:
		out := make([]*string, len(col))
		for i, v := range col {
			if s, ok := v.(string); ok {
				out[i] = &s
			}
		}
		return data.NewField(name, labels, out)
	case KindTime:
		out := make([]*time.Time, len(col))
		for i, v := range col {
			if t, ok := v.(time.Time); ok {
				out[i] = &t
			}
		}
		return data.NewField(name, labels, out)
	case KindBool:
		out := make([]*bool, len(col))
		for i, v := range col {
			if b, ok := v.(bool); ok {
				out[i] = &b
			}
		}
		return data.NewField(name, labels, out)
	case KindBytes:
		out := make([]*string, len(col))
		for i, v := range col {
			if b, ok := v.([]byte); ok {
				s := base64.StdEncoding.EncodeToString(b)
				out[i] = &s
			}
		}
		return data.NewField(name, labels, out)
	default:
		return data.NewField(name, labels, []*string{})
	}
}

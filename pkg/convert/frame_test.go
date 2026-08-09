package convert

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Column helpers so fixtures read as their Oracle meaning.
func intCol(name string, prec int64) ColumnInfo {
	return ColumnInfo{Name: name, ScanType: reflect.TypeOf(float64(0)), Precision: prec, Scale: 0}
}

func fltCol(name string) ColumnInfo {
	return ColumnInfo{Name: name, ScanType: reflect.TypeOf(float64(0)), Precision: 10, Scale: 2}
}

func boolCol(name string) ColumnInfo {
	return ColumnInfo{Name: name, ScanType: reflect.TypeOf(false)}
}

func strCol(name string) ColumnInfo {
	return ColumnInfo{Name: name, ScanType: reflect.TypeOf("")}
}

func timeCol(name string) ColumnInfo {
	return ColumnInfo{Name: name, ScanType: reflect.TypeOf(time.Time{})}
}

func ts(h int, m int) time.Time {
	return time.Date(2026, 8, 8, h, m, 0, 0, time.UTC)
}

func TestBuildFrame_TableTyped(t *testing.T) {
	cols := []ColumnInfo{intCol("id", 10), fltCol("ratio"), strCol("name"), timeCol("at"), intCol("big", 30)}
	rows := [][]any{
		{int64(42), 3.14, "web-1", ts(9, 0), "12345678901234567890"},
		{int64(7), nil, nil, nil, nil}, // NULL float/string/time/big
	}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTable})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}

	wantTypes := []data.FieldType{
		data.FieldTypeNullableInt64,
		data.FieldTypeNullableFloat64,
		data.FieldTypeNullableString,
		data.FieldTypeNullableTime,
		data.FieldTypeNullableString, // big NUMBER kept as string in table (ADR-8)
	}
	if len(frame.Fields) != len(wantTypes) {
		t.Fatalf("fields = %d, want %d", len(frame.Fields), len(wantTypes))
	}
	for i, ft := range wantTypes {
		if got := frame.Fields[i].Type(); got != ft {
			t.Errorf("field %s type = %v, want %v", frame.Fields[i].Name, got, ft)
		}
		if frame.Fields[i].Len() != 2 {
			t.Errorf("field %s len = %d, want 2", frame.Fields[i].Name, frame.Fields[i].Len())
		}
	}

	// Values survived the round trip, including the big NUMBER string.
	if got := *frame.Fields[0].At(0).(*int64); got != 42 {
		t.Errorf("id[0] = %d, want 42", got)
	}
	if frame.Fields[1].NilAt(1) != true {
		t.Errorf("ratio[1] should be NULL")
	}
	if got := *frame.Fields[4].At(0).(*string); got != "12345678901234567890" {
		t.Errorf("big[0] = %q, want full precision", got)
	}
}

func TestBuildFrame_AutoDecision(t *testing.T) {
	withTimeAndValue := func() (*data.Frame, error) {
		return BuildFrame([]ColumnInfo{timeCol("t"), intCol("v", 10)},
			[][]any{{ts(9, 0), int64(1)}}, Options{Format: FormatAuto})
	}
	if f, err := withTimeAndValue(); err != nil {
		t.Fatalf("time+value: %v", err)
	} else if f.Meta == nil || f.Meta.Type != data.FrameTypeTimeSeriesWide {
		t.Errorf("time+value meta = %+v, want TimeSeriesWide", f.Meta)
	}

	noTime := []ColumnInfo{intCol("v", 10), strCol("name")}
	if f, err := BuildFrame(noTime, [][]any{{int64(1), "x"}}, Options{Format: FormatAuto}); err != nil {
		t.Fatalf("no time: %v", err)
	} else if f.Meta != nil {
		t.Errorf("no-time frame meta = %+v, want nil (table)", f.Meta)
	}

	timeNoValue := []ColumnInfo{timeCol("t"), strCol("name")}
	if f, err := BuildFrame(timeNoValue, [][]any{{ts(9, 0), "x"}}, Options{Format: FormatAuto}); err != nil {
		t.Fatalf("time+string: %v", err)
	} else if f.Meta != nil {
		t.Errorf("time+string frame meta = %+v, want nil (table)", f.Meta)
	}
}

func TestBuildFrame_TimeSeriesWide(t *testing.T) {
	// §7.3 rule 4 (wide) applies with ≥2 value columns; a string column with a
	// single distinct value is attached as labels on the value fields.
	cols := []ColumnInfo{timeCol("t"), intCol("cpu", 10), fltCol("mem"), strCol("host")}
	rows := [][]any{
		{ts(9, 0), int64(1), 0.5, "web-1"},
		{ts(9, 1), int64(2), 0.6, "web-1"},
	}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if frame.Meta == nil || frame.Meta.Type != data.FrameTypeTimeSeriesWide {
		t.Fatalf("meta = %+v, want TimeSeriesWide", frame.Meta)
	}
	if len(frame.Fields) != 3 { // time + cpu + mem; host folded into Labels
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	if got := frame.Fields[1].Labels["host"]; got != "web-1" {
		t.Errorf("cpu labels = %v, want host=web-1", frame.Fields[1].Labels)
	}
	if got := frame.Fields[2].Labels["host"]; got != "web-1" {
		t.Errorf("mem labels = %v, want host=web-1", frame.Fields[2].Labels)
	}

	// Multiple distinct strings → the column becomes a plain field (§7.3.4).
	rows2 := [][]any{
		{ts(9, 0), int64(1), 0.5, "web-1"},
		{ts(9, 1), int64(2), 0.6, "web-2"},
	}
	frame2, err := BuildFrame(cols, rows2, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if len(frame2.Fields) != 4 {
		t.Fatalf("fields = %d, want 4 (time + cpu + mem + host)", len(frame2.Fields))
	}
	if frame2.Fields[1].Labels != nil {
		t.Errorf("cpu labels = %v, want nil when the string column varies", frame2.Fields[1].Labels)
	}
}

func TestBuildFrame_TimeSeriesLong(t *testing.T) {
	// 1 value column + ≥1 string column → long format (§7.3 rule 3).
	cols := []ColumnInfo{timeCol("t"), strCol("host"), intCol("v", 10)}
	rows := [][]any{
		{ts(9, 0), "web-1", int64(1)},
		{ts(9, 1), "web-2", int64(2)},
		{ts(9, 2), "web-1", int64(3)},
	}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if frame.Meta == nil || frame.Meta.Type != data.FrameTypeTimeSeriesLong {
		t.Fatalf("meta = %+v, want TimeSeriesLong", frame.Meta)
	}
	names := []string{"host", "t", "v"}
	if len(frame.Fields) != 3 {
		t.Fatalf("fields = %d, want 3", len(frame.Fields))
	}
	for i, want := range names {
		if frame.Fields[i].Name != want {
			t.Errorf("field %d = %q, want %q", i, frame.Fields[i].Name, want)
		}
	}
	if frame.Fields[2].Labels != nil {
		t.Errorf("value field labels = %v, want nil (dimensions are string fields)", frame.Fields[2].Labels)
	}
}

func TestBuildFrame_TimeSeriesValueColumnsFloat64(t *testing.T) {
	// Grafana alerting SSE and the dataplane reader need float64 time-series
	// value columns (report §10.1); int/bool value columns must be coerced in
	// time-series format even though tables keep int64/bool (§6.1 rules 1/4).
	cols := []ColumnInfo{timeCol("t"), intCol("cpu", 10), fltCol("mem"), boolCol("up"), strCol("host")}
	rows := [][]any{
		{ts(9, 0), int64(3), 0.5, true, "web-1"},
		{ts(9, 1), nil, 0.6, false, "web-1"}, // NULL int + false bool
	}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	// wide: time + cpu + mem + up (host folded into labels)
	if len(frame.Fields) != 4 {
		t.Fatalf("fields = %d, want 4", len(frame.Fields))
	}
	for i, want := range []data.FieldType{
		data.FieldTypeNullableFloat64, // cpu (int coerced)
		data.FieldTypeNullableFloat64, // mem
		data.FieldTypeNullableFloat64, // up (bool coerced)
	} {
		got := frame.Fields[i+1].Type()
		if got != want {
			t.Errorf("field %d type = %v, want %v", i+1, got, want)
		}
	}
	if got := *frame.Fields[1].At(0).(*float64); got != 3 {
		t.Errorf("cpu[0] = %v, want 3", got)
	}
	if got := frame.Fields[1].At(1); got != (*float64)(nil) {
		t.Errorf("cpu[1] = %v, want nil (NULL preserved)", got)
	}
	if got := *frame.Fields[3].At(0).(*float64); got != 1 {
		t.Errorf("up[0] = %v, want 1 (true coerced)", got)
	}
	if got := *frame.Fields[3].At(1).(*float64); got != 0 {
		t.Errorf("up[1] = %v, want 0 (false coerced)", got)
	}

	// long: single value column + string dim → [host, t, v]; v must be float64.
	longCols := []ColumnInfo{timeCol("t"), strCol("host"), intCol("v", 10)}
	longRows := [][]any{{ts(9, 0), "web-1", int64(7)}}
	longFrame, err := BuildFrame(longCols, longRows, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame long: %v", err)
	}
	if got := longFrame.Fields[2].Type(); got != data.FieldTypeNullableFloat64 {
		t.Errorf("long value type = %v, want NullableFloat64", got)
	}
	if got := *longFrame.Fields[2].At(0).(*float64); got != 7 {
		t.Errorf("long v[0] = %v, want 7", got)
	}
}

func TestBuildFrame_CardinalityGuard(t *testing.T) {
	cols := []ColumnInfo{timeCol("t"), strCol("host"), intCol("v", 10)}
	rows := [][]any{
		{ts(9, 0), "web-1", int64(1)},
		{ts(9, 1), "web-2", int64(2)},
		{ts(9, 2), "web-3", int64(3)},
	}
	_, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries, MaxSeries: 2})
	if err == nil || !strings.Contains(err.Error(), "cardinality") {
		t.Fatalf("err = %v, want cardinality error", err)
	}
}

func TestBuildFrame_NoValueColumnHint(t *testing.T) {
	// Explicit string large-number mode in time series leaves no numeric value
	// column → the error should hint the ADR-8 knob.
	cols := []ColumnInfo{timeCol("t"), intCol("big", 30)}
	rows := [][]any{{ts(9, 0), "12345678901234567890"}}
	_, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries, LargeNumbers: "string"})
	if err == nil || !strings.Contains(err.Error(), "largeNumberMode=float") {
		t.Fatalf("err = %v, want largeNumberMode hint", err)
	}
}

func TestBuildFrame_DuplicateColumns(t *testing.T) {
	cols := []ColumnInfo{intCol("a", 10), strCol("a"), strCol("a_1"), strCol("a")}
	rows := [][]any{{int64(1), "x", "y", "z"}}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTable})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	want := []string{"a", "a_1", "a_1_1", "a_2"}
	for i, w := range want {
		if frame.Fields[i].Name != w {
			t.Errorf("field %d = %q, want %q", i, frame.Fields[i].Name, w)
		}
	}
}

func TestBuildFrame_EmptyResult(t *testing.T) {
	cols := []ColumnInfo{timeCol("t"), intCol("v", 10), strCol("name")}

	table, err := BuildFrame(cols, nil, Options{Format: FormatTable})
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	if len(table.Fields) != 3 {
		t.Fatalf("table fields = %d, want 3", len(table.Fields))
	}
	for i, want := range []data.FieldType{data.FieldTypeNullableTime, data.FieldTypeNullableInt64, data.FieldTypeNullableString} {
		if table.Fields[i].Type() != want {
			t.Errorf("table field %d type = %v, want %v", i, table.Fields[i].Type(), want)
		}
	}

	// Empty time-series frame keeps the fields typed. With exactly 1 value
	// column + a string column the shape is long: [dim, time, value] (§7.3.3).
	tsf, err := BuildFrame(cols, nil, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("time series: %v", err)
	}
	if len(tsf.Fields) != 3 {
		t.Errorf("ts fields = %d, want 3 (host + time + value)", len(tsf.Fields))
	}
}

func TestBuildFrame_NilTimeSkipped(t *testing.T) {
	cols := []ColumnInfo{timeCol("t"), intCol("v", 10)}
	rows := [][]any{
		{nil, int64(1)}, // NULL time must be dropped in time series (§7.3.7)
		{ts(9, 0), int64(2)},
	}
	frame, err := BuildFrame(cols, rows, Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if frame.Rows() != 1 {
		t.Errorf("rows = %d, want 1 (NULL-time row skipped)", frame.Rows())
	}
	if got := *frame.Fields[1].At(0).(*float64); got != 2 {
		t.Errorf("v[0] = %v, want 2 (time-series value columns are float64 for alerting)", got)
	}
}

func TestBuildFrame_LargeNumberByFormat(t *testing.T) {
	big := intCol("big", 30)
	rows := [][]any{{"12345678901234567890"}}

	table, err := BuildFrame([]ColumnInfo{big}, rows, Options{Format: FormatTable})
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	if table.Fields[0].Type() != data.FieldTypeNullableString {
		t.Errorf("table type = %v, want NullableString", table.Fields[0].Type())
	}

	// time_series needs a time column for ts, so pair it with one.
	tsf, err := BuildFrame([]ColumnInfo{timeCol("t"), big}, [][]any{{ts(9, 0), "12345678901234567890"}},
		Options{Format: FormatTimeSeries})
	if err != nil {
		t.Fatalf("ts: %v", err)
	}
	if tsf.Fields[1].Type() != data.FieldTypeNullableFloat64 {
		t.Errorf("ts type = %v, want NullableFloat64", tsf.Fields[1].Type())
	}
}

func TestBuildFrame_ExecutedQueryString(t *testing.T) {
	frame, err := BuildFrame([]ColumnInfo{intCol("v", 10)}, [][]any{{int64(1)}},
		Options{Format: FormatTable, ExecutedQueryString: "SELECT 1 FROM DUAL"})
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if frame.Meta == nil || frame.Meta.ExecutedQueryString != "SELECT 1 FROM DUAL" {
		t.Errorf("meta = %+v, want ExecutedQueryString", frame.Meta)
	}
}

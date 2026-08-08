package convert

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	ft64 := reflect.TypeOf(float64(0))
	ft32 := reflect.TypeOf(float32(0))
	tstr := reflect.TypeOf("")
	tb := reflect.TypeOf(true)
	tt := reflect.TypeOf(time.Time{})

	cases := []struct {
		name string
		ci   ColumnInfo
		mode LargeNumberMode
		want Kind
	}{
		{"fractional NUMBER", ColumnInfo{ScanType: ft64, Precision: 10, Scale: 2}, LargeNumberString, KindFloat},
		{"integer NUMBER prec 10", ColumnInfo{ScanType: ft64, Precision: 10, Scale: 0}, LargeNumberString, KindInt},
		{"integer NUMBER prec 15", ColumnInfo{ScanType: ft64, Precision: 15, Scale: 0}, LargeNumberString, KindInt},
		{"large NUMBER string mode", ColumnInfo{ScanType: ft64, Precision: 16, Scale: 0}, LargeNumberString, KindLargeNumber},
		{"large NUMBER float mode", ColumnInfo{ScanType: ft64, Precision: 30, Scale: 0}, LargeNumberFloat, KindFloat},
		{"unconstrained NUMBER literal", ColumnInfo{ScanType: ft64, Precision: 0, Scale: 0}, LargeNumberString, KindFloat},
		{"BINARY_FLOAT (float32)", ColumnInfo{ScanType: ft32}, LargeNumberString, KindFloat},
		{"DATE/TIMESTAMP", ColumnInfo{ScanType: tt}, LargeNumberString, KindTime},
		{"VARCHAR2", ColumnInfo{ScanType: tstr}, LargeNumberString, KindString},
		{"BOOLEAN 23c", ColumnInfo{ScanType: tb}, LargeNumberString, KindBool},
		{"CLOB (nil ScanType)", ColumnInfo{ScanType: nil, DBTypeName: "LongVarChar"}, LargeNumberString, KindString},
		{"BLOB (nil ScanType)", ColumnInfo{ScanType: nil, DBTypeName: "LongRaw"}, LargeNumberString, KindBytes},
		{"unknown LOB (nil ScanType)", ColumnInfo{ScanType: nil, DBTypeName: "SomethingNew"}, LargeNumberString, KindString},
		{"unrecognized Go type reads as text", ColumnInfo{ScanType: reflect.TypeOf(int32(0))}, LargeNumberString, KindString},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.ci, tc.mode); got != tc.want {
				t.Errorf("Classify(%+v) = %v, want %v", tc.ci, got, tc.want)
			}
		})
	}
}

func TestScanDestAndValueOf(t *testing.T) {
	ts := time.Date(2026, 8, 8, 10, 30, 0, 0, time.FixedZone("+03", 3*3600))

	t.Run("round trips", func(t *testing.T) {
		checks := []struct {
			kind Kind
			val  any
		}{
			{KindInt, int64(42)},
			{KindFloat, 3.14},
			{KindString, "hi"},
			{KindLargeNumber, "12345678901234567890"},
			{KindTime, ts},
			{KindBool, true},
			{KindBytes, []byte{0x01, 0xff}},
		}
		for _, c := range checks {
			dest := ScanDest(c.kind)
			if err := setFromValue(dest, c.val); err != nil {
				t.Fatalf("kind %v: seed dest: %v", c.kind, err)
			}
			got := ValueOf(dest)
			if c.kind == KindTime {
				// ValueOf normalizes to UTC (§6.2 rule 5).
				want := ts.UTC()
				if !got.(time.Time).Equal(want) {
					t.Errorf("kind %v: ValueOf = %v, want %v", c.kind, got, want)
				}
				continue
			}
			if !reflect.DeepEqual(got, c.val) {
				t.Errorf("kind %v: ValueOf = %#v, want %#v", c.kind, got, c.val)
			}
		}
	})

	t.Run("NULL yields nil", func(t *testing.T) {
		for _, kind := range []Kind{KindInt, KindFloat, KindString, KindTime, KindBool} {
			if got := ValueOf(ScanDest(kind)); got != nil {
				t.Errorf("kind %v: ValueOf(NULL) = %#v, want nil", kind, got)
			}
		}
	})
}

// setFromValue seeds a scan destination the way database/sql.Scan would, so the
// round-trip test does not depend on a real driver.
func setFromValue(dest any, val any) error {
	switch d := dest.(type) {
	case *sql.NullInt64:
		d.Valid, d.Int64 = true, val.(int64)
	case *sql.NullFloat64:
		d.Valid, d.Float64 = true, val.(float64)
	case *sql.NullString:
		d.Valid, d.String = true, val.(string)
	case *sql.NullTime:
		d.Valid, d.Time = true, val.(time.Time)
	case *sql.NullBool:
		d.Valid, d.Bool = true, val.(bool)
	case *[]byte:
		*d = val.([]byte)
	default:
		return fmt.Errorf("unknown dest type %T", dest)
	}
	return nil
}

func TestEffectiveLargeMode(t *testing.T) {
	cases := []struct {
		config string
		format string
		want   LargeNumberMode
	}{
		{"", FormatTable, LargeNumberString},
		{"", FormatTimeSeries, LargeNumberFloat},
		{"", FormatAuto, LargeNumberString},
		{"string", FormatTable, LargeNumberString},
		{"string", FormatTimeSeries, LargeNumberString},
		{"float", FormatTable, LargeNumberFloat},
		{"float", FormatTimeSeries, LargeNumberFloat},
	}
	for _, tc := range cases {
		if got := EffectiveLargeMode(tc.config, tc.format); got != tc.want {
			t.Errorf("EffectiveLargeMode(%q, %q) = %q, want %q", tc.config, tc.format, got, tc.want)
		}
	}
}

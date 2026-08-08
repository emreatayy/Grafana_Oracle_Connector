// Package convert implements the typed Oracle→Grafana column mapping (report
// §6) and the frame-generation algorithm (report §7). It is deliberately pure:
// every function works on our own ColumnInfo and [][]any, so the whole mapping
// is unit-testable without a real database (report §6.3). The plugin's query
// path adapts database/sql rows into these inputs via a thin adapter.
package convert

import (
	"database/sql"
	"reflect"
	"time"
)

// Query formats (report §4.3). Values match the frontend MyQuery.format keys.
const (
	FormatAuto       = "auto"
	FormatTable      = "table"
	FormatTimeSeries = "time_series"
)

// Kind is the plugin-level column kind, decided from ColumnInfo (report §6.2
// rule 1). It drives both the scan destination and the Grafana field type.
type Kind uint8

const (
	KindInvalid Kind = iota
	KindFloat        // NUMBER(scale>0), FLOAT, BINARY_FLOAT/BINARY_DOUBLE
	KindInt          // NUMBER(scale=0, prec<=15): IDs/counters
	KindLargeNumber  // NUMBER(scale=0, prec>15) rendered as string (ADR-8)
	KindString       // CHAR/VARCHAR2/NVARCHAR2/CLOB
	KindTime         // DATE/TIMESTAMP*
	KindBytes        // BLOB/RAW
	KindBool         // BOOLEAN (23c+)
)

// LargeNumberMode is the ADR-8 policy for big NUMBER columns. The datasource
// config may fix one; when unset each format has its own default.
type LargeNumberMode string

const (
	LargeNumberString LargeNumberMode = "string" // table default
	LargeNumberFloat  LargeNumberMode = "float"  // time_series default
)

// ColumnInfo is our own column descriptor (report §6.3): the pure functions
// take []ColumnInfo so they are testable without a real database.
type ColumnInfo struct {
	Name       string
	DBTypeName string
	ScanType   reflect.Type
	Precision  int64 // from DecimalSize(); 0 when unknown
	Scale      int64
}

// InfoFromColumnType adapts a database/sql.ColumnType into ColumnInfo — the
// thin adapter of §6.3.
func InfoFromColumnType(ct *sql.ColumnType) ColumnInfo {
	ci := ColumnInfo{
		Name:       ct.Name(),
		DBTypeName: ct.DatabaseTypeName(),
		ScanType:   ct.ScanType(),
	}
	if prec, scale, ok := ct.DecimalSize(); ok {
		ci.Precision, ci.Scale = prec, scale
	}
	return ci
}

// Classify maps a ColumnInfo to a Kind using ScanType() + DecimalSize()
// (report §6.2 rules 1 and 4). DatabaseTypeName() is deliberately NOT used for
// type decisions — go-ora returns internal driver names — except as the last
// tiebreaker for LOB columns whose ScanType is nil (rule 1).
func Classify(ci ColumnInfo, large LargeNumberMode) Kind {
	st := ci.ScanType
	switch {
	case st == nil:
		// LOB columns are the only nil-ScanType case (verified). CLOB/NCLOB
		// read as text, BLOB/RAW as bytes (base64 in the frame); anything else
		// falls back to text rather than failing the whole query.
		switch ci.DBTypeName {
		case "LongVarChar":
			return KindString
		case "LongRaw":
			return KindBytes
		default:
			return KindString
		}
	case st == reflect.TypeOf(time.Time{}):
		return KindTime
	case st.Kind() == reflect.String:
		return KindString
	case st.Kind() == reflect.Bool:
		return KindBool
	case st.Kind() == reflect.Float32 || st.Kind() == reflect.Float64:
		// Every NUMBER reports float64 (verified), even scale=0; split by
		// DecimalSize (rule 4). Precision 0 = unconstrained NUMBER literal
		// (e.g. `1`) → float. BINARY_FLOAT (float32) has no size → float.
		switch {
		case ci.Scale > 0:
			return KindFloat
		case ci.Scale == 0 && ci.Precision > 0 && ci.Precision <= 15:
			return KindInt
		case ci.Scale == 0 && ci.Precision > 15:
			if large == LargeNumberFloat {
				return KindFloat
			}
			return KindLargeNumber
		default: // precision unknown
			return KindFloat
		}
	default:
		// Anything unrecognized reads as text (never fail the whole query).
		return KindString
	}
}

// ScanDest returns a fresh scan destination for a Kind (report §6.2 rule 2).
// go-ora supports every target here (verified against v2.9.0 value_setter.go):
// NUMBER arrives as a string in the row and setString parses it into the Null*
// target; NULL zeroes the struct (Valid=false); CLOB→sql.NullString and
// BLOB→[]byte are handled by setLob. As a bonus, sql.Null* implements
// sql.Scanner, so even an unknown driver value falls back cleanly.
func ScanDest(kind Kind) any {
	switch kind {
	case KindFloat:
		return new(sql.NullFloat64)
	case KindInt:
		return new(sql.NullInt64)
	case KindLargeNumber, KindString:
		return new(sql.NullString)
	case KindTime:
		return new(sql.NullTime)
	case KindBytes:
		return new([]byte)
	case KindBool:
		return new(sql.NullBool)
	default:
		return new(any)
	}
}

// ValueOf extracts the concrete value from a scanned destination, or nil for
// NULL. Time values are normalized to UTC (report §6.2 rule 5) — the session
// TIME_ZONE is already pinned to UTC (F1); .UTC() is belt-and-braces for
// TIMESTAMP WITH TIME ZONE columns.
func ValueOf(dest any) any {
	switch d := dest.(type) {
	case *sql.NullInt64:
		if !d.Valid {
			return nil
		}
		return d.Int64
	case *sql.NullFloat64:
		if !d.Valid {
			return nil
		}
		return d.Float64
	case *sql.NullString:
		if !d.Valid {
			return nil
		}
		return d.String
	case *sql.NullTime:
		if !d.Valid {
			return nil
		}
		return d.Time.UTC()
	case *sql.NullBool:
		if !d.Valid {
			return nil
		}
		return d.Bool
	case *[]byte:
		if *d == nil {
			return nil
		}
		return *d
	}
	return nil
}

// IsValueKind reports whether a Kind can be a time-series value column
// (report §7.1: numeric + boolean). KindLargeNumber counts even though it scans
// as text — the column IS numeric in Oracle, which is all the auto decision
// needs (§7.2).
func IsValueKind(kind Kind) bool {
	switch kind {
	case KindFloat, KindInt, KindLargeNumber, KindBool:
		return true
	}
	return false
}

// EffectiveLargeMode resolves the ADR-8 policy: an explicit config value wins,
// otherwise the per-format default — string for table, float for time_series.
func EffectiveLargeMode(config string, format string) LargeNumberMode {
	switch config {
	case string(LargeNumberString):
		return LargeNumberString
	case string(LargeNumberFloat):
		return LargeNumberFloat
	}
	if format == FormatTimeSeries {
		return LargeNumberFloat
	}
	return LargeNumberString
}

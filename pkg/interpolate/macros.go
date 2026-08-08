package interpolate

import (
	"fmt"
	"strconv"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data/sqlutil"
)

// oracleTimeLayout formats a time range bound for TO_DATE(..., 'YYYY-MM-DD
// HH24:MI:SS') — the ADR-9 literal that preserves DATE index range scans and is
// NLS-independent (ANSI TIMESTAMP literals force full scans).
const oracleTimeLayout = "2006-01-02 15:04:05"

// OracleMacros supplies the $__ macros this datasource understands (report
// §8.1). Keys present here override sqlutil.DefaultMacros; interval and
// interval_ms are left to the defaults, which read the derived query interval.
// Everything a SQL editor must never emit silently — timeGroup (T-SQL
// datepart), table/column (empty strings for us) — is shadowed with an explicit
// error so users never get broken SQL.
var OracleMacros = sqlutil.Macros{
	"from":            macroFrom,
	"to":              macroTo,
	"timeFilter":      macroTimeFilter,
	"timeFrom":        macroTimeFrom,
	"timeTo":          macroTimeTo,
	"unixEpochFilter": macroUnixEpochFilter,
	"unixEpochGroup":  macroUnixEpochGroup,
	"interval_s":      macroIntervalS,
	"time":            macroTime,
	"timeGroup":       macroTimeGroupUnsupported,
	"table":           macroUnsupported("table"),
	"column":          macroUnsupported("column"),
}

// requireArgs enforces a macro's argument count with a wrapped
// sqlutil.ErrorBadArgumentCount (the convention sqlutil itself uses).
func requireArgs(name string, args []string, n int) error {
	if len(args) != n {
		return fmt.Errorf("%w: %s expects %d argument, received %d", sqlutil.ErrorBadArgumentCount, name, n, len(args))
	}
	return nil
}

// macroFrom and macroTo expand to the query range as bare epoch milliseconds.
func macroFrom(q *sqlutil.Query, _ []string) (string, error) {
	return strconv.FormatInt(q.TimeRange.From.UnixMilli(), 10), nil
}

func macroTo(q *sqlutil.Query, _ []string) (string, error) {
	return strconv.FormatInt(q.TimeRange.To.UnixMilli(), 10), nil
}

// toDate renders a bound as an ADR-9 TO_DATE literal.
func toDate(t time.Time) string {
	return fmt.Sprintf("TO_DATE('%s','YYYY-MM-DD HH24:MI:SS')", t.UTC().Format(oracleTimeLayout))
}

// macroTimeFilter expands to the inclusive range filter on a DATE column.
func macroTimeFilter(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__timeFilter", args, 1); err != nil {
		return "", err
	}
	col := args[0]
	return fmt.Sprintf("%s >= %s AND %s <= %s", col, toDate(q.TimeRange.From), col, toDate(q.TimeRange.To)), nil
}

func macroTimeFrom(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__timeFrom", args, 1); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s >= %s", args[0], toDate(q.TimeRange.From)), nil
}

func macroTimeTo(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__timeTo", args, 1); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s <= %s", args[0], toDate(q.TimeRange.To)), nil
}

// macroUnixEpochFilter expands to epoch-second comparisons — for NUMBER columns
// that store Unix time.
func macroUnixEpochFilter(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__unixEpochFilter", args, 1); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s >= %d AND %s <= %d", args[0], q.TimeRange.From.Unix(), args[0], q.TimeRange.To.Unix()), nil
}

// macroUnixEpochGroup buckets an epoch-second column into N-second groups.
func macroUnixEpochGroup(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__unixEpochGroup", args, 2); err != nil {
		return "", err
	}
	n, err := parseIntervalSeconds(args[1])
	if err != nil {
		return "", fmt.Errorf("$__unixEpochGroup: %w", err)
	}
	return fmt.Sprintf("FLOOR(%s / %d) * %d", args[0], n, n), nil
}

// macroIntervalS emits the derived interval in whole seconds (0 for
// sub-second intervals).
func macroIntervalS(q *sqlutil.Query, _ []string) (string, error) {
	return strconv.FormatInt(q.Interval.Milliseconds()/1000, 10), nil
}

// macroTime returns its bare column argument — the sqlutil default does not
// exist, so this is a convenience alias that keeps the column unquoted.
func macroTime(q *sqlutil.Query, args []string) (string, error) {
	if err := requireArgs("$__time", args, 1); err != nil {
		return "", err
	}
	return args[0], nil
}

// macroTimeGroupUnsupported shadows the T-SQL default so a SQL editor never
// gets broken datepart(...) output.
func macroTimeGroupUnsupported(_ *sqlutil.Query, _ []string) (string, error) {
	return "", fmt.Errorf("$__timeGroup is not supported for Oracle — use $__unixEpochGroup(column, interval) for epoch-second bucketing")
}

// macroUnsupported errors for $__table/$__column, which have no meaning for a
// datasource without a configured table (the sqlutil defaults would emit "").
func macroUnsupported(name string) sqlutil.MacroFunc {
	return func(_ *sqlutil.Query, _ []string) (string, error) {
		return "", fmt.Errorf("$__%s is not supported by this datasource", name)
	}
}

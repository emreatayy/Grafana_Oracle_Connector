// Package interpolate expands Grafana query macros ($__*) and template
// variables (${var}) in Oracle SQL on the backend (report §8, ADR-2). It is a
// pure package — no DB access — so the whole engine is table-driven testable
// (report §16).
//
// Interpolation is backend-only by design: alert rules are evaluated
// server-side without a frontend template engine (§8.3), so both macros and
// variables are expanded here, never in the browser.
package interpolate

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data/sqlutil"
)

// Params feeds the interpolation engine. TimeRange/Interval/MaxDataPoints come
// from the Grafana query; ScopedVars from the frontend (applyTemplateVariables
// — empty for alert queries, §8.3); NumericVars from the datasource config
// (§8.2).
type Params struct {
	TimeRange     backend.TimeRange
	Interval      time.Duration
	MaxDataPoints int64
	ScopedVars    map[string]any
	NumericVars   map[string]bool
}

// Interpolate expands $__ macros then ${var} templates in raw SQL.
//
// The macro-first order is safe (validated): after the macro pass no $__
// remains, so a variable value can never re-enter macro expansion, and macro
// output (TO_DATE literals, epoch numbers) contains no ${} for the variable
// pass to re-scan. The variable pass is single-shot — inserted values are never
// re-expanded (a value containing "$__" or "${...}" stays literal).
//
// Every guard fails loudly with a message that names the macro/variable, never
// the SQL (report §15): a typo'd $__ macro, a malformed ${, a variable nested
// inside a macro argument, or an unresolved variable all return an error rather
// than SQL that silently mis-evaluates.
func Interpolate(raw string, p Params) (string, error) {
	if err := guardMacroArgs(raw); err != nil {
		return "", err
	}

	q := &sqlutil.Query{
		RawSQL:        raw,
		Interval:      deriveInterval(p.Interval, p.MaxDataPoints, p.TimeRange),
		TimeRange:     p.TimeRange,
		MaxDataPoints: p.MaxDataPoints,
	}
	sql, err := sqlutil.Interpolate(q, OracleMacros)
	if err != nil {
		return "", err
	}

	// Leftover $__ (unknown/lowercase macro) and an unterminated ${ are rejected
	// before variable expansion — at this point they can only come from the
	// author's SQL, never from a variable value.
	if err := guardAfterMacros(sql); err != nil {
		return "", err
	}

	return interpolateVars(sql, p)
}

// macroArgRe matches a $__ macro with a parenthesised argument list. It is a
// coarse scanner (nested parens not tracked) — only used for the argument
// guard; sqlutil performs the real parse.
var macroArgRe = regexp.MustCompile(`\$__[a-zA-Z]+\s*\(([^)]*)\)`)

// guardMacroArgs rejects a ${var} nested inside a $__macro(...) argument. With
// macros expanded first, "$__timeFilter(${col})" would otherwise quote the
// column name and later fail or mis-evaluate — reject it up front.
func guardMacroArgs(raw string) error {
	for _, m := range macroArgRe.FindAllStringSubmatch(raw, -1) {
		if strings.Contains(m[1], "${") {
			return fmt.Errorf("variables inside macro arguments are not supported; use a literal column name")
		}
	}
	return nil
}

// unterminatedVarRe matches a "${" that never closes (a malformed variable).
var unterminatedVarRe = regexp.MustCompile(`\$\{[^{}]*$`)

// guardAfterMacros rejects what the macro pass silently left behind (report
// §15): an unknown $__ macro (typo or wrong case) and an unterminated ${.
func guardAfterMacros(sql string) error {
	if strings.Contains(sql, "$__") {
		return fmt.Errorf("unknown macro: the query contains a $__ macro this datasource does not support (check spelling and case)")
	}
	if m := unterminatedVarRe.FindString(sql); m != "" {
		return fmt.Errorf("malformed variable: %q is missing its closing brace", m)
	}
	return nil
}

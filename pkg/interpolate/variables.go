package interpolate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// varRe matches a ${...} template. The inner group excludes braces, so a nested
// "${" never matches — substitution is single-pass and inserted values are not
// re-scanned.
var varRe = regexp.MustCompile(`\$\{([^{}]+)\}`)

// numericRe is the strict numeric-literal gate for emitting a value bare in SQL
// (§8.2). No exponent, no separators, no whitespace — "1e5", "1_000" and
// "5) OR 1=1 --" never pass, so a bare value can never smuggle SQL.
var numericRe = regexp.MustCompile(`^[+-]?(\d+(\.\d+)?|\.\d+)$`)

// interpolateVars replaces every ${name[:fmt]} in sql using p.ScopedVars. It is
// the single variable pass: ReplaceAllStringFunc feeds each token's replacement
// straight into the output, never re-scanning it, so a value containing "$__"
// or "${...}" stays literal. All tokens are validated before substitution; the
// first error aborts.
func interpolateVars(sql string, p Params) (string, error) {
	var err error
	out := varRe.ReplaceAllStringFunc(sql, func(match string) string {
		if err != nil {
			return "" // a previous token already failed; output is discarded
		}
		inner := match[2 : len(match)-1] // strip "${" and "}"
		name, format, hasFormat := splitVarFormat(inner)
		s, e := renderVar(name, format, hasFormat, p)
		if e != nil {
			err = e
			return ""
		}
		return s
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// splitVarFormat splits "name:format" on the last colon (the format is
// optional).
func splitVarFormat(inner string) (name, format string, hasFormat bool) {
	if i := strings.LastIndex(inner, ":"); i >= 0 {
		return inner[:i], inner[i+1:], true
	}
	return inner, "", false
}

// renderVar resolves one ${name[:fmt]} token.
func renderVar(name, format string, hasFormat bool, p Params) (string, error) {
	val, found := p.ScopedVars[name]
	if !found {
		return "", fmt.Errorf("unresolved variable: ${%s} — select a value in the dashboard or add a default", name)
	}

	switch format {
	case "", "csv", "singlequote":
	default:
		return "", fmt.Errorf("unsupported variable format :%s in ${%s} (supported: plain, :csv, :singlequote)", format, name)
	}

	if arr, ok := asVarArray(val); ok {
		if !hasFormat || format == "" {
			return "", fmt.Errorf("variable ${%s} is multi-valued — use ${%s:csv} for multi-value", name, name)
		}
		if len(arr) == 0 {
			return "NULL", nil // IN (NULL) → no selection, no rows (§8.2)
		}
		parts := make([]string, len(arr))
		for i, el := range arr {
			s, err := renderVarElement(name, format, el, p.NumericVars[name])
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return strings.Join(parts, ", "), nil
	}

	return renderVarElement(name, format, val, p.NumericVars[name])
}

// renderVarElement renders one value for a format. plain and csv share the
// numeric gate; singlequote always quotes.
func renderVarElement(name, format string, val any, numericHint bool) (string, error) {
	switch v := val.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil // Oracle has no boolean literal — 1/0 (§8.2)
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil // never %v: "1.7e+09" is an invalid Oracle literal
	case string:
		switch v {
		case "$__all", "$__all_var", "$__alll":
			return "", fmt.Errorf("variable ${%s} has 'All' selected — expand all values or set a default", name)
		}
		if format == "singlequote" {
			return "'" + escapeQuote(v) + "'", nil
		}
		return quoteOrBare(v, numericHint), nil
	default:
		if format == "singlequote" {
			return "'" + escapeQuote(fmt.Sprint(v)) + "'", nil
		}
		return quoteOrBare(fmt.Sprint(v), numericHint), nil
	}
}

// quoteOrBare emits a string either as a bare numeric literal or a quoted
// string. The numeric hint (the variable is in numericVariables OR all digits)
// only permits *trying* bare; numericRe still gates it (§8.2).
func quoteOrBare(s string, numericHint bool) string {
	if (numericHint || allDigits(s)) && numericRe.MatchString(s) {
		return s
	}
	return "'" + escapeQuote(s) + "'"
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// escapeQuote doubles single quotes — the SQL injection defense (§15).
func escapeQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// asVarArray reports whether val is a slice (a multi-value variable).
func asVarArray(val any) ([]any, bool) {
	switch a := val.(type) {
	case []any:
		return a, true
	case []string:
		out := make([]any, len(a))
		for i := range a {
			out[i] = a[i]
		}
		return out, true
	default:
		return nil, false
	}
}

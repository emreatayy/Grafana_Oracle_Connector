package oracle

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// oraCodeRe extracts the ORA-#### code from a driver error message.
var oraCodeRe = regexp.MustCompile(`ORA-\s*([0-9]{5})`)

// knownORA maps common ORA codes to a short, human-readable hint (report §5.3,
// enriched in §13/F10). Unknown codes fall through to the original message.
// The set targets the report's §13 "Test" scenarios (bad password, URL/service,
// connection) plus the query errors users actually hit.
var knownORA = map[string]string{
	// Connection & credentials (report §13)
	"01017": "invalid username or password",
	"01033": "ORACLE initialization or shutdown in progress",
	"01034": "Oracle not available (instance not running)",
	"12154": "TNS: could not resolve the connect identifier (check host and service name)",
	"12170": "TNS: connect timeout (network or listener unreachable)",
	"12505": "TNS: listener does not currently know of the requested service",
	"12514": "service not known — check the service name / SID",
	"12541": "TNS: no listener on the given host and port",
	"12545": "connect failed — target host does not exist or is unreachable",
	"12560": "TNS: protocol adapter error (listener or network issue)",
	"27101": "shared memory realm does not exist (instance not running)",
	"28000": "the account is locked",
	"28001": "the password has expired",
	"28009": "connection to the server failed (Oracle Net configuration error)",
	// Query errors
	"00054": "resource busy (acquire with NOWAIT or wait for the lock)",
	"00900": "invalid SQL statement",
	"00904": "invalid identifier (unknown column)",
	"00907": "missing right parenthesis",
	"00911": "invalid character in SQL statement",
	"00932": "inconsistent datatypes — a value type does not match the column/expected type",
	"00933": "SQL command not properly ended",
	"00942": "table or view does not exist",
	"00979": "not a GROUP BY expression",
	"01403": "no data found",
	"01722": "invalid number",
	"04063": "object has errors — a package, procedure or function is invalid (compile it first)",
	"06550": "PL/SQL error — see the line/column in the message",
}

// networkFragments detect transport-level failures that carry no ORA code.
var networkFragments = []string{
	"dial ",
	"connection refused",
	"no such host",
	"i/o timeout",
	"connection timed out",
	"network is unreachable",
	"broken pipe",
	"connect: connection",
}

// FormatError converts a driver error into a readable, safe message. It never
// includes raw SQL; credentials are redacted separately via Redact where the
// password is known.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()

	if m := oraCodeRe.FindStringSubmatch(msg); len(m) > 1 {
		code := m[1]
		if hint, ok := knownORA[code]; ok {
			return fmt.Sprintf("ORA-%s: %s", code, hint)
		}
		// Unknown code: keep the code plus the text that follows it on the
		// same line (e.g. "ORA-20001: custom application error").
		loc := oraCodeRe.FindStringIndex(msg)
		detail := strings.TrimSpace(msg[loc[1]:])
		detail = strings.TrimPrefix(detail, ":")
		detail = strings.TrimSpace(detail)
		if i := strings.IndexAny(detail, "\r\n"); i >= 0 {
			detail = detail[:i]
		}
		detail = strings.TrimSpace(detail)
		if detail == "" {
			detail = firstLine(msg)
		}
		return fmt.Sprintf("ORA-%s: %s", code, detail)
	}

	lower := strings.ToLower(msg)
	for _, frag := range networkFragments {
		if strings.Contains(lower, frag) {
			return "network error: " + firstLine(msg)
		}
	}

	return firstLine(msg)
}

// Redact removes any occurrence of secret from msg so it can be shown in the
// UI or logs. The raw, path-escaped, and query-escaped forms are all removed
// (a DSN embedding would carry the escaped form).
func Redact(msg, secret string) string {
	if secret == "" || msg == "" {
		return msg
	}
	for _, s := range []string{secret, url.PathEscape(secret), url.QueryEscape(secret)} {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}

// firstLine returns the first non-empty line of msg, trimmed.
func firstLine(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return msg
}

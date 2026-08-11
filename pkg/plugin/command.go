package plugin

// F15 — command execution (report §12, ADR-13). Operational tasks
// (start/stop/enable/disable scheduler jobs, call procedures/functions) cannot
// flow through the query pipeline — Grafana re-runs queries on refresh,
// alerting and variable changes, so a side-effecting action must be an
// explicit, one-shot user action. Commands are therefore a separate
// CallResource path:
//
//	parse {sql, confirm} → require confirm=true → allowlist check (§12.3)
//	→ ExecContext (BEGIN…END block) / QueryRow (SELECT … FROM DUAL)
//	→ respond {ok, message, rowsAffected?}; every attempt audit-logged.
//
// The same Oracle account is used for queries and commands; the query path
// keeps its read-only guard, the command path is restricted by the allowlist
// (single-account model, §12.3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"

	"github.com/emreatayy/oracle-datasource/pkg/oracle"
)

// commandRequest is the body the frontend posts (report §12.1). confirm must
// always be true — the backend never executes an unconfirmed command, even when
// the datasource setting disables the UI dialog (that setting only skips the
// dialog; the frontend still sends confirm=true).
type commandRequest struct {
	SQL     string `json:"sql"`
	Confirm bool   `json:"confirm"`
}

// commandResponse is the structured result rendered inline (report §12.1).
type commandResponse struct {
	OK           bool   `json:"ok"`
	Message      string `json:"message"`
	RowsAffected int64  `json:"rowsAffected,omitempty"`
}

// commandResource serves the "command" path under CallResource (report §12.1).
func (d *Datasource) commandResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	var body commandRequest
	if len(req.Body) == 0 {
		return sender.Send(resourceError(http.StatusBadRequest,
			"command requires a JSON body {sql, confirm}"))
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return sender.Send(resourceError(http.StatusBadRequest, "invalid command request body"))
	}

	uid, user := "", ""
	if req.PluginContext.DataSourceInstanceSettings != nil {
		uid = req.PluginContext.DataSourceInstanceSettings.UID
	}
	if req.PluginContext.User != nil {
		user = req.PluginContext.User.Login
	}

	if !body.Confirm {
		log.DefaultLogger.Warn("oracle command rejected: not confirmed",
			"datasource", uid, "user", user, "command", d.auditCommand(body.SQL))
		return sender.Send(resourceError(http.StatusBadRequest,
			"command was not confirmed — no action taken"))
	}

	if err := validateCommand(body.SQL); err != nil {
		log.DefaultLogger.Warn("oracle command rejected: not allowlisted",
			"datasource", uid, "user", user, "command", d.auditCommand(body.SQL))
		return sender.Send(resourceError(http.StatusBadRequest, err.Error()))
	}

	log.DefaultLogger.Info("oracle command executed",
		"datasource", uid, "user", user, "command", d.auditCommand(body.SQL))

	if err := d.ensureReady(ctx); err != nil {
		return sender.Send(resourceError(http.StatusInternalServerError, d.safeMessage(err)))
	}

	// commandTimeoutSec bounds execution (report §12.3); maxRows/maxSeries do
	// not apply — commands never produce a data frame.
	if d.settings.CommandTimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d.settings.CommandTimeoutSec)*time.Second)
		defer cancel()
	}

	var (
		res  = commandResponse{OK: true, Message: "command completed"}
		exec func() error
	)
	if isSelectDual(body.SQL) {
		// A function call: the returned value is the command's result (§12.1).
		exec = func() error {
			var val any
			if err := d.db.QueryRowContext(ctx, body.SQL).Scan(&val); err != nil {
				return err
			}
			res.Message = fmt.Sprintf("%v", val)
			return nil
		}
	} else {
		// An anonymous PL/SQL block; rowsAffected is reported when the driver
		// provides one (blocks typically return -1, so it is omitted).
		exec = func() error {
			result, err := d.db.ExecContext(ctx, body.SQL)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err == nil && n >= 0 {
				res.RowsAffected = n
			}
			return nil
		}
	}
	if err := exec(); err != nil {
		log.DefaultLogger.Error("oracle command failed",
			"datasource", uid, "user", user, "error", d.safeMessage(err))
		return sender.Send(resourceError(http.StatusInternalServerError, d.safeMessage(err)))
	}

	out, err := json.Marshal(res)
	if err != nil {
		return sender.Send(resourceError(http.StatusInternalServerError, "command response encode failed"))
	}
	return sender.Send(&backend.CallResourceResponse{
		Status:  http.StatusOK,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    out,
	})
}

// auditCommand is the statement as written to the audit log: whitespace
// collapsed to one line and the configured password redacted (report §12.3,
// §15 — the statement is the audit record, values never are).
func (d *Datasource) auditCommand(sql string) string {
	msg := strings.Join(strings.Fields(sql), " ")
	if d.settings != nil {
		msg = oracle.Redact(msg, d.settings.Password())
	}
	return msg
}

// isSelectDual reports whether a (already allowlisted) command is the
// SELECT … FROM DUAL form, which returns a value instead of a block result.
func isSelectDual(sql string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToUpper(sql)), "SELECT")
}

// validateCommand checks a command against the §12.3 allowlist and returns an
// English, UI-safe error on rejection. Allowed shapes:
//
//   - BEGIN … END; blocks whose statements are DBMS_SCHEDULER procedure calls
//     (any procedure, including SET_ATTRIBUTE) or plain procedure calls;
//   - SELECT <expr> FROM DUAL (function calls returning a value).
//
// Rejected: DDL/DML, DROP, TRUNCATE, control flow, comments, quoted
// identifiers, anything that does not match the allowlisted call shape.
func validateCommand(sql string) error {
	s := strings.TrimSpace(sql)
	if s == "" {
		return errors.New("command is empty")
	}
	if strings.Contains(s, "--") || strings.Contains(s, "/*") {
		return errors.New("comments are not allowed in commands")
	}
	switch firstKeyword(s) {
	case "BEGIN":
		return validateBeginBlock(s)
	case "SELECT":
		return validateSelectDual(s)
	}
	return errors.New("command must be a BEGIN...END; PL/SQL block or a SELECT ... FROM DUAL")
}

// validateBeginBlock validates the BEGIN … END; form. Each inner statement must
// be a single allowlisted procedure call, ;-terminated, with no control flow.
func validateBeginBlock(sql string) error {
	s := strings.TrimSpace(sql)
	if !strings.HasSuffix(s, ";") {
		return errors.New("a BEGIN block must end with ';'")
	}
	// Drop the final ';' and require the trailing word to be END.
	body := strings.TrimSpace(s[:len(s)-1])
	if !strings.HasSuffix(strings.ToUpper(body), "END") {
		return errors.New("a BEGIN block must end with 'END;'")
	}
	section := strings.TrimSpace(body[:len(body)-len("END")])
	if !strings.HasPrefix(strings.ToUpper(section), "BEGIN") {
		return errors.New("a BEGIN block must start with 'BEGIN'")
	}
	inner := strings.TrimSpace(section[len("BEGIN"):])
	if inner == "" {
		return errors.New("a BEGIN block contains no statements")
	}
	if !strings.HasSuffix(inner, ";") {
		return errors.New("each statement in a BEGIN block must end with ';'")
	}
	stmts, err := splitStatements(inner)
	if err != nil {
		return err
	}
	if len(stmts) == 0 {
		return errors.New("a BEGIN block contains no statements")
	}
	for _, stmt := range stmts {
		if err := validateCommandCall(stmt); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements splits a ;-terminated statement section on top-level
// semicolons, honouring string literals and nested parentheses, so a ';' inside
// an argument never hides an extra statement.
func splitStatements(s string) ([]string, error) {
	var stmts []string
	depth := 0
	inString := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // '' escape inside a literal
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced parentheses in command")
			}
		case c == ';' && depth == 0:
			if stmt := strings.TrimSpace(s[start:i]); stmt != "" {
				stmts = append(stmts, stmt)
			}
			start = i + 1
		}
	}
	if inString {
		return nil, errors.New("unterminated string literal in command")
	}
	if depth != 0 {
		return nil, errors.New("unbalanced parentheses in command")
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		stmts = append(stmts, tail)
	}
	return stmts, nil
}

// identRe matches a single Oracle identifier: a letter or underscore followed
// by letters, digits, $, # or _.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]*$`)

// denylistedPackages are well-known Oracle packages that can execute arbitrary
// SQL or DDL (DBMS_SQL.EXECUTE, DBMS_UTILITY.EXEC_DDL_STATEMENT / EXEC_SQL_STATEMENT),
// which would defeat the allowlist even though they are technically "plain
// procedure calls". A deliberate hardening of §12.3, documented in the report.
var denylistedPackages = map[string]bool{
	"DBMS_SQL":     true,
	"DBMS_SYS_SQL": true,
	"DBMS_UTILITY": true,
}

// validateCommandCall validates one inner statement: it must be a single
// procedure call of the form name(.name)* (args) with balanced arguments, whose
// package is DBMS_SCHEDULER (any procedure, §12.3) or a plain, non-denylisted
// procedure.
func validateCommandCall(stmt string) error {
	s := strings.TrimSpace(stmt)
	i := 0
	for i < len(s) && (isIdentChar(s[i]) || s[i] == '.') {
		i++
	}
	path := s[:i]
	if path == "" {
		return fmt.Errorf("statement %q is not allowlisted: expected a procedure call", stmt)
	}
	for _, seg := range strings.Split(path, ".") {
		if !identRe.MatchString(seg) {
			return fmt.Errorf("statement %q is not allowlisted: invalid name %q (quoted identifiers are not allowed)", stmt, seg)
		}
	}
	if i >= len(s) || s[i] != '(' {
		return fmt.Errorf("statement %q is not allowlisted: expected '(' after the procedure name", stmt)
	}
	end, err := matchParens(s, i)
	if err != nil {
		return fmt.Errorf("statement %q: %v", stmt, err)
	}
	if rest := strings.TrimSpace(s[end:]); rest != "" {
		return fmt.Errorf("statement %q is not allowlisted: unexpected trailing %q", stmt, rest)
	}
	for _, seg := range strings.Split(path, ".") {
		if denylistedPackages[strings.ToUpper(seg)] {
			return fmt.Errorf("statement %q is not allowlisted: package %s can execute arbitrary SQL", stmt, strings.ToUpper(seg))
		}
		if strings.EqualFold(seg, "DBMS_SCHEDULER") {
			return nil // any DBMS_SCHEDULER procedure is allowed (§12.3)
		}
	}
	return nil // plain procedure call (§12.3)
}

// matchParens returns the index just past the closing ')' that matches the '('
// at s[open], honouring string literals and nested parentheses.
func matchParens(s string, open int) (int, error) {
	depth := 0
	inString := false
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
			if depth < 0 {
				return 0, errors.New("unbalanced parentheses")
			}
		}
	}
	return 0, errors.New("unbalanced parentheses")
}

// validateSelectDual validates the SELECT <expr> FROM DUAL form. The expression
// is unrestricted — it is read-only (DUAL has no rows to mutate) and the
// useful command is a function call whose return value becomes the result.
func validateSelectDual(sql string) error {
	s := strings.TrimSpace(sql)
	s = strings.TrimSuffix(s, ";")
	expr := strings.TrimSpace(s[len("SELECT"):])
	fromIdx := findTopLevelKeyword(expr, "FROM")
	if fromIdx < 0 {
		return errors.New("a SELECT command must read from DUAL (SELECT ... FROM DUAL)")
	}
	if sel := strings.TrimSpace(expr[:fromIdx]); sel == "" {
		return errors.New("a SELECT command is missing an expression")
	} else if strings.Contains(sel, ";") {
		return errors.New("a SELECT command contains an extra statement")
	}
	table := strings.TrimSpace(expr[fromIdx+len("FROM"):])
	if !strings.EqualFold(table, "DUAL") {
		return fmt.Errorf("a SELECT command must read from DUAL, not %q", table)
	}
	return nil
}

// findTopLevelKeyword returns the index of the first occurrence of the word kw
// outside string literals and parentheses in s, or -1.
func findTopLevelKeyword(s, kw string) int {
	depth := 0
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inString = false
			}
		case c == '\'':
			inString = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		default:
			if depth == 0 && isIdentStart(c) {
				j := i
				for j < len(s) && isIdentChar(s[j]) {
					j++
				}
				if strings.ToUpper(s[i:j]) == kw {
					return i
				}
				i = j - 1
			}
		}
	}
	return -1
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || c == '#' || (c >= '0' && c <= '9')
}

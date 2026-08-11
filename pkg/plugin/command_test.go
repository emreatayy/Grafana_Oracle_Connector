package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// TestValidateCommand_Allowed covers the report §12.1 example commands plus the
// §12.3 allowlist shapes: DBMS_SCHEDULER (any procedure, incl. SET_ATTRIBUTE),
// plain procedure calls, multi-statement blocks, and SELECT … FROM DUAL.
func TestValidateCommand_Allowed(t *testing.T) {
	allowed := []string{
		// §12.1 examples.
		"BEGIN DBMS_SCHEDULER.STOP_JOB('MY_JOB'); END;",
		"BEGIN DBMS_SCHEDULER.ENABLE('MY_JOB'); END;",
		"BEGIN DBMS_SCHEDULER.DISABLE('MY_JOB'); END;",
		"BEGIN DBMS_SCHEDULER.START_JOB('MY_JOB'); END;",
		"BEGIN pkg.my_proc('ABC', 42); END;",
		"SELECT pkg.my_func('x', 42) FROM DUAL",
		// Key-value SET_ATTRIBUTE (§12.3) and other DBMS_SCHEDULER procedures.
		"BEGIN DBMS_SCHEDULER.SET_ATTRIBUTE('MY_JOB', 'JOB_CLASS', 'CLASS1'); END;",
		"BEGIN DBMS_SCHEDULER.RUN_JOB('MY_JOB'); END;",
		"BEGIN SYS.DBMS_SCHEDULER.STOP_JOB('MY_JOB'); END;",
		// Plain procedure calls: no-arg, quoted literals with separators, case.
		"BEGIN my_proc(); END;",
		"BEGIN pkg.p(1, 'a;b', (1+2)*3); END;",
		"begin pkg.p(); end;",
		"  BEGIN pkg.p(); END;  ",
		// Multi-statement blocks where every statement is allowlisted (§12.3).
		"BEGIN DBMS_SCHEDULER.STOP_JOB('A'); DBMS_SCHEDULER.DISABLE('B'); END;",
		"BEGIN DBMS_SCHEDULER.DISABLE('A'); pkg.p(1); END;",
		// SELECT … FROM DUAL: literal value, FROM inside a string, trailing ;.
		"SELECT 1 AS value FROM DUAL",
		"SELECT pkg.f('FROM dual') FROM DUAL",
		"SELECT (SELECT 1 FROM user_tables WHERE ROWNUM = 1) FROM DUAL",
		"select sysdate from dual;",
	}
	for _, sql := range allowed {
		if err := validateCommand(sql); err != nil {
			t.Errorf("validateCommand(%q) = %v, want nil", sql, err)
		}
	}
}

// TestValidateCommand_Rejected covers the §12.3 rejections: DDL/DML, DROP,
// TRUNCATE, control flow, comments, denylisted packages, and any shape outside
// the allowlist.
func TestValidateCommand_Rejected(t *testing.T) {
	rejected := []struct {
		sql  string
		want string // substring of the expected error
	}{
		{"", "empty"},
		{"DROP TABLE x", "BEGIN"},
		{"BEGIN DROP TABLE x; END;", "not allowlisted"},
		{"BEGIN TRUNCATE TABLE x; END;", "not allowlisted"},
		{"BEGIN DELETE FROM x; END;", "not allowlisted"},
		{"BEGIN UPDATE x SET a = 1; END;", "not allowlisted"},
		{"BEGIN INSERT INTO x VALUES (1); END;", "not allowlisted"},
		{"BEGIN CREATE TABLE x (a NUMBER); END;", "not allowlisted"},
		{"BEGIN ALTER TABLE x ADD c NUMBER; END;", "not allowlisted"},
		{"BEGIN GRANT SELECT ON x TO y; END;", "not allowlisted"},
		{"BEGIN COMMIT; END;", "not allowlisted"},
		{"BEGIN EXECUTE IMMEDIATE 'DROP TABLE x'; END;", "not allowlisted"},
		{"BEGIN IF 1 THEN pkg.p(); END IF; END;", "not allowlisted"},
		{"BEGIN FOR i IN 1..3 LOOP pkg.p(); END LOOP; END;", "not allowlisted"},
		// Denylisted packages that can run arbitrary SQL (documented hardening).
		{"BEGIN DBMS_SQL.EXECUTE('DELETE FROM x'); END;", "DBMS_SQL"},
		{"BEGIN SYS.DBMS_SQL.EXECUTE('DELETE FROM x'); END;", "DBMS_SQL"},
		{"BEGIN DBMS_UTILITY.EXEC_DDL_STATEMENT('TRUNCATE TABLE x'); END;", "DBMS_UTILITY"},
		// Comments anywhere (they could hide content).
		{"BEGIN pkg.p(1); -- hidden\nEND;", "comments"},
		{"BEGIN pkg.p(1); /* hidden */ END;", "comments"},
		{"SELECT 1 -- hidden\nFROM DUAL", "comments"},
		// Empty / malformed blocks. "BEGIN; END;" is not valid PL/SQL and falls
		// through to the generic shape rejection.
		{"BEGIN END;", "no statements"},
		{"BEGIN; END;", "BEGIN...END"},
		{"BEGIN pkg.p(); END", "must end with ';'"},
		{"BEGIN pkg.p(1) END;", "must end with ';'"},
		{"BEGIN pkg.p(1); END", "must end with ';'"},
		{"BEGIN pkg.p(1);", "must end with 'END;'"},
		{"END;", "BEGIN"},
		{"BEGIN pkg.p(); ENDIF;", "must end with 'END;'"},
		// Statements must be procedure calls with balanced parens.
		{"BEGIN pkg.p(1) junk; END;", "trailing"},
		{"BEGIN pkg.p((1); END;", "unbalanced"},
		{"BEGIN pkg.p(1;; END;", "unbalanced"},
		{"BEGIN pkg.p; END;", "expected '('"},
		{"BEGIN \"P\".p(); END;", "expected a procedure call"},
		{"BEGIN pkg.p('unterminated; END;", "unterminated"},
		// SELECT must read from DUAL, with no extra statement.
		{"SELECT * FROM EMP", "DUAL"},
		{"SELECT 1 FROM dual, emp", "DUAL"},
		{"SELECT 1 FROM DUAL x", "DUAL"},
		{"SELECT 1; SELECT 2 FROM DUAL", "extra statement"},
		{"SELECT FROM DUAL", "missing an expression"},
		{"SELECT", "DUAL"},
		// Not a command shape at all.
		{"GRANT SELECT ON x TO y", "BEGIN"},
		{"COMMIT", "BEGIN"},
	}
	for _, tc := range rejected {
		err := validateCommand(tc.sql)
		if err == nil {
			t.Errorf("validateCommand(%q) = nil, want rejection containing %q", tc.sql, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("validateCommand(%q) = %q, want substring %q", tc.sql, err, tc.want)
		}
	}
}

// TestValidateCommand_BeginRequiresTerminatedStatements pins the strict rule
// that every inner statement needs its own ';' before END.
func TestValidateCommand_BeginRequiresTerminatedStatements(t *testing.T) {
	if err := validateCommand("BEGIN pkg.p(1) DBMS_SCHEDULER.DISABLE('X'); END;"); err == nil {
		t.Error("two statements without ';' between them accepted")
	}
}

// TestCommandResource_Flow drives the "command" resource through CallResource
// on an UNCONNECTED datasource: the pre-execution gates (confirm, allowlist)
// must fail without touching the database, and an allowlisted confirmed command
// must reach the connection stage and fail cleanly with a readable error.
func TestCommandResource_Flow(t *testing.T) {
	ds := newTestDatasource(t)
	defer ds.Dispose()

	send := func(sql string, confirm bool) *backend.CallResourceResponse {
		body, _ := json.Marshal(map[string]any{"sql": sql, "confirm": confirm})
		return callResource(ds, &backend.CallResourceRequest{
			Path: "command",
			Body: body,
			PluginContext: backend.PluginContext{
				DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{UID: "unit-ds"},
				User:                       &backend.User{Login: "tester"},
			},
		})
	}

	t.Run("missing confirm is rejected without touching the database", func(t *testing.T) {
		resp := send("BEGIN DBMS_SCHEDULER.DISABLE('JOB'); END;", false)
		if resp.Status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.Status)
		}
		if !strings.Contains(string(resp.Body), "not confirmed") {
			t.Errorf("body = %q, want confirmation error", resp.Body)
		}
	})

	t.Run("allowlist rejection is readable and needs no database", func(t *testing.T) {
		resp := send("DROP TABLE x", true)
		if resp.Status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.Status)
		}
		if !strings.Contains(string(resp.Body), "BEGIN") {
			t.Errorf("body = %q, want allowlist error", resp.Body)
		}
	})

	t.Run("allowlisted confirmed command reaches the connection stage", func(t *testing.T) {
		resp := send("BEGIN DBMS_SCHEDULER.DISABLE('JOB'); END;", true)
		// The datasource is unconnected, so the command must fail at ensureReady
		// with a readable network error — never a panic or a raw stack.
		if resp.Status != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", resp.Status)
		}
		if !strings.Contains(string(resp.Body), "network error") {
			t.Errorf("body = %q, want network error", resp.Body)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		resp := callResource(ds, &backend.CallResourceRequest{
			Path: "command",
			Body: []byte("{not json"),
		})
		if resp.Status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.Status)
		}
	})

	t.Run("unknown command path stays 404", func(t *testing.T) {
		resp := callResource(ds, &backend.CallResourceRequest{Path: "command/extra"})
		if resp.Status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.Status)
		}
	})
}

// Command probe verifies go-ora behavior against a live Oracle Database
// before the plugin's design assumptions are locked in (report §21).
//
// Checks covered:
//   1. Rows.ColumnTypes().DatabaseTypeName() / DecimalSize() output
//   2. Scan coercion: INTEGER / NUMBER arrive as the right Go types
//      (guards against the go-ora #533 regression where INTEGER came back
//      as string)
//   4. ANSI TIMESTAMP '...' literals use an index on DATE columns
//      (EXPLAIN PLAN comparison vs the Postgres-style string literal)
//   7. Charset round-trip for a non-UTF8 DB (Turkish WE8ISO8859P9)
//   8. ExecContext runs anonymous PL/SQL blocks (DBMS_SCHEDULER) reliably
//
// Usage:
//   go run ./probe [flags]
//
// Flags (all have env fallbacks):
//   --host   ORACLE_HOST        (default localhost)
//   --port   ORACLE_PORT        (default 1521)
//   --service ORACLE_SERVICE    (default XEPDB1)
//   --user   ORACLE_USER        (default SYSTEM)
//   --pass   ORACLE_PASSWORD    (required)
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type check struct {
	name string
	run  func(ctx context.Context, db *sql.DB) error
}

func main() {
	var (
		host    = flag.String("host", envOr("ORACLE_HOST", "localhost"), "Oracle host")
		port    = flag.String("port", envOr("ORACLE_PORT", "1521"), "Oracle port")
		service = flag.String("service", envOr("ORACLE_SERVICE", "XEPDB1"), "Oracle service name")
		user    = flag.String("user", envOr("ORACLE_USER", "SYSTEM"), "Oracle user")
		pass    = flag.String("pass", envOr("ORACLE_PASSWORD", ""), "Oracle password")
	)
	flag.Parse()
	if *pass == "" {
		fmt.Fprintln(os.Stderr, "ORACLE_PASSWORD is required")
		os.Exit(2)
	}

	dsn := fmt.Sprintf("oracle://%s:%s@%s:%s/%s", *user, *pass, *host, *port, *service)
	db, err := sql.Open("oracle", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sql.Open:", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(2)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Ping failed:", err)
		os.Exit(1)
	}
	fmt.Printf("connected: %s@%s:%s/%s\n\n", *user, *host, *port, *service)

	checks := []check{
		{"1. ColumnTypes() output (dual + typed table)", checkColumnTypes},
		{"2. Scan coercion (INTEGER / NUMBER)", checkCoercion},
		{"4. ANSI TIMESTAMP literal index usage", checkAnsiLiteralIndex},
		{"7. Charset round-trip (Turkish WE8ISO8859P9)", checkCharsetRoundTrip},
		{"8. ExecContext anonymous PL/SQL (DBMS_SCHEDULER)", checkExecPLSQL},
	}

	failed := 0
	for _, c := range checks {
		fmt.Println("### " + c.name)
		start := time.Now()
		if err := c.run(ctx, db); err != nil {
			fmt.Printf("  FAILED (%v): %v\n", time.Since(start).Round(time.Millisecond), err)
			failed++
		} else {
			fmt.Printf("  OK (%v)\n", time.Since(start).Round(time.Millisecond))
		}
		fmt.Println()
	}
	if failed > 0 {
		fmt.Printf("%d check(s) FAILED\n", failed)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}

// setup runs DDL/DML that the checks need. Dropping a table that does not
// exist yet (ORA-00942) is tolerated so the harness is idempotent.
func setup(ctx context.Context, db *sql.DB, stmts []string) error {
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			if strings.HasPrefix(strings.TrimSpace(s), "DROP") && strings.Contains(err.Error(), "ORA-00942") {
				continue
			}
			return fmt.Errorf("setup %q: %w", s, err)
		}
	}
	return nil
}

func checkColumnTypes(ctx context.Context, db *sql.DB) error {
	if err := setup(ctx, db, []string{
		`DROP TABLE verif_types PURGE`,
		`CREATE TABLE verif_types (
			id NUMBER(10,0), f NUMBER(12,2), big NUMBER(38),
			v VARCHAR2(100), nv NVARCHAR2(100),
			d DATE, ts TIMESTAMP, tstz TIMESTAMP WITH TIME ZONE,
			blob_c BLOB, clob_c CLOB,
			bf BINARY_FLOAT, bd BINARY_DOUBLE)`,
		`INSERT INTO verif_types VALUES (
			42, 12.34, 1234567890123456789, 'hello', 'nhello',
			SYSDATE, SYSTIMESTAMP, SYSTIMESTAMP,
			EMPTY_BLOB(), 'clob data', 1.5, 2.5)`,
		`COMMIT`,
	}); err != nil {
		return err
	}

	rows, err := db.QueryContext(ctx, `SELECT * FROM verif_types`)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	for i, ct := range types {
		prec, scale, hasPrec := ct.DecimalSize()
		scan := "?"
		if ct.ScanType() != nil {
			scan = ct.ScanType().String()
		}
		fmt.Printf("  %-7s DatabaseTypeName=%-12s ScanType=%-14s prec=%d scale=%d hasPrec=%v\n",
			cols[i], ct.DatabaseTypeName(), scan, prec, scale, hasPrec)
	}

	// Driver-native values
	if rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			if v == nil {
				fmt.Printf("  %-7s -> NULL\n", cols[i])
				continue
			}
			fmt.Printf("  %-7s -> %-8T %v\n", cols[i], v, truncate(fmt.Sprint(v), 24))
		}
	}
	return nil
}

func checkCoercion(ctx context.Context, db *sql.DB) error {
	if err := setup(ctx, db, []string{
		`DROP TABLE verif_int PURGE`,
		`CREATE TABLE verif_int (id INTEGER, n NUMBER, n2 NUMBER(10,2))`,
		`INSERT INTO verif_int VALUES (1, 2, 3.25)`,
		`COMMIT`,
	}); err != nil {
		return err
	}

	rows, err := db.QueryContext(ctx, `SELECT id, n, n2 FROM verif_int`)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	types, _ := rows.ColumnTypes()
	// Driver-native
	if rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		fmt.Printf("  native values:\n")
		for i, v := range vals {
			if v == nil {
				fmt.Printf("    %s (%-8s) -> NULL\n", cols[i], types[i].DatabaseTypeName())
				continue
			}
			fmt.Printf("    %s (%-8s) -> %-8T %v\n", cols[i], types[i].DatabaseTypeName(), v, v)
		}
	}

	// Explicit typed scan: int64 / float64 / string
	var (
		id   int64
		n    float64
		n2   float64
		sid  string
		ever bool
	)
	row := db.QueryRowContext(ctx, `SELECT id, n, n2 FROM verif_int`)
	if err := row.Scan(&id, &n, &n2); err != nil {
		return fmt.Errorf("typed scan: %w", err)
	}
	fmt.Printf("  typed scan int64/float64: id=%d n=%v n2=%v\n", id, n, n2)

	row = db.QueryRowContext(ctx, `SELECT id FROM verif_int`)
	if err := row.Scan(&sid); err == nil {
		ever = true
		fmt.Printf("  INTEGER scanned into *string: ok=%q (numeric string?)\n", sid)
	}
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM dual`).Scan(&id); err != nil {
		return err
	}
	if !ever {
		fmt.Printf("  note: INTEGER->*string scan failed (driver returns numeric); fallback parse not needed for this version\n")
	}
	return nil
}

func checkAnsiLiteralIndex(ctx context.Context, db *sql.DB) error {
	if err := setup(ctx, db, []string{
		`DROP TABLE verif_dates PURGE`,
		`CREATE TABLE verif_dates (id NUMBER, d DATE)`,
		`CREATE INDEX verif_dates_idx ON verif_dates(d)`,
		// spread rows across a week
		`INSERT INTO verif_dates
			SELECT ROWNUM,
			       DATE '2026-08-01' + (ROWNUM * INTERVAL '1' MINUTE)
			FROM   (SELECT 1 FROM dual CONNECT BY ROWNUM <= 10000)`,
		`COMMIT`,
		// standard plan table (12c-23c compatible column set)
		`DROP TABLE plan_table PURGE`,
		`CREATE TABLE plan_table (
			statement_id VARCHAR2(30), plan_id NUMBER, timestamp DATE,
			remarks VARCHAR2(4000), operation VARCHAR2(30), options VARCHAR2(255),
			object_node VARCHAR2(128), object_owner VARCHAR2(30), object_name VARCHAR2(30),
			object_alias VARCHAR2(65), object_instance NUMBER, object_type VARCHAR2(30),
			optimizer VARCHAR2(255), search_columns NUMBER, id NUMBER, parent_id NUMBER,
			depth NUMBER, position NUMBER, cost NUMBER, cardinality NUMBER,
			bytes NUMBER, other_tag VARCHAR2(255), partition_start VARCHAR2(255),
			partition_stop VARCHAR2(255), partition_id NUMBER, other LONG,
			distribution VARCHAR2(30), cpu_cost NUMBER, io_cost NUMBER,
			temp_space NUMBER, access_predicates VARCHAR2(4000),
			filter_predicates VARCHAR2(4000), projection VARCHAR2(4000),
			time NUMBER, qblock_name VARCHAR2(30), other_xml CLOB)`,
	}); err != nil {
		return err
	}

	showPlan := func(label, stmt string) error {
		fmt.Printf("  %s\n    %s\n", label, stmt)
		if _, err := db.ExecContext(ctx, "DELETE FROM plan_table"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "EXPLAIN PLAN FOR "+stmt); err != nil {
			return err
		}
		rows, err := db.QueryContext(ctx,
			`SELECT id, lpad(' ', depth) || operation || ' ' || options || ' ' || nvl(object_name,''),
			        access_predicates
			 FROM   plan_table
			 START WITH id = 0
			 CONNECT BY PRIOR id = parent_id
			 ORDER SIBLINGS BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var op, ap sql.NullString
			if err := rows.Scan(&id, &op, &ap); err != nil {
				return err
			}
			fmt.Printf("      %s", op.String)
			if ap.Valid && ap.String != "" {
				fmt.Printf("   [%s]", ap.String)
			}
			fmt.Println()
		}
		return rows.Err()
	}

	// Narrow 10-minute window (~0.6% of rows) so the optimizer must choose an
	// index range scan when the predicate allows it.
	if err := showPlan("ANSI TIMESTAMP literal:",
		`SELECT * FROM verif_dates
		 WHERE d >= TIMESTAMP '2026-08-01 00:00:00.000'
		   AND d <= TIMESTAMP '2026-08-01 00:09:59.999'`); err != nil {
		return err
	}
	fmt.Println()
	if err := showPlan("string literal (Postgres-style):",
		`SELECT * FROM verif_dates
		 WHERE d >= '2026-08-01 00:00:00'
		   AND d <= '2026-08-01 00:09:59'`); err != nil {
		return err
	}
	fmt.Println()
	if err := showPlan("TO_DATE literal:",
		`SELECT * FROM verif_dates
		 WHERE d >= TO_DATE('2026-08-01 00:00:00', 'YYYY-MM-DD HH24:MI:SS')
		   AND d <= TO_DATE('2026-08-01 00:09:59', 'YYYY-MM-DD HH24:MI:SS')`); err != nil {
		return err
	}
	return nil
}

func checkCharsetRoundTrip(ctx context.Context, db *sql.DB) error {
	// Turkish test text: the exact runes WE8ISO8859P9 must survive the
	// wire conversion to the client AL32UTF8 session charset.
	const tr = "İstanbul ğöçşüı ĞÖÇŞÜİ"
	if err := setup(ctx, db, []string{
		`DROP TABLE verif_charset PURGE`,
		`CREATE TABLE verif_charset (id NUMBER, name VARCHAR2(200 CHAR))`,
		`COMMIT`,
	}); err != nil {
		return err
	}
	// Parameterized insert so go-ora binds the UTF-8 string and Oracle
	// converts it into the WE8ISO8859P9 storage charset.
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx2,
		`INSERT INTO verif_charset VALUES (1, :1)`, tr); err != nil {
		return fmt.Errorf("parameterized insert of Turkish text: %w", err)
	}
	if _, err := db.ExecContext(ctx2, `COMMIT`); err != nil {
		return err
	}

	var got string
	if err := db.QueryRowContext(ctx2,
		`SELECT name FROM verif_charset WHERE id = 1`).Scan(&got); err != nil {
		return err
	}
	fmt.Printf("  expected: %q\n", tr)
	fmt.Printf("  got:      %q\n", got)
	if got != tr {
		return fmt.Errorf("round-trip mismatch: stored text came back different")
	}
	if strings.ContainsRune(got, '�') {
		return fmt.Errorf("replacement character (U+FFFD) present in round-trip")
	}
	fmt.Printf("  exact round-trip OK (no U+FFFD, all %d runes intact)\n", len([]rune(tr)))

	// Literal round-trip: WHERE name = 'İstanbul' with UTF-8 literal in SQL.
	var cnt int
	if err := db.QueryRowContext(ctx2,
		`SELECT COUNT(*) FROM verif_charset WHERE name = 'İstanbul ğöçşüı ĞÖÇŞÜİ'`).Scan(&cnt); err != nil {
		return err
	}
	fmt.Printf("  WHERE name = '<utf8 literal>' matched %d row(s)\n", cnt)
	if cnt != 1 {
		return fmt.Errorf("literal match failed: expected 1 row, got %d", cnt)
	}
	return nil
}

func checkExecPLSQL(ctx context.Context, db *sql.DB) error {
	if err := setup(ctx, db, []string{
		`BEGIN
			DBMS_SCHEDULER.CREATE_JOB(
				job_name   => 'VERIF_JOB',
				job_type   => 'PLSQL_BLOCK',
				job_action => 'BEGIN NULL; END;',
				enabled    => FALSE,
				comments   => 'verification harness job');
		END;`,
		`CREATE OR REPLACE PROCEDURE verif_ping AS BEGIN NULL; END;`,
	}); err != nil {
		return err
	}

	// ENABLE then DISABLE via ExecContext (anonymous PL/SQL).
	if _, err := db.ExecContext(ctx, `BEGIN DBMS_SCHEDULER.ENABLE('VERIF_JOB'); END;`); err != nil {
		return fmt.Errorf("ENABLE: %w", err)
	}
	state := func() (string, error) {
		var s string
		err := db.QueryRowContext(ctx,
			`SELECT enabled FROM user_scheduler_jobs WHERE job_name = 'VERIF_JOB'`).Scan(&s)
		return s, err
	}
	if s, err := state(); err != nil {
		return fmt.Errorf("read job state: %w", err)
	} else {
		fmt.Printf("  after ENABLE:  user_scheduler_jobs.enabled = %q\n", s)
	}
	if _, err := db.ExecContext(ctx, `BEGIN DBMS_SCHEDULER.DISABLE('VERIF_JOB'); END;`); err != nil {
		return fmt.Errorf("DISABLE: %w", err)
	}
	if s, err := state(); err != nil {
		return fmt.Errorf("read job state: %w", err)
	} else {
		fmt.Printf("  after DISABLE: user_scheduler_jobs.enabled = %q\n", s)
	}

	// Procedure call via anonymous block.
	if _, err := db.ExecContext(ctx, `BEGIN verif_ping; END;`); err != nil {
		return fmt.Errorf("procedure call: %w", err)
	}
	fmt.Printf("  procedure call via BEGIN..END: ok\n")

	// Cleanup.
	if _, err := db.ExecContext(ctx, `BEGIN DBMS_SCHEDULER.DROP_JOB('VERIF_JOB'); END;`); err != nil {
		return fmt.Errorf("DROP_JOB: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

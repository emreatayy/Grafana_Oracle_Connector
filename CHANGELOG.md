# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Design report (v2)** — complete technical design and implementation plan
  for a Grafana ↔ Oracle data source plugin with optional Prometheus proxy:
  version support matrix, architecture with decision records (ADRs), column
  type mapping, backend macro/variable engine, Prometheus proxy mode, Unified
  Alerting foundation, annotations, and operational **command execution**
  (scheduler job control + procedure calls via a "Run command" button).
  See `docs/project-report.md`.
- **Empirical verification harness** (`verification/`) — Go probe + Docker
  Compose services that validate the report's riskiest assumptions against a
  live Oracle 21c XE (and a Turkish `WE8ISO8859P9` database) before any plugin
  code is written.
- **Release & packaging plan** (`docs/release-and-packaging.md`) — the
  distribution model for a Grafana datasource plugin: signed plugin archive,
  GitHub Release, Grafana catalog, versioning, and the planned CI/release
  workflows (created in F13).
- **Community health files** — `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, and GitHub issue/PR templates.
- **Grafana plugin check** (`verification/grafana_plugin_check.py`) — a
  repeatable API-level script (report §17.2 Workflow 3 subset) that verifies a
  built plugin against a running matrix Grafana: plugin load, health "Test",
  query path, and alert-rule acceptance (F5 e2e seed).
- **F0 — plugin scaffold** — datasource plugin generated with
  `@grafana/create-plugin` at the repo root (plugin ID
  `emreatayy-oracle-datasource`):
  - Go backend (`pkg/`, grafana-plugin-sdk-go **v0.296.1**) + TypeScript/React
    frontend (`src/`, @grafana/ui **13.1.0**); go-ora **v2.9.0** pinned.
  - `plugin.json`: `alerting: true`, `annotations: true` (ADR-12),
    `grafanaDependency >=12.3.0`.
  - §17.1 dev matrix `docker-compose.yaml` (profiles `grafana12`/`grafana13`/
    `oracle18`/`oracle21`/`oracle23`/`oracle_tr`/`prom`) + `devenv/prometheus.yml`.
  - F0 unit CI workflow (`.github/workflows/ci.yml`): Go vet/test + golangci-lint
    + Node typecheck/lint/Jest + full `mage` build, on push to `main` and PRs.
  - **Check #3 closed** — the built plugin loads unsigned on Grafana **12.4.0**
    and **13.1.0**, the health "Test" returns SUCCESS, and the alert-rule API
    accepts a query against it (§21 row 3).
- **F1 — Oracle connection core**:
  - `pkg/oracle` — DSN builder (ConnString > host+port+service > host+port+SID
    via the `?SID=` option, `CONNECTION TIMEOUT` backstop), ORA-#### error
    parsing with a known-code map and password redaction, ordered session-param
    application.
  - `pkg/plugin` — per-instance pooled `sql.DB` via `go_ora.NewConnector`
    (avoids the package-singleton `sessionParam` leak across datasources);
    session `TIME_ZONE` pinned to UTC with `AddSessionParam` (go-ora otherwise
    hardcodes the host offset); lazy connect so a down DB does not break
    instance construction; `Dispose()` closes the pool (config change → new
    instance); 5s health timeout; query timeout + `ctx` cancellation with
    **server-side cancel verified** on Oracle 21c (heavy `CONNECT BY` query
    returns ORA-01013 ~360ms after cancel); read-only `SELECT`/`WITH` guard that
    skips comments; generic string/time frame builder (typed mapper is F2).
  - F1 config editor (connection fields + limits) and SQL query editor in
    `src/`; provisioned Oracle datasource in `provisioning/`.
  - **DoD met** — config changes dispose the instance; panel-close cancellation
    is integration-tested. Health "Test" now reports the real Oracle version
    banner on the 12.4/13.1 matrix (6/6 checks).
- **F2 — typed type mapping + frame generation** (report §6, §7, ADR-8):
  - `pkg/convert` (pure, DB-free) — `ColumnInfo` + `Classify` decides each
    column's kind from `ScanType()` + `DecimalSize()` (never go-ora's internal
    `DatabaseTypeName()`); typed scan targets (`sql.Null*`, `[]byte`); `ValueOf`
    normalizes time to UTC. `BuildFrame` shapes per §7: `auto` (time + numeric
    value → time series, else table), `table` (all columns typed, NULL kept,
    duplicate columns suffixed, empty result → typed empty header), `time_series`
    (wide with single-distinct string → labels, or long with string dims +
    time + value; NULL-time rows skipped, duplicate time kept, maxSeries
    cardinality guard). BLOB/RAW → base64 `*string` field (SDK has no bytes
    vector).
  - `largeNumberMode` (ADR-8) — NUMBER(prec>15, scale=0) preserved at full
    precision as `*string` in tables, `*float64` in time series; configurable
    (config > per-query > per-format default).
  - `pkg/plugin/query.go` now scans typed (`convert.ScanDest`/`ValueOf`) and
    builds frames through `convert.BuildFrame`; the provisional generic
    string/time builder is removed. Query model gains `format`,
    `timeColumn`, `largeNumberMode`.
  - Frontend: Format `<Select>` (auto/table/time_series) in the query editor;
    "Large numbers" `<Select>` (Auto/string/float) in the config editor.
  - **DoD met** — typed table + time-series panels verified on Oracle 21c:
    NUMBER(10)→`*int64`, NUMBER(10,2)→`*float64`, NULL preserved, NUMBER(20)
    full precision, DATE→`*time.Time`, CLOB→`*string`, duplicate time kept,
    NULL-time rows skipped.
- **Integration test suite** (`pkg/plugin/integration_test.go`, env-gated by
  `ORACLE_TEST_DSN`) — health + version banner, bad-password ORA-01017 (no
  credential leak), typed row select, UTC session timezone, query cancellation,
  read-only guard rejection, and the F2 typed-column matrix (table typed +
  large-number-in-time-series).

### Verified (empirically, 2026-08-07)

- go-ora v2.9.0 `ColumnTypes()`: `DatabaseTypeName()` returns internal driver
  names (not Oracle types) — do not use for type decisions; `DecimalSize()` is
  accurate; `ScanType()` is reliable at the broad level.
- go-ora regression #533 persists in v2.9.0: NUMBER values arrive as `string`
  when scanned into `interface{}`, but typed scans (`*int64`, `*float64`)
  convert correctly.
- ANSI `TIMESTAMP '...'` literals cause a **full table scan** on DATE columns;
  `TO_DATE('...', 'YYYY-MM-DD HH24:MI:SS')` preserves index range scans and is
  NLS-independent → ADR-9 revised.
- Grafana alert evaluator sends `maxDataPoints=43200` and `IntervalMs=1000ms`
  by default (not ~100) → interval derivation updated (§10.4, §8.4).
- `data/sqlutil.Interpolate` is macro-only; there is no variable interpolation
  — multi-value `IN(...)` and `${var:csv}` are implemented by the plugin.
- `WE8ISO8859P9` (Turkish) text round-trips exactly through go-ora v2.9.0
  (parameterized insert + literal match, no U+FFFD); `SetStringConverter` is
  not required for this charset.
- `ExecContext` reliably runs anonymous `BEGIN…END` PL/SQL: `DBMS_SCHEDULER`
  ENABLE/DISABLE, job-state read-back, and procedure calls all work.

### Verified (empirically, 2026-08-08)

- SID connections work via the go-ora `?SID=<sid>` DSN option — the connect
  descriptor uses `(SID=...)` when the option is set (connect_config.go).
- Session `TIME_ZONE` is hardcoded to the host offset in the go-ora auth
  handshake; `AddSessionParam(db, "TIME_ZONE", "'UTC'")` overrides it on every
  pooled connection — `SESSIONTIMEZONE` confirmed `+00:00` on a live session.
- go-ora mid-query `ctx` cancellation works: a heavy `CONNECT BY` query returns
  ORA-01013 ("user requested cancel of current operation") ~360 ms after cancel
  — the F1 panel-close DoD is met.
- Grafana's `/api/ds/query` takes query fields flattened (not nested under
  `model`); the `model` wrapper is only used by the alerting/provisioning API.
- go-ora v2.9.0 typed scans: NUMBER rows arrive as strings but parse cleanly
  into `sql.NullInt64`/`NullFloat64`/`NullString`; CLOB → `NullString`
  (`setLob`), BLOB → `[]byte`; NULL → `Valid=false`/nil. Scanning into `[]any`
  still yields strings (#533) — typed scan targets only.
- grafana-plugin-sdk-go v0.296 has **no** `[][]byte` field vector (`NewField`
  supports int8–uint64, float32/64, string, bool, time, json.RawMessage) →
  BLOB/RAW fields are base64 `*string` (report §6.1 corrected).
- SDK `FrameMeta.ExecutedQueryString` and `FrameTypeTimeSeriesWide`/`Long`
  exist; long format = string dims + time + value (Grafana pivots to wide).
- F2 typed frames verified on Oracle 21c: the `gox_f2` table returns
  NUMBER(10)→`*int64`, NUMBER(10,2)→`*float64`, NULL rows as null cells,
  NUMBER(20) at full precision as `*string` (table) / `*float64` (time series),
  DATE→`*time.Time` (UTC), CLOB→`*string`; duplicate-time rows kept, NULL-time
  rows dropped from the time-series frame.

### Changed

- Project content moved to **English-only** (files, docs, UI, log messages).
- Turkish draft report (`docs/proje-raporu.md`) replaced by the English report.
- `.gitignore` comments translated to English.

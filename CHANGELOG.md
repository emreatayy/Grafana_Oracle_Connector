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
- **F3 — backend macro & variable engine** (`pkg/interpolate`, pure, DB-free):
  - `Interpolate(raw, Params)` — macro-first then single-pass variable
    expansion over the `sqlutil` engine, with fail-closed guards: a `${var}`
    inside a `$__macro(...)` argument, an unknown/lowercase `$__` macro, a
    malformed unterminated `${`, an unresolved variable, and an unsupported
    format all error explicitly (never silently mis-evaluating SQL, §15).
  - Oracle macro table — `$__from`/`$__to` (epoch ms), `$__timeFilter`/
    `$__timeFrom`/`$__timeTo` (ADR-9 `TO_DATE('…','YYYY-MM-DD HH24:MI:SS')`
    literals: NLS-independent + index-friendly), `$__unixEpochFilter`/
    `$__unixEpochGroup(col, interval)` (`FLOOR` bucketing),
    `$__interval`/`_ms`/`_s` (via the §10.4 derivation), `$__time(col)`; the
    T-SQL `$__timeGroup` and empty `$__table`/`$__column` sqlutil defaults are
    shadowed with explicit errors.
  - `deriveInterval` (§10.4): `interval_ms = max(IntervalMs, range_ms /
    max(1, maxDataPoints))` — alert defaults verified (1s for ≤ 12h, 2s for
    24h); the single shared source for SQL `$__interval` and later the
    Prometheus proxy `step` (F6/F7).
  - Variables — `${var}`/`:csv`/`:singlequote`; empty multi-value → `NULL` (so
    `IN (NULL)` = "no selection, no data"); `'`→`''` escaping; bare emission
    gated by a strict numeric regex (`numericVariables` is a hint, not a trust
    grant — `1e5` and `5) OR 1=1 --` are never bare); `$__all` sentinel →
    error; unresolved → explicit error (§8.3).
  - `query.go` — interpolates **before** `ensureReady` (a macro/variable error
    never pings the DB), re-runs the read-only guard on the interpolated SQL
    (§15), and never writes interpolated SQL back into the model (raw
    `ExecutedQueryString`, §7.5).
  - Frontend — `applyTemplateVariables` attaches stringified `scopedVars`
    (alert rules skip it — §8.3); "Numeric variables" field in the config
    editor (`src/components/ConfigEditor.tsx`).
  - **DoD met** — `$__timeFilter`, `$__unixEpochGroup`, `${var:csv}`, empty
    selection→`IN (NULL)` table-driven tested; alerting-context simulation
    works; `TestIntegration_Macros` on Oracle 21c.
- **F4 — SQL query editor** (report §14.1):
  - `src/types.ts` — `QueryMode = 'sql' | 'prometheus'`; `MyQuery.mode?` and
    inert `promql?` (until F6); `MyDataSourceOptions.defaultMode?: QueryMode`.
  - Mode-tab infrastructure in `src/components/QueryEditor.tsx` — `TabsBar`
    with `Oracle (SQL)` | `Prometheus (PromQL)`; the mode is stored **per
    query** and defaults to the datasource `defaultMode` (config "Default mode"
    `<Select>` in `src/components/ConfigEditor.tsx`). The Prometheus tab is a
    placeholder until F6/F7.
  - The bare textarea is replaced by the **`@grafana/plugin-ui`** `SQLEditor`
    (the package that succeeded `@grafana/experimental` for Grafana 13 — its
    peer range caps at SDK ^11 and would break `npm ci` against the 13.1.0
    pin). Oracle monarch language in `src/language/oracle.ts`: spreads
    `grafanaStandardSQLLanguage` + Oracle/PL/SQL keywords, built-in functions,
    and the `||` operator; `tokenPostfix: '.oracle'`.
  - Format `<Select>` (auto/table/time_series, from F2) and a static Time
    column `<Select allowCustomValue>` (Auto + custom text; metadata list
    wired in F9) sit above the editor.
  - Backend fail-closed mode guard in `pkg/plugin/query.go` — a saved
    `mode:"prometheus"` query returns a clean 400 ("not implemented, planned in
    phase F6") before any interpolation or connection; unit-tested
    (`TestQuery_PrometheusModeRejected`).
  - **DoD met** — SQL panel renders in both formats; mode stored per query;
    verified by the query-editor e2e specs and the 12.4/13.1 plugin check 6/6.
- **Integration test suite** (`pkg/plugin/integration_test.go`, env-gated by
  `ORACLE_TEST_DSN`) — health + version banner, bad-password ORA-01017 (no
  credential leak), typed row select, UTC session timezone, query cancellation,
  read-only guard rejection, the F2 typed-column matrix (table typed +
  large-number-in-time-series), and the F3 macro matrix (timeFilter + host csv,
  empty selection → `IN (NULL)`, `unixEpochGroup`, unresolved-variable error,
  raw `ExecutedQueryString`).
- **Query-variable support** (`src/variableSupport.ts` + `src/datasource.ts`,
  report §8.5) — the plugin extends `DataSourceVariableSupport` and registers
  it as `this.variables`, so Grafana's query-variable editor renders the
  plugin's own F4 SQL editor and runs the preview through the standard backend
  query path. Previously `getVariableQueryEditor` returned `null` (no custom
  variable support declared), so the variable editor had **no Query field** and
  "Preview of values" stayed at 0. Verified live on Grafana 13.1 (:3002):
  selecting `oracle-datasource`, entering `SELECT DISTINCT txt FROM gox_demo
  ORDER BY txt`, and clicking Preview returns all 12 values (node-0 … node-11).

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
- F3 macros verified on Oracle 21c (`TestIntegration_Macros`): `$__timeFilter`
  + `${host:csv}` → `[42]` / `[8,42]` / empty selection → 0 rows
  (`IN (NULL)`); `$__unixEpochGroup(id, 10)` → `[0,0,0,40]` (computed
  expressions classify Prec==0 → float, §6.2); an unresolved `${missing}`
  errors without touching the DB; `ExecutedQueryString` stays raw.
- `DataSourceWithBackend.applyTemplateVariables` is the variable-transport
  hook: it fires for dashboard queries (attaches `scopedVars` to the backend
  JSON) and is **never** called for alert rules — ngAlert bypasses the
  frontend, so the §8.3 backend fallback works for free.
- `@grafana/experimental@2.1.6` peer range (`@grafana/ui ^10.4.0 || ^11.0.0`)
  is incompatible with the 13.1.0 SDK pin and breaks `npm ci` (ERESOLVE) — its
  successor **`@grafana/plugin-ui@0.17.3`** (peer `@grafana/ui ^13.0.0`, the
  current `grafana/athena-datasource` dependency) re-exports the identical
  `SQLEditor` + `LanguageDefinition` API, so F4 uses plugin-ui. It is not in
  `.config/bundler/externals.ts` and is bundled; `@grafana/ui` stays external
  so monaco comes from the host Grafana (12.4 and 13.1 both ship it).
- plugin-ui `SQLEditor` gives the monaco CodeEditor textarea **no accessible
  label** — the e2e locator is a `data-testid` wrapper (`query-editor-sql`),
  not `getByRole('textbox', { name: 'SQL' })`.
- F4 verified on Grafana 12.4 + 13.1 (plugin check 6/6) and the query-editor
  e2e specs: the SQL editor renders, the mode tabs switch and are stored per
  query, and the backend rejects a saved `mode:"prometheus"` query with a
  clean 400 before touching Oracle.
- The datasource settings live under a **different property per host Grafana**:
  `instanceSettings` on `DataSourceApi` (≤ 12.x) vs `datasourceInstanceSettings`
  on `DataSourceWithBackend` (13.x). The plugin floor is 12.3, so the query
  editor reads both defensively (the F4 e2e run covered only 13.1 and missed
  the 12.4 crash; the query-editor spec now also passes on 12.4).
- **F5 — Unified Alerting (SQL, basic)** (report §10): Layer 1 (time-series →
  `Reduce(last)` → `Is above`) and Layer 2 ("rows > 0" → scalar `SELECT COUNT(*)`
  → `Reduce(count)` → `Is above(0)`) alert rules verified end-to-end. New
  example templates (`examples/alerting/`) and a repeatable check script
  (`verification/alert_check.py`) that provisions the rules, waits for
  evaluations, and asserts `(state, health)` against the prometheus-compat API —
  7/7 scenarios on Grafana 12.4.0 (:3001) and 13.1.0 (:3002).
- **F6 — Prometheus proxy (backend)** (report §9): a pure `pkg/prom` client
  (`ValidateURL`, basic/bearer auth, `query_range` → wide matrix frame,
  `query` → wide single-row vector frame, §10.4 step derivation, legend
  template, response-size bound). `mode: "prometheus"` queries are routed to the
  proxy (disabled proxy → clean 400, never touching Oracle); `CheckHealth`
  probes `status/buildinfo` when enabled. ConfigEditor gains a **Prometheus**
  FieldSet (enable, URL, auth type, secret basic user/password + bearer token,
  TLS skip verify). Verified end-to-end against a real Prometheus 3.5.0 on
  Grafana 12.4 + 13.1 (`verification/prom_check.py`, 5/5): correct frames in
  both modes, raw PromQL on `executedQueryString`, and proxied failures never
  echo the configured token (credentials never logged).
- **F7 — Prometheus query editor + metric browser** (report §14.1 + §18): a real
  **PromQL editor** (monaco SQLEditor with a custom monarch language:
  functions, operators, duration/string/number tokens, template variables and
  `$__` macros highlighted) replaces the F4 placeholder, with **Instant**
  toggle, **Legend format** and **Step** fields. A **metric browser**
  (`MetricBrowser` component) lists metric names via
  `label/__name__/values`, loads per-metric label values via `series?match[]=`,
  and builds a filtered `{label="value"}` PromQL expression into the editor
  ("Use query") — the F7 DoD "metric browser → PromQL → panel + alert".
  Resources are proxied through the plugin `CallResource` handler
  (`prometheus/labels`, `prometheus/label/<name>/values`, `prometheus/series`,
  unknown paths → clean 404, disabled proxy → 400). Mode is stored per query;
  legend format/step flow into the frame (`SetLegendFormat`, §10.4 step).
  Verified end-to-end: `verification/prom_check.py` 11/11 incl. metric-browser
  resources, `verification/alert_check.py` 8 scenarios incl. a Prometheus-mode
  `up` alert rule on a provisioned prom-only datasource
  (`provisioning/datasources/datasources-prom.yml`), and a metric-browser e2e
  spec, all on Grafana 12.4 + 13.1.
- **F8 — Grafana query variables** (report §8.5 + §18): the variable editor
  (wired in F3 via `DataSourceVariableSupport`) now fully supports both
  variable sources. **SQL single-column** queries feed the dropdown through the
  standard query path. **Prometheus `label_values(...)`** is intercepted in the
  backend before the PromQL parser — `label_values(label)` lists every value of
  a label (`/api/v1/label/<name>/values`), `label_values(metric, label)` lists
  a label's distinct values among the series matching a metric
  (`/api/v1/series?match[]=`), both as a one-column string frame (new
  `pkg/prom` `LabelValues`/`Series` methods). Multi-value `IN(...)` (core in
  F3) and empty-value `IN (NULL)` are verified end-to-end via `scopedVars`.
  `applyTemplateVariables` now **also merges the template service's variable
  values** (`getTemplateSrv().getVariables()`) — Grafana 13 scenes puts only
  `__`-internal vars in a panel request's `scopedVars`, so dashboard `${var}`
  would otherwise never reach the backend on 13 (report §8.1). The PromQL
  language highlights `label_values`. Verified end-to-end:
  `verification/variable_check.py` 5/5 on Grafana 12.4 + 13.1 (SQL variable,
  both `label_values` forms through the real proxy, multi-value IN, empty →
  no rows), and e2e `tests/variables.spec.ts` (dropdown variable for SQL +
  Prometheus, and a multi-select dashboard whose `${var:csv}` panel shows only
  the selected values) — the F8 DoD "dropdown variable + multi-select dashboard
  scenarios e2e".
- **F9 — Annotations + autocomplete** (report §11 + §14.3 + §18): a dashboard
  **annotation** whose target is a datasource query now renders on the panel.
  `applyTemplateVariables` marks the query when the annotation runner injects
  `scopedVars.__annotation` (`query.annotation`), the backend forces the **table**
  shape and sets `frame.Meta.DataTopic = Annotations` (the frame meta that makes
  Grafana draw the events; the reader maps `time`/`title`/`text` by name).
  New **metadata resource** (`metadata/tables`, `metadata/columns` under
  `CallResource`) serves the accessible table list (`ALL_TAB_COMMENTS`, bounded to
  200, 5-min TTL cache) and the typed column list (`ALL_TAB_COLUMNS` ⋈
  `ALL_COL_COMMENTS`, schema-qualified names supported), degrading **silently to
  empty arrays** when the account lacks the metadata views or the DB is down. The
  SQL editor is now built per datasource (`buildOracleLanguage(ds)`) and its
  **completion** resolves Tables/Columns from the metadata resource via the
  plugin-ui `customProvider` extension points (spread over the standard provider
  so keyword completion is kept); the **time-column dropdown** lists the
  DATE/TIMESTAMP columns of the parsed FROM table. Verified: `TestIntegration_Annotation`
  + `TestIntegration_Metadata` (6 subtests) on Oracle 21c, e2e
  `tests/annotations.spec.ts` (asserts `schema.meta.dataTopic === 'annotations'`
  on the annotation runner's `/api/ds/query` call — server `GET /api/annotations`
  only returns persisted annotations), full e2e suite 13/13.
- **F10 — Health enrichment + error resolution** (report §13 + §18): the
  datasource "Test" result is now a **structured JSON detail** —
  `{"oracle":{"ok":…,"version":…},"prometheus":{"enabled":…,"ok":…}}` — carrying
  `oracle.ok` / `prometheus.ok` booleans, the version banner, and the readable
  error in the failing subsystem. A proxy that is not configured reports
  `enabled=false, ok=true`, so health stays **green without a Prometheus
  config** and never lets a missing proxy mask an Oracle failure. The ORA error
  map grows from 12 to **27 codes** covering the bad password / URL / connection
  scenarios (01017, 01033, 01034, 12154, 12170, 12505, 12514, 12541, 12545,
  12560, 27101, 28000, 28001, 28009) and common query errors (00900, 00907,
  00911, 00932, 00979, 01403, 01722, 04063, 06550); network failures map to a
  `network error:` prefix. Every message is DSN-password-redacted and never
  carries SQL. Verified: bad password → `ORA-01017: invalid username or
  password`, unreachable host → `network error: …`, both as JSON on the Test
  screen (config-editor e2e asserts the alert text); `TestHealthResult_Shapes`
  (all four §13 states) + `TestIntegration_Health`/`_BadPassword`/`_BadHost` on
  Oracle 21c; full e2e suite 13/13; plugin check 6/6 on 12.4 + 13.1.
- **F15 — Command execution (operations UI)** (report §12 + §18): a **"Run
  command"** button in the query editor posts the current SQL as an anonymous
  PL/SQL command to a new backend **`command` resource** under
  `CallResourceHandler` — strictly outside the query pipeline (never re-run by
  panel refresh, alerting or variables). The backend **always requires
  `confirm:true`** (400 "not confirmed" otherwise), then checks the **allowlist**
  (§12.3, ADR-13): `BEGIN…END;` blocks whose statements are
  `DBMS_SCHEDULER.<proc>(…)` (any procedure, incl. key-value `SET_ATTRIBUTE`) or
  plain procedure calls, plus `SELECT <expr> FROM DUAL` (function value returned
  inline); the scanner is string/paren-aware and rejects comments, DDL/DML,
  control flow, unbalanced or unterminated statements, and the escape-hatch
  packages `DBMS_SQL` / `DBMS_SYS_SQL` / `DBMS_UTILITY`. Execution is bound by a
  new `commandTimeoutSec` (default 10 s) and every command is **audit-logged**
  (datasource UID, user, allowlist-passed statement, password-redacted). The UI
  gates the action with a confirmation dialog (config-toggleable — disabling it
  only skips the dialog, never the backend floor) and renders the result or the
  readable ORA-mapped error inline under the button. **Oracle 21c findings
  (§12.1):** `DBMS_SCHEDULER.START_JOB` was **removed in 21c** (compile-time
  `PLS-00302`; absent from the spec even for SYS) — `RUN_JOB('JOB',
  use_current_session => FALSE)` is the documented replacement and drives the
  start/stop flow; `ALTER ANY JOB` is likewise gone, consolidated into
  `MANAGE SCHEDULER`. Verified: `TestIntegration_Command` on Oracle 21c
  (confirm gate, DISABLE→DISABLED / ENABLE→SCHEDULED / RUN_JOB async→RUNNING /
  STOP_JOB→SCHEDULED, procedure side-effect, function value "42", DDL/DML
  rejected with the table surviving, readable ORA on a bad job name), e2e
  `tests/command.spec.ts` on **12.4 + 13.1** (value rendered inline through the
  dialog; DDL surfaces the inline rejection), full e2e suite 16/16 on 13.1,
  plugin check 6/6 on 12.4 + 13.1.
- **F11 — Security, log hygiene, self-metrics** (report §15 + §18): the plugin
  now exports a small **Prometheus metric set** — `oracle_datasource_query_total`,
  `oracle_datasource_errors_total` and `oracle_datasource_query_duration`
  (seconds) — labelled `{datasource=DS uid, mode=sql|prometheus}` and registered
  on `prometheus.DefaultRegisterer`, so the SDK diagnostics server (which
  gathers `prometheus.DefaultGatherer`) serves them through the `CollectMetrics`
  RPC and Grafana exposes them at **`GET /metrics/plugins/emreatayy-oracle-datasource`**
  (the per-plugin endpoint — plugin series are not merged into the main
  `/metrics`). A `queryRecorder` wired into the single `query()` path counts,
  times and error-tags every SQL/Prometheus/annotation query, with the UID
  plumbed from the request `PluginContext`. The **query path now logs at the right level**:
  `Debug` on execution (datasource/mode/refId only), `Warn` on client rejections
  (read-only guard, interpolation, disabled proxy), `Error` on real failures —
  every message password-redacted and free of SQL, values and credentials
  (§15 log-hygiene rule). §15 audit re-confirmed: `secureJsonData` credentials,
  F3 variable escaping, the ADR-7 guard, the F15 allowlist + confirm + audit,
  and the SSRF-safe Prom URL. Verified: `TestQueryRecorder`,
  `TestQueryData_MetricsLabels` (UID → label plumbing),
  `TestMetrics_RegisteredOnDefaultGatherer`,
  `TestLogHygiene` (rejection/failure logs never contain the SQL or password)
  and `TestIntegration_QueryMetrics` on Oracle 21c; the series are visible on
  `GET /metrics` of the 12.4 and 13.1 matrix Grafanas after a panel query; lint
  0 issues.

### Fixed

- **Time-series alerting was broken** (report §10.1 Layer 1): time-series value
  columns were emitted as `*int64` / `*bool`, and Grafana's alerting SSE +
  dataplane readers require **float64** — every time-series alert rule failed
  with "no float64 value column found in frame oracle". `convert` now coerces
  int/bool value columns to `NullableFloat64` in time-series format
  (`timeSeriesValueField`), while tables keep int64/bool (report §6.1). Verified
  by unit tests and the F5 alert check (L1-firing is green on both versions).
- **Prometheus-mode queries were dropped by `filterQuery`** (report §8.5/F8):
  the frontend filter only checked the `sql` field, so `runRequest` silently
  discarded every `mode: "prometheus"` query — F7 dashboard panels and F8
  `label_values` variables never reached the plugin. The filter is now
  mode-aware (`prometheus` → checks `promql`).
- **Panel-editor query mode regressed under monaco's async onChange** (F8 e2e):
  plugin-ui's `useLatestCallback` can fire a stale `onChange("")` that flipped a
  just-switched editor back to the previous mode and clobbered the tab state.
  `QueryEditor` now guards callbacks with a live `modeRef` (events are applied
  only while their mode is active).
- **data-query e2e flaked only when running LAST in the suite** (F8 e2e):
  Grafana 13's new datasource combobox can leave its option list momentarily
  empty, so plugin-e2e's `datasource.set` (fill + arrows + Enter) silently kept
  the default test-data datasource and no `/api/ds/query` POST was ever made
  (trace-verified). The spec now selects the datasource deterministically and
  waits for the oracle QueryEditor to mount before refreshing.
- Query editor crashed on Grafana **12.4** with
  `TypeError: undefined is not an object (evaluating 'e.datasource.datasourceInstanceSettings.jsonData')`
  — `datasourceInstanceSettings` exists only on the 13.x
  `DataSourceWithBackend`; the editor now falls back to the ≤ 12.x
  `instanceSettings` property. Verified: query-editor e2e 5/5 on **both**
  12.4 (:3001) and 13.1 (:3002), full e2e 8/8 on 12.4, plugin check 6/6 on both.

### Changed

- Project content moved to **English-only** (files, docs, UI, log messages).
- Turkish draft report (`docs/proje-raporu.md`) replaced by the English report.
- `.gitignore` comments translated to English.

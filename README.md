# Grafana Oracle Connector

[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/LICENSE)
[![Grafana](https://img.shields.io/badge/Grafana-%E2%89%A512.3-F46800.svg)](https://grafana.com)
[![Oracle](https://img.shields.io/badge/Oracle-%E2%89%A512c-red.svg)](https://www.oracle.com/database/)
[![Status](https://img.shields.io/badge/status-F13%20done-brightgreen.svg)](#status)
[![CI](https://github.com/emreatayy/Grafana_Oracle_Connector/actions/workflows/ci.yml/badge.svg)](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/.github/workflows/ci.yml)

A **Grafana data source plugin for Oracle Database** with an optional
**Prometheus proxy** mode. It is built on the Grafana plugin SDK with a pure-Go
Oracle driver — **no Oracle Client (OCI / Instant Client) is required**.

Unlike a "run a SELECT and show strings" plugin, this one is designed to be a
first-class Grafana citizen: correct column type mapping (real time series),
Unified Alerting support, annotations, backend interpolation, and operational
**command execution** (start/stop/enable/disable scheduler jobs, call
procedures) — all driven from the query editor.

> **Design-first repository.** This project starts with a complete technical
> design report and empirical verification of its riskiest assumptions against
> a real Oracle Database, *before* the first line of plugin code. See
> [Status](#status) and [Documentation](#documentation).

---

## Table of Contents

- [Status](#status)
- [Features](#features)
- [Installation](#installation)
- [Architecture](#architecture)
- [Version Support](#version-support)
- [Repository Structure](#repository-structure)
- [Getting Started (Development)](#getting-started-development)
- [Metrics](#metrics)
- [Documentation](#documentation)
- [Roadmap](#roadmap)
- [Release & Packaging](#release--packaging)
- [Contributing](#contributing)
- [Security](#security)
- [License](#license)

---

## Status

| Stage | State |
|---|---|
| Design | ✅ Complete — `docs/project-report.md` (v2) |
| Empirical verification | ✅ **19 of 19** design assumptions verified (Oracle 21c XE, go-ora v2.9.0, Grafana 12.4/13.1, Prometheus 3.5.0) — see `verification/` |
| Implementation | ✅ **All 16 phases complete (F0–F15)** (connection core + typed mapping + macro & variable engine + SQL query editor + basic alerting + Prometheus proxy backend + PromQL editor & metric browser + query variables + annotations + editor autocomplete + health JSON + ORA error resolution + command execution / operations UI + security audit, log hygiene & self-metrics + performance, limits, streaming & caching + user installation guide, dashboard example templates + readonly account SQL, `mage archive`/`mage validate` packaging, sign + release workflow, full e2e + **F14 ops/optional: column→unit formatting, Oracle TLS (TCPS) options, TNS connect-descriptor host pass-through, Prometheus `ALERTS` Layer-3 view**) |

The report's riskiest assumptions were validated empirically before any code
was written, which produced two design corrections early:

- **Scan strategy** (§6.2) — go-ora's `DatabaseTypeName()` returns internal
  driver names (not Oracle types), and the #533 regression (NUMBER arriving as
  `string` in `[]any`) persists in v2.9.0. Type decisions therefore use
  `ScanType()` + `DecimalSize()` and typed scans.
- **Date-literal macro** (ADR-9) — ANSI `TIMESTAMP '...'` literals defeat DATE
  indexes (full scan); `TO_DATE('...', 'YYYY-MM-DD HH24:MI:SS')` keeps the
  index and is NLS-independent.

The last open question — the `alerting` flag on Grafana 12.4 / 13.1 — was
closed with the F0-built plugin: the datasource loads, the health "Test"
returns SUCCESS, and the rules API accepts an alert rule that queries it
(report §21 row 3).

---

## Features

- **Real time series** — correct column-type mapping turns Oracle queries into
  native Grafana time series (`time.Time` fields), tables, and stat/gauge
  panels. Large `NUMBER`, `NULL`, and duplicate-timestamp scenarios are handled
  explicitly.
- **Unified Alerting** — time-series queries and classic "rows > 0" patterns
  work with Grafana Unified Alerting; a correct reduce strategy and an
  alert-safe interval derivation are part of the design (§10).
- **Operational command execution** — a **"Run command"** button in the query
  editor executes allowlisted `BEGIN…END` operations:
  - `DBMS_SCHEDULER` job **enable / disable / start / stop**
  - function & procedure calls
  - confirmation dialog, audit logging, and a timeout guard
  - strictly **outside the read-only query path** — dashboard refreshes, alert
    evaluations, and query variables never trigger commands (§12).
- **Backend interpolation** — macros and variables (`$__timeFilter`,
  `$__interval`, `${var:csv}`, …) are expanded on the backend, so alert and
  query-variable queries are safe and consistent.
- **Prometheus proxy (optional)** — a toggleable proxy mode: PromQL
  query/query-range, metric browser, and vector/matrix → frame conversion.
- **Annotations** — time-range annotation queries rendered on panels (§11).
- **Pure-Go driver** — `sijms/go-ora` (MIT), no OCI / Instant Client, no
  Oracle Client libraries on the target system.
- **Multi-version** — one build supports Grafana ≥ 12.3, Oracle ≥ 12c, and
  Prometheus 2.40+ / 3.x (see [Version Support](#version-support)).

---

## Installation

Grafana **12.3+** and Oracle **12c+** are required; **no Oracle Client (OCI /
Instant Client) is needed** — the backend is a pure-Go driver. The full
step-by-step guide — archive install, the unsigned-plugin flag, every datasource
field, the read-only user, the Prometheus proxy, the exporter guide and
troubleshooting — is in
[`docs/installation.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/installation.md).

### Quick install

1. Download `emreatayy-oracle-datasource-<version>.zip` (or `.tar.gz`) from the
   release assets.
2. Unpack it so the plugin folder `emreatayy-oracle-datasource/` (with
   `plugin.json` at its root) lands under Grafana's plugin directory
   (`/var/lib/grafana/plugins` by default).
3. Restart Grafana. A **signed** release archive loads as-is; an **unsigned**
   build additionally needs:
   ```sh
   export GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=emreatayy-oracle-datasource
   ```
4. **Connections → Data sources → Oracle Datasource**: enter host / port, a
   service name **or** SID, and the account credentials. **Save & test** — the
   result is structured JSON (`{"oracle":{...},"prometheus":{...}}`).

### Recommended read-only user

Connect the datasource as a dedicated **read-only** Oracle account, never as
`SYSTEM`/`SYS`. The plugin blocks anything but `SELECT`/`WITH` at the query
path (ADR-7); the database account is the second layer.
[`examples/readonly_user.sql`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/examples/readonly_user.sql)
creates the account with the minimum grants (`CREATE SESSION`, `SELECT ON
SYS.V_$VERSION` for the health banner, `SELECT` on the dashboards' tables) and
verifies that a `DELETE` fails with `ORA-01031`.

### Metrics and the exporter guide

The plugin's own self-metrics are served by Grafana at
`/metrics/plugins/emreatayy-oracle-datasource` (see [Metrics](#metrics)). To
monitor Oracle itself, pair the **Prometheus proxy** mode with the official
Oracle exporter (`oracle/oracle-db-appdev-monitoring`) scraped by Prometheus —
the exporter + Prometheus + Grafana wiring is in
[`docs/installation.md` §5](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/installation.md).

---

## Architecture

```
+-------------------------- Grafana ---------------------------+
|  Frontend (TypeScript/React)      Backend (Go, plugin SDK)    |
|  Query editor (SQL / PromQL)      QueryData (read-only path)  |
|  "Run command" button ───postResource──▶ CallResourceHandler  |
|  Config editor                    Health check / Test button  |
+---------------------------------------------------------------+
                │                          │
                │ SQL                      │ SQL (ExecContext)
                ▼                          ▼
        +-------------------+      +---------------------+
        |   go-ora driver   |      |   allowlisted ops   |
        |  (pure Go, OCI)   |      |  BEGIN…END blocks   |
        +-------------------+      +---------------------+
                │
        +-------------------+            +----------------+
        |   Oracle DB       |            |  Prometheus    |
        +-------------------+            |  (optional,    |
                                         |   proxy mode)  |
                                         +----------------+
```

Key design rules (full detail in the report):

- **The query path is read-only.** Commands never flow through
  `QueryData`, so alerts/dashboards/variables can never re-execute an
  operation (§12.2).
- **All interpolation happens on the backend.** The frontend sends raw SQL;
  macros and variables are expanded server-side (§8).
- **Defensive type mapping.** Column types are decided from `ScanType()` +
  `DecimalSize()` with value-based fallback — never from go-ora's internal
  `DatabaseTypeName()` (§6.2).
- **Alert-aware intervals.** In the alerting context Grafana sends
  `maxDataPoints=43200` and `IntervalMs=1000ms`; the plugin derives a sane
  bucketing interval from that (§10.4, empirically verified).

See [Documentation](#documentation) for the full report.

---

## Version Support

Official support matrix (enforced in CI from F0):

| System | Official support (floor) | Primary (main) lines | CI test matrix |
|---|---|---|---|
| **Grafana** | **≥ 12.3** | 12.4, 13.x | `12.4` + `13.1` |
| **Oracle** | **≥ 12c** | 19c, 21c, 23ai | XE `18c` + `21c` + `23c` |
| **Prometheus** | **≥ 2.40 + 3.x** | 3.x | `3.5` (LTS) |

The Grafana floor is 12.3, not 12.0: the pinned `@grafana/ui`/`@grafana/data`
13.1.0 enforce it (report §2.5). Grafana 11.x and Oracle 11g R2 are "works but
not supported" (compatible, unofficial). The rationale for these policies is in
report §2.

Pinned dependencies (pinned at F0, 2026-08-07):

- `github.com/sijms/go-ora/v2` **v2.9.0**
- `github.com/grafana/grafana-plugin-sdk-go` **v0.296.1**
- `@grafana/ui` / `@grafana/data` **13.1.0**
- Node ≥ 22, Go ≥ 1.26 (development toolchain)

---

## Repository Structure

```
.
├── .github/                  # Issue/PR templates, CI workflow (ci.yml), community health files
├── docs/
│   ├── project-report.md     # Master design & implementation report (v2)
│   ├── release-and-packaging.md  # Distribution, signing, and release plan
│   └── README.md             # Documentation index
├── verification/             # Empirical verification harness (§21 checks)
│   ├── probe/                # Go probe (go-ora behavior checks)
│   ├── docker-compose.yml    # Oracle 21c + Turkish-charset + Grafana services
│   ├── go.mod / go.sum
│   └── README.md             # How to build and run the checks
├── devenv/                   # Dev-env auxiliary config (prometheus.yml)
├── src/                      # Frontend (TypeScript/React) — query editor, config editor
│   ├── plugin.json           # Plugin manifest (id, alerting, annotations, executables)
│   └── components/ module.ts datasource.ts types.ts
├── pkg/                      # Backend (Go, plugin SDK)
│   ├── plugin/               # Datasource, health check, query path
│   ├── oracle/               # DSN building, ORA error parsing, session params
│   └── models/               # Config/query models
├── .config/                  # create-plugin build/dev config (webpack, docker base)
├── docker-compose.yaml       # Dev loop + §17.1 version matrix (profiles)
├── go.mod / go.sum           # Go backend module
├── package.json              # Frontend build/test scripts
├── Magefile.go               # Grafana plugin build (mage)
├── CHANGELOG.md
├── CONTRIBUTING.md
├── LICENSE                   # Apache-2.0
├── README.md
└── SECURITY.md
```

> **F0 (2026-08-07):** scaffolded with `@grafana/create-plugin` at the repo
> root — `src/` + `pkg/` + `plugin.json` + `Magefile.go` + the §17.1
> `docker-compose.yaml` matrix and the F0 unit CI workflow.
>
> **F1 (2026-08-08):** real Oracle connection core in `pkg/oracle` + `pkg/plugin`
> — pooled `sql.DB` per datasource instance, session `TIME_ZONE` pinned to UTC,
> query timeout + `ctx` cancellation (verified server-side cancel on Oracle 21c),
> read-only `SELECT`/`WITH` guard, ORA-#### error parsing with password
> redaction, and the F1 config/query editors in `src/`.
>
> **F2 (2026-08-08):** typed type mapping + frame generation in `pkg/convert`
> (pure, DB-free) — NUMBER/INTEGER→`*int64`, fractional→`*float64`, DATE→
> `*time.Time` (UTC), NULL preserved, CLOB/BLOB, `largeNumberMode` (ADR-8)
> keeps NUMBER(20) at full precision as `*string` in tables and float in time
> series. §7 shaping: auto/table/time_series, wide/long, duplicate columns,
> empty frames, maxSeries guard. Format `<Select>` (auto/table/time_series) in
> the query editor + `largeNumberMode` in config. Verified on Oracle 21c (unit
> + integration tests, typed fields in table & time-series panels).
>
> **F3 (2026-08-08):** backend macro & variable engine in `pkg/interpolate`
> (pure, DB-free) — `$__timeFilter`/`timeFrom`/`timeTo` (ADR-9 `TO_DATE`
> literals), `$__unixEpochFilter`/`$__unixEpochGroup`, `$__from`/`$__to` epoch
> ms, `$__interval`/`_ms`/`_s` via the §10.4 derivation, `${var}`/`:csv`
> (empty → `IN (NULL)`)/`:singlequote` with a strict numeric-literal bare gate.
> Fail-closed guards reject variables in macro arguments, unknown/lowercase
> `$__` macros, malformed `${`, unresolved variables, and unsupported formats
> (never silent mis-evaluating SQL). Frontend `applyTemplateVariables` attaches
> stringified `scopedVars` (alert queries skip it — §8.3); "Numeric variables"
> config field. Verified: 30+ table-driven unit cases (incl. alerting-context
> DoD simulation) + `TestIntegration_Macros` on Oracle 21c.
>
> **F4 (2026-08-08):** SQL query editor in `src/` — mode-tab infrastructure
> (`Oracle (SQL)` | `Prometheus (PromQL)`, stored per query, DS `defaultMode`
> in config; the Prometheus tab is a placeholder until F6 and the backend
> rejects a saved `mode:"prometheus"` query before any connection). The bare
> textarea is replaced by the **`@grafana/plugin-ui`** `SQLEditor` (the package
> that succeeded `@grafana/experimental` for Grafana 13 — its peer range caps
> at SDK ^11 and would break `npm ci`) with an Oracle monarch language
> (`src/language/oracle.ts`: ANSI + Oracle keywords, built-in functions, `||`).
> Time-column dropdown (Auto + custom text; metadata list in F9). Verified:
> query-editor e2e specs + Grafana 12.4/13.1 plugin check 6/6.
>
> **F5 (2026-08-08):** Unified Alerting (SQL, basic) — **time-series value
> columns are now float64** in time-series format (Grafana's alerting SSE
> rejects `*int64`), fixing Layer 1 time-series alerts; Layer 2 "rows > 0" uses
> a **scalar** `SELECT COUNT(*)` → `Reduce(count)` (a multi-row table errors with
> duplicate labels — §10.2). Examples in `examples/alerting/`; automated check
> `verification/alert_check.py` (7/7 scenarios on Grafana 12.4 :3001 + 13.1
> :3002: L1 firing/normal/empty, L2 firing/normal/nodata, err-sql).
>
> **F6 (2026-08-09):** Prometheus proxy (backend) — `pkg/prom` client
> (`ValidateURL`, basic/bearer auth, query_range → wide matrix frame, query →
> wide single-row vector frame, §10.4 step derivation, legend template,
> response-size bound). `mode: "prometheus"` queries route to the proxy (a
> disabled proxy → clean 400, never touching Oracle); `CheckHealth` probes
> `status/buildinfo` when enabled. ConfigEditor **Prometheus** FieldSet (enable,
> URL, auth type, secret basic user/password + bearer token, TLS skip verify).
> Verified against a real Prometheus 3.5.0 on Grafana 12.4 + 13.1
> (`verification/prom_check.py`, 5/5): correct frames in both modes, raw PromQL
> on `executedQueryString`, and proxied failures never echo the configured token
> (credentials never logged).
>
> **F7 (2026-08-10):** Prometheus query editor + metric browser — the
> Prometheus tab is now a real editor: a **metric browser** (prefix-filtered
> metric dropdown from `label/__name__/values` + per-label value filters
> derived from `series`; "Use query" commits the built PromQL selector) and a
> monaco **PromQL editor** (`src/language/promql.ts`: `#` comments, durations,
> label matchers, aggregation keywords/functions), plus **Instant** / **Legend
> format** (`{{ __name__ }}`) / **Step** query options. The browser talks to the
> plugin's new `CallResource` handler (`prometheus/labels`,
> `prometheus/label/<name>/values`, `prometheus/series` — whitelisted, gated by
> `promEnabled`, label names validated to `[a-zA-Z0-9_:]`), which proxies the
> same credentials the queries use. Verified on Grafana 12.4 + 13.1: resource
> endpoints against a real Prometheus 3.5.0 and a **Prometheus-mode alert rule**
> (`prom_check.py` + `alert_check.py` 8 scenarios), plus a metric-browser e2e
> against the provisioned `prometheus-datasource`.
>
> **F8 (2026-08-10):** Grafana **query variables** — the variable editor
> (wired in F3 via `DataSourceVariableSupport`) supports **SQL single-column**
> queries feeding the dropdown, and **Prometheus `label_values(label)`** /
> **`label_values(metric, label)`**, intercepted in the backend before the
> PromQL parser and served as a one-column frame through the proxy (new
> `pkg/prom` `LabelValues`/`Series`). Multi-value `${var:csv}` → `IN (...)` and
> empty selection → `IN (NULL)` (no rows) are verified end-to-end. Verified on
> Grafana 12.4 + 13.1 (`verification/variable_check.py`, 5/5) and e2e
> (`tests/variables.spec.ts`: dropdown variable for SQL + Prometheus, and a
> multi-select dashboard whose panel shows only the selected values).
>
> **F9 (2026-08-11):** **Annotations + autocomplete** — a dashboard annotation
> whose target is a datasource query is marked on the panel: the frontend sets
> `query.annotation` when the annotation runner injects `scopedVars.__annotation`,
> the backend forces the **table** shape and sets `frame.Meta.DataTopic =
> Annotations` (the marker that draws the events; the reader maps
> `time`/`title`/`text` by name). New **metadata resource** (`metadata/tables`,
> `metadata/columns` under `CallResource`, from `ALL_TAB_COMMENTS` +
> `ALL_TAB_COLUMNS` ⋈ `ALL_COL_COMMENTS`, 200-table bound, 5-min TTL cache,
> silent-empty on missing permissions / DB down) feeds **SQL-editor completion**
> (plugin-ui `customProvider` tables/columns) and the **time-column dropdown**
> (DATE/TIMESTAMP columns of the parsed FROM table). Verified: `TestIntegration_Annotation`
> + `TestIntegration_Metadata` on Oracle 21c, and e2e `tests/annotations.spec.ts`
> (asserts `dataTopic === 'annotations'` on the annotation runner's
> `/api/ds/query` call); full e2e suite 13/13. **go-ora rejects literal `?`
> placeholders** (ORA-00911) — the metadata queries use the repo's numbered
> `:N` style.
>
> **F10 (2026-08-11):** **Health enrichment + error resolution** — the datasource
> "Test" result is a **structured JSON detail** (report §13):
> `{"oracle":{"ok":…,"version":…},"prometheus":{"enabled":…,"ok":…}}` with the
> readable error in the failing subsystem; a proxy that is not configured
> reports `enabled=false, ok=true`, so health stays **green without a Prometheus
> config** and never masks an Oracle failure. The ORA error map grows to **27
> codes** for the bad password / URL / connection scenarios and common query
> errors (01017, 01034, 12514/12505/12541/12545/12560/12154/12170, 00911,
> 06550, …); network failures map to a `network error:` prefix; every message is
> DSN-password-redacted. Verified: bad password → `ORA-01017: invalid username
> or password`, unreachable host → `network error: …` as JSON on the Test screen
> (config-editor e2e asserts the alert text), `TestHealthResult_Shapes` +
> `TestIntegration_Health*` on Oracle 21c; plugin check 6/6, full e2e suite
> 13/13.
>
> **F15 (2026-08-11):** **Command execution (operations UI)** — a **"Run
> command"** button in the query editor posts an anonymous PL/SQL command to a
> new backend **`command` resource** (report §12, ADR-13), strictly outside the
> query pipeline — never re-run by panel refresh, alerting or variables. The
> backend **always requires `confirm:true`**, then enforces the **allowlist**:
> `BEGIN…END;` blocks whose statements are `DBMS_SCHEDULER.<proc>(…)` or plain
> procedure calls, plus `SELECT <expr> FROM DUAL` (function value rendered
> inline); comments, DDL/DML, control flow, and the escape-hatch packages
> `DBMS_SQL` / `DBMS_SYS_SQL` / `DBMS_UTILITY` are rejected. A confirmation
> dialog gates the action (config-toggleable — disabling it only skips the
> dialog); results and errors render inline; execution is bound by
> `commandTimeoutSec` (default 10 s) and every command is audit-logged.
> **Oracle 21c note:** `DBMS_SCHEDULER.START_JOB` was **removed in 21c**
> (compile-time `PLS-00302`); `RUN_JOB('JOB', use_current_session => FALSE)` is
> the replacement for starting a job. Verified: `TestIntegration_Command` on
> Oracle 21c (confirm gate, DISABLE/ENABLE/RUN_JOB/STOP_JOB lifecycle, procedure
> side-effect, function value "42", DDL/DML rejected), e2e
> `tests/command.spec.ts` on 12.4 + 13.1, full e2e suite 16/16 on 13.1, plugin
> check 6/6 on 12.4 + 13.1.
>
> **F11 (2026-08-11):** **Security audit, log hygiene & self-metrics** — the
> plugin now exports `oracle_datasource_query_total`, `oracle_datasource_errors_total`
> and `oracle_datasource_query_duration` (seconds), labelled
> `{datasource=DS uid, mode=sql|prometheus}` (report §18 F11). They are
> registered on `prometheus.DefaultRegisterer`, so the SDK diagnostics server
> serves them via the `CollectMetrics` RPC and Grafana exposes them at
> **`/metrics/plugins/emreatayy-oracle-datasource`** — verified live on the
> 12.4 and 13.1 matrix Grafanas after a panel query. The query path logs at the
> right level (`Debug` executed / `Warn` client rejection / `Error` failure) and
> **never logs SQL, values or credentials** — every message is
> password-redacted; `TestLogHygiene` pins this down. §15 audit re-confirmed
> secureJsonData secrets, variable escaping, the read-only guard, the command
> allowlist, and the SSRF-safe Prom URL. See the [Metrics](#metrics) section.
>
> **F12 (2026-08-11):** **Performance, limits, streaming & caching** — the
> §18 F12 DoD is **measured on real Oracle**, not assumed: a 120k-row
> generated query is **stopped at the `maxRows` cap (default 100k)** with a
> **warning notice** on the frame (Grafana shows it on the panel) and a
> `queries_truncated_total` counter bump; **cancellation** stops a heavy
> `CONNECT BY` query in ~311 ms (ORA-01013, panel-close path); and a **pool
> under load** — 20 concurrent queries over a 5-connection pool — completes in
> ~365 ms with the measured peak never exceeding the `maxOpenConns` cap and the
> pool draining back to the idle high-water mark. The Prometheus proxy response
> is **capped at 64 MiB** with a readable limit error (was a confusing parse
> failure). The metadata cache TTL (5 min), row streaming from the driver and
> pool parameters were already in place; F12 adds two observability counters
> (`rows_scanned_total`, `queries_truncated_total`) to the Metrics section.
>
> **F13 (2026-08-11):** **Documentation, templates, packaging, e2e** — the
> release-ready package and the "every example works" DoD are done and locked
> by tests. [Installation guide](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/installation.md) covers the version
> matrix, archive install + the unsigned-plugin flag, every datasource field,
> the recommended read-only user, and the official-exporter → Prometheus →
> proxy guide; `examples/dashboard/oracle-overview.json` demonstrates all three
> query shapes on any Oracle (synthetic-load SQL time series, `user_tables`
> SQL table, Prometheus-proxy `up` panel) — and `TestIntegration_DashboardExamples`
> now runs both SQL panels through the real query path and asserts the frames
> (96-point series, ≤ 20-row table), so the examples can't rot.
> `examples/readonly_user.sql` provisions the read-only account. Packaging:
> `mage archive` builds `dist/emreatayy-oracle-datasource-1.0.0.zip` + `.tar.gz`
> in the Grafana layout, `mage validate` runs `plugin-validator` at **zero
> errors**, `npm run sign` (`GRAFANA_ACCESS_POLICY_TOKEN`) signs `dist/` before
> archiving, and `.github/workflows/release.yml` automates build → sign → archive
> → validate → GitHub Release on a `v*` tag. Full e2e suite + plugin-check
> matrix 6/6 on Grafana 12.4 + 13.1 green.

---

## Metrics

The plugin exposes five self-metrics to Grafana (report §18 F11 + F12), labelled
by **datasource UID** and **query mode** (`sql` | `prometheus`):

| Metric | Type | Meaning |
|---|---|---|
| `oracle_datasource_query_total` | counter | Queries executed |
| `oracle_datasource_errors_total` | counter | Queries that failed |
| `oracle_datasource_query_duration` | histogram (s) | Query duration |
| `oracle_datasource_rows_scanned_total` | counter | Rows read into query frames (F12) |
| `oracle_datasource_queries_truncated_total` | counter | Queries stopped early by the `maxRows` limit (F12) |

They are registered on `prometheus.DefaultRegisterer`; the Grafana SDK's
diagnostics server serves them through the `CollectMetrics` RPC and Grafana
exposes them at the per-plugin endpoint `/metrics/plugins/emreatayy-oracle-datasource`
(plugin series are not merged into the main `/metrics`). Verify with:

```sh
curl -s http://<grafana>/metrics/plugins/emreatayy-oracle-datasource | grep oracle_datasource
```

Metric labels and help text never contain values or passwords (§15).

---

## Getting Started (Development)

### Prerequisites

- **Go** ≥ 1.26
- **Node.js** ≥ 22
- **Docker** (Docker Desktop) — for the Oracle verification containers

> Note: the `gvenzl/oracle-xe` images are amd64-only; on Apple Silicon they
> run under Rosetta emulation (slow first start, works fine).

### Run the empirical checks

The `verification/` harness validates go-ora behavior against a live Oracle DB.
Start the services (Oracle takes a few minutes to become healthy):

```sh
docker compose --profile oracle21 up -d     # checks 1, 2, 4, 8
docker compose --profile oracle_tr up -d    # check 7 (Turkish WE8ISO8859P9)
```

Then run the probe:

```sh
cd verification
go mod tidy
ORACLE_PASSWORD=verif_password go run ./probe          # -> oracle21
ORACLE_PASSWORD=verif_password ORACLE_PORT=1522 go run ./probe --port 1522   # -> oracle_tr
```

The probe prints a per-check verdict and exits non-zero if any check fails.
Details in `verification/README.md`.

### Build and run the plugin (F0+)

Install dependencies, then build **backend** and **frontend** separately (both
write to `dist/`):

```sh
npm install
mage -v                    # backend binaries → dist/gpx_oracle_datasource_*
npm run build              # frontend → dist/module.js (+ copies plugin.json, README, …)
```

Run it in Grafana via the §17.1 version matrix (built plugin volume-mounted,
loaded unsigned):

```sh
docker compose --profile grafana12 up -d grafana12   # Grafana 12.4 → http://127.0.0.1:3001
docker compose --profile grafana13 up -d grafana13   # Grafana 13.1 → http://127.0.0.1:3002
```

(The explicit service name keeps the create-plugin dev loop from starting too;
`dist/` is re-mounted automatically, so after editing code just re-run `mage -v`
/ `npm run build` and refresh.)

**Hot-reload dev loop** (create-plugin standard): `npm run dev` runs webpack
watch, `npm run server` starts the dev Grafana (supervisord, port 3000):

```sh
npm run dev      # terminal 1 — frontend watch
npm run server   # terminal 2 — dev Grafana + backend
```

Unit checks (mirror CI): `npm run typecheck && npm run lint && npm run test:ci`
and `mage test` for the Go backend. Full phase-by-phase detail: report §18.

Verify the built plugin against a running matrix Grafana (plugin load, health
"Test", query path, alert-rule acceptance — 6 checks):

```sh
./verification/grafana_plugin_check.py --url http://127.0.0.1:3001   # -> 6/6 passed
./verification/grafana_plugin_check.py --url http://127.0.0.1:3002
```

---

## Documentation

| Document | Purpose |
|---|---|
| [`docs/project-report.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/project-report.md) | **Master design & implementation report (v2)** — version matrix, architecture + ADRs, type mapping, macros, Prometheus mode, alerting, command execution, security, test plan, 16-phase roadmap. |
| [`docs/installation.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/installation.md) | User installation & configuration guide — archive install, datasource fields, read-only user, Prometheus proxy, exporter guide, troubleshooting. |
| [`docs/release-and-packaging.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/release-and-packaging.md) | Distribution model, build/signing pipeline, versioning, and release workflow plan. |
| [`verification/README.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/verification/README.md) | How to build and run the empirical verification harness. |

---

## Roadmap

16 implementation phases in 4 waves (~52–54 person-days). **F13 is done —
everything through Wave C and the first three Wave D phases have landed:**
(scaffold + Oracle connection core + typed type mapping & frame generation +
backend macro & variable engine + SQL query editor + basic alerting + the
Prometheus proxy backend + the PromQL editor & metric browser + Grafana query
variables + **annotations** + **editor autocomplete** + **health JSON detail**
+ **ORA error resolution** + **command execution** + **security audit, log
hygiene & self-metrics**: pooled connection, session timezone pinning,
ctx-cancelled queries, read-only guard, ORA error parsing, typed
NUMBER/DATE/CLOB/NULL fields, wide/long time series, `largeNumberMode`,
`$__timeFilter`/`$__unixEpochGroup`/`${var:csv}` interpolation with fail-closed
guards, mode-tab query editor with Oracle + PromQL monarch languages, float64
time-series value columns + scalar-`COUNT(*)` "rows > 0" alert rules, a
`pkg/prom` proxy client with query_range/query frames, step derivation, legend
templates, basic/bearer auth, a ConfigEditor Prometheus section, the metric
browser backed by a proxied `CallResource` handler, query variables — SQL
single-column + Prometheus `label_values` with multi-value `IN` — dashboard
annotations marked with `DataTopic.Annotations` on the query frame, a TTL-cached
metadata resource (`ALL_TAB_COMMENTS` + `ALL_TAB_COLUMNS`) feeding SQL-editor
table/column completion and the time-column dropdown, a structured health
`{oracle,prometheus}.ok` JSON with a 27-code ORA error map (bad password / URL /
connection + query errors, all credential-redacted), a "Run command" button
that executes allowlisted one-shot scheduler/procedure commands through the
backend `command` resource (confirm-gated, audit-logged, `commandTimeoutSec`
bound, `SELECT … FROM DUAL` values rendered inline), the F11 self-metrics
(`oracle_datasource_query_total`/`errors_total`/`query_duration`, labelled by
DS uid + mode, exposed through Grafana's `/metrics`) with a §15 log-hygiene
audit (no SQL, values or passwords in logs), the **F12 measured guarantees**
(100k-row `maxRows` truncation with a frame warning notice, ~311 ms
cancellation, 20-over-5 pool under load, 64 MiB Prometheus response cap), and
the **F13 release readiness** (user installation guide incl. read-only
operating account + exporter guide, a three-shape dashboard template +
`examples/readonly_user.sql`, `mage archive`/`mage validate` packaging at
zero validator errors, `npm run sign` signing + the tag-triggered release
workflow, and the full e2e pass) — all verified on Grafana 12.4 + 13.1, the
alert rules and the proxy against a real Prometheus 3.5.0, and the examples
against a real Oracle 21c XE).
The critical path going forward was **F14 (ops/optional)** — TLS/TNS
options, the Prometheus `ALERTS` view (Layer 3 alerting), and optional
exporter orchestration. **F14 is now done** (column→unit formatting, Oracle
TLS / TCPS options, TNS connect-descriptor host pass-through, and the
self-contained Layer-3 `ALERTS` demo rule — the remaining optional exporter
orchestration is explicitly out of scope, see [Status](#status)). **All 16
phases F0–F15 are complete.**

| Wave | Phases | Scope | Effort |
|---|---|---|---|
| **A — Oracle Core** | F0–F5 | Scaffold, connection core, type mapping, macro engine, query editor, basic alerting | ~21 pg |
| **B — Prometheus** | F6–F7 | Proxy backend, PromQL editor + metric browser | ~7 pg |
| **C — Data & UX** | F8–F10, F15 | Variables, annotations + autocomplete, health/error UX, command execution UI | ~12 pg |
| **D — Maturity & Release** | F11–F14 | Security/self-metrics, performance/limits, docs + packaging + e2e, ops/optional | ~12–14 pg |

**F15 (command execution UI)** was the last Wave C phase and **F11 + F12 +
F13 (security/self-metrics, performance/limits, then documentation /
templates / packaging / e2e)** were the first three Wave D phases to land —
all **done**, and **F14 (ops/optional — TLS/TNS, Layer 3 `ALERTS`, column
units) completed the phase list** (see [Status](#status)). Full phase-by-phase
detail with acceptance criteria: report §18.

---

## Release & Packaging

The distribution plan — build → sign → GitHub Release → Grafana catalog — is
documented in [`docs/release-and-packaging.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/docs/release-and-packaging.md).

The tl;dr: a Grafana data source plugin is distributed as a **signed plugin
archive** (`.zip` / `.tar.gz`) produced by the `mage` build, not as an npm or
container package. Release automation is created in **F13** when the plugin
builds; the workflow files will use the standard Grafana plugin release action
and require a signing API key stored as a GitHub secret.

---

## Contributing

Please read [`CONTRIBUTING.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/CONTRIBUTING.md) — it covers the design-first
workflow (report → ADR → verification → code), the English-only project
convention, and how to run the verification harness before submitting changes.

## Security

Vulnerability reporting and security expectations: [`SECURITY.md`](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/SECURITY.md).

## License

[Apache License 2.0](https://github.com/emreatayy/Grafana_Oracle_Connector/blob/main/LICENSE) — © 2026 the project contributors.
Third-party licenses: `go-ora` (MIT), Grafana plugin SDK (Apache-2.0).

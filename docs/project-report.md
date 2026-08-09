# Grafana ↔ Oracle Data Source Plugin — Technical Design & Implementation Report (v2)

**Date:** 2026-08-07
**Reference project:** https://github.com/albertowd/oracle-grafana (Apache-2.0)
**Scope:** End-to-end design and implementation roadmap for a new Grafana data source plugin with dual-mode Oracle (SQL) + Prometheus (PromQL), alerting support, and **multi-version** support.

**Project language:** English (single language). UI strings, documentation, log messages, and user-facing error messages are English-only.

> This version (v2) extends the v1 analysis report with: (1) a version support matrix, (2) an explicit ADR decision log, (3) concrete SQL expansion definitions for every macro, (4) a full specification of the type mapping and frame generation algorithms, (5) a security section, (6) a detailed 15-phase implementation plan, and (7) a list of assumptions to verify.

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Version Support Matrix (Multi-Version)](#2-version-support-matrix-multi-version)
3. [Reference Project Analysis](#3-reference-project-analysis)
4. [Architecture and Decision Records (ADR)](#4-architecture-and-decision-records-adr)
5. [Oracle Connection Layer](#5-oracle-connection-layer)
6. [Column Type Mapping](#6-column-type-mapping)
7. [Frame Generation Algorithm](#7-frame-generation-algorithm)
8. [Macro and Variable Engine (Backend Interpolation)](#8-macro-and-variable-engine-backend-interpolation)
9. [Prometheus Mode (Proxy)](#9-prometheus-mode-proxy)
10. [Alerting System](#10-alerting-system)
11. [Annotations](#11-annotations)
12. [Command Execution and Operations](#12-command-execution-and-operations)
13. [Health Check](#13-health-check)
14. [Frontend Design](#14-frontend-design)
15. [Security Design](#15-security-design)
16. [Test Strategy](#16-test-strategy)
17. [Dev Environment and CI](#17-dev-environment-and-ci)
18. [Implementation Roadmap — 16 Phases](#18-implementation-roadmap--16-phases)
19. [Risks and Mitigations](#19-risks-and-mitigations)
20. [Distribution and Release](#20-distribution-and-release)
21. [Assumptions to Verify and Open Decisions](#21-assumptions-to-verify-and-open-decisions)
22. [References](#22-references)

---

## 1. Executive Summary

`albertowd/oracle-grafana` is a data source plugin that connects to Oracle with `SELECT` statements and converts the results into Grafana panels, using a Go backend + TypeScript frontend architecture. The core idea is correct, but it has three structural gaps:

1. **It cannot produce time series** — it converts every column to `string` and never emits a `time.Time` field; the `timeseries` panel does not work.
2. **No Prometheus support** — there is no metrics-agent integration for high-frequency / long-retention scenarios.
3. **No alerting** — without time series, Grafana Unified Alerting has nothing to evaluate.

This report (a) analyzes the reference project, (b) defines a **version support matrix**, (c) designs the dual-mode architecture (SQL + PromQL) in full detail, (d) builds the alerting foundation, (e) designs **operational command execution** (start/stop/enable/disable scheduler jobs, call procedures), and (f) presents a 16-phase implementation plan with acceptance criteria.

**Core decisions:**

- **Prometheus is not required for time series and alerting.** A plugin with correct type mapping can produce time series and alerts directly from Oracle. Prometheus is an **optional** layer.
- **All interpolation (macros + variables) happens in the backend** — alert and query-variable queries do not go through the frontend.
- **Multi-version:** a single SDK build supports Grafana 12.x/13.x, Oracle 12c+, and Prometheus 2.40+/3.x (Section 2).
- **Pure Go Oracle driver (go-ora)** is used; no OCI/Instant Client required.
- **Operational commands are a first-class feature:** a "Run command" button executes allowlisted `BEGIN…END` operations (scheduler + procedure calls) through the backend resource handler, strictly outside the read-only query path (Section 12).

---

## 2. Version Support Matrix (Multi-Version)

### 2.0 Final Summary

| System | Official support (floor) | Primary (main) lines | CI test matrix |
|---|---|---|---|
| **Grafana** | **≥12.0** | 12.4, 13.x | `12.4` + `13.1` |
| **Oracle** | **≥12c** | 19c, 21c, 23ai | XE `18c` + `21c` + `23c` |
| **Prometheus** | **≥2.40 + 3.x** | 3.x | `3.5` (LTS) |

- Grafana 11.x and Oracle 11g R2: "works but not supported" (compatible, unofficial).
- The rationale for these policies is below (§2.1).

### 2.1 Policy

- The plugin ships as a **single build** that runs on every supported line (the backend protocol is stable, so a single SDK version suffices).
- **Grafana:** support = the lines that are actively receiving security fixes at release time. An EOL line is dropped from the list — because the plugin runs **inside** Grafana; an old Grafana means an old frontend runtime and old `@grafana/ui` compatibility (a real technical cost, not just policy).
- **Oracle:** the support decision is **independent of Oracle's support calendar**; go-ora compatibility and testability are what decide it. Because versions still common in enterprises are kept, the **floor is 12c**; the primary lines are 19c+.
- **Prometheus:** version-neutral — the only dependency is the stable HTTP API v1 contract.
- CI runs e2e on the **oldest + newest** supported lines (Section 17).
- Frontend packages (`@grafana/ui`, `@grafana/data`) are pinned to a version compatible with the **lowest** supported Grafana.

### 2.2 Grafana

| Level | Versions | Description |
|---|---|---|
| **Supported (official)** | **12.4, 13.0, 13.1** | The actively supported lines at release time (Aug 2026) |
| **Minimum (declared)** | `>=12.0.0` | `plugin.json → dependencies.grafanaVersion` |
| **CI test matrix** | `12.4` + `13.1` | e2e with Docker images (Section 17) |
| **Compatible (unofficial)** | 11.x | Backend protocol is stable; not tested, not promised |
| **Not supported** | < 11.x | Security support ended; blocked by the plugin.json minimum |

> **Note:** Grafana has no official "LTS". Its policy: "Only the latest version receives active development; the previous minor and the last minor of the previous major receive security and critical bug fixes." The support set must therefore be updated at every release. As of Aug 2026 the supported lines are 13.1, 13.0, 12.4, 12.3.

### 2.3 Oracle Database

| Level | Versions | Description |
|---|---|---|
| **Supported (official)** | **12c (12.1/12.2) and newer** | go-ora speaks the 11g+ protocol; 12c is still common in enterprises |
| **Primary (main) lines** | **19c, 21c, 23ai (23c/26ai)** | Priority development + testing; 19c Premier→2029, 21c Premier→2027, 23ai/26ai Premier→2031 |
| **Development/CI** | XE 18c, 21c, 23c | `gvenzl/oracle-xe` slim images (18c is the closest XE to 12c; no official 12c XE image) |
| **Compatible (unverified)** | 11g R2 | go-ora targets it; not tested, not promised |
| **Not supported** | < 11g R2 | Outside go-ora scope |
| **Version-dependent features** | `BOOLEAN` type only on **23c+** | On pre-23c, return a readable error + `CAST` suggestion |

**"No-bugs" guarantee:** The generated macro SQL and metadata queries use only **12c-era** constructs (the ANSI `TIMESTAMP '…'` literal exists since 9i; `ALL_TAB_COLUMNS` and friends have always existed). Version-specific behavior (e.g., 23c `BOOLEAN`) is explicitly gated (feature gate). Because go-ora behavior can change between versions (#533), the scan strategy never trusts driver coercion (Section 6.2). Version detection is done in the health check via `V$VERSION`.

### 2.4 Prometheus

| Level | Versions | Description |
|---|---|---|
| **Supported (official)** | **2.x (≥2.40) and 3.x (≥3.0)** | HTTP API v1 is stable; 3.5 is an LTS line |
| **Compatible (unofficial)** | PromQL-compatible servers: VictoriaMetrics, Mimir, Thanos | Works via their `/api/v1/*` compatibility |
| **Editor/PromQL** | 3.x syntax targeted | Editor hint for features removed in 2.x |

> The plugin does not interpret PromQL itself; it forwards the query to the server (proxy). PromQL compatibility therefore mostly belongs to the server. The plugin depends only on the HTTP API v1 contract.

### 2.5 Dependency Pins

| Dependency | Strategy | Note |
|---|---|---|
| `grafana-plugin-sdk-go` | **v0.296.1** (pinned in F0, 2026-08-07) | Resolved by the create-plugin scaffold |
| `sijms/go-ora/v2` | **v2.9.0** (verified 2026-08-07) | Pinned in `go.mod` at F0 (used from F1); `ColumnTypes` behavior verified (§6.2); #533 native-string behavior persists — always scan into explicit types |
| `@grafana/ui`, `@grafana/data` | **13.1.0** (pinned in F0) | create-plugin scaffold resolved these; they enforce an **effective Grafana floor of 12.3** (`grafanaDependency >=12.3.0`) — the §2.0 ≥12.0 target is adjusted accordingly; the CI matrix (12.4 + 13.1) is unaffected |
| `prom/prometheus` (CI) | 3.5 LTS | For e2e |
| `gvenzl/oracle-xe` (CI) | `18c-slim` + `21c-slim` + `23c-slim` | License acceptance + small footprint; 18c = oldest test floor |
| `grafana/grafana-oss` (dev matrix) | `12.4.0`; **13.1.x only as `grafana-enterprise:13.1.0`** | `grafana-oss` Docker tags stop at 13.0.2 (2026-06-02); enterprise image = same OSS binary, free pull (§17.1) |

---

## 3. Reference Project Analysis

### 3.1 Architecture (summary)

```
Grafana (panel / alert / variable)
   |  gRPC (plugin SDK protocol)
   v
Backend Plugin (Go) — gpx_oracle_grafana
   |-- QueryDataHandler  -> OracleDatasource.query(...)
   |-- CheckHealthHandler
   +-- instancemgmt (one instance per data source)
        |  database/sql + github.com/sijms/go-ora/v2
        v
     Oracle DB (host:port/service  |  connString / SID)
Frontend (TS/React): datasource.ts, interpolate.ts, QueryEditor.tsx, ConfigEditor.tsx
```

### 3.2 Strengths

- Simple installation (a single `.tar.gz` + unsigned-loading flag).
- Pure Go driver — no OCI/Instant Client needed (a big win over godror).
- `$__from`/`$__to` and `$variable` interpolation; a well-thought-out "SQL preview" feature.
- Query-variable support; password stored via `secureJsonData`, masked in logs (`********`).
- Flexible connection: simple fields or a single `ConnString` (including SID).

### 3.3 Weaknesses and Root Causes

| # | Problem | Root cause | Impact |
|---|---|---|---|
| 1 | Cannot produce time series | Every cell read as `string(scannedValue)`; types never inspected; no `time.Time` field | `timeseries` does not work; fake `TO_CHAR` timestamps |
| 2 | No numeric panels | NUMBER columns come back as strings | stat/gauge/bar and unit config impossible |
| 3 | No alerting | No time series -> Unified Alerting has nothing to evaluate | No alerts |
| 4 | NULL is corrupted | null -> literal `"(null)"` | Wrong values |
| 5 | No cancellation | `context` ignored; `stmt.Query()` (without ctx) | Query keeps running in Oracle after the panel closes |
| 6 | No standard macros | Only `$__from`/`$__to` | GROUP BY bucketing cannot be written by hand |
| 7 | No multi-value variables | "future" | No `IN (…)` |
| 8 | No pool settings | `SetMaxOpenConns` etc. never called | Risk of connection explosion |
| 9 | No metadata/type detection | Column names arrive as a string array | No autocomplete, no time-column selection |
| 10 | No Prometheus | Every panel refresh runs live SQL | No high-frequency / long-retention |
| 11 | No annotations | No `annotations: true`; no handler | Cannot mark events |
| 12 | Raw error codes | ORA surfaced directly | Not user friendly |

---

## 4. Architecture and Decision Records (ADR)

### 4.1 Overall Architecture

```
Grafana (panel / alert rule / variable / annotation)
   |  gRPC (plugin SDK)
   v
+------------------------------ Grafana Server ------------------------------+
|  Backend Plugin (Go)                                                          |
|   |-- QueryDataHandler                                                        |
|   |    |-- mode="sql"   -> Oracle executor (go-ora, type mapping, macros)    |
|   |    +-- mode="prom"  -> Prometheus proxy (HTTP API v1 -> frame)           |
|   |-- CheckHealthHandler -> Oracle + (opt.) Prometheus health check           |
|   |-- AnnotationQueryHandler (opt.)                                          |
|   +-- instancemgmt (one instance per DS, dispose on config change)           |
|        |                                   |  HTTPS (API v1)                  |
|        v                                   v                                  |
|     Oracle DB (SELECT only)           Prometheus Server                      |
|                                             ^                                 |
|                                             | scrape /metrics (9161)          |
|                                      Oracle Metric Exporter                   |
|                                 (oracle-db-appdev-monitoring, container)     |
+------------------------------------------------------------------------------+
                        |  HTTP
Frontend (React/TS)
   QueryEditor:  [ Oracle (SQL) | Prometheus (PromQL) ]  tab-button
   ConfigEditor: Oracle section + (opt.) Prometheus section
   datasource.ts: sends raw query to backend (interpolation happens in backend)
```

### 4.2 Decision Records (ADR)

| ID | Decision | Rationale | Status |
|---|---|---|---|
| ADR-1 | **Prometheus backend-proxy** (Option A) | Secrets never reach the browser, no CORS issues, one query editor | ✅ Approved |
| ADR-2 | **Interpolation entirely in the backend** (macros + variables) | Alert and query-variable queries do not pass through the frontend; escaping is centralized in the backend | ✅ Approved |
| ADR-3 | **Single SDK build + multi-version** | Backend protocol is stable; CI matrix [12.4, 13.1] | ✅ Approved |
| ADR-4 | **Pure Go driver (go-ora/v2)** | No OCI; distribution advantage | ✅ Approved |
| ADR-5 | **Time series = correct type mapping**; Prometheus optional | Root fix for the primary complaint (time-based charts) | ✅ Approved |
| ADR-6 | **SQL time_series -> wide frame**; Prometheus range -> wide; vector -> single row | Efficient long format favored by Grafana; no duplicate time | ✅ Approved |
| ADR-7 | **SQL read-only enforcement** — query path: first-keyword guard + read-only user recommended; **command path is a deliberate, allowlisted exception** (ADR-13) | Prevent accidental DML | ✅ Approved |
| ADR-13 | **Command execution via `CallResourceHandler`** — a "Run command" button in the query editor posts to the backend resource handler, which executes only allowlisted operations (`BEGIN…END` blocks for `DBMS_SCHEDULER` + procedure/function calls) with `ExecContext`; commands never enter the query pipeline (no dashboard refresh / alert / variable re-run) | Scheduler/procedure operations need elevated grants; commands must be explicit, audited user actions | ✅ Approved |
| ADR-8 | **Large NUMBER precision policy** (int64/float64/string split) | IDs above 15 digits must not silently break | ✅ Approved |
| ADR-9 | **Macro literal style = `TO_DATE('…', 'YYYY-MM-DD HH24:MI:SS')`** | NLS-independent (explicit format); **verified index-friendly on DATE columns** — the ANSI `TIMESTAMP` literal caused a full scan (§21 check 4) | ✅ Approved (revised) |
| ADR-10 | **`@grafana/create-plugin` scaffold** | Mage, Jest, docker-compose, CI come ready | ✅ Approved |
| ADR-11 | **Defaults:** maxRows=100k, maxSeries=1000, queryTimeout=30s, step derivation (Section 9) | Prevents resource leaks/explosions | ✅ Approved |
| ADR-12 | **Declare both `alerting: true` and `annotations: true` in `plugin.json`** | `alerting` flag behavior varies by version; declaring is harmless and safe | ⏳ Verify empirically in F0 (Section 21) |

### 4.3 Data Model (`types.ts`)

```ts
export type QueryMode = 'sql' | 'prometheus';

export interface MyQuery extends DataQuery {
  mode: QueryMode;
  // SQL mode
  sql?: string;                 // RAW SQL (contains macros + ${variable}; interpolation is backend-side)
  format?: 'auto' | 'table' | 'time_series';
  timeColumn?: string;          // explicitly selected time column (filled from metadata)
  largeNumberMode?: 'string' | 'float';  // large NUMBER policy (default 'string' for table / 'float' for ts)
  scopedVars?: Record<string, unknown>;  // attached by applyTemplateVariables at query time (never saved; absent for alert queries — §8.3)
  // Prometheus mode
  promql?: string;
  legendFormat?: string;        // series name template (default '{{ __name__ }}')
  step?: string;                // optional; if empty the backend derives it
}

export interface MyDataSourceOptions extends DataSourceJsonData {
  // Oracle
  o_connStr?: string; o_hostname?: string; o_port?: number;
  o_service?: string; o_sid?: string; o_user?: string;
  maxOpenConns?: number; maxIdleConns?: number; connMaxLifetimeSec?: number;
  queryTimeoutSec?: number; commandTimeoutSec?: number; maxRows?: number; maxSeries?: number;
  defaultMode?: QueryMode; defaultFormat?: 'auto' | 'table' | 'time_series';
  largeNumberMode?: 'string' | 'float';
  numericVariables?: string[];  // may expand unquoted in SQL — a hint, not a trust grant (§8.2)
  // Prometheus (optional)
  promEnabled?: boolean; promUrl?: string;
  promAuthType?: 'none' | 'basic' | 'token';
  promTlsSkipVerify?: boolean;
}

export interface MySecureJsonData {
  o_password?: string;
  promUser?: string; promPassword?: string; promToken?: string;
}
```

> **Commands are ad-hoc:** command text is **not** part of `MyQuery` — it is posted per action via `datasource.postResource('command', { sql, confirm })` and executed by the backend resource handler (Section 12). No dashboard, alert, or variable query ever carries it.

---

## 5. Oracle Connection Layer

### 5.1 Connection Configuration

- Connection sources: (a) `host + port + service`, (b) `host + port + sid`, (c) a single `ConnString` (priority: connString > fields).
- go-ora connection string: `oracle://user:pass@host:port/service`.
- **Pool parameters** (ADR-11): `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`. Defaults configurable; safe values if empty (e.g., 10/5/30m).
- **Query timeout:** `context.WithTimeout` for every query (default 30s); `ctx` cancellation also cancels the query when the panel closes.
- **Read-only session:** no forced `SET TRANSACTION READ ONLY` (Oracle sessions are not per-statement); instead a read-only DB user is documented + a first-keyword guard (ADR-7).

### 5.2 Health Check (core)

`SELECT 1 FROM DUAL` (Oracle requires `FROM`) + optional `SELECT banner FROM V$VERSION WHERE ROWNUM = 1`. Enriched in Section 13.

### 5.3 Error Model

- Parse go-ora errors for the `ORA-####` pattern; map known codes to readable messages (Section 13).
- Distinguish network errors during connection ("connection refused", "timeout").

### 5.4 Cross-Platform Behavior

Grafana runs on many operating systems; Oracle typically runs on Linux (or as a managed/cloud DB). The plugin is **platform-independent by design** and behaves identically on every OS Grafana supports:

- **The plugin runs on the Grafana host, not on Oracle.** The backend connects to Oracle over TCP using go-ora's wire protocol. Oracle's OS is irrelevant — it is just a network endpoint (also true for Windows Oracle and cloud DBs like OCI Autonomous).
- **The only platform-specific artifact is the plugin binary itself.** Grafana backend plugins are packaged with per-platform binaries (`bin_linux-amd64/`, `bin_linux-arm64/`, `bin_darwin-arm64/`, `bin_darwin-amd64/`, `bin_windows-amd64/`, …); Grafana loads the matching one. `mage build` cross-compiles the full GOOS/GOARCH matrix in one step.
- **Pure-Go is the enabler.** `godror` would require Oracle Instant Client (native C) on every Grafana host, per OS/arch — the classic cross-platform killer. go-ora has **no OCI dependency**, so the whole backend stays **CGO-free** and cross-compiles cleanly.

**Determinism constraints (must hold):**
- Keep the backend CGO-free (no packages that force cgo) so `mage build` stays trivial.
- **Timezone:** pin `sessionTimeZone=UTC` and convert to UTC in code — the host OS timezone never affects results.
- **TLS:** Go uses the system cert pool (Windows system store / Linux ca-certificates); no host-specific paths baked in.
- **NLS/encoding:** session NLS is set explicitly at connect (§6.4) so the host locale never leaks in.
- **No reliance on env vars** such as `NLS_LANG` (OCI's mechanism) — that would break cross-host determinism.

CI smoke-tests the binary matrix (minimum `linux-amd64` + `linux-arm64`); behavior is identical everywhere.

---

## 6. Column Type Mapping

### 6.1 Mapping Table

| Oracle type | Go target | Grafana field | Note |
|---|---|---|---|
| NUMBER (scale > 0) | `float64` | `*float64` | Fractional values |
| NUMBER (scale = 0, prec ≤ 15) | `int64` | `*int64` | IDs/counters |
| NUMBER (scale = 0, prec > 15) | per ADR-8 | `*string` (table) / `*float64` (ts) | Precision preserved; loss documented in ts |
| INTEGER / SMALLINT | `int64` | `*int64` | Pre-23c `INTEGER`→NUMBER conversion note |
| FLOAT / BINARY_DOUBLE / BINARY_FLOAT | `float64` | `*float64` | |
| DATE | `time.Time` (UTC) | `*time.Time` | ← time field |
| TIMESTAMP[±TZ] | `time.Time` (UTC) | `*time.Time` | ← time field; TZ→UTC conversion |
| CHAR / VARCHAR2 / NVARCHAR2 | `string` | `*string` | |
| CLOB / NCLOB | `string` | `*string` | Mind go-ora inline vs locator mode |
| BLOB / RAW | `[]byte` | `*string` (base64) | SDK has no bytes vector (v0.296) → base64 in table; read side stays `[]byte` |
| BOOLEAN (23c+) | `bool` | `*bool` | Not supported before 23c; `CAST` suggestion |
| NULL | `null` | `data.Sentinel` / null field | never `"(null)"` |
| XMLTYPE / INTERVAL / UDT | — | error | Readable error + suggested `CAST(...)` |

### 6.2 Scan Strategy — Defensive Against go-ora (verified 2026-08-07)

> **Verified against Oracle 21c XE with go-ora v2.9.0:**
> - `DatabaseTypeName()` returns **go-ora internal names, not Oracle types** — `VARCHAR2`→`NCHAR`, `TIMESTAMP`→`TimeStampDTY`, `TIMESTAMP WITH TZ`→`TimeStampTZ_DTY`, `CLOB`→`LongVarChar`, `BLOB`→`LongRaw`, `BINARY_FLOAT`→`IBFloat`, `BINARY_DOUBLE`→`IBDouble`. **Do not use it for Oracle-type decisions.**
> - `DecimalSize()` **is accurate** for NUMBER (`prec`/`scale` correctly populated; `NUMBER(10,0)` → 10/0).
> - `ScanType()` is reliable at the **broad** level: `float64` for *all* NUMBERs (even scale=0), `time.Time` for DATE/TIMESTAMP, `string`, `float32`. It cannot separate `int64` from `float64`.
> - **Native values:** NUMBER scanned into `interface{}`/`[]any` arrives as `string` in v2.9.0 — the #533 behavior **still exists**; typed scans (`*int64`, `*float64`, `sql.Null*`) convert correctly.

1. Classify each column by `ScanType()` + `DecimalSize()` (not `DatabaseTypeName()`): `time.Time` → date/time; `float64`/`float32` → numeric; `string` → string; `nil` ScanType → LOB (the value decides: `[]byte` → BLOB/RAW, `string` → CLOB).
2. Select the target scan type per column from the 6.1 table; scan into `sql.NullInt64`, `sql.NullFloat64`, `sql.NullString`, `sql.NullTime`, or `[]byte`.
3. **Never** scan into `[]any` to decide types — NUMBER values arrive as `string` there. Always scan into an explicit typed target; typed scans are reliable regardless of the native-string quirk.
4. `int64`/`float64` decision: use `ColumnType.DecimalSize()` scale when available (accurate); otherwise default to `float64` when ambiguous.
5. Convert time values **to UTC**: TIMESTAMP WITH TZ → UTC; for DATE, fix the session timezone (config `sessionTimeZone`, default `UTC`).

### 6.3 Testability (critical)

`database/sql.Rows` cannot be faked; sqlmock's ColumnType support is weak. Therefore:

- The mapping logic is split into a **pure function**: `([]ColumnInfo, []any) → []*data.Field` (`ColumnInfo` is our own struct carrying `DatabaseTypeName`, `DecimalSize`, `ScanType`, `Name`).
- A **thin adapter** reads from `*sql.Rows` and feeds this pure function.
- Unit tests exercise the pure function with fixtures; a real DB is only used in integration (Section 16).

### 6.4 Character Sets and Encoding (NLS)

**Principle:** the plugin never re-encodes per column and never needs to know the DB's storage charset. The conversion happens **in Oracle on the wire**: go-ora declares its client session charset as **AL32UTF8 (UTF-8)**, and Oracle converts its stored charset → UTF-8 before sending. Therefore every DB charset behaves the same:

- **AL32UTF8** — fully transparent (recommended default).
- **UTF8 (legacy)** — Oracle converts to client AL32UTF8; fine.
- **Single-byte Western (WE8ISO8859P1/P9, WE8MSWIN1252/1254, US7ASCII)** — wire conversion → UTF-8; Turkish `İ ı ş ğ ö ü ç` round-trips correctly.
- **CJK (JA16SJIS, JA16EUC, ZHS16GBK, ZHT16BIG5, KO16MSWIN949)** — same wire conversion.
- **AL16UTF16** (national charset for NCHAR/NVARCHAR2/NCLOB) — driver-level conversion; **verify go-ora NCHAR handling empirically**.
- **Arabic/Greek/Cyrillic (AR8MSWIN1256, EL8ISO8859P7, CL8ISO8859P5)** — same wire conversion.

**The real risk is go-ora, not Oracle.** go-ora claims *"most character sets supported"* and ships a documented escape hatch: `SetStringConverter(db, charset, nCharset)` for unsupported charsets. **Verified 2026-08-07:** Turkish `WE8ISO8859P9` round-trips exactly through go-ora v2.9.0 (parameterized + literal) with no `SetStringConverter` needed. CJK (`ZHS16GBK`), `AL16UTF16` NCHAR and `US7ASCII` remain untested — if one is not covered, use `SetStringConverter` or document it (§21, §16, §17.1).

**Session NLS must be pinned at connect** so user SQL behaves identically on every DB (independent of the host locale):

```sql
ALTER SESSION SET NLS_NUMERIC_CHARACTERS = '.,'
ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD"T"HH24:MI:SS'
ALTER SESSION SET NLS_TIMESTAMP_FORMAT = 'YYYY-MM-DD"T"HH24:MI:SS.FF3'
ALTER SESSION SET NLS_DATE_LANGUAGE = 'AMERICAN'
ALTER SESSION SET NLS_SORT = 'BINARY'
ALTER SESSION SET NLS_COMP = 'BINARY'
```

- `NLS_NUMERIC_CHARACTERS` — decimal separator (a Turkish DB may default to comma).
- `NLS_DATE_FORMAT`/`NLS_TIMESTAMP_FORMAT` — predictable `TO_CHAR`/implicit date strings.
- `NLS_DATE_LANGUAGE='AMERICAN'` — stable month/day names.
- `NLS_SORT='BINARY'`, `NLS_COMP='BINARY'` — predictable `ORDER BY`/comparisons (avoids the Turkish dotless-`ı` collation surprise).

**Additional rules:**
- SQL sent to Oracle is UTF-8; the driver encodes it to the session charset (AL32UTF8), so non-ASCII literals in user SQL (e.g., `WHERE name = 'İstanbul'`) round-trip correctly.
- Undecodable/invalid bytes are handled gracefully (replace with U+FFFD or a readable error); the plugin never crashes.
- End-to-end the pipeline is UTF-8: driver → Go `string` → Grafana frame → frontend JSON → charts/CSV/variables/annotations.
- Note: CHAR/VARCHAR2 column length is chars or bytes depending on `NLS_LENGTH_SEMANTICS`; affects only the autocomplete metadata display (minor).

---

## 7. Frame Generation Algorithm

### 7.1 Inputs

- `format` value: `auto` | `table` | `time_series`.
- `timeColumn`: used if the user explicitly selected it; otherwise automatic.
- Time columns: `DATE`/`TIMESTAMP*`; value columns: numeric + `BOOLEAN`; label candidates: strings.

### 7.2 `auto` Decision

1. Is there a time column? No → **table**.
2. Time + at least 1 value column? Yes → **time_series**; otherwise table.

### 7.3 `time_series` Shaping

1. Select the time column (explicit selection → first time column).
2. Determine value columns (numeric + boolean).
3. **Exactly 1 value column + ≥1 string column** → **long format**: one series per distinct (string label combination); string columns become `data.Labels`; value is a single field.
4. **Otherwise wide format:** time is a single `time.Time` field; each value column is a separate field. String columns are attached as labels to series only if they have a single distinct value, otherwise they are fields.
5. **Cardinality guard (ADR-11):** if distinct series exceed `maxSeries` (default 1000), return an error with a `GROUP BY`/filter suggestion.
6. **Duplicate-time guard:** repeated timestamps within a series cause a Grafana error; the docs state "write your query with `ORDER BY` and unique timestamps". The plugin does not auto-append `ORDER BY` (Postgres/MySQL pattern).
7. Rows with NULL time are skipped in time series; NULL values become gaps in the series.

### 7.4 `table` Shaping

- All columns become fields with types preserved (time also as `time.Time`); NULL is a real null.
- Rows with NULL time are **not** skipped.

### 7.5 General Rules

- **Duplicate column name** → automatic suffix (`col`, `col_1`, `col_2`).
- **Empty result** → empty frame with correct field types (Grafana shows "no data").
- `frame.Meta.ExecutedQueryString` = the raw SQL **before** interpolation (to avoid leaking values, Section 15).
- `frame.Meta.DataTopic` set when needed for annotations (Section 11).
- Optional unit/label mapping: column→unit mapping in config (F13 option).

---

## 8. Macro and Variable Engine (Backend Interpolation)

### 8.1 Architectural Decision (ADR-2)

- The frontend sends the **raw** SQL + query model; it **does not** interpolate.
- The backend uses the `sqlutil` engine — current import path `github.com/grafana/grafana-plugin-sdk-go/data/sqlutil` (moved from the legacy `backend/sqlutil`) — for parsing, longest-match and argument splitting, and defines its own **Oracle macro table**.
- Dashboard variables reach the backend via `DataSourceWithBackend.applyTemplateVariables` (implemented in F3): the hook attaches the query's `scopedVars` (values stringified to strings / arrays of strings; built-ins and ad-hoc variables dropped), which the backend expands together with the macros. **Alert rules never call this hook** — ngalert evaluates server-side with no frontend — so alert queries carry no `scopedVars` and resolve only `$__*` + `TimeRange` (§8.3).
- The frontend "SQL preview" feature is **display-only** (templateSrv + local macro simulation).

> **Why sqlutil's defaults are not enough (verified 2026-08-07 against `data/sqlutil`):** `Interpolate` is **macro-only** — it expands `$__*` macros and never touches `${var}` (the `Query` model has no variables field), so **variable interpolation is entirely ours**. The default `$__timeFilter` produces a Postgres-style string literal (`col >= '...'`); in Oracle this risks NLS-dependent implicit conversion. `$__unixEpochFilter`/`$__unixEpochGroup` are **not** in the defaults; `$__timeGroup` emits `datepart(...)` (T-SQL/Postgres), not Oracle `TRUNC`. Multi-value `IN(...)` expansion is **not** in sqlutil. → Take the engine (parse/longest-match/args), write the macro definitions Oracle-specific.

### 8.2 Macro Expansion Table (Oracle)

| Macro | Expansion (concrete Oracle SQL) | Note |
|---|---|---|
| `$__from` | `<from_epoch_ms>` | number (epoch ms) — preserves current behavior |
| `$__to` | `<to_epoch_ms>` | number (epoch ms) |
| `$__timeFilter(col)` | `col >= TO_DATE('2026-08-07 09:00:00', 'YYYY-MM-DD HH24:MI:SS') AND col <= TO_DATE('2026-08-07 10:00:00', 'YYYY-MM-DD HH24:MI:SS')` | `TO_DATE` + explicit format (ADR-9): NLS-independent and **verified index-friendly on DATE columns**; second precision |
| `$__timeFrom(col)` | `col >= TO_DATE('...', 'YYYY-MM-DD HH24:MI:SS')` | |
| `$__timeTo(col)` | `col <= TO_DATE('...', 'YYYY-MM-DD HH24:MI:SS')` | |
| `$__unixEpochFilter(col)` | `col >= 1754326800 AND col <= 1754330400` | for **epoch-seconds** columns (numeric) |
| `$__unixEpochGroup(col, interval)` | `FLOOR(col / <interval_s>) * <interval_s>` | epoch-seconds column; `<interval_s>` resolved from the interval string |
| `$__interval` | `1m` | `gtime.FormatInterval(IntervalMs)` |
| `$__interval_ms` | `60000` | number |
| `$__interval_s` | `60` | number (helper for Oracle bucketing) |
| `$__time(col)` | `col` | selects the time column in the SELECT list |
| `${var}` | `'value'` | single value; quoted if string, bare if numeric |
| `${var:csv}` | `'a','b'` | multi-value → for `IN (...)` |
| `${var:csv}` (numeric) | `1,2` | unquoted |
| Empty multi-value | `IN (NULL)` | instead of invalid `IN ()`; "no selection = no data" |

**Expansion rules:**

- All time literals are **UTC**, formatted `YYYY-MM-DD HH24:MI:SS` (second precision) — DATE-column friendly and NLS-independent (ADR-9). For TIMESTAMP columns needing milliseconds, a `TO_TIMESTAMP('…', 'YYYY-MM-DD HH24:MI:SS.FF3')` variant can be added; note that any `TIMESTAMP`-literal / `TO_TIMESTAMP` comparison implicitly converts a DATE column to TIMESTAMP and **loses the index** (verified).
- In string values, `'` is escaped as `''` (SQL-injection protection, Section 15).
- If the numeric-ness of a variable is unknown, it expands unquoted when it looks numeric (all digits) according to the config `numericVariables` list, or quoted when ambiguous.

### 8.3 Alerting Context

- No dashboard → no `scopedVars`. The macro engine:
  - Derives `$__from`/`$__to`/`$__timeFilter*` from `query.TimeRange`.
  - For an unresolved variable → **explicit error** (no silent failure).
- `$__interval` can be wrong in the alerting context (see Section 10.4) — hence an alert-specific step/interval derivation.

### 8.4 Interval Derivation

- `IntervalMs` (sent by Grafana from the panel) is the primary source.
- **If `maxDataPoints` is absent or in the alerting context** (Grafana sends `IntervalMs=1000ms`, `maxDataPoints=43200` — verified, §21 row 5) → `interval = max(IntervalMs, range_ms / max(1, maxDataPoints))` = `max(1000ms, range_ms/43200)`; the exact formula is in Section 10.4.

---

## 9. Prometheus Mode (Proxy)

### 9.1 Configuration

`promEnabled`, `promUrl`, `promAuthType` (none/basic/token → secure), `promTlsSkipVerify`. URL validation: **http/https schemes only**; embedded credentials rejected; whitespace stripped.

### 9.2 Endpoints

| Endpoint | Use |
|---|---|
| `GET /api/v1/query_range` | `mode=prom` time series (matrix) |
| `GET /api/v1/query` | instant value (vector) → stat/table |
| `GET /api/v1/label/__name__/values` | metric browser |
| `GET /api/v1/label/<name>/values` | Prometheus query variable (`label_values`) |
| `GET /api/v1/status/buildinfo` | health check |

### 9.3 Matrix → Frame

- `step` derivation: if `maxDataPoints > 0` → `step = max(1, (to-from)/maxDataPoints)`; otherwise `max(1, IntervalMs/1000)`. In the alerting context `maxDataPoints` is used.
- Result is a **wide frame**: a single `time.Time` field + one `*float64` field per series (efficient because query_range timestamps are aligned).
- Series labels (`metric` map) → `data.Labels` per field; `__name__` → series name (legend format applied; default `{{ __name__ }}`).
- Zero series → empty frame.

### 9.4 Vector → Frame

- Single point in time: `time=now` + value + labels → single-row frame (stat/gauge/table).

### 9.5 Errors and Limits

- Prometheus `error`/`errorType` fields mapped to readable messages; HTTP 4xx/5xx distinguished.
- `ctx` cancellation used throughout; client timeout (default 30s).
- Response size: if Prometheus supports a `limit` parameter it is bounded by `maxSeries`; otherwise documented.

---

## 10. Alerting System

### 10.1 Layers

| Layer | Mode | Pattern |
|---|---|---|
| **Layer 1 — Time-series alert** | SQL + PromQL | `$__timeFilter(ts)` + "Format: time_series" → `Reduce(last)` → `Is above(threshold)` |
| **Layer 2 — Row-based alert** | SQL | Query returning ≥1 row when the condition holds → `Reduce(count)` → `Is above(0)` |
| **Layer 3 — Alertmanager (opt.)** | Prometheus | Querying `ALERTS` / Grafana's native Alertmanager DS — outside the core |

### 10.2 Layer 2 Correct Reduce (important)

The "rows > 0" pattern must use **`count`** as the reduce step (not `last` — on a multi-row table the last value is meaningless). **Crucially, the query must return a single scalar row — `SELECT COUNT(*)` — not one row per matching record.** The multi-row variant is broken at the Grafana platform level, verified empirically on 12.4 + 13.1 (2026-08-08):

- A multi-row result with no string (label) columns becomes a **vector of `mathexp.Number`s with identical `nil` labels** — the expression engine's `Reduce` command passes `Number` inputs through **unchanged** (it only reduces `Series`), so `count` never counts them, and the threshold step then errors: *"frame cannot uniquely be identified by its labels: has duplicate results with labels {}"*.
- The **scalar** `SELECT COUNT(*)` returns exactly one Number → `Reduce(count)` → `Is above 0`. A single row yields **1 → firing**; zero matching rows yield **0 → Normal**; an empty table (no rows at all) is `NoData` (health `nodata`).

Working pattern (used in the F5 example templates, §18):

```
SELECT COUNT(*) AS alert_flag
FROM   my_tab
WHERE  used_percent > 85
AND    $__timeFilter(ts);
-> Reduce: count -> Threshold: Is above 0
```

To see *which* rows matched, alert on a time-series aggregate instead (`SELECT ts, used_percent …` with `Format: time_series` → `Reduce(last)`), where the duplicate-label problem cannot occur.

### 10.3 Macro Safety in the Alerting Context

- No dashboard variables → fall back to defaults + explicit error (Section 8.3). Verified by tests.

### 10.4 The `$__interval` / `maxDataPoints` Problem (critical)

Grafana sends `IntervalMs=1000ms` **by default** in alert queries → `$__interval` = `1s` → GROUP BY bucketing breaks, Prometheus `step` becomes 1s (huge responses). **Verified (2026-08-07, Grafana source — §21 row 5):** the alert evaluator's defaults are `maxDataPoints=43200` (12 h at 1 s) and `IntervalMs=1000ms` (`pkg/services/ngalert/models/alert_query.go`), so the 1 s problem is real for any alert range ≤ 12 h. Solution:

- Backend interval formula: `interval_ms = max(IntervalMs, range_ms / max(1, maxDataPointsOrDefault))` — with the verified defaults this evaluates to `max(1000ms, range_ms/43200)` = **1 s** for ranges ≤ 12 h (a 24 h range → 2 s).
- This formula is the single shared place for both SQL `$__interval` and Prometheus `step`; the alert-specific derivation (§8.4) uses it so bucketing never silently defaults to 1 s for large tables.

### 10.5 Error Behavior

- If an alert query returns an error, the rule's **execution-error state** follows `execErrState` (`Error` by default): the rule enters the Error state internally and fires no alert. **Verified** on 12.4 + 13.1 (2026-08-08).
- Empty result → the rule's **no-data state** follows `noDataState` (`NoData` by default).
- **How the prometheus-compat API surfaces these** (`GET /api/prometheus/grafana/api/v1/rules`): the public `state` field maps Error and NoData to the single string `"inactive"` (only Alerting/Pending/Recovering have distinct states), so the rule **`health`** field is what distinguishes them — `health: "error"` for a failing query, `health: "nodata"` for no data, `health: "ok"` for a normal evaluation. A query that fails against the datasource (e.g. missing table) therefore appears as `state="inactive" + health="error"`, and with `execErrState=Alerting` as `state="firing"`. Alert-rule verification must assert on `(state, health)` jointly, never `state` alone (the F5 check script does exactly this).

### 10.6 plugin.json Flags

- Both `"alerting": true` and `"annotations": true` are declared (ADR-12). Because the `alerting` flag's behavior varies by version, it is verified empirically in F0 (Section 21).

---

## 11. Annotations

- `plugin.json` → `"annotations": true`.
- `AnnotationQueryHandler` → returns a single frame:
  - Fields: `time` (`*time.Time`), `title` (string), `text` (string), `tags` (comma-separated string).
  - `frame.Meta.DataTopic = data.DataTopicAnnotations` (required for Grafana to treat it as annotations).
- "Add annotation" in the panel → default template:

```sql
SELECT ts, title, text, tags
FROM   events
WHERE  $__timeFilter(ts)
```

---

## 12. Command Execution and Operations

Operational tasks (start/stop/enable/disable scheduler jobs, call stored procedures/functions) cannot go through the normal query pipeline — Grafana re-executes queries on panel refresh, alert evaluation and variable changes, so a "stop job" action must be an **explicit, one-shot user action**. Commands are therefore a separate path (ADR-13).

### 12.1 Flow

```
Query editor  ── "Run command" button  ──>  datasource.postResource('command', { sql, confirm })
                                                │
                                backend.CallResourceHandler
                                                │
                            1. allowlist check (12.3)
                            2. db.ExecContext(sql)        (anonymous PL/SQL block)
                            3. response: { ok, message }  (no data frame)
```

- The button is next to "Run query" in the query editor; the panel refresh / alert machinery never touches it.
- The command box accepts an **anonymous PL/SQL block** (SQL*Plus `EXEC` syntax is not valid over go-ora):
  - `BEGIN DBMS_SCHEDULER.STOP_JOB('MY_JOB'); END;`
  - `BEGIN DBMS_SCHEDULER.ENABLE('MY_JOB'); END;`
  - `BEGIN DBMS_SCHEDULER.DISABLE('MY_JOB'); END;`
  - `BEGIN DBMS_SCHEDULER.START_JOB('MY_JOB'); END;`
  - `BEGIN pkg.my_proc('ABC', 42); END;` (procedure) / `SELECT pkg.my_func(...) FROM DUAL` (function)
- Parameters in v1 are typed as **literals** inside the block (no bind-parameter UI); bindings are a later phase.
- Result: structured `{ ok, message, rowsAffected? }` rendered inline; errors shown readably (ORA mapping reused from §13).

### 12.2 Why not a query with a flag

A mutation flag on a `DataQuery` would be sent through `QueryData`, which Grafana re-fires on refresh, alerting and variable changes — unacceptable for side-effecting operations. The resource-handler path keeps commands **out of the query pipeline entirely** (no `$__timeFilter`, no frames, no alerting, no annotations, no dashboard re-run).

### 12.3 Allowlist (security core, ADR-13)

- The command text must match one of the allowlisted shapes **before** execution:
  - `BEGIN` … `END;` blocks whose inner statement references only `DBMS_SCHEDULER` (all procedures) or a plain procedure call; **key-value `DBMS_SCHEDULER.SET_ATTRIBUTE` is allowed**.
  - `SELECT <expr> FROM DUAL` (function calls returning a value).
- **Rejected:** DDL/DML outside the allowlist, `DROP`, `TRUNCATE`, multi-statement blocks with side effects beyond the allowlist, anything starting with a non-allowlisted keyword.
- A **confirmation dialog** precedes execution (config-toggleable); every command is **audit-logged** (Grafana `log`, fields: datasource UID, user, allowlist pass/fail, statement — never bind values).
- Accounts: the same Oracle user is used for queries and commands; the query path keeps its read-only guard, the command path is restricted by the allowlist (single-account model). Documentation recommends minimal extra grants (`MANAGE SCHEDULER` or per-job `ALTER`/`EXECUTE` on `DBMS_SCHEDULER`; `EXECUTE` on the procedures to call).
- `maxRows`/`maxSeries` do not apply; a separate `commandTimeoutSec` (default 10s) bounds execution.

### 12.4 Status reads (complementary)

Job state is read with normal queries (`SELECT * FROM user_scheduler_jobs` / `dba_scheduler_jobs` / `ALL_SCHEDULER_JOB_RUN_DETAILS`), so the workflow is **read status via query → act via button**.

---

## 13. Health Check

| Check | Method | Note |
|---|---|---|
| Oracle connection | `SELECT 1 FROM DUAL` | fast; health is called often, fail after 5s |
| SELECT privilege | optional sample query on a target table (config) | optional |
| Oracle version | `SELECT banner FROM V$VERSION WHERE ROWNUM = 1` | for version-dependent feature warnings |
| Prometheus (opt.) | `GET /api/v1/status/buildinfo` | **must NOT hard-fail health if not configured** |
| Message | JSON detail (`oracle.ok`, `prometheus.ok`, versions) | "Test" screen in ConfigEditor |

---

## 14. Frontend Design

### 14.1 QueryEditor

```
[ Oracle (SQL) ]  [ Prometheus (PromQL) ]         <- mode tab-button
----------------------------------------------------------------
SQL mode:
  SQL editor (@grafana/experimental SQLEditor, syntax + autocomplete)
  Format as: [Auto | Time series | Table]   Time column: [auto | dropdown]
  Preview: interpolated SQL (read-only, display only)
Prometheus mode:
  Metric browser: [oracledb_tablespace_used_percent | v] (prefix filter)
  PromQL editor (+ function help)
  Legend format: [{{ __name__ }}]
```

- The mode selection is stored **per query**; the DS default mode is in config.
- **Editor implementation (F4):** the `@grafana/experimental` SQLEditor named in the
  sketch is realized via **`@grafana/plugin-ui`**, the package that succeeded it for
  Grafana 13 — `@grafana/experimental@2.1.6` peerDependencies cap at
  `@grafana/ui ^10.4 || ^11.0`, incompatible with the pinned 13.1.0 SDK (and `npm ci`
  would fail). `@grafana/plugin-ui@0.17.3` (peer `^13.0.0`, used by the current Athena
  datasource on 13.1) exports the same `SQLEditor` + `LanguageDefinition` API.
- **Deferred:** the read-only "Preview: interpolated SQL" is not in F4. ADR-2 owns
  interpolation server-side, so a faithful preview needs a TS mirror of the Go engine
  (divergence risk) or a backend resource — a natural fit with F8 variables / F15
  commands, which already use `CallResourceHandler`.
- **Run command** button (SQL mode): posts the current SQL as an anonymous PL/SQL command via `postResource('command', …)` (§12). A deliberate, one-shot action — never re-run by refresh/alerting; result/error shown inline.

### 14.2 ConfigEditor

```
Oracle:  ConnString / host / port / service / sid
         user / password (secure) / TLS options
         Pool: maxOpen/maxIdle/lifetime  |  Timeout  |  maxRows  |  maxSeries
----------------------------------------------------------------
Prometheus  [ x ] "Use Prometheus" (optional)
         URL, Basic auth (secure), Bearer token (secure), TLS verify
----------------------------------------------------------------
General:  default mode, default format, large-NUMBER policy,
          column->unit mapping (opt.)
```

### 14.3 Autocomplete (metadata)

- Sources: `ALL_TAB_COLUMNS`/`ALL_TAB_COMMENTS` + `ALL_TAB_PRIVS` (tables the user can access).
- **If permission is missing**, disables silently; no error shown.
- The time-column dropdown comes from the same metadata (column types are unknowable without running a query).
- Metadata results are cached with a short TTL (e.g., 5 min); the table list is bounded.

---

## 15. Security Design

| Topic | Measure |
|---|---|
| Passwords/tokens | `secureJsonData` (Grafana encrypts); decrypted in the backend |
| SQL injection (variables) | single-quote escaping (`''`) + numeric validation in the backend (Section 8.2) |
| DML (query path) | first-keyword guard + read-only user recommended (ADR-7); single Oracle account (queries + commands) with minimal extra grants documented |
| Command execution | backend **allowlist** only — scheduler ops + `BEGIN…END` blocks (§12.3); commands never enter the query pipeline; confirmation dialog; every command audit-logged (ADR-13) |
| Log hygiene | interpolated (value-bearing) SQL is **never** logged; raw SQL logged with placeholders; `frame.Meta.ExecutedQueryString` is raw SQL |
| SSRF | Prom URL comes from admin config only; http/https schemes; embedded credentials rejected |
| TLS | DS-level skip-verify/CA; Oracle TLS/Wallet options (F14) |
| Packaging | signed plugin (Grafana sign) or local unsigned flag |

---

## 16. Test Strategy

| Layer | Scope | Method | Where |
|---|---|---|---|
| **Unit (Go)** | Type mapping (pure function), macro expansions (table-driven), Prometheus matrix/vector→frame (fixture JSON), interval/step derivation, variable escaping | `go test` | CI (every commit) |
| **Integration (Go)** | Real Oracle XE: real SQL, types, NULL, health scenarios — **18c (oldest) + 23c (newest)** + **non-UTF8 charset DB** (Turkish `WE8ISO8859P9`) | docker-compose Oracle + test script | CI on **schedule**, on demand locally (Oracle ~2GB RAM, slow startup) |
| **Integration (commands)** | Real Oracle XE: create/enable/disable/start/stop a scheduler job via `CallResourceHandler`; procedure/function call; allowlist rejects DDL | docker-compose Oracle | CI schedule |
| **Integration (Prom)** | Prometheus + exporter: 2–3 min scrape → `query_range` | docker-compose | CI schedule |
| **e2e (UI)** | Grafana 12.4 + 13.1: panel, mode switching, alert rule creation (Grafana API rule injection) | Playwright | CI matrix |
| **Manual** | query variables, multi-value, annotations, large result/cancel | runbook | — |

**Testability commitment:** the mapping/macro/frame logic is unit-testable without a DB (Section 6.3). `database/sql.Rows` is never tested directly.

---

## 17. Dev Environment and CI

### 17.1 docker-compose (profiles)

```yaml
services:
  grafana12:  { image: grafana/grafana-oss:12.4,    profiles: ["grafana12"] }
  grafana13:  { image: grafana/grafana-enterprise:13.1, profiles: ["grafana13"] }
  oracle18:   { image: gvenzl/oracle-xe:18c-slim,   profiles: ["oracle18"] }
  oracle21:   { image: gvenzl/oracle-xe:21-slim,    profiles: ["oracle21"] }
  oracle23:   { image: gvenzl/oracle-xe:23c-slim,   profiles: ["oracle23"] }
  oracle_tr:  { image: gvenzl/oracle-xe:21-slim,    profiles: ["oracle_tr"],
                environment: ["ORACLE_CHARACTERSET=WE8ISO8859P9"] }
  prometheus: { image: prom/prometheus:v3.5.0,      profiles: ["prom"] }
  exporter:   { image: oracle-db-appdev-monitoring, profiles: ["prom"] }
```

- The plugin build is volume-mounted into the container + `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS`.
- Oracle: `gvenzl` images simplify license acceptance; a startup script loads demo data.
- The `oracle_tr` profile boots a Turkish-charset DB (`WE8ISO8859P9`) to test non-UTF8 round-trips (§6.4).

**Implemented at F0 (2026-08-07):** the matrix lives in the repo-root
`docker-compose.yaml` (profiles `grafana12`/`grafana13`/`oracle18`/`oracle21`/
`oracle23`/`oracle_tr`/`prom`) alongside the create-plugin dev-Grafana service
(the `npm run server` loop); Prometheus config is `devenv/prometheus.yml`. The
dev matrix binds 127.0.0.1 ports 3001/3002 (Grafana), 1523–1526 (Oracle) and
9090 (Prometheus) so it never collides with the `verification/` harness, which
owns 1521/1522. **Note (2026-08-07):** `grafana/grafana-oss` Docker tags stop at
**13.0.2** (last update 2026-06-02); the `grafana13` service therefore uses
`grafana/grafana-enterprise:13.1.0` — the same OSS binary, free to pull, and the
same image the create-plugin dev service already defaults to. Pinned patch
tags are used (12.4.0 / 13.1.0).

### 17.2 CI

- **Workflow 1 (unit):** `go test` + frontend Jest — every commit.
- **Workflow 2 (integration):** schedule — Oracle + Prometheus compose; results report.
- **Workflow 3 (e2e matrix):** Grafana 12.4 and 13.1 — Playwright + alert-rule API test.
- **Workflow 4 (release):** tag → `mage build` → `.tar.gz` + (opt.) sign.

---

## 18. Implementation Roadmap — 16 Phases

Effort is in person-days (pg) for 1 developer. Dependencies and critical path: **F2 and F3** (types + macros) are the backbone of the whole SQL path; F6/F7 follow. The empirical checks in F0 (Section 21) feed F1–F3 directly.

### Wave A — Oracle Core

**F0 — Scaffold, version matrix and dev environment** · 3 pg
- Scaffold with `@grafana/create-plugin`; plugin.json (`alerting: true`, `annotations: true`, min Grafana, backend: true).
- go-ora and SDK version pins; **verification points:** `ColumnTypes().DatabaseTypeName()` output, `alerting` flag behavior, macro literals working on Oracle (Section 21).
- docker-compose profiles (17.1); mage build/run loop; CI unit workflow.
- **DoD:** An empty datasource installs on Grafana 12.4 and 13.1 and passes the health "Test"; CI green.
- **Status (2026-08-07):** ✅ **done.** Scaffold at repo root (plugin ID `emreatayy-oracle-datasource`, `pkg/` Go backend, `src/` frontend), version pins (§2.5), §17.1 compose profiles, `ci.yml` unit workflow, and check #3 closed (§21 row 3). Health "Test" + alert-rule acceptance verified on 12.4.0 and 13.1.0 (enterprise image — `grafana-oss` has no 13.1 tag).

**F1 — Oracle connection core** · 3 pg
- ConnString/fields/SID parsing; pool parameters; `context` cancellation; query timeout; read-only guard.
- Error-parsing core (ORA pattern).
- **DoD:** Config changes dispose the instance; a long query is cancelled when the panel closes (integration test).
- **Status (2026-08-08):** ✅ **done.** `pkg/oracle` (DSN, ORA error parsing, session params) + pooled `sql.DB` via per-instance `go_ora.NewConnector` (avoids the package-singleton `sessionParam` leak across datasources). Session `TIME_ZONE` pinned UTC through `AddSessionParam` (go-ora hardcodes the host offset otherwise). Read-only guard passes only `SELECT`/`WITH` (comments skipped). **Verified on real Oracle 21c:** health banner, 6/6 matrix checks on Grafana 12.4.0 + 13.1.0, and the DoD — a heavy `CONNECT BY` query returns ORA-01013 within ~360 ms of ctx cancel (panel close). Integration suite `ORACLE_TEST_DSN=… go test ./pkg/plugin/ -run Integration` all green; unit + lint clean. NUMBER still arrives as strings (#533) — the typed mapper is F2.

**F2 — Type mapping + frame generation** · 5 pg (critical path)
- Pure `convert` package (6.3); the 6.1 map; the 7 algorithm; wide/long; maxSeries; duplicate columns; empty frame.
- `largeNumberMode` (ADR-8).
- **DoD:** Table + time-series panels work typed from Oracle; NULL, large NUMBER, and duplicate-time scenarios are unit+integration tested.
- **Status (2026-08-08):** ✅ **done.** `pkg/convert` (pure, DB-free: `Classify` from `ScanType`+`DecimalSize`, typed `sql.Null*` scan targets, §7 shaping). `query.go` now scans typed and calls `BuildFrame`; go-ora #533 (NUMBER arrives as `string` in `[]any`) is fully worked around — every cell lands in an explicit typed field. **Verified on Oracle 21c:** `TestIntegration_TypedColumns` — NUMBER(10)→`*int64`, NUMBER(10,2)→`*float64`, NULL preserved, NUMBER(20) full precision as `*string` in table / `*float64` in time series (ADR-8), DATE→`*time.Time`, CLOB→`*string`, duplicate time kept, NULL-time rows skipped. Unit (pure fixtures) + lint clean; integration suite all green. Frontend: Format `<Select>` (auto/table/time_series) in the query editor + `largeNumberMode` in config (Auto=ADR-8 per-format).

**F3 — Macro and variable engine** · 4 pg (critical path)
- `sqlutil` engine + Oracle macro table (8.2); interval derivation (8.4); alerting fallback (8.3); multi-value `IN(...)` + escaping.
- **DoD:** `$__timeFilter`, `$__unixEpochGroup`, `${var:csv}`, empty selection→`IN (NULL)` table-driven tested; alerting-context simulation works.
- **Status (2026-08-08):** ✅ **done.** `pkg/interpolate` (pure, DB-free): macro-first then single-pass variable expansion over the `sqlutil` engine, with fail-closed guards — `${var}` inside a macro argument, an unknown/lowercase `$__` macro, and an unterminated `${` all error (never silent SQL). Oracle macro table shadows the T-SQL `timeGroup` and empty `table`/`column` defaults with explicit errors; ADR-9 `TO_DATE('…','YYYY-MM-DD HH24:MI:SS')` literals; `$__interval` via the §10.4 derivation (`max(IntervalMs, range_ms/maxDataPoints)` — alert defaults 1s/2s verified). Variables: `:csv` (empty → `NULL` so `IN (NULL)` = "no selection"), `:singlequote`, strict numeric-literal gate for bare emission (numericVariables is a hint, not a trust grant — `1e5` and `5) OR 1=1 --` are never bare), `'`→`''` escaping, `$__all` sentinel → error. `query.go` interpolates before `ensureReady`, keeps the raw SQL in `ExecutedQueryString`, and re-runs the read-only guard on the interpolated form (§15). Frontend `applyTemplateVariables` attaches stringified `scopedVars` (alert queries skip it — §8.3). **Verified:** 30+ table-driven unit cases (incl. the alerting-context DoD simulation) + `TestIntegration_Macros` on Oracle 21c — `$__timeFilter` + `${host:csv}` (`[42]` / `[8,42]` / empty→0 rows), `$__unixEpochGroup(id,10)`→`[0,0,0,40]`, unresolved `${missing}` errors, raw `ExecutedQueryString`; full integration suite + unit + lint clean.

**F4 — SQL query editor** · 3 pg
- Mode-tab infrastructure; SQLEditor (via `@grafana/plugin-ui` — see §14.1); Format-as; Time-column dropdown (wired to F9 metadata, static first).
- **DoD:** SQL panel renders in both formats; mode stored per query.
- **Status (2026-08-08):** ✅ **done.** Mode-tab infrastructure (`@grafana/ui` `Tab`s): `Oracle (SQL)` | `Prometheus (PromQL)`, stored **per query** (`query.mode`, DS `defaultMode` in config); the Prometheus tab is a placeholder until F6 and the backend **rejects** a saved `mode:"prometheus"` query with a clear error before any connection (fail closed, never executed as SQL). SQL editor is the **`@grafana/plugin-ui`** `SQLEditor` with an Oracle monarch language (`src/language/oracle.ts`, built on `grafanaStandardSQLLanguage` + Oracle keywords/functions/`||`); autocomplete (tables/columns from metadata) is F9. Format-as kept; **Time-column** dropdown (Auto + custom text via `allowCustomValue`; metadata list in F9), already honored by `convert.Options.TimeColumn`. **DoD met** — SQL panel renders in both formats and the mode is stored per query; verified on Grafana 12.4 + 13.1 (plugin check 6/6) and the query-editor e2e specs.

**F5 — Alerting (SQL, basic)** · 3 pg
- Layer 1 + Layer 2 patterns; `Reduce(count)`; the interval/maxDataPoints formula (10.4); error state.
- **DoD:** Time-series and "rows>0" alert rules work on Grafana 12.4 + 13.1 (e2e).
- **Status (2026-08-08):** ✅ **done.** **Core bug fixed:** time-series value columns are now **float64** (`convert.timeSeriesValueField` coerces int/bool in time-series format) — Grafana's alerting SSE/dataplane readers previously rejected `*int64` columns ("no float64 value column found in frame"), breaking every Layer 1 rule. Layer 2 verified to require a **scalar** `SELECT COUNT(*)` (the report's earlier multi-row pattern errors with duplicate labels — see §10.2, documented). Interval formula (§10.4) already feeds `$__interval`; error/NoData semantics verified against the prometheus-compat API (§10.5). **DoD met — verified end-to-end on Grafana 12.4.0 (:3001) and 13.1.0 (:3002), 6/6 alert scenarios** (L1 firing/normal, L2 firing/normal/nodata, err-sql) via the committed `verification/alert_check.py` and example rules in `examples/alerting/`.

### Wave B — Prometheus

**F6 — Prometheus proxy (backend)** · 4 pg
- Config section (toggle, auth, TLS); URL validation; query/query_range; step derivation; matrix/vector→frame; error types; timeout.
- **DoD:** Fixture + real Prometheus integration: correct frames in both modes; credentials never logged.

**F7 — Prometheus query editor + metric browser** · 3 pg
- PromQL editor; metric browser (`label/__name__/values`); legend format.
- **DoD:** Selecting from the metric browser → PromQL → panel + alert.

### Wave C — Data and UX

**F8 — Variables** · 3 pg
- Query variables (SQL single column + Prometheus `label_values`); multi-value `IN(...)` (core in F3; DS integration here); empty-value behavior.
- **DoD:** Dropdown variable + multi-select dashboard scenarios e2e.

**F9 — Annotations + autocomplete** · 4 pg
- Annotation handler + frame (11); metadata service (14.3, cache + silent disable without permissions); feeding the time-column dropdown.
- **DoD:** Annotations are marked on the panel; editor completion and time-column list work.

**F10 — Health enrichment + error resolution** · 2 pg
- The Section 13 table; ORA-#### → readable English message map; ConfigEditor "Test" screen JSON.
- **DoD:** Clear messages for bad password/URL/connection scenarios; health green without Prometheus config.

**F15 — Command execution (operations UI)** · 3 pg
- "Run command" button (query editor) → `postResource('command', …)` → backend `CallResourceHandler`; allowlist (12.3); `ExecContext` for anonymous PL/SQL blocks; confirmation dialog; audit log; `commandTimeoutSec`.
- **DoD:** Start/stop/enable/disable a scheduler job and call a procedure from the query editor; allowlist rejects DDL; e2e on 12.4 + 13.1.

### Wave D — Maturity and Release

**F11 — Security, log hygiene, self-metrics** · 2 pg
- Section 15 audit; `oracle_datasource_query_duration`, `errors_total`, `query_total` (labels: DS uid, mode); log levels.
- **DoD:** Secure scan; no values/passwords in logs; metrics visible in Grafana.

**F12 — Performance, limits, streaming, caching** · 3 pg
- `maxRows` (stop reading at N), row streaming, metadata cache TTL, Prometheus response size, pool fine-tuning.
- **DoD:** 100k+ rows, cancellation, and pool behavior under load measured.

**F13 — Documentation, templates, packaging, e2e** · 4 pg
- README (installation, read-only user, version matrix, exporter guide); alert/panel example templates; `plugin-validator`; `.tar.gz` + sign; full e2e.
- **DoD:** Release-ready package; every example in the docs works.

**F14 — Ops/optional** · 3–5 pg
- TLS/Wallet options, TNS names; Alertmanager `ALERTS` view (Layer 3); (opt.) exporter orchestration/health (Option D); column→unit automation.
- **DoD:** All user stories (Section 3.2 of v1) covered; options clearly marked.

### Summary

| Wave | Phases | Effort |
|---|---|---|
| A — Oracle Core | F0–F5 | ~21 pg |
| B — Prometheus | F6–F7 | ~7 pg |
| C — Data & UX | F8–F10, F15 | ~12 pg |
| D — Maturity & Release | F11–F14 | ~12–14 pg |
| **Total** | **F0–F15 (16 phases)** | **~52–54 pg** |

**Critical path:** F0 → F2 → F3 → F5 (SQL time series + alerting, i.e., the user's primary complaint) and in parallel F0 → F6 (Prometheus). F1, F4, F7–F10 are partly parallelizable; F11–F14 run in the background. F15 (commands) depends on F1 + F4 and can follow F5.

---

## 19. Risks and Mitigations

| Risk | Assessment / Mitigation |
|---|---|
| go-ora corner types (XMLTYPE, INTERVAL, UDT) and version regressions (#533) | Safe defaults; readable error + `CAST` suggestion for unsupported types; pin + verify in F0 (Section 21) |
| `ColumnTypes().DatabaseTypeName()` empty/different | Scan-target + value-based fallback (6.2); empirical verification in F0 |
| Live SQL hits Oracle on every panel | Bound volume with `$__interval`/format-as; recommend Prometheus mode for high-frequency scenarios; `maxRows` default 100k |
| `$__interval`=1s problem in the alerting context | The 10.4 formula (maxDataPoints-based step/interval) + test |
| PromQL/SQL semantic difference (confusion) | Show example queries and help text on mode switch |
| Grafana SDK/frontend package breakage | Pinning + CI matrix (12.4/13.1) + e2e dogfooding |
| Oracle container weight (CI) | Unit in CI, integration on schedule (15/16) |
| `alerting: true` flag behavior varies by version | Declare both flags (ADR-12) + empirical verification in F0 |
| Multi-instance / TNS / Wallet complexity | Planned for F14; single-connection pattern first |
| Alert noise | Threshold/again-interval example templates (F13) |
| go-ora LOB "streaming" limited | Inline reads for large CLOB; streaming need measured in F12 |
| `maxRows` cannot inject LIMIT into arbitrary SQL | Stop reading rows at N (F12); optional `ROWNUM` wrap documented |
| Non-UTF8 DB charsets / go-ora conversion gaps | Session charset = AL32UTF8 (Oracle converts on the wire); round-trip test in F0; `SetStringConverter` escape hatch; integration test with a Turkish-charset DB (§6.4) |
| Host-locale leakage (timezone/NLS) | Pin `sessionTimeZone=UTC` + session NLS at connect; no host env-var dependency (§5.4) |
| Backend CGO accident breaks cross-compilation | Keep backend CGO-free; CI builds the full GOOS/GOARCH matrix (§5.4) |
| Accidental destructive command / misuse | Confirmation dialog + backend allowlist (§12.3) + audit log; commands never auto-re-run on refresh/alerting |

---

## 20. Distribution and Release

- `mage build` → `.tar.gz`; extract to the plugins folder; `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=<plugin-id>` (dev) or **Grafana plugin sign** (release).
- Docker-compose demo environment (17.1); README installation guide (Grafana + Oracle + Prometheus + exporter).
- CI: unit + integration (schedule) + e2e matrix + release (tag → tar.gz).
- **Version support policy:** the support set is updated at every Grafana major release; an EOL line moves to "not supported" in the docs.

---

## 21. Assumptions to Verify and Open Decisions

Empirical checks run **2026-08-07** against a live Oracle 21c XE (go-ora v2.9.0), Grafana 12.4/13.1, and the F0-built plugin. **8 of 8 resolved.**

| # | Check | Affects |
|---|---|---|
| 1 | **✅ Verified (2026-08-07):** `DatabaseTypeName()` returns go-ora internal names (`VARCHAR2`→`NCHAR`, `TIMESTAMP`→`TimeStampDTY`, `CLOB`→`LongVarChar`, `BLOB`→`LongRaw`, `BINARY_FLOAT`→`IBFloat`); `DecimalSize()` is accurate; `ScanType()` is reliable at the broad level | F2 type mapping + fallback (6.2) |
| 2 | **✅ Verified (2026-08-07):** #533 persists in v2.9.0 — NUMBER values scan as `string` into `interface{}`, but typed scans (`*int64`, `*float64`) work correctly | 6.2 scan strategy |
| 3 | **✅ Verified (2026-08-07, F0-built plugin on Grafana 12.4.0 + 13.1.0):** the datasource loads unsigned (plugin `enabled=true`), the health "Test" returns SUCCESS, and the rules API **accepts** an alert rule whose query references the datasource type (`/api/v1/provisioning/alert-rules`, uid `efugrzg0mp2pse`). Grafana echoed `maxDataPoints=43200`/`IntervalMs=1000` (matches row 5). Caveat: `grafana-oss` Docker tags stop at **13.0.2**; 13.1.x was verified on `grafana-enterprise:13.1.0` (same OSS binary) | ADR-12 |
| 4 | **✅ Verified (2026-08-07, ADR-9 revised):** ANSI `TIMESTAMP` literal → FULL SCAN on DATE columns; `TO_DATE('…','YYYY-MM-DD HH24:MI:SS')` → INDEX RANGE SCAN and NLS-independent | ADR-9 / 8.2 |
| 5 | **✅ Verified (2026-08-07, Grafana source `pkg/services/ngalert`):** alert evaluator defaults `maxDataPoints=43200` (12 h at 1 s) and `IntervalMs=1000ms`; both are passed into the plugin's `QueryDataRequest` → §10.4 formula uses 43200 as the default | 10.4 formula |
| 6 | **✅ Verified (2026-08-07, `data/sqlutil`):** sqlutil has **no** variable interpolation — `Interpolate` is macro-only and the `Query` model has no variables field (`WithVariables` does not exist). Multi-value `IN(...)` + quoting + `${var:csv}` are built by us (§8.2). | 8.2 / F3 |
| 7 | **✅ Verified (2026-08-07, `WE8ISO8859P9`):** go-ora round-trips Turkish text exactly (parameterized insert + read-back + literal match; no U+FFFD). `SetStringConverter` **not** needed for this charset. CJK (`ZHS16GBK`) still untested. | 6.4 |
| 8 | **✅ Verified (2026-08-07):** go-ora `ExecContext` runs anonymous `BEGIN…END` reliably — `DBMS_SCHEDULER.ENABLE`/`DISABLE`, job-state read-back, procedure call all work | 12 |
| 9 | **✅ Verified (2026-08-08, F5 end-to-end on Grafana 12.4.0 :3001 + 13.1.0 :3002, 7/7 scenarios via `verification/alert_check.py`):** (a) a time-series rule with `Reduce(last)` fires — proof that time-series value columns are now float64 (the `*int64` frame was rejected by alerting SSE); (b) **Layer 2 requires a scalar `SELECT COUNT(*)`** — a multi-row table (`SELECT 1 AS alert_flag …`) errors with duplicate labels `{}` in the threshold step; `COUNT(*)` always returns one row (0 over an empty scan) → Normal, never NoData; NoData needs a row-returning query with zero matches (`SELECT 1 … WHERE no match`); an **empty time-series** frame is NOT NoData (`Reduce(last)` = NaN → threshold false → Normal); (c) a broken query with `execErrState=Error` shows `state="inactive" + health="error"` and with `execErrState=Alerting` shows `state="firing"` — the prometheus-compat API collapses Error/NoData into "inactive", so verification must assert `(state, health)` jointly | 10.2 / 10.5 / F5 |

**Open decisions (with recommended defaults):**

- `sessionTimeZone` default: `UTC` (recommended) — DATE/TIMESTAMP conversion follows it.
- `maxRows` default: `100_000` (recommended); `maxSeries`: `1000`.
- `largeNumberMode` default: `string` for table, `float` for time_series (ADR-8).
- Plugin ID: **finalized in F0** as `emreatayy-oracle-datasource` (`<org>-oracle-datasource` format, §18 F0).

---

## 22. References

- Reference project: https://github.com/albertowd/oracle-grafana (Apache-2.0)
- go-ora version regression: https://github.com/sijms/go-ora/issues/533
- go-ora type mapping (DeepWiki): https://deepwiki.com/sijms/go-ora/2.4-result-sets-and-dataset
- Grafana SDK `data/sqlutil`: https://pkg.go.dev/github.com/grafana/grafana-plugin-sdk-go/data/sqlutil
- sqlutil default macros: https://github.com/grafana/grafana-plugin-sdk-go/blob/main/data/sqlutil/macros.go
- Grafana versions/EOL: https://endoflife.date/grafana
- Grafana release notes: https://grafana.com/docs/grafana/latest/whatsnew/
- Oracle versions/EOL: https://endoflife.date/oracle-database
- Prometheus release cycle: https://prometheus.io/docs/introduction/release-cycle/
- Prometheus 3.x migration: https://prometheus.io/docs/prometheus/latest/migration/
- gvenzl/oracle-xe (dev image): https://hub.docker.com/r/gvenzl/oracle-xe
- Official Oracle exporter: `oracle/oracle-db-appdev-monitoring`

# Grafana Oracle Connector

[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Grafana](https://img.shields.io/badge/Grafana-%E2%89%A512.3-F46800.svg)](https://grafana.com)
[![Oracle](https://img.shields.io/badge/Oracle-%E2%89%A512c-red.svg)](https://www.oracle.com/database/)
[![Status](https://img.shields.io/badge/status-F1%20done-brightgreen.svg)](#status)
[![CI](https://github.com/emreatayy/Grafana_Oracle_Connector/actions/workflows/ci.yml/badge.svg)](.github/workflows/ci.yml)

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
- [Architecture](#architecture)
- [Version Support](#version-support)
- [Repository Structure](#repository-structure)
- [Getting Started (Development)](#getting-started-development)
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
| Empirical verification | ✅ **8 of 8** design assumptions verified (Oracle 21c XE, go-ora v2.9.0, Grafana 12.4/13.1) — see `verification/` |
| Implementation | ✅ **F1 done** (Oracle connection core: pooled connection, session timezone pinning, ctx-cancelled queries, read-only guard, ORA error parsing) — next: F2 (type mapping) |

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
| [`docs/project-report.md`](docs/project-report.md) | **Master design & implementation report (v2)** — version matrix, architecture + ADRs, type mapping, macros, Prometheus mode, alerting, command execution, security, test plan, 16-phase roadmap. |
| [`docs/release-and-packaging.md`](docs/release-and-packaging.md) | Distribution model, build/signing pipeline, versioning, and release workflow plan. |
| [`verification/README.md`](verification/README.md) | How to build and run the empirical verification harness. |

---

## Roadmap

16 implementation phases in 4 waves (~52–54 person-days). **F1 is done**
(scaffold + Oracle connection core: pooled connection, session timezone
pinning, ctx-cancelled queries, read-only guard, ORA error parsing). The
critical path going forward is **F2 → F3 → F5** (typed time series + alerting)
and in parallel **F6** (Prometheus proxy).

| Wave | Phases | Scope | Effort |
|---|---|---|---|
| **A — Oracle Core** | F0–F5 | Scaffold, connection core, type mapping, macro engine, query editor, basic alerting | ~21 pg |
| **B — Prometheus** | F6–F7 | Proxy backend, PromQL editor + metric browser | ~7 pg |
| **C — Data & UX** | F8–F10, F15 | Variables, annotations + autocomplete, health/error UX, command execution UI | ~12 pg |
| **D — Maturity & Release** | F11–F14 | Security/self-metrics, performance/limits, docs + packaging + e2e, ops/optional | ~12–14 pg |

**F15 (command execution UI)** is part of Wave C — it depends on F1 + F4 and is
the feature that makes the plugin operationally useful (scheduler job control,
procedure calls). Full phase-by-phase detail with acceptance criteria: report
§18.

---

## Release & Packaging

The distribution plan — build → sign → GitHub Release → Grafana catalog — is
documented in [`docs/release-and-packaging.md`](docs/release-and-packaging.md).

The tl;dr: a Grafana data source plugin is distributed as a **signed plugin
archive** (`.zip` / `.tar.gz`) produced by the `mage` build, not as an npm or
container package. Release automation is created in **F13** when the plugin
builds; the workflow files will use the standard Grafana plugin release action
and require a signing API key stored as a GitHub secret.

---

## Contributing

Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) — it covers the design-first
workflow (report → ADR → verification → code), the English-only project
convention, and how to run the verification harness before submitting changes.

## Security

Vulnerability reporting and security expectations: [`SECURITY.md`](SECURITY.md).

## License

[Apache License 2.0](LICENSE) — © 2026 the project contributors.
Third-party licenses: `go-ora` (MIT), Grafana plugin SDK (Apache-2.0).

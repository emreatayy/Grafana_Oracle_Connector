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
  workflows (created in F0/F13).
- **Community health files** — `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, and GitHub issue/PR templates.

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

### Changed

- Project content moved to **English-only** (files, docs, UI, log messages).
- Turkish draft report (`docs/proje-raporu.md`) replaced by the English report.
- `.gitignore` comments translated to English.

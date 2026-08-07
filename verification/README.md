# Verification harness

Empirical checks from report §21, run against a real Oracle Database before
the plugin's design assumptions are locked in.

## Checks

| # | Check | Service | Method |
|---|---|---|---|
| 1 | `ColumnTypes().DatabaseTypeName()` / `DecimalSize()` output | `oracle21` | `probe` |
| 2 | INTEGER/NUMBER scan coercion (guards the go-ora #533 regression) | `oracle21` | `probe` |
| 4 | ANSI `TIMESTAMP '…'` literal index usage vs string literal | `oracle21` | `probe` (EXPLAIN PLAN) |
| 7 | Turkish `WE8ISO8859P9` round-trip | `oracle_tr` | `probe` |
| 8 | `ExecContext` anonymous PL/SQL (`DBMS_SCHEDULER`) | `oracle21` | `probe` |
| 5 | Alert evaluation `maxDataPoints` / `intervalMs` | `grafana12` | **Verified from Grafana source** (`pkg/services/ngalert`): defaults 43200 / 1000ms |

## Setup

1. Start the services you need (Oracle containers take a few minutes to
   initialize — wait for `healthy`):

   ```sh
   docker compose --profile oracle21 up -d
   docker compose --profile oracle_tr up -d     # optional, check 7
   docker compose --profile grafana up -d       # optional, check 5
   ```

2. Build and run the probe against a service:

   ```sh
   go mod tidy
   ORACLE_PASSWORD=verif_password go run ./probe            # -> oracle21 (checks 1,2,4,8)
   ORACLE_PASSWORD=verif_password ORACLE_PORT=1522 go run ./probe --port 1522   # -> oracle_tr (check 7)
   ```

   `--host` / `--port` / `--service` (default `XEPDB1`) / `--user` (default `SYSTEM`)
   can be overridden; the password comes from `ORACLE_PASSWORD` (or `--pass`).

The probe prints a per-check verdict and exits non-zero if any check fails.

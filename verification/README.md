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

## Grafana plugin check (F0 + F5 seed, report §17.2 Workflow 3 API subset)

`grafana_plugin_check.py` verifies a **built** plugin against a running Grafana
from the repo-root matrix (report §17.1) — plugin loads, health "Test",
end-to-end query path, and alert-rule acceptance. This is the API-level half of
the future e2e workflow; the Playwright UI half lands with F5.

```sh
# build the plugin first (both halves write to dist/)
mage -v && npm run build

# start a matrix Grafana with the plugin volume-mounted (admin/admin, pinned)
docker compose --profile grafana12 up -d grafana12   # :3001
docker compose --profile grafana13 up -d grafana13   # :3002

# run the check against each
./grafana_plugin_check.py --url http://127.0.0.1:3001
./grafana_plugin_check.py --url http://127.0.0.1:3002
```

Checks performed (idempotent — the test alert rule + folder are deleted after):

| # | Check | API call |
|---|---|---|
| 1 | Grafana reachable | `GET /api/health` |
| 2 | Plugin loaded + enabled | `GET /api/plugins/<id>` |
| 3 | Provisioned datasource exists | `GET /api/datasources/name/<name>` |
| 4 | Health "Test" succeeds | `POST /api/datasources/uid/<uid>/health` |
| 5 | Query path returns a frame | `POST /api/ds/query` |
| 6 | Alerting flag honored | `POST /api/v1/provisioning/alert-rules` |

Requires Python 3 stdlib only; `--url/--user/--password/--plugin-id/
--datasource-name/--timeout` are overridable and environment variables
`GRAFANA_URL/GRAFANA_USER/GRAFANA_PASSWORD` are honored.

## Alerting check (F5, report §10)

`alert_check.py` provisions a set of Unified Alerting rules against a running
Grafana, waits for evaluations, and asserts `(state, health)` the way the
prometheus-compat rules API reports them (§10.5). 7 scenarios: Layer 1
time-series firing/normal/empty, Layer 2 scalar-`COUNT(*)` firing/normal/nodata,
and a broken query. It seeds the `f5_alert` table via SQL*Plus when `--oracle`
is given (the datasource is read-only and cannot create tables):

```sh
# host sqlplus (Oracle instant client), or the docker wrapper below
./alert_check.py --url http://127.0.0.1:3002 \
    --oracle "SYSTEM/verif_password@//localhost:1521/XEPDB1"
./alert_check.py --url http://127.0.0.1:3001 \
    --oracle "SYSTEM/verif_password@//localhost:1521/XEPDB1" \
    --oracle-cmd "docker exec -i f0_oracle21 /opt/oracle/product/21c/dbhomeXE/bin/sqlplus"
```

Run it on **both** matrix Grafanas (12.4 :3001, 13.1 :3002); it cleans up the
rules, folder, and (with `--oracle`) the table afterwards. Example alert rules
live in `examples/alerting/`.

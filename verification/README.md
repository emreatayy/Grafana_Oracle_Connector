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

## Alerting check (F5 + F7, report §10 + §18)

`alert_check.py` provisions a set of Unified Alerting rules against a running
Grafana, waits for evaluations, and asserts `(state, health)` the way the
prometheus-compat rules API reports them (§10.5). 8 scenarios: Layer 1
time-series firing/normal/empty, Layer 2 scalar-`COUNT(*)` firing/normal/nodata,
a broken query, and (F7) a **Prometheus-mode** rule `up > 0` on the provisioned
prom-only datasource from `provisioning/datasources/datasources-prom.yml` —
the F7 DoD "panel + alert" (needs the matrix Prometheus up). It seeds the
`f5_alert` table via SQL*Plus when `--oracle` is given (the datasource is
read-only and cannot create tables):

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

## Prometheus proxy check (F6 + F7, report §9)

`prom_check.py` verifies the Prometheus proxy against a **real Prometheus** from
the repo-root matrix. It creates two throwaway datasources — one with the
configured Prometheus URL (query_range → wide matrix frame + instant → wide
single-row frame, raw PromQL on `executedQueryString`) and one pointing at an
unreachable host with a distinctive bearer token (the proxied failure must be a
readable 500 whose text never echoes the token — the F6 DoD "credentials never
logged"). **F7 adds the metric-browser resources**, proxied through the plugin
`CallResource` handler: `label/__name__/values` (metric list), `labels`, and
`series?match[]=up`, plus a resource → instant-query round trip (metric browser
→ PromQL → panel) and a clean 404 for an unknown resource path. Both datasources
are deleted afterwards.

```sh
# Prometheus + Oracle profiles up; plugin built and mounted (see above)
docker compose --profile prom --profile oracle21 up -d prometheus

# run the check against each matrix Grafana
./prom_check.py --url http://127.0.0.1:3001
./prom_check.py --url http://127.0.0.1:3002
```

Two datasources are used deliberately instead of create → update → query:
Grafana's server-side plugin-settings cache can keep serving the **old**
settings for a while after a datasource is updated (verified on 12.4 + 13.1,
report §21 row 10), so the check does not rely on settings refresh. The
provisioned `prometheus-datasource` (F7, for the alerting + e2e paths) is
created on first Grafana start from `provisioning/datasources/datasources-prom.yml`
— restart the Grafana after adding it so it gets provisioned.

## Variable check (F8, report §8.5 + §18)

`variable_check.py` exercises the two sources a Grafana **query variable** reads
its values from, through the real `/api/ds/query` path (the same path the
variable editor and dashboard variable evaluation use): a SQL single-column
query, both Prometheus `label_values` forms, and multi-value `IN(...)`. It uses
the **provisioned** datasources (`datasources.yml` oracle-datasource,
`datasources-prom.yml` prometheus-datasource) so there is no settings-cache
quirk:

| # | Check | Expect |
|---|---|---|
| 1 | SQL single-column (`SELECT DISTINCT object_type … IN ('TABLE','VIEW')`) | `['TABLE','VIEW']` |
| 2 | `label_values(job)` (single-arg) | `['prometheus']` |
| 3 | `label_values(up, job)` (two-arg) | `['prometheus']` |
| 4 | multi-value `WHERE object_type IN (${v:csv})`, `v=['TABLE','VIEW']` | both rows |
| 5 | empty multi-value (`v=[]`) | `IN (NULL)` → no rows |

```sh
# prometheus-datasource is provisioned from datasources-prom.yml — restart the
# Grafana after adding it (see prom check above); oracle + prom profiles up
docker compose --profile prom --profile oracle21 up -d prometheus
./variable_check.py --url http://127.0.0.1:3001
./variable_check.py --url http://127.0.0.1:3002
```

Pure Python 3 stdlib; exits non-zero on any failed check. The e2e half of the
F8 DoD ("dropdown variable + multi-select dashboard scenarios") is
`tests/variables.spec.ts`.

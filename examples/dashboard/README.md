# Dashboard / panel examples (F13, report §14)

A single importable Grafana dashboard demonstrating the plugin's three query
shapes against **any** Oracle database — no application schema is needed. The
first two panels need only `CREATE SESSION` + `SELECT`; the third also needs the
datasource's **Prometheus proxy** enabled (report §9).

## What each panel is

| Panel | Query | Shape | Requires |
|---|---|---|---|
| **Synthetic load** | `SELECT ts, load FROM (SELECT SYSDATE - (LEVEL - 1) / 96 AS ts, LEVEL AS load FROM DUAL CONNECT BY LEVEL <= 96) WHERE $__timeFilter(ts)` | **SQL time series** (`Format: time_series`) | Only `CREATE SESSION` + `SELECT` |
| **User tables** | `SELECT * FROM (SELECT table_name, num_rows, last_analyzed FROM user_tables ORDER BY table_name) WHERE ROWNUM <= 20` | **SQL table** (`Format: table`) | Only `CREATE SESSION` + `SELECT` |
| **Oracle exporter up** | `up{job="oracle"}` | **Prometheus proxy** (`mode: "prometheus"`) | Prometheus proxy enabled in the datasource settings + the exporter being scraped (§5 of `docs/installation.md`) |

## Why these queries

- **`CONNECT BY` over `DUAL`** generates a deterministic time series on any
  Oracle — the classic synthetic-axis pattern, useful for smoke-testing a new
  datasource install before wiring real tables. The inner subquery generates
  **96 × 15-minute** points; `$__timeFilter(ts)` (expanded backend-side to a
  `TO_DATE`-based `BETWEEN`) bounds them to the dashboard's time range. With the
  default range (`now-6h`) you see the last 24 points.
- **`user_tables`** always exists (it is *your* schema's tables), so the table
  panel works on a fresh account. Note the when-Oracle idiom **wrapped
  `ROWNUM`**: `WHERE ROWNUM <= 20` after an `ORDER BY` in the same query would
  apply the limit *before* the sort (a classic Oracle trap); `SELECT * FROM (…
  ORDER BY …) WHERE ROWNUM <= 20` sorts first, then caps.
- **`up{job="oracle"}`** is the standard Prometheus scrape-health series for the
  official Oracle exporter (`oracle/oracle-db-appdev-monitoring`) — see the
  exporter guide. When the proxy is disabled, Prometheus-mode queries return a
  clean 400 (they never touch Oracle).

## Importing

1. Replace `<DS_UID>` in `oracle-overview.json` with your datasource's UID
   (look in **Connections → Data sources → Oracle Datasource**, its UID is in
   the URL or the environment variable list; in this repo's development matrix
   it is `P7F605F7554A4A003`).
2. In Grafana: **Dashboards → New → Import → Upload dashboard JSON file**
   (or paste the JSON), pick the datasource, **Import**.

Or import via the API:

```sh
curl -s -u admin:admin -H 'Content-Type: application/json' \
  -X POST http://localhost:3002/api/dashboards/db \
  --data-binary @oracle-overview.json
```

## DoD check ("every example works")

After importing you should see, from top to bottom:

1. A smooth saw-tooth **time-series** panel rising to 96 (the `Level` counter) —
   this proves the SQL mode + `$__timeFilter` + time-series framing.
2. A **table** of your account's tables with `NUM_ROWS` / `LAST_ANALYZED`
   columns — proves the table framing and NULL handling.
3. A flat **`up` = 1** series for the exporter — proves the Prometheus proxy
   path (skipped if you did not enable it).

If panel 1 is blank, widen the dashboard time range (`now-24h`) so the 96
generated points fall inside it.
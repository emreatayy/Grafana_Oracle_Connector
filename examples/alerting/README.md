# Alerting examples (F5, report §10)

Two working **Unified Alerting** rule templates for the Oracle datasource —
the "Layer 1" time-series rule and the "Layer 2" rows-based rule — together
with the `seed.sql` table they query.

## What each file is

| File | Purpose |
|---|---|
| `seed.sql` | Creates the `f5_alert` metrics table and seeds it with recent UTC samples (see its header). |
| `layer1_time_series.json` | Layer 1 — `SELECT ts, value …` with **Format: time_series** → `Reduce(last)` → `Is above(25)`. |
| `layer2_rows.json` | Layer 2 — scalar `SELECT COUNT(*) AS alert_flag …` → `Reduce(count)` → `Is above(0)`. |

## Why Layer 2 must be a scalar query (important)

A "rows > 0" alert must count with `SELECT COUNT(*)`, **not** return one row
per matching record. A multi-row table becomes a vector of numbers with
identical empty labels, `Reduce(count)` passes those numbers through unchanged,
and the threshold step then fails with `frame cannot uniquely be identified by
its labels: has duplicate results with labels {}`. This is Grafana platform
behavior, verified on 12.4 and 13.1 (report §10.2). The scalar query returns
`1` (firing) / `0` (Normal) / `NoData` on an empty table.

## Applying the rules

1. Seed the table against the datasource's Oracle user (or equivalent):

   ```sh
   sqlplus -S "SYSTEM/verif_password@//localhost:1521/XEPDB1" @seed.sql
   ```

2. Replace `<DS_UID>` in each JSON file with your datasource's UID (in the
   development matrix it is `P7F605F7554A4A003`).

3. Create the folder, then the rules via the provisioning API:

   ```sh
   curl -s -u admin:admin -H 'Content-Type: application/json' \
     -X POST http://localhost:3002/api/folders \
     -d '{"uid":"oracle-alerting","title":"Oracle alerting examples"}'

   curl -s -u admin:admin -H 'Content-Type: application/json' \
     -X POST http://localhost:3002/api/v1/provisioning/alert-rules \
     -d @layer1_time_series.json
   curl -s -u admin:admin -H 'Content-Type: application/json' \
     -X POST http://localhost:3002/api/v1/provisioning/alert-rules \
     -d @layer2_rows.json
   ```

4. Speed up evaluation (optional; the interval must divide the scheduler's
   10 s, so 10 is the smallest allowed):

   ```sh
   curl -s -u admin:admin -H 'Content-Type: application/json' -X PUT \
     http://localhost:3002/api/v1/provisioning/folder/oracle-alerting/rule-groups/oracle-examples \
     -d "{\"interval\":10,\"rules\":[$(cat layer1_time_series.json),$(cat layer2_rows.json)]}"
   ```

## Reading rule state

`GET /api/prometheus/grafana/api/v1/rules` collapses every non-firing state
into the string `"inactive"`, so read the rule's **`health`** field to tell
them apart: `"ok"` = normal evaluation, `"error"` = query failed,
`"nodata"` = no data (report §10.5). Assert on `(state, health)` jointly —
`verification/alert_check.py` automates the full matrix check.

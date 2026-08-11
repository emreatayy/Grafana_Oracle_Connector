#!/usr/bin/env python3
"""F8 — Grafana query-variable end-to-end check (report §8.5 + §18).

Exercises the two variable sources a Grafana query variable reads its values
from, through the real /api/ds/query path (the same path the variable editor
and dashboard variable evaluation use):

  SQL single-column      SELECT DISTINCT object_type FROM ALL_OBJECTS
                         WHERE object_type IN ('TABLE','VIEW') -> ['TABLE','VIEW']
  Prom label_values      label_values(job)                      -> the jobs of
                         the provisioned matrix Prometheus
  Prom label_values 2-arg label_values(up, job)                 -> same jobs,
                         collected from series?match[]=up
  multi-value IN         sql with ${v:csv} + scopedVars v=['TABLE','VIEW']
                         -> the interpolated IN returns both rows (report §8.2)

Uses the provisioned datasources (datasources.yml oracle-datasource,
datasources-prom.yml prometheus-datasource) so there is no settings-cache
quirk. Requires the oracle + prom profiles up and the plugin built + mounted.

Usage:
  ./variable_check.py [--url http://127.0.0.1:3002] [--user admin] [--password admin]

Pure Python 3 stdlib. Exits non-zero if any check fails.
"""

import argparse
import base64
import json
import sys
import urllib.error
import urllib.request

PLUGIN_ID = "emreatayy-oracle-datasource"
ORACLE_DS = "oracle-datasource"        # provisioning/datasources/datasources.yml
PROM_DS = "prometheus-datasource"      # provisioning/datasources/datasources-prom.yml


def api(base, auth, method, path, body=None):
    req = urllib.request.Request(base + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", "Authorization": auth})
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:  # noqa: BLE001 - non-JSON error body
            return e.code, raw
    except Exception as e:  # noqa: BLE001 - network error surface
        return -1, str(e)


def field_names(frame):
    schema = frame.get("schema") or {}
    return [f.get("name", "") for f in schema.get("fields", [])]


def column(frame, idx=0):
    """One column of a frame as a list of values (what the dropdown shows for a
    single-column variable query). Grafana frame JSON is **column-major**:
    `data.values` is a list of columns, each an array of cell values."""
    data = frame.get("data") or {}
    values = data.get("values") or []
    return values[idx] if len(values) > idx else []


def ds_query(base, auth, uid, refid, model):
    # /api/ds/query takes query fields FLATTENED at the top level of each query
    # object (the "model" nesting is only used by the alerting API) — same
    # shape as grafana_plugin_check.py.
    query = dict(model)
    query["refId"] = refid
    query["datasourceUid"] = uid
    query["datasource"] = {"type": PLUGIN_ID, "uid": uid}
    status, payload = api(base, auth, "POST", "/api/ds/query",
                          {"from": "now-6h", "to": "now", "queries": [query]})
    res = ((payload or {}).get("results", {}).get(refid, {}) or {}) if isinstance(payload, dict) else {}
    return status, res


def main():
    ap = argparse.ArgumentParser(description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default="http://127.0.0.1:3002", help="Grafana base URL")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="admin")
    args = ap.parse_args()

    base = args.url.rstrip("/")
    auth = "Basic " + base64.b64encode(f"{args.user}:{args.password}".encode()).decode()
    passed, failed = 0, []

    def check(ok, label):
        nonlocal passed
        if ok:
            passed += 1
            print(f"  [ok] {label}")
        else:
            failed.append(label)
            print(f"  [FAIL] {label}")

    def ds_uid(name):
        status, payload = api(base, auth, "GET", f"/api/datasources/name/{name}")
        d = payload if isinstance(payload, dict) else {}
        return d.get("uid") if status == 200 else None

    oracle_uid = ds_uid(ORACLE_DS)
    prom_uid = ds_uid(PROM_DS)
    if not oracle_uid:
        print(f"  [FAIL] datasource {ORACLE_DS} not found — provision datasources.yml")
        sys.exit(1)
    if not prom_uid:
        print(f"  [FAIL] datasource {PROM_DS} not found — provision datasources-prom.yml "
              "and start the matrix Prometheus")
        sys.exit(1)
    print(f"  [..] {ORACLE_DS} = {oracle_uid}, {PROM_DS} = {prom_uid}")

    # --- 1. SQL single-column query variable --------------------------------
    sql = "SELECT DISTINCT object_type FROM ALL_OBJECTS WHERE object_type IN ('TABLE','VIEW') ORDER BY 1"
    st, res = ds_query(base, auth, oracle_uid, "A",
                       {"sql": sql, "format": "table", "refId": "A",
                        "datasource": {"type": PLUGIN_ID, "uid": oracle_uid}})
    frames = res.get("frames") or []
    names = column(frames[0]) if frames else []
    check(st == 200 and frames and names == ["TABLE", "VIEW"],
          f"SQL single-column variable -> {names} (HTTP {st})")

    # --- 2. Prometheus label_values(label) ---------------------------------
    # Deterministic from devenv/prometheus.yml: the matrix Prometheus scrapes
    # exactly two jobs (prometheus + oracle_exporter), so label_values(job)
    # returns them sorted.
    st, res = ds_query(base, auth, prom_uid, "A",
                       {"mode": "prometheus", "promql": "label_values(job)", "format": "table",
                        "refId": "A", "datasource": {"type": PLUGIN_ID, "uid": prom_uid}})
    frames = res.get("frames") or []
    jobs = column(frames[0]) if frames else []
    check(st == 200 and frames and jobs == ["oracle_exporter", "prometheus"],
          f"label_values(job) -> {jobs} (HTTP {st})")

    # --- 3. Prometheus label_values(metric, label) --------------------------
    st, res = ds_query(base, auth, prom_uid, "A",
                       {"mode": "prometheus", "promql": "label_values(up, job)", "format": "table",
                        "refId": "A", "datasource": {"type": PLUGIN_ID, "uid": prom_uid}})
    frames = res.get("frames") or []
    jobs2 = column(frames[0]) if frames else []
    check(st == 200 and frames and jobs2 == ["oracle_exporter", "prometheus"],
          f"label_values(up, job) -> {jobs2} (HTTP {st})")

    # --- 4. multi-value IN(...) via scopedVars ------------------------------
    sql_in = ("SELECT object_type, COUNT(*) AS n FROM ALL_OBJECTS "
              "WHERE object_type IN (${v:csv}) GROUP BY object_type ORDER BY object_type")
    st, res = ds_query(base, auth, oracle_uid, "A",
                       {"sql": sql_in, "format": "table", "refId": "A",
                        "scopedVars": {"v": ["TABLE", "VIEW"]},
                        "datasource": {"type": PLUGIN_ID, "uid": oracle_uid}})
    frames = res.get("frames") or []
    rows = column(frames[0]) if frames else []
    check(st == 200 and frames and rows == ["TABLE", "VIEW"],
          f"multi-value ${'{v:csv}'} (v=TABLE,VIEW) -> {rows} (HTTP {st})")

    # --- 5. empty multi-value -> IN (NULL) -> no rows -----------------------
    st, res = ds_query(base, auth, oracle_uid, "A",
                       {"sql": sql_in, "format": "table", "refId": "A",
                        "scopedVars": {"v": []},
                        "datasource": {"type": PLUGIN_ID, "uid": oracle_uid}})
    frames = res.get("frames") or []
    rows = column(frames[0]) if frames else []
    check(st == 200 and frames and rows == [],
          f"empty multi-value -> IN (NULL) -> no rows ({rows!r}, HTTP {st})")

    print(f"\n{passed}/5 checks passed" + ("" if not failed else f" — FAILED: {failed}"))
    sys.exit(0 if not failed else 1)


if __name__ == "__main__":
    main()

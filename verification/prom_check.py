#!/usr/bin/env python3
"""F6 — Prometheus proxy end-to-end check (report §9).

Creates two throwaway datasources through the Grafana API and POSTs
`/api/ds/query` in both Prometheus modes:

  DS A (valid Prometheus URL, no auth)
    matrix  (query_range)  -> wide time-series frame: [time, v1, v2, ...]
                             with the raw PromQL on schema.meta.executedQueryString
    instant (query)        -> wide single-row frame: [time, v1, v2, ...], 1 row
  DS B (unreachable URL + a distinctive bearer token)
    proxy failure          -> per-query status 500 with a readable error whose
                             text must NOT contain the token — the F6 DoD
                             "credentials never logged".

Two datasources are used deliberately: Grafana's server-side plugin-settings
cache can keep serving the OLD settings for a while after a datasource is
updated, so a create-then-update-then-query sequence is not deterministic
(verified empirically on 12.4 + 13.1). Creating a fresh datasource with the
target settings is.

Requires the prom + oracle profiles up and the plugin built + mounted:
  docker compose --profile prom --profile oracle21 up -d prometheus
  /Users/bleda/go/bin/mage -v && npm run build
  docker compose restart grafana12 grafana13   # if the plugin was rebuilt

Usage:
  ./prom_check.py [--url http://127.0.0.1:3002] [--user admin]
      [--password admin] [--prom-url http://prometheus:9090]

`--prom-url` is the URL as the GRAFANA CONTAINER sees it (service name on the
compose network). Pure Python 3 stdlib. Exits non-zero if any check fails.
"""

import argparse
import base64
import json
import sys
import urllib.error
import urllib.request

PLUGIN_ID = "emreatayy-oracle-datasource"
DS_NAME_A = "f6-prom-check-a"
DS_UID_A = "F6PROMCKA"
DS_NAME_B = "f6-prom-check-b"
DS_UID_B = "F6PROMCKB"
SECRET = "s3cret-prom-token-9f3a"  # distinctive value to assert it never leaks


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
    """Field names of one /api/ds/query frame (schema.fields -> [names])."""
    schema = frame.get("schema") or {}
    return [f.get("name", "") for f in schema.get("fields", [])]


def frame_rows(frame):
    data = frame.get("data") or {}
    values = data.get("values") or []
    return len(values[0]) if values else 0


def main():
    ap = argparse.ArgumentParser(description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default="http://127.0.0.1:3002", help="Grafana base URL")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="admin")
    ap.add_argument("--prom-url", default="http://prometheus:9090",
        help="Prometheus base URL as the Grafana container sees it")
    args = ap.parse_args()

    base = args.url.rstrip("/")
    auth = "Basic " + base64.b64encode(f"{args.user}:{args.password}".encode()).decode()

    passed = 0
    failed = 0

    def check(ok, message):
        nonlocal passed, failed
        passed += int(ok)
        failed += int(not ok)
        print(("OK  " if ok else "FAIL") + "  " + message)
        return ok

    oracle_json = {
        "o_hostname": "oracle21", "o_port": 1521, "o_service": "XEPDB1",
        "o_user": "SYSTEM", "sessionTimeZone": "UTC",
    }

    def create_ds(uid, name, prom_json, secure):
        body = {
            "name": name, "type": PLUGIN_ID, "access": "proxy",
            "uid": uid, "orgId": 1, "editable": True,
            "jsonData": {**oracle_json, **prom_json},
            "secureJsonData": {"o_password": "verif_password", **secure},
        }
        status, payload = api(base, auth, "POST", "/api/datasources", body)
        created_uid = (payload.get("datasource") or {}).get("uid") if isinstance(payload, dict) else None
        if status != 200 or created_uid != uid:
            print(f"FAIL  could not create datasource {uid} (HTTP {status}): {payload}")
            sys.exit(1)
        print(f"OK   datasource {uid} created")

    def delete_ds(uid):
        status, _ = api(base, auth, "DELETE", f"/api/datasources/uid/{uid}")
        print(f"OK   datasource {uid} deleted (HTTP {status})")

    def dsq(refid, uid, model):
        # /api/ds/query takes query fields FLATTENED at the top level (the
        # nested "model" wrapper is the alerting-API style and is rejected here).
        return {"refId": refid, "datasource": {"type": PLUGIN_ID, "uid": uid},
                "queryType": "", "relativeTimeRange": {"from": 300, "to": 0},
                **model}

    create_ds(DS_UID_A, DS_NAME_A,
              {"promEnabled": True, "promUrl": args.prom_url, "promAuthType": "none"},
              {})
    create_ds(DS_UID_B, DS_NAME_B,
              {"promEnabled": True, "promUrl": "http://no-such-prom.invalid:9090",
               "promAuthType": "token"},
              {"promToken": SECRET})
    try:
        # --- matrix (query_range) ---
        status, payload = api(base, auth, "POST", "/api/ds/query",
                              {"from": "now-5m", "to": "now",
                               "queries": [dsq("A", DS_UID_A, {"mode": "prometheus",
                                                               "promql": "up{job=\"prometheus\"}"})]})
        ok = status == 200 and isinstance(payload, dict)
        detail = f"HTTP {status}"
        matrix_frame = None
        if ok:
            frames = (payload.get("results", {}).get("A", {}) or {}).get("frames") or []
            if frames:
                matrix_frame = frames[0]
                names = field_names(matrix_frame)
                detail = f"fields={names} rows={frame_rows(matrix_frame)}"
                ok = len(names) >= 2 and names[0] == "time" and frame_rows(matrix_frame) > 1
        check(ok, f"query_range returns a wide time-series frame -> {detail}")
        if matrix_frame:
            meta = (matrix_frame.get("schema") or {}).get("meta") or {}
            check(meta.get("executedQueryString") == "up{job=\"prometheus\"}",
                  f"executedQueryString = {meta.get('executedQueryString')!r}, want the raw PromQL")

        # --- instant (query) ---
        status, payload = api(base, auth, "POST", "/api/ds/query",
                              {"from": "now-5m", "to": "now",
                               "queries": [dsq("B", DS_UID_A, {"mode": "prometheus",
                                                               "promql": "up", "instant": True})]})
        ok = status == 200 and isinstance(payload, dict)
        detail = f"HTTP {status}"
        if ok:
            frames = (payload.get("results", {}).get("B", {}) or {}).get("frames") or []
            if frames:
                names = field_names(frames[0])
                rows = frame_rows(frames[0])
                detail = f"fields={names} rows={rows}"
                ok = len(names) >= 2 and names[0] == "time" and rows == 1
        check(ok, f"instant query returns a wide single-row frame -> {detail}")

        # --- credentials never logged (DoD) ---
        status, payload = api(base, auth, "POST", "/api/ds/query",
                              {"from": "now-5m", "to": "now",
                               "queries": [dsq("C", DS_UID_B, {"mode": "prometheus",
                                                               "promql": "up"})]})
        # /api/ds/query returns HTTP 200 with the per-query result inside
        # results.<refId>; a failed query carries status 500 + error there.
        res = (payload.get("results", {}).get("C", {}) or {}) if isinstance(payload, dict) else {}
        text = json.dumps(res)
        ok = res.get("status") == 500 and "prometheus unreachable" in text
        check(ok, f"proxy failure surfaces as a readable error (query status {res.get('status')})")
        check(SECRET not in text,
              "configured token is not echoed in the error response (credentials never logged)")
    finally:
        delete_ds(DS_UID_A)
        delete_ds(DS_UID_B)

    print(f"\n{passed} passed, {failed} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()

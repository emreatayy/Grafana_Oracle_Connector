#!/usr/bin/env python3
"""F5 — Unified Alerting end-to-end check (report §10).

Provisions a set of alert rules against a running Grafana whose datasource
points at Oracle, waits for evaluations, and asserts the rule state/health the
way the prometheus-compat rules API actually reports them (report §10.5: every
non-firing state collapses into "inactive", so the health field disambiguates).

Scenarios (all use a 1 h relative time range), asserted on (state, health):
  L1-firing   time_series + Reduce(last) + Is above(25)          -> firing, health ok
  L1-normal   time_series + Reduce(last) + Is above(100)         -> inactive, health ok
  L1-empty    time_series over an EMPTY result set               -> inactive, health ok
              (empty time-series is NOT NoData: Reduce(last)=NaN -> threshold false)
  L2-firing   scalar SELECT COUNT(*) rows>15 + Reduce(count) + >0 -> firing, health ok
  L2-normal   scalar COUNT(*) with no matching rows (metric='nope') -> inactive, health ok
  L2-nodata   SELECT 1 with no matching rows (empty TABLE)       -> inactive, health nodata
  err-sql     SELECT * FROM missing_table_xyz                    -> inactive, health error
  Prom-firing Prometheus-mode time series `up` + Reduce(last) + >0 -> firing, health ok
              (F7 DoD "panel + alert": a metric-browser PromQL alert rule
              evaluates through the proxy; needs the provisioned
              prometheus-datasource from datasources-prom.yml + matrix Prom)

The seed table `f5_alert` is created and dropped via SQL*Plus when `--oracle`
is given (the datasource is read-only, so the script itself cannot write).
Without `--oracle`, apply examples/alerting/seed.sql manually first and the
table is left in place.

Usage:
  ./alert_check.py [--url http://127.0.0.1:3002] [--user admin]
      [--password admin] [--datasource-uid P7F605F7554A4A003]
      [--oracle "SYSTEM/verif_password@//localhost:1521/XEPDB1"]

Pure Python 3 stdlib. Exits non-zero if any scenario fails.
"""

import argparse
import base64
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

FOLDER = "f5-check-folder"
GROUP = "f5-check"
TABLE = "f5_alert"
PLUGIN_ID = "emreatayy-oracle-datasource"
PROM_DS_NAME = "prometheus-datasource"  # provisioning/datasources/datasources-prom.yml
SLEEP_STEP_S = 5
POLL_S = 45
# (sql, format, reducer, threshold-op, params, expected state, expected health)
# State/health are asserted jointly (report §10.5). Semantics worth knowing:
#   - COUNT(*) always returns ONE row (0 over an empty scan) -> Normal, never NoData.
#   - NoData needs an EMPTY TABLE: a row-returning query with zero matches.
#   - An empty TIME-SERIES frame is NOT NoData: Reduce(last) yields NaN, the
#     threshold is false -> Normal. (Verified from Grafana source, mathexp/reduce.)
#   - A broken query shows state "inactive" + health "error" (execErrState=Error).
SCENARIOS = {
    "L1-firing": ("SELECT ts, value FROM f5_alert WHERE $__timeFilter(ts)", "time_series", "last", "gt", [25], "firing", "ok"),
    "L1-normal": ("SELECT ts, value FROM f5_alert WHERE $__timeFilter(ts)", "time_series", "last", "gt", [100], "inactive", "ok"),
    "L1-empty": ("SELECT ts, value FROM f5_alert WHERE ts < TO_DATE('2020-01-01','YYYY-MM-DD') AND $__timeFilter(ts)", "time_series", "last", "gt", [0], "inactive", "ok"),
    "L2-firing": ("SELECT COUNT(*) AS alert_flag FROM f5_alert WHERE value > 15 AND $__timeFilter(ts)", "table", "count", "gt", [0], "firing", "ok"),
    "L2-normal": ("SELECT COUNT(*) AS alert_flag FROM f5_alert WHERE metric='nope' AND $__timeFilter(ts)", "table", "count", "gt", [0], "inactive", "ok"),
    "L2-nodata": ("SELECT 1 AS alert_flag FROM f5_alert WHERE metric='nope' AND $__timeFilter(ts)", "table", "count", "gt", [0], "inactive", "nodata"),
    "err-sql": ("SELECT * FROM missing_table_xyz", "table", "count", "gt", [0], "inactive", "error"),
}

# Prometheus-mode scenarios (F7 DoD "panel + alert"). Run through the
# provisioned prometheus-datasource; `up` is always present on a live Prometheus.
PROM_SCENARIOS = {
    "Prom-firing": ("up", "time_series", "last", "gt", [0], "firing", "ok"),
}

ALL_SCENARIOS = {**SCENARIOS, **PROM_SCENARIOS}


def api(base, auth, method, path, body=None):
    req = urllib.request.Request(base + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json", "Authorization": auth})
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()[:500]
    except Exception as e:  # noqa: BLE001 - network error surface
        return -1, str(e)


def oracle_sql(conn, sql_text, oracle_cmd="sqlplus"):
    """Run SQL text against Oracle via SQL*Plus (stdin). Returns (rc, output).

    oracle_cmd may be a plain ``sqlplus`` (instant client on PATH) or a
    wrapper such as ``docker exec -i f0_oracle21 /opt/oracle/product/21c/dbhomeXE/bin/sqlplus``
    — SQL is piped on stdin either way, so the committed seed file is read in
    Python rather than referenced with @, keeping the wrapper portable.
    """
    cmd = oracle_cmd.split() + ["-S", conn]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=120,
                              input=sql_text)
        return proc.returncode, proc.stdout + proc.stderr
    except FileNotFoundError:
        return 127, f"'{oracle_cmd}' not found (install an Oracle instant client or use --oracle-cmd)"


def main():
    ap = argparse.ArgumentParser(description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default="http://127.0.0.1:3002", help="Grafana base URL")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="admin")
    ap.add_argument("--datasource-uid", default="P7F605F7554A4A003")
    ap.add_argument("--oracle", default=None,
        help='SQL*Plus connect string, e.g. "SYSTEM/verif_password@//localhost:1521/XEPDB1"; '
             "seeds and drops the f5_alert table via SQL*Plus.")
    ap.add_argument("--oracle-cmd", default="sqlplus",
        help='SQL*Plus command; add a "docker exec -i f0_oracle21 /opt/oracle/product/21c/dbhomeXE/bin/sqlplus" '
             "wrapper when sqlplus is not on the host PATH.")
    args = ap.parse_args()

    base = args.url.rstrip("/")
    auth = "Basic " + base64.b64encode(f"{args.user}:{args.password}".encode()).decode()

    def dsq(refid, sql, fmt):
        return {"refId": refid, "datasourceUid": args.datasource_uid, "queryType": "",
                "model": {"refId": refid,
                          "datasource": {"type": PLUGIN_ID, "uid": args.datasource_uid},
                          "sql": sql, "format": fmt, "intervalMs": 15000, "maxDataPoints": 200},
                "relativeTimeRange": {"from": 3600, "to": 0}}

    # F7: resolve the provisioned prometheus-datasource (datasources-prom.yml).
    # The plugin requires Oracle settings to load, but this instance is only
    # ever queried in Prometheus mode, so `up` runs through the proxy.
    st, pd = api(base, auth, "GET", f"/api/datasources/name/{PROM_DS_NAME}")
    prom_uid = (pd or {}).get("uid") if isinstance(pd, dict) else None
    if st != 200 or not prom_uid:
        print(f"  [FAIL] prometheus datasource {PROM_DS_NAME} not found (HTTP {st}) — "
              "start the matrix Prometheus and restart Grafana to re-provision "
              "datasources-prom.yml")
        sys.exit(1)
    print(f"  [..] prometheus datasource {PROM_DS_NAME} = {prom_uid}")

    def pdsq(refid, promql, fmt):
        return {"refId": refid, "datasourceUid": prom_uid, "queryType": "",
                "model": {"refId": refid,
                          "datasource": {"type": PLUGIN_ID, "uid": prom_uid},
                          "mode": "prometheus", "promql": promql, "format": fmt,
                          "intervalMs": 15000, "maxDataPoints": 200},
                "relativeTimeRange": {"from": 3600, "to": 0}}

    def expr(refid, typ, expression, reducer=None, ev=None, params=None):
        m = {"refId": refid, "type": typ, "expression": expression,
             "datasource": {"type": "__expr__", "uid": "__expr__"}}
        if reducer:
            m["reducer"] = reducer
        if ev:
            m["conditions"] = [{"evaluator": {"type": ev, "params": params}}]
        return {"refId": refid, "datasourceUid": "__expr__", "model": m}

    failures = []

    def check(name, cond, detail):
        tag = "PASS" if cond else "FAIL"
        if not cond:
            failures.append(name)
        print(f"  [{tag}] {name}: {detail}")

    # 0. Oracle seed (optional but preferred) ---------------------------------
    if args.oracle:
        seed = open("examples/alerting/seed.sql").read()
        rc, out = oracle_sql(args.oracle, seed, args.oracle_cmd)
        if rc != 0:
            print(f"  [FAIL] seed: rc={rc}\n{out[-800:]}")
            sys.exit(1)
        print("  [PASS] seed: f5_alert created via SQL*Plus")
    else:
        print(f"  [..] seed: --oracle not given; assuming examples/alerting/seed.sql was applied")

    # 1. Provision rules ------------------------------------------------------
    api(base, auth, "DELETE", f"/api/folders/{FOLDER}")
    st, _ = api(base, auth, "POST", "/api/folders", {"uid": FOLDER, "title": "F5 check"})
    if st not in (200, 201):
        print(f"  [FAIL] folder create: {st}")
        sys.exit(1)

    uids = {}

    def provision(name, data):
        body = {"title": f"F5-{name}", "folderUID": FOLDER, "ruleGroup": GROUP,
                "condition": "C", "for": "0m", "noDataState": "NoData", "execErrState": "Error",
                "isPaused": False,
                "data": data}
        st, r = api(base, auth, "POST", "/api/v1/provisioning/alert-rules", body)
        if st == 201:
            uids[name] = r.get("uid")
        else:
            print(f"  [FAIL] create {name}: {st} {str(r)[:200]}")

    for name, (sql, fmt, red, ev, params, _st, _hl) in SCENARIOS.items():
        provision(name, [dsq("A", sql, fmt), expr("B", "reduce", "A", reducer=red),
                         expr("C", "threshold", "B", ev=ev, params=params)])
    for name, (promql, fmt, red, ev, params, _st, _hl) in PROM_SCENARIOS.items():
        provision(name, [pdsq("A", promql, fmt), expr("B", "reduce", "A", reducer=red),
                         expr("C", "threshold", "B", ev=ev, params=params)])
    check(f"provision", len(uids) == len(ALL_SCENARIOS), f"{len(uids)}/{len(ALL_SCENARIOS)} rules")

    # 2. Fast group interval (10 s is the scheduler minimum) ------------------
    prov_rules = [r for r in (api(base, auth, "GET", "/api/v1/provisioning/alert-rules")[1] or [])
                  if r.get("folderUID") == FOLDER]
    st, g = api(base, auth, "PUT", f"/api/v1/provisioning/folder/{FOLDER}/rule-groups/{GROUP}",
                {"interval": 10, "rules": prov_rules})
    if st not in (200, 201):
        print(f"  [FAIL] group interval: {st} {str(g)[:200]}")

    # 3. Poll the rules API and record state transitions ----------------------
    history = {n: [] for n in ALL_SCENARIOS}
    t0 = time.time()
    while time.time() - t0 < POLL_S:
        time.sleep(SLEEP_STEP_S)
        _, rules = api(base, auth, "GET", "/api/prometheus/grafana/api/v1/rules")
        snap = {}
        for rg in (rules.get("data") or {}).get("groups", []):
            for r in rg.get("rules", []):
                n = r.get("name", "")
                if n.startswith("F5-"):
                    snap[n[3:]] = (r.get("state"), r.get("health"))
        for n in ALL_SCENARIOS:
            if n in snap and (not history[n] or history[n][-1] != snap[n]):
                history[n].append((round(time.time() - t0), snap[n]))

    print("\n  state history (s -> (state, health)):")
    for n in sorted(ALL_SCENARIOS):
        print(f"    {n:10} {history[n] if history[n] else 'NO EVAL'}")

    # 4. Final assertions: (state, health) jointly ----------------------------
    _, rules = api(base, auth, "GET", "/api/prometheus/grafana/api/v1/rules")
    snap = {}
    for rg in (rules.get("data") or {}).get("groups", []):
        for r in rg.get("rules", []):
            n = r.get("name", "")
            if n.startswith("F5-"):
                snap[n[3:]] = (r.get("state"), r.get("health"))

    print("\n  final:")
    for name in sorted(ALL_SCENARIOS):
        got = snap.get(name, ("?", "?"))
        want_state, want_health = ALL_SCENARIOS[name][5], ALL_SCENARIOS[name][6]
        state_ok = got[0] == want_state
        health_ok = got[1] == want_health
        check(f"{name}",
              state_ok and health_ok,
              f"state={got[0]} health={got[1]} "
              f"(want {want_state}/{want_health})")

    # 5. Cleanup --------------------------------------------------------------
    for name, uid in uids.items():
        api(base, auth, "DELETE", f"/api/v1/provisioning/alert-rules/{uid}")
    api(base, auth, "DELETE", f"/api/folders/{FOLDER}")
    if args.oracle:
        rc, out = oracle_sql(args.oracle, f"DROP TABLE {TABLE} PURGE;", args.oracle_cmd)
        if rc != 0:
            print(f"  [..] drop table rc={rc}: {out[-300:]}")
    print(f"\ncleaned up; allpass={not failures}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

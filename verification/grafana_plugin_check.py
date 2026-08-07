#!/usr/bin/env python3
"""Grafana plugin F0 check.

Verifies a built, unsigned datasource plugin against a running Grafana
(report §17.2 Workflow 3, API-level subset — the Playwright e2e is later).
Each check prints a verdict and the script exits non-zero if any fail.

Checks:
  1. Grafana is reachable            GET  /api/health -> 200
  2. Plugin is loaded + enabled      GET  /api/plugins/<id>.enabled == true
  3. Datasource exists               GET  /api/datasources/name/<name>
  4. Health "Test" succeeds          POST /api/datasources/uid/<uid>/health -> status OK
  5. Query path works end-to-end     POST /api/ds/query returns a non-empty frame
  6. Alerting flag honored           POST /api/v1/provisioning/alert-rules accepts
                                     a rule whose query references the datasource
                                     (rule + folder are cleaned up afterwards)

Usage:
  ./grafana_plugin_check.py [--url http://127.0.0.1:3001] [--user admin]
      [--password admin] [--plugin-id emreatayy-oracle-datasource]
      [--datasource-name oracle-datasource] [--timeout 60]

Environment variables GRAFANA_URL / GRAFANA_USER / GRAFANA_PASSWORD are honored
and overridden by CLI flags. Pure Python 3 stdlib — no curl/jq required.
"""

import argparse
import base64
import json
import sys
import time
import urllib.error
import urllib.request

DEFAULT_URL = "http://127.0.0.1:3001"
DEFAULT_USER = "admin"
DEFAULT_PASSWORD = "admin"
DEFAULT_PLUGIN_ID = "emreatayy-oracle-datasource"
DEFAULT_DATASOURCE_NAME = "oracle-datasource"
DEFAULT_TIMEOUT_S = 60

# Run every check even after a failure; exit non-zero if any failed.
PASS = "✅"   # ✅
FAIL = "❌"   # ❌


def log(ok: bool, message: str) -> bool:
    print(f"  {PASS if ok else FAIL} {message}")
    return ok


class GrafanaCheck:
    def __init__(self, base_url: str, user: str, password: str, plugin_id: str,
                 datasource_name: str, timeout_s: int):
        self.base_url = base_url.rstrip("/")
        self.user = user
        self.plugin_id = plugin_id
        self.datasource_name = datasource_name
        self.timeout_s = timeout_s
        token = base64.b64encode(f"{user}:{password}".encode()).decode()
        self.headers = {
            "Authorization": f"Basic {token}",
            "Content-Type": "application/json",
        }

    # -- low-level --------------------------------------------------------

    def api(self, method: str, path: str, body=None):
        """Perform a Grafana API call, return (http_status, parsed_json).

        Raises ValueError on transport errors / unparseable JSON.
        """
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base_url + path, data=data, headers=self.headers,
                                     method=method)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                parsed = json.loads(raw) if raw else None
            except json.JSONDecodeError:
                parsed = raw.decode(errors="replace")
            return e.code, parsed

    def wait_ready(self) -> bool:
        deadline = time.time() + self.timeout_s
        while time.time() < deadline:
            status, _ = self.api("GET", "/api/health")
            if status == 200:
                return True
            time.sleep(2)
        return False

    # -- checks -----------------------------------------------------------

    def check_plugin(self) -> bool:
        status, payload = self.api("GET", "/api/plugins")
        if status != 200 or not isinstance(payload, list):
            return log(False, f"plugin listing failed (HTTP {status})")
        for plugin in payload:
            if plugin.get("id") == self.plugin_id:
                enabled = plugin.get("enabled") is True
                return log(enabled, f"plugin {self.plugin_id} loaded and enabled")
        return log(False, f"plugin {self.plugin_id} not in /api/plugins")

    def check_datasource(self) -> str:
        """Return the datasource uid (or "")."""
        status, payload = self.api("GET", f"/api/datasources/name/{self.datasource_name}")
        if status != 200:
            log(False, f"datasource '{self.datasource_name}' not found (HTTP {status})")
            return ""
        uid = payload.get("uid", "")
        ok = log(bool(uid), f"datasource '{self.datasource_name}' found (uid {uid})")
        return uid if ok else ""

    def check_health(self, ds_uid: str) -> bool:
        if not ds_uid:
            return log(False, "health check skipped (no datasource uid)")
        status, payload = self.api("POST", f"/api/datasources/uid/{ds_uid}/health")
        ok = status == 200 and isinstance(payload, dict) and payload.get("status") == "OK"
        detail = payload.get("message") if isinstance(payload, dict) else payload
        return log(ok, f"health 'Test' -> {detail} (HTTP {status})")

    def check_query(self, ds_uid: str) -> bool:
        if not ds_uid:
            return log(False, "query check skipped (no datasource uid)")
        body = {
            "from": "now-1h",
            "to": "now",
            "queries": [{
                "refId": "A",
                "datasource": {"type": self.plugin_id, "uid": ds_uid},
                "queryType": "query",
                "model": {"refId": "A", "constant": 6.5},
            }],
        }
        status, payload = self.api("POST", "/api/ds/query", body)
        ok = False
        detail = f"HTTP {status}"
        if status == 200 and isinstance(payload, dict):
            result = payload.get("results", {}).get("A", {})
            frames = result.get("frames") or []
            ok = result.get("status") == 200 and len(frames) > 0
            detail = f"HTTP {status}, {len(frames)} frame(s)"
        return log(ok, f"query path returns a frame -> {detail}")

    def check_alerting(self, ds_uid: str) -> bool:
        if not ds_uid:
            return log(False, "alerting check skipped (no datasource uid)")
        # Folder via the legacy API — /api/v1/provisioning/folders does not
        # exist on Grafana 12.4 (13.x only).
        status, folder = self.api("POST", "/api/folders", {"title": "F0 check"})
        if status != 200:
            return log(False, f"could not create alert folder (HTTP {status})")
        folder_uid = folder.get("uid", "")
        rule = {
            "title": "F0 check - oracle datasource alerting",
            "orgID": 1,
            "folderUID": folder_uid,
            "ruleGroup": "f0",
            "condition": "C",
            "for": "0s",
            "isPaused": True,
            "data": [
                {"refId": "A", "datasourceUid": ds_uid,
                 "relativeTimeRange": {"from": 600, "to": 0},
                 "model": {"refId": "A"}},
                {"refId": "B", "datasourceUid": "__expr__",
                 "model": {"type": "reduce", "expression": "A", "reducer": "last"}},
                {"refId": "C", "datasourceUid": "__expr__",
                 "model": {"type": "threshold", "expression": "B",
                           "conditions": [{"evaluator": {"type": "gt", "params": [0]}}]}},
            ],
            "noDataState": "NoData",
            "execErrState": "Error",
        }
        status, payload = self.api("POST", "/api/v1/provisioning/alert-rules", rule)
        rule_uid = payload.get("uid", "") if isinstance(payload, dict) else ""
        ok = status in (200, 201) and bool(rule_uid)
        log(ok, f"alert-rule API accepts a query against the datasource"
                f" (rule uid {rule_uid or 'N/A'})")
        # Best-effort cleanup so the script is idempotent.
        if rule_uid:
            self.api("DELETE", f"/api/v1/provisioning/alert-rules/{rule_uid}")
        if folder_uid:
            self.api("DELETE", f"/api/folders/{folder_uid}?force=true")
        return ok


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Verify a built unsigned Grafana datasource plugin "
                    "(report §17.2 Workflow 3, API subset).")
    ap.add_argument("--url", default=os_get("GRAFANA_URL", DEFAULT_URL))
    ap.add_argument("--user", default=os_get("GRAFANA_USER", DEFAULT_USER))
    ap.add_argument("--password", default=os_get("GRAFANA_PASSWORD", DEFAULT_PASSWORD))
    ap.add_argument("--plugin-id", default=DEFAULT_PLUGIN_ID)
    ap.add_argument("--datasource-name", default=DEFAULT_DATASOURCE_NAME)
    ap.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT_S)
    args = ap.parse_args()

    print(f"Grafana plugin check against {args.url} "
          f"(plugin {args.plugin_id}, datasource '{args.datasource_name}')\n")
    check = GrafanaCheck(args.url, args.user, args.password,
                         args.plugin_id, args.datasource_name, args.timeout)

    results = []
    results.append(("1. Grafana reachable", check.wait_ready()))
    if results[0][1]:
        results.append(("2. Plugin loaded + enabled", check.check_plugin()))
        ds_uid = check.check_datasource()
        results.append(("3. Datasource exists", bool(ds_uid)))
        results.append(("4. Health 'Test' succeeds", check.check_health(ds_uid)))
        results.append(("5. Query path returns a frame", check.check_query(ds_uid)))
        results.append(("6. Alerting flag honored", check.check_alerting(ds_uid)))

    failed = [name for name, ok in results if not ok]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed"
          + (f", failed: {', '.join(failed)}" if failed else ""))
    return 1 if failed else 0


def os_get(key: str, default: str) -> str:
    import os
    return os.environ.get(key, default)


if __name__ == "__main__":
    sys.exit(main())

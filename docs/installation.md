# Installing and configuring the Oracle datasource

User-facing installation and configuration guide. Development setup is covered
in the [README](../README.md#getting-started-development); the release pipeline
behind the archives is in [release-and-packaging.md](release-and-packaging.md).

---

## 1. Requirements

| System | Minimum | Tested lines |
|---|---|---|
| **Grafana** | **12.3** | 12.4, 13.1 |
| **Oracle Database** | **12c** | 19c, 21c, 23ai |
| **Prometheus** *(optional proxy)* | 2.40 | 3.x |

- The plugin needs **no Oracle Client** — the backend is a pure-Go driver
  (`go-ora`), so there are no OCI / Instant Client libraries to install.
- Oracle access from Grafana requires the host to reach the Oracle listener
  (port 1521 by default) over TCP.
- Grafana must run the plugin **backend binaries for its own OS/architecture**
  — the release archive ships `darwin`, `linux` and `windows` (amd64 + arm64)
  executables, so any Grafana host is covered.

---

## 2. Install the plugin

### 2.1 From the release archive (recommended)

1. Download the plugin archive for the version you want
   (`emreatayy-oracle-datasource-<version>.zip` or `.tar.gz`) from the release
   assets on GitHub.
2. Unpack it into Grafana's plugin directory so you end up with a folder named
   **exactly** `emreatayy-oracle-datasource`:

   ```sh
   # Linux/macOS
   mkdir -p /var/lib/grafana/plugins
   tar xzf emreatayy-oracle-datasource-<version>.tar.gz -C /var/lib/grafana/plugins

   # Windows (PowerShell)
   # Expand-Archive emreatayy-oracle-datasource-<version>.zip -DestinationPath "$env:ProgramFiles\GrafanaLabs\grafana\data\plugins"
   ```

   The archive contains a single top-level `emreatayy-oracle-datasource/`
   directory with `plugin.json` at its root — do not nest it one level deeper.
3. **Unsigned dev installs only** — a Grafana-signed plugin installs with no
   extra step. If you are running an unsigned build (e.g. from `git` or a local
   `mage archive`), set in `grafana.ini` / env:

   ```ini
   [plugins]
   allow_loading_unsigned_plugins = emreatayy-oracle-datasource
   ```
   ```sh
   export GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS=emreatayy-oracle-datasource
   ```
4. Restart Grafana. The plugin appears under **Configuration → Plugins →
   Oracle Datasource**.

> **Signed installs.** Production installs should use a signed archive so the
> `allow_loading_unsigned_plugins` flag is not needed. Signed archives are
> published as GitHub Release assets (see
> [release-and-packaging.md](release-and-packaging.md) §4).

### 2.2 With `grafana-cli` (once published to the catalog)

```sh
grafana-cli plugins install emreatayy-oracle-datasource
```

This path is available once the plugin is published on Grafana.com.

---

## 3. Create a datasource

**Connections → Data sources → Add data source → Oracle Datasource.**

### 3.1 Connection

Use either the **Connection string** (full `oracle://` URL) or the individual
fields:

| Field | Meaning | Example |
|---|---|---|
| Connection string | Full `oracle://` URL — when set, overrides the fields below | `oracle://user:pass@dbhost:1521/ORCLPDB1` |
| Host | Oracle host | `dbhost` / `10.0.0.5` |
| Port | Oracle listener port | `1521` |
| Service name | Oracle service name (either service name **or** SID required) | `ORCLPDB1` |
| SID | Oracle SID | `ORCL` |
| Username | Oracle account (see [read-only user](#4-recommended-read-only-user)) | `GRAFANA_READONLY` |
| Password | Stored encrypted in `secureJsonData` — never in the database | — |

A SID is requested with the `?SID=` option and resolves to a `(SID=...)`
descriptor instead of `(SERVICE_NAME=...)`.

**TNS connect descriptor (F14):** the **Host** field also accepts a full TNS
connect descriptor. When it starts with `(`, the whole `(DESCRIPTION=…)` string
is passed to go-ora, which reads the ADDRESS list and CONNECT_DATA from it —
**Port / Service name / SID are then ignored**, and `tnsnames.ora` is **not**
consulted (paste the resolved descriptor, or use a Connection string):

```
Host: (DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=dbhost)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=ORCLPDB1)))
```

### 3.2 Prometheus proxy (optional)

A Prometheus-mode tab lets queries run against a Prometheus HTTP API v1 through
the plugin (report §9). When **Enable proxy** is on:

| Field | Meaning |
|---|---|
| URL | Base URL of the Prometheus server (http/https only; embedded credentials are rejected) |
| Auth type | `None`, `Basic auth` (user + password) or `Bearer token` |
| Basic user / Basic password | Basic auth credentials (stored encrypted) |
| Bearer token | Bearer token for token auth (stored encrypted) |
| Skip TLS verify | Off by default; enable only for trusted private Prometheus servers |

When the proxy is **disabled**, Prometheus-mode queries fail cleanly with a 400
— they are never run against Oracle.

### 3.3 General

| Field | Meaning |
|---|---|
| Default mode | Editor tab opened for new queries (`SQL` or `Prometheus`) |
| Require command confirmation | Show the confirmation dialog before a "Run command" action (the backend always requires confirmation regardless) |

### 3.4 Limits

| Field | Default | Meaning |
|---|---|---|
| Session time zone | `UTC` | Pinned at connect (`ALTER SESSION SET TIME_ZONE`) so results are independent of the host locale |
| Query timeout (s) | `30` | Cancels a query server-side after this long |
| Command timeout (s) | `10` | Bounds a single "Run command" action |
| Max open connections | `10` | Pool size cap (`MaxOpenConns`) |
| Max idle connections | `5` | Idle connections kept open (`MaxIdleConns`) |
| Connection lifetime (s) | `1800` | Max age of a pooled connection (`ConnMaxLifetime`) |
| Max rows | `100000` | Result rows cap; when hit the frame carries a **truncation warning** |
| Max series | `1000` | Maximum time series per query |
| Large numbers | `Auto` | NUMBER(scale=0, precision>15) policy: string (full precision) in tables, float in time series (ADR-8) |
| Numeric variables | *(empty)* | Dashboard variable names allowed to expand unquoted in SQL (comma-separated) |

After saving, use **Save & test**. The "Test" result is structured JSON —
`{"oracle":{"ok":…,"version":…},"prometheus":{"enabled":…,"ok":…}}` — with the
readable, credential-free error in the failing subsystem (report §13).

### 3.5 Formatting

| Field | Meaning |
|---|---|
| Column units | Map Oracle columns to Grafana display units (report §16), one `NAME=unit` per line or comma-separated — e.g. `LOAD=percent` or `MEMORY_BYTES=decbytes`. Column names match **case-insensitively** (Oracle returns uppercase unquoted names). Applied server-side as `FieldConfig.Unit`, so panels, tooltips and axes use the unit automatically |

### 3.6 Connection security (TLS)

Oracle TLS (TCPS listeners) is supported without any wallet:

| Field | Meaning |
|---|---|
| TLS mode | `None` (plain, default) · `SSL, skip certificate verify` · `SSL, verify custom CA` |
| CA certificate | PEM bundle the server is verified against when TLS mode is `SSL, verify custom CA` (stored in `jsonData` — it is a public certificate, not a credential) |

- `skip certificate verify` is an **operator opt-in** for self-signed test
  racks: it encrypts the session but performs no certificate validation. It is
  never the default.
- `verify custom CA` pins the server to the configured CA, layered **on top of**
  the system roots. A wrong or unparsable bundle is a **hard error at
  save-time** — the datasource fails to construct rather than silently
  downgrading to plaintext.

---

## 4. Recommended: read-only user

Use a dedicated **read-only** Oracle account for the datasource. The plugin
already blocks anything but `SELECT`/`WITH` (first-keyword guard, ADR-7); the
database account is the second layer, so a mis-typed query can never mutate
data. Do **not** connect as `SYSTEM`/`SYS` for dashboards.

`examples/readonly_user.sql` creates the account with the minimum grants
(`CREATE SESSION`, `SELECT ON SYS.V_$VERSION` for the health banner, plus
`SELECT` on the application tables the dashboards read) and includes a
verification block (a `DELETE` must fail with `ORA-01031`).

```sh
# as DBA (SYSTEM/SYS)
sqlplus SYSTEM@ORCLPDB1 @examples/readonly_user.sql
```

> The editor's autocomplete uses the public `ALL_TAB_COMMENTS` /
> `ALL_TAB_COLUMNS` / `ALL_COL_COMMENTS` views, which already restrict to the
> objects this account can see — no extra grants needed for completion.
>
> The "Run command" operations UI (report §12) needs elevated scheduler /
> procedure privileges; give those to a separate operator account if used, not
> to the dashboards' read-only user.

---

## 5. Exporter guide (Oracle metrics via Prometheus)

The optional Prometheus mode turns the datasource into a *query proxy* to a
Prometheus server. To monitor Oracle itself you pair it with the official
Oracle database exporter
([`oracle/oracle-db-appdev-monitoring`](https://hub.docker.com/r/oracle/oracle-db-appdev-monitoring))
and scrape it with Prometheus; the plugin then charts those metrics like any
Prometheus data source.

```
Oracle DB  ──SQL──▶  oracle-db-appdev-monitoring (exporter)  ──/metrics──▶  Prometheus  ──HTTP API──▶  this datasource (Prometheus mode)
```

1. **Run the exporter** on the same network as Prometheus:

   ```sh
   docker run -d --name oracle-exporter \
     -e ORACLE_USERNAME='SYSTEM' \
     -e ORACLE_PASSWORD='<password>' \
     -e ORACLE_DSN='//oracle21:1521/XEPDB1' \
     -p 9161:9161 \
     oracle/oracle-db-appdev-monitoring
   ```

   The exporter's metrics endpoint is `http://localhost:9161/metrics`
   (`oracledb_*` metrics; Oracle Wallet / TLS setups are covered in the
   exporter image's documentation).

2. **Scrape it with Prometheus** — add to `prometheus.yml`:

   ```yaml
   scrape_configs:
     - job_name: oracle
       static_configs:
         - targets: ['oracle-exporter:9161']
   ```

3. **Query it from Grafana** — in a panel, switch the datasource to
   **Prometheus** mode (or create a Prometheus-default datasource), enable the
   proxy in the datasource settings, point it at Prometheus, and run e.g.:

   ```promql
   up{job="oracle"}
   oracledb_activity_pga_used_bytes{...}
   rate(oracledb_physical_reads_total{...}[5m])
   ```

The metric browser in the Prometheus tab lists available `oracledb_*` metrics
directly. Alert rules, annotations and variables work in Prometheus mode the
same way they do for SQL queries.

> A Prometheus-only datasource still requires the Oracle **connection** fields
> to be filled in (the plugin builds its DSN at instance construction) — it is
> simply never queried in SQL mode.

### 5.1 Layer 3: alerting on Prometheus `ALERTS` (F14)

The plugin proxies the Prometheus HTTP API as-is, so **Prometheus' own alert
state is queryable**: `ALERTS{alertstate="firing"}` returns one time series per
firing alert (report §10.1 Layer 3). This is the standard way to surface
exporter-driven alerts — e.g. "the Oracle exporter itself is down" — straight
onto a Grafana panel, without a second datasource.

A reference rule ships in the dev environment (`devenv/rules.yml`): with no
exporter running, `up{job="oracle_exporter"} == 0` evaluates true and
`OracleExporterDown` starts firing ~1 minute after Prometheus starts. Query it
through the datasource in **Prometheus** mode:

```promql
ALERTS{alertstate="firing"}
```

Each firing series carries the alert labels (`alertname`, `severity`, …) so a
panel or alert rule can group by them — e.g. `max by (alertname)
(ALERTS{alertstate="firing"})`.

> **Option D — exporter orchestration/health** (optional, **not implemented**):
> auto-starting the exporter container, or health-probing it, is out of scope
> for this datasource. The plugin's job ends at *querying* the exporter's
> metrics; running and health-checking the exporter belongs to your
> orchestration layer (systemd, Docker, Kubernetes). The `up{job=...} == 0`
> alert above covers the common "exporter is down" case regardless.

---

## 6. Using the plugin

- **SQL panels** — any `SELECT`/`WITH` query; format **Auto**/Table/Time series.
  `$__timeFilter`, `$__timeFrom`/`$__timeTo`, `$__unixEpochFilter`,
  `$__unixEpochGroup`, `$__interval`/`_ms`/`_s` macros and `${var:csv}` /
  `${var:singlequote}` variables are expanded on the backend (report §8).
- **Time series** — a query with a `DATE`/`TIMESTAMP` column and a numeric
  column renders as native time series. Add the `$__timeFilter` macro to bound
  it to the panel range.
- **Alerting** — time-series queries with `Reduce(last)` and scalar
  `SELECT COUNT(*)` "rows > 0" rules are verified against Grafana Unified
  Alerting (report §10). See `examples/alerting/`.
- **Dashboard templates** — ready-made panel/dashboard examples that run on any
  Oracle: `examples/dashboard/`.
- **Annotations** — a query annotation on the datasource renders events on the
  panel (report §11).
- **Run command** — the operations UI executes allowlisted
  `DBMS_SCHEDULER`/procedure commands (report §12), never part of the query
  path.

---

## 7. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| "Test" shows a network error | Grafana cannot reach the Oracle listener — check the host/port and firewall |
| `ORA-01017: invalid username or password` | Wrong credentials; the password is never shown in the error |
| `ORA-12514` / `ORA-12154` | Wrong service name / SID — see §3.1 |
| Plugin shows disabled after install | Unsigned plugin without the `allow_loading_unsigned_plugins` flag (§2.1); or a `plugin.json` change that needs a Grafana restart |
| A dashboard query is slower than expected | Use `$__timeFilter` to bound the range; consider Prometheus mode for high-frequency panels; check `maxRows` truncation notice (§3.4) |
| Prometheus mode returns 400 "not implemented / proxy disabled" | Enable the proxy in the datasource settings (§3.2) |
| Autocomplete is empty | The account cannot see the objects (grants) or the metadata cache is cold — it fills on first successful load (§4) |
| Number columns show as strings | `largeNumberMode: Auto` keeps >15-digit `NUMBER` as strings in table panels (ADR-8); switch to `Float` to lose precision deliberately |

---

## 8. Upgrading

1. Stop Grafana.
2. Replace the `emreatayy-oracle-datasource` folder under the plugin directory
   with the new version's archive contents.
3. Start Grafana — the datasource settings are preserved (they live in Grafana's
   database, not in the plugin folder).

The supported version matrix and the EOL policy are maintained in the
[README](../README.md#version-support).

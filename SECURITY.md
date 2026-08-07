# Security Policy

This project is a Grafana data source plugin that connects to Oracle Database.
Its security model and threat considerations are designed in report §15
("Security"); this file describes the practical expectations for
contributors and reporters.

## Supported versions

The security support window follows the [version support matrix](docs/project-report.md#2-version-support-matrix-multi-version).
Only the current release line receives security fixes. Pre-release
(unreleased) code is under development and not a supported surface.

| Version | Supported |
|---|---|
| Latest release | ✅ |
| Development (main) | ✅ (best effort — no release yet) |
| Older releases | ❌ |

## Reporting a vulnerability

**Do not open a public GitHub issue for a security vulnerability.**

Please report it privately to the repository owner:

- **GitHub:** use the repository's private vulnerability reporting form
  (Security → "Report a vulnerability"), or
- **Email:** the maintainer's contact address listed on their GitHub profile
  (repository owner: `emreatayy`).

Please include:

- the affected version / commit,
- a minimal description of the vulnerability and its impact,
- a proof of concept if available,
- whether the issue is already known publicly.

You should receive an acknowledgment within 3 business days, and a fix or a
mitigation plan within a reasonable timeframe. Please keep the issue private
until a fix is released.

## Security-sensitive areas

These areas are treated as high-risk; changes touching them need extra review
and — where possible — a verification-harness check:

- **Command execution** (§12): the "Run command" path executes anonymous
  PL/SQL. It must remain **outside the query path**, restricted to an
  **allowlist**, require user confirmation, and be audited. Never allow alert
  queries, dashboard refreshes, or query variables to trigger it.
- **SQL interpolation** (§8): all macro/variable expansion happens on the
  backend; values must be safely bound or quoted (multi-value `IN(...)` and
  `${var:csv}`), and no interpolated SQL is ever logged.
- **Credentials**: connection passwords and Prometheus credentials are stored
  in Grafana `secureJsonData` and are never logged, echoed, or returned by the
  plugin. The health-check/Test response exposes connectivity and version
  information only.
- **Proxy mode** (§9): outbound requests to the Prometheus endpoint must
  validate the URL, support TLS verification (or an explicit
  skip-verification opt-in), and never leak credentials in errors.

## Vulnerability disclosure

Once a fix is released, a short security note is added to the
[CHANGELOG](CHANGELOG.md) describing the issue, its impact, and the fixed
version — without exposing attacker-useful detail beyond what is necessary.

## Contributor expectations

- Never commit secrets (passwords, signing keys, tokens) — including in
  tests or examples.
- Follow the [contributing guide](CONTRIBUTING.md): design changes land in
  the report first, assumptions are verified empirically, and code keeps the
  query path read-only.

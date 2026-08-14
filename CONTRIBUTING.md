# Contributing to Grafana Oracle Connector

Thanks for taking the time to contribute! This project is small but **design
-first**: risky decisions are written down and verified empirically before
code is written. Please read this page before opening an issue or a pull
request.

## Table of Contents

- [Project conventions](#project-conventions)
- [Design-first workflow](#design-first-workflow)
- [Plugin Installation](#plugin-installation)
- [Pull request process](#pull-request-process)
- [Commit message style](#commit-message-style)
- [Issue templates](#issue-templates)

## Project conventions

- **English-only project content.** All files, code, comments, UI strings,
  log messages, documentation, and commit messages are written in **English**.
  No other language appears in the repository. (Conversation in issue threads
  may use any language, but anything committed to the repository is English.)
- **Code style.** Go code is formatted with `gofmt`/`goimports` and follows
  `go vet` cleanly. TypeScript/React follows the project's ESLint + Prettier
  configuration (introduced with the frontend scaffold in F0).
- **Dependencies.** Third-party libraries are pinned and only added with a
  justification recorded in the design report (§2.5 pins).
- **No secrets.** Never commit passwords, connection strings, or signing keys.
  Connection credentials always live in Grafana's `secureJsonData`.

## Design-first workflow

Implementation follows the 16-phase roadmap in `docs/project-report.md`
(§18). Before significant code:

1. **Update or extend the report.** Feature behavior is specified in the
   relevant report section (type mapping §6, macros §8, alerting §10, command
   execution §12, …).
2. **Record decisions as ADRs.** A new architectural choice gets a numbered
   ADR in §4.2 with the trade-off and the chosen alternative.
3. **Verify assumptions empirically.** If the design depends on a behavior
   that is not certain (go-ora driver behavior, Grafana SDK behavior, Oracle
   SQL semantics), add a check to the `verification/` harness and run it
   against a real Oracle database before relying on the assumption.
4. **Then implement.** Link the pull request to the report section / ADR it
   implements.

## Plugin Installation

Grafana servers can load plugins from the `/var/lib/grafana/plugins` folder.
Download the `emreatayy-oracle-datasource-<version>.tar.gz` or `.zip` release
archive and extract it to this folder.

For a `.tar.gz` archive:

```sh
sudo tar -xzf emreatayy-oracle-datasource-<version>.tar.gz \
  -C /var/lib/grafana/plugins

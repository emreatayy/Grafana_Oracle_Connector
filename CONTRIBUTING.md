# Contributing to Grafana Oracle Connector

Thanks for taking the time to contribute! This project is small but **design
-first**: risky decisions are written down and verified empirically before
code is written. Please read this page before opening an issue or a pull
request.

## Table of Contents

- [Project conventions](#project-conventions)
- [Design-first workflow](#design-first-workflow)
- [Development setup](#development-setup)
- [Running the verification harness](#running-the-verification-harness)
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

## Development setup

Prerequisites: Go ≥ 1.26, Node.js ≥ 22, Docker (Docker Desktop).

```sh
# Clone and enter the project
git clone https://github.com/emreatayy/Grafana_Oracle_Connector.git
cd Grafana_Oracle_Connector

# Verification harness (Go)
cd verification
go mod tidy
```

The plugin build loop (`@grafana/create-plugin` + `mage`) is introduced in F0;
until then the repository contains the design report and the verification
harness only.

## Running the verification harness

The harness validates the design assumptions that are documented in report §21:

```sh
cd verification
docker compose --profile oracle21 up -d      # Oracle 21c XE on 127.0.0.1:1521
docker compose --profile oracle_tr up -d     # Turkish-charset DB on 127.0.0.1:1522 (optional)

ORACLE_PASSWORD=verif_password go run ./probe                     # checks 1, 2, 4, 8
ORACLE_PASSWORD=verif_password ORACLE_PORT=1522 go run ./probe --port 1522   # check 7
```

Wait for the Oracle healthcheck to report `healthy` (a few minutes; the amd64
images run under Rosetta on Apple Silicon). Full instructions:
`verification/README.md`.

## Pull request process

1. **Fork** the repository (or work on a branch for maintainers).
2. **Create a branch** with a descriptive name, e.g.
   `feat/f2-type-mapping` or `fix/f1-timeout`.
3. Make your changes — **commit message style below**.
4. If you changed behavior, update the relevant report section, CHANGELOG.md,
   and (when applicable) the verification harness.
5. Open a **pull request** against `main` using the PR template.
6. The PR should:
   - describe the change and link the report section / ADR it implements,
   - state which verification checks were run and their outcome,
   - keep the diff focused on one concern.

## Commit message style

Conventional Commits, English only:

```
<type>(<scope>): <short summary>

<body — what and why, one paragraph or bullet list>
```

`type`: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `perf`,
`build`, `ci`. `scope` examples: `report`, `verification`, `f0`, `f2`,
`release`. Example:

```
feat(verification): add ANSI-literal index usage check

Explains why: ADR-9 depended on TIMESTAMP literals using the index on DATE
columns; the probe now proves they full-scan and that TO_DATE keeps the
index, which is why ADR-9 was revised.
```

## Issue templates

Please use the GitHub issue templates (bug report / feature request) —
they ask for the report-section references and verification results that make
triage fast.

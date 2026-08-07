# Documentation

This project is design-first: the master document is the technical design and
implementation report; everything else supports it.

| Document | Purpose |
|---|---|
| [project-report.md](project-report.md) | **Master design & implementation report (v2).** Version support matrix, architecture + ADRs, column type mapping, macro/variable engine, Prometheus proxy mode, Unified Alerting, annotations, **command execution**, health, frontend, security, test plan, dev environment/CI, 16-phase roadmap, risks, and the empirically verified assumptions (§21). |
| [release-and-packaging.md](release-and-packaging.md) | How the plugin is packaged, signed, versioned, and released (GitHub Release + Grafana catalog); the planned CI/release automation. |
| [`../verification/README.md`](../verification/README.md) | How to run the empirical verification harness (report §21 checks) against a live Oracle Database. |

## Reading order

New contributors: start with the [README](../README.md), then the report's
[Executive Summary](project-report.md#1-executive-summary) and
[Roadmap](project-report.md#18-implementation-roadmap--16-phases), then jump
into whichever section your work touches.

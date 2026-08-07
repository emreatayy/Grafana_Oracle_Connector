# Release & Packaging

How this plugin is packaged, versioned, signed, and released. This document is
part of the design-first workflow: the **automation** (CI/release workflows)
is created in **F0** (CI) and **F13** (release packaging), once the plugin
builds — per the release/CI decision made with the maintainer on 2026-08-07.
Until then this page is the spec the workflows implement.

## 1. Distribution model — what a Grafana plugin "package" is

Grafana data source plugins are distributed as **signed plugin archives**
(`.zip` / `.tar.gz`), **not** as npm packages, container images, or GitHub
Packages. There is no package manager involved at install time: the archive is
unzipped into Grafana's plugin directory (or installed via `grafana-cli
plugins install <plugin-id>`).

> Why not GitHub Packages / npm? Grafana loads plugins from its own plugin
> directory and requires a Grafana-signed manifest for unattended installs.
> The "package" is therefore the **signed `.zip`**, and the "registry" is
> Grafana's plugin catalog (`https://grafana.com/grafana/plugins/`).

### 1.1 Install methods

| Method | Mechanism | Signature required |
|---|---|---|
| `grafana-cli plugins install <plugin-id>` | Downloads the signed archive from Grafana.com | ✅ |
| Manual ZIP into `plugins/` | Unzip + `allow_loading_unsigned_plugins` (dev only) | ❌ (dev) |
| Docker image plugin mount | Volume-mount into `GF_PATHS_PLUGINS` | ✅ for production |

### 1.2 Artifacts produced by the build

| Artifact | Produced by | Purpose |
|---|---|---|
| `dist/` (uncompressed build) | `mage -v` | Development, plugin-validator input |
| `dist/<plugin-id>-<version>.zip` | `mage archive` | Release asset + catalog upload |
| `dist/<plugin-id>-<version>.tar.gz` | `mage archive` | Release asset (container mounts) |
| `MANIFEST.txt` | `mage sign` (signing key) | Grafana-signed manifest inside the zip |

## 2. Build pipeline (mage)

The plugin uses the standard Grafana plugin build toolchain:

```sh
npx grafana-sdk mage -v        # or: mage -v
mage -v                        # build backend + frontend → dist/
mage buildFrontend             # TypeScript/React → dist/
mage buildBackend              # Go → dist/ (per-OS binaries)
mage lint                      # golangci-lint + eslint
mage archive                   # dist/*.zip + .tar.gz
mage sign                      # sign with GRAFANA_SIGNING_KEY / GRAFANA_API_KEY
mage validate                  # plugin-validator checks
```

Layout expected by the toolchain (created in F0):

```
.
├── src/                 # TypeScript/React frontend
├── pkg/                 # Go backend (plugin + drivers)
├── Magefile.go          # build/archive/sign/validate targets
├── plugin.json          # id, type: datasource, backend: true, alerting/annotations flags
└── go.mod               # module <org>/<plugin-id>
```

### 2.1 Backend binaries

`mage buildBackend` cross-compiles the Go backend for every platform in
`plugin.json` (`executables`): `darwin-amd64`, `darwin-arm64`,
`linux-amd64`, `linux-arm64`, `windows-amd64`. This is why **no OCI/Instant
Client** is required — the pure-Go `go-ora` driver keeps every binary
self-contained.

## 3. Versioning

- **Semantic Versioning** (`MAJOR.MINOR.PATCH`), from the report's release
  policy (§20). The current stage is pre-0.1.0.
- Releases are tagged **`v<version>`** (e.g. `v0.1.0`); the tag is the trigger
  for the release automation.
- `plugin.json`'s `version` and `info.version` are kept in sync with the tag
  (the build reads the version from the tag/`package.json`).
- Pre-releases use semver build metadata / `-rc.N` suffixes.

### 3.1 Version → Grafana compatibility

The `info.version`/`dependencies.grafanaVersion` in `plugin.json` declare the
minimum Grafana (≥ 12.0) and are validated by the Grafana catalog on publish.

## 4. Signing

Grafana plugins must be signed to install without
`allow_loading_unsigned_plugins`. Signing is done via the Grafana signing API
using an API key; the **private signing key is never committed** — it lives in
GitHub Secrets.

### 4.1 Signing levels

| Level | Use | What it proves |
|---|---|---|
| `private` | Testing in your own Grafana instances | Signed, unpublished |
| `public` | Grafana.com catalog / public distribution | Signed + validated by Grafana |

Signing requires the **backend and plugin.json metadata to be stable**, so the
sign step runs on the release tag, not on every commit.

## 5. Release automation (planned — created in F0/F13)

Once the plugin builds, the repository will contain:

### 5.1 CI workflow (`.github/workflows/ci.yml` — created in F0)

- **Trigger:** `push` to `main` and `pull_request`.
- **Steps:** `go vet`/`go test` (backend), ESLint + Prettier (frontend),
  `mage lint`, `mage -v` (build), and — when Oracle containers are available —
  the [verification harness](../verification/README.md) matrix
  (Grafana 12.4 + 13.1, Oracle XE 18c/21c/23c) from the version matrix (§2).

### 5.2 Release workflow (`.github/workflows/release.yml` — created in F13)

- **Trigger:** tags `v*` matching the version policy.
- **Steps** (standard Grafana plugin release flow):
  1. `mage -v` → build `dist/`
  2. `mage archive` → produce `.zip` + `.tar.gz`
  3. `mage sign` → sign with the `GRAFANA_API_KEY` secret (or the private
     signing key) at the configured signing level
  4. `mage validate` → run `plugin-validator`
  5. **GitHub Release** — publish the signed archives as release assets with
     the changelog notes
  6. *(optional, when ready)* **Grafana catalog publish** — submit to
     `https://grafana.com/grafana/plugins/` for public distribution

### 5.3 Required GitHub secrets

| Secret | Used for |
|---|---|
| `GRAFANA_API_KEY` | Grafana signing API (public/private signing) |
| (optional) `GITHUB_TOKEN` | auto-provided; GitHub Release creation |

Secrets are added once in F13, when the release workflow is created. Until
then the plugin is distributed as an unsigned `dist/` archive for local
testing only.

## 6. Publishing to the Grafana catalog

Public distribution (Phase 6 of the release workflow) requires:

1. A **publicly** signed plugin archive,
2. a `README` (plugin catalog page) written from the plugin's `README.md`,
3. the plugin to pass Grafana's validation rules (`mage validate` / the
   catalog submission checks).

This is intentionally a **late** step (after F13), gated behind the maintainer
decision to go public — the repository is currently private.

## 7. Reproducibility & integrity

- Dependencies are pinned (`go.mod`/`go.sum`, `package-lock.json`); version
  pins are recorded in report §2.5.
- Each release asset ships its SHA-256 checksum (computed in the workflow and
  attached to the GitHub Release).
- `SBOM` generation (e.g. `govulncheck` + dependency scan) is part of the
  F11 security phase; findings are published with the release.

## 8. Rollback & support

- A release can be rolled back by installing the previous version's archive
  (or reverting the `info.version`). Grafana keeps previously installed plugin
  versions available for downgrade.
- Security fixes follow [SECURITY.md](../SECURITY.md); the supported surface is
  the current release line (report §2).

## 9. Verification plan for this document

When the workflows are created (F0 for CI, F13 for release), these are the
DoD checks:

- [ ] `mage -v` produces a valid `dist/` from a clean checkout
- [ ] `mage archive` + `mage sign` produce a signed zip that installs via
      `grafana-cli plugins install <plugin-id>` on Grafana 12.4 and 13.1
- [ ] `plugin-validator` reports zero errors
- [ ] Tag `v0.1.0` triggers the release workflow and publishes assets
- [ ] The published archive loads without `allow_loading_unsigned_plugins`

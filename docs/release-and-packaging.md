# Release & Packaging

How this plugin is packaged, versioned, signed, and released. This document is
part of the design-first workflow: the **automation** (CI/release workflows)
is created in **F0** (CI — **done 2026-08-07**, see §5.1) and **F13**
(release packaging), once the plugin builds — per the release/CI decision made
with the maintainer on 2026-08-07. Until then this page is the spec the
workflows implement.

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
| `MANIFEST.txt` | `npm run sign` (sign-plugin) | Grafana-signed manifest inside the zip |

## 2. Build pipeline (mage)

The plugin uses the standard Grafana plugin build toolchain. The targets below
are the ones that exist in this repository (`mage -l` lists them; `buildAll` is
the default target):

```sh
mage -v                        # default target: backend + frontend → dist/
npm run build                  # frontend only (TypeScript/React → dist/)
mage buildAll                  # or: the default; backend for all platforms
mage test                      # backend tests
mage lint                      # golangci-lint run ./... (backend style)
npm run lint                   # eslint (frontend)
mage archive                   # dist/<plugin-id>-<version>.zip + .tar.gz
npm run sign                   # sign dist/ with GRAFANA_ACCESS_POLICY_TOKEN
mage validate                  # plugin-validator checks the release archive
mage clean                     # remove dist/ and coverage/
```

`mage archive` produces the `.zip` **and** `.tar.gz` together, each with the
single top-level `<plugin-id>/` directory Grafana installs expect. `mage
validate` runs `@grafana/plugin-validator` against the latest archive in
`dist/` and must report **zero errors**.

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

`mage buildAll` (the default `mage -v`) cross-compiles the Go backend for every
platform in `plugin.json` (`executables`): `darwin-amd64`, `darwin-arm64`,
`linux-amd64`, `linux-arm64`, `windows-amd64`. This is why **no OCI/Instant
Client** is required — the pure-Go `go-ora` driver keeps every binary
self-contained.

## 3. Versioning

- **Semantic Versioning** (`MAJOR.MINOR.PATCH`), from the report's release
  policy (§20). The current version is **1.0.0** (in `plugin.json`,
  `package.json` and the `dist/` archives).
- Releases are tagged **`v<version>`** (e.g. `v1.0.0`); the tag is the trigger
  for the release automation.
- `plugin.json`'s `version` and `info.version` are kept in sync with the tag
  (the build reads the version from the tag/`package.json`).
- Pre-releases use semver build metadata / `-rc.N` suffixes.

### 3.1 Version → Grafana compatibility

The `info.version`/`dependencies.grafanaVersion` in `plugin.json` declare the
minimum Grafana (≥ 12.0) and are validated by the Grafana catalog on publish.

## 4. Signing

Grafana plugins must be signed to install without
`allow_loading_unsigned_plugins`. Signing is done with **`@grafana/sign-plugin`**
(the `npm run sign` script) against the Grafana signing API. It needs a
**Grafana access policy token** in the `GRAFANA_ACCESS_POLICY_TOKEN` environment
variable (the legacy `GRAFANA_API_KEY` still works but is deprecated); the
**token is never committed** — it lives in GitHub Secrets. The command signs
`dist/` in place (writes `MANIFEST.txt`) *before* `mage archive`, so the
manifest ends up inside the archives.

```sh
GRAFANA_ACCESS_POLICY_TOKEN='glpat_...' npm run sign -- --signatureType=private
GRAFANA_ACCESS_POLICY_TOKEN='glpat_...' npm run sign -- --signatureType=public --rootUrls=https://grafana.example.com
```

### 4.1 Signing levels

| Level | Use | What it proves |
|---|---|---|
| `private` | Testing in your own Grafana instances | Signed, unpublished |
| `public` | Grafana.com catalog / public distribution | Signed + validated by Grafana |

Signing requires the **backend and plugin.json metadata to be stable**, so the
sign step runs on the release tag, not on every commit.

## 5. Release automation (CI created in F0, release workflow in F13)

Both workflows exist in `.github/workflows/`:

### 5.1 CI workflow (`.github/workflows/ci.yml` — created in F0)

- **Trigger:** `push` to `main` and `pull_request`.
- **Steps:** `go vet`/`go test` (backend), ESLint + Prettier (frontend),
  `mage lint`, `mage -v` (build), and — when Oracle containers are available —
  the [verification harness](../verification/README.md) matrix
  (Grafana 12.4 + 13.1, Oracle XE 18c/21c/23c) from the version matrix (§2).

### 5.2 Release workflow (`.github/workflows/release.yml` — created in F13)

- **Trigger:** tags `v*` matching the version policy, or manual
  `workflow_dispatch` (which lets you pick `--signatureType` and `--rootUrls`).
- **Steps** (standard Grafana plugin release flow — the file implements all of
  them):
  1. `npm ci`, then `mage buildAll` → build `dist/`
  2. `npm run sign` → sign `dist/` with `GRAFANA_ACCESS_POLICY_TOKEN`; the step
     is **skipped gracefully when the secret is not set** (the archives are
     then published unsigned — private use only)
  3. `mage archive` → produce `.zip` + `.tar.gz` containing `MANIFEST.txt`
  4. `mage validate` → run `plugin-validator`
  5. **GitHub Release** — publish the archives as release assets with
     auto-generated release notes
  6. *(optional, when ready)* **Grafana catalog publish** — submit to
     `https://grafana.com/grafana/plugins/` for public distribution

### 5.3 Required GitHub secrets

| Secret | Used for |
|---|---|
| `GRAFANA_ACCESS_POLICY_TOKEN` | Grafana signing API (private/public signing) |
| (optional) `GITHUB_TOKEN` | auto-provided; GitHub Release creation |

Signing secrets are **not required** until a signed release is wanted: without
`GRAFANA_ACCESS_POLICY_TOKEN` the release workflow publishes the unsigned
archives (`allow_loading_unsigned_plugins` needed to install them — see
[installation.md](installation.md) §2.1). Public signing additionally needs the
catalog-release decision of §6.

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

Status after F13 (verified locally, 2026-08-11):

- [x] `mage -v` produces a valid `dist/` from a clean checkout (`mage archive`
      + unzip-install layout checked)
- [x] `mage archive` produces `dist/emreatayy-oracle-datasource-1.0.0.zip` +
      `.tar.gz` with the single top-level `<plugin-id>/` directory
- [x] `npm run sign` documented and wired into the workflow (runs on the tag
      with `GRAFANA_ACCESS_POLICY_TOKEN`); the signed-zip install check needs a
      signing token and a Grafana host
- [x] `plugin-validator` reports **zero errors** (10 follow-up warnings are all
      publish-flow: private repo, unsigned, screenshots, catalog account)
- [ ] Tag `v1.0.0` triggers the release workflow and publishes assets — needs
      the tag pushed to GitHub (the workflow is created and ready)
- [ ] The published archive loads without `allow_loading_unsigned_plugins` —
      needs a signed release

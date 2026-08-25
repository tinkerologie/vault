# AGENTS.md — Vault Agent Instructions

This is the repository-wide source of truth for AI coding assistants. Keep it
short, durable, and aligned with the current source. Path-specific rules live
in `.github/instructions/`; optional GitHub Copilot profiles live in
`.github/agents/`.

When documentation and source disagree, verify the source and update the
documentation in the same change.

## Project

- **Product:** Vault, a third-party Unraid backup and restore plugin
- **Runtime:** Go daemon on Linux/amd64, built with `CGO_ENABLED=0`
- **UI:** Svelte 5 single-page application embedded in the Go binary
- **API:** Chi v5 REST API, WebSocket events, and MCP endpoint
- **Database:** SQLite via `modernc.org/sqlite`
- **Repository:** `github.com/ruaan-deysel/vault`

Vault runs with privileged access to Docker, libvirt, filesystems, and storage
credentials. Treat restore, deletion, deployment, database replacement, and
path remapping as sensitive operations.

## Read Before Editing

Read the source that owns the contract you are changing:

| Area                      | Authoritative source                                         |
| ------------------------- | ------------------------------------------------------------ |
| API routes and middleware | `internal/api/routes.go`, `internal/api/middleware.go`       |
| Handler response helpers  | `internal/api/handlers/respond.go`                           |
| Database open/schema      | `internal/db/db.go`, `internal/db/migrations.go`             |
| Engine contracts          | `internal/engine/types.go`                                   |
| Storage contracts/factory | `internal/storage/adapter.go`, `internal/storage/factory.go` |
| Backup execution          | `internal/runner/`                                           |
| Scheduling                | `internal/scheduler/`                                        |
| Web UI                    | `web/src/`, `web/package.json`                               |
| Build and packaging       | `Makefile`, `ansible/`, `plugin/vault.plg`                   |
| Release automation        | `.github/workflows/`, `VERSION`, `CHANGELOG.md`              |

Do not copy interface signatures, endpoint lists, dependency versions, or
schema inventories from this file without checking those sources.

## Architecture

```text
Cobra CLI
  └─ API server (Chi + WebSocket + MCP)
      ├─ handlers
      ├─ scheduler
      ├─ runner
      │   ├─ engine handlers
      │   └─ storage adapters
      └─ SQLite repositories
```

Important supporting packages include `internal/anomaly`, `internal/crypto`,
`internal/dedup`, `internal/diagnostics`, `internal/discovery`,
`internal/replication`, `internal/release`, `internal/safepath`, and
`internal/unraid`.

Follow existing wiring. Do not introduce a second router, bypass the storage
factory, duplicate runner behavior in handlers, or add speculative
abstractions.

## Core Constraints

- Use idiomatic Go, `gofmt`, and `goimports`.
- Wrap errors with useful context using `%w`.
- Propagate `context.Context` through cancellable work.
- Keep the binary pure Go; do not introduce CGO dependencies.
- Put Linux-only implementations behind build tags and keep non-Linux builds
  working with an appropriate stub.
- Keep SQLite in WAL mode with DSN pragmas applied to every connection.
- Validate input at API, filesystem, storage, and restore trust boundaries.
- Never log credentials, API keys, passphrases, cookies, private keys, or
  sensitive configuration blobs.
- Reuse `internal/safepath` for untrusted or storage-relative paths.
- Preserve unrelated working-tree changes.

## Repository Layout

```text
cmd/vault/          CLI entry point
internal/api/       HTTP server, middleware, routes, handlers
internal/db/        SQLite handle, schema, repositories, snapshots
internal/engine/    Container, VM, folder, plugin, and ZFS operations
internal/runner/    Job execution, upload, restore, verification, dedup
internal/storage/   Local, SFTP, SMB, NFS, WebDAV, and S3 adapters
internal/scheduler/ Cron scheduling and maintenance jobs
internal/ws/        WebSocket hub
web/                Svelte 5 application and Vitest tests
plugin/             Unraid plugin metadata, PHP pages, assets, service script
ansible/            Build, deployment, and live verification
docs/               User and maintainer documentation
```

## Search

Use `rg` for ordinary source and text searches. Use `ast-grep` only when the
query genuinely depends on syntax. `ast-grep` does not parse Svelte files;
use `rg` or Svelte-aware tooling there.

Before changing a shared function or interface, find every caller. Fix a bug
at the common root when possible and add the smallest regression check that
would fail without the fix.

## Build and Test

### Local checks

```bash
make test
make lint
make security-check
make pre-commit-run
make build-local
```

Web-only checks:

```bash
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
```

Use targeted tests while iterating, then run the checks proportional to the
change. `make test` prepares the embedded changelog before running Go tests.

### Ansible lifecycle

Build without changing the Unraid host:

```bash
make build
```

After explicit user approval and confirmation of the intended deployment
configuration:

```bash
make deploy
make verify
```

Only for an explicitly requested clean reinstall:

```bash
make redeploy
```

- `make build` runs the Ansible build path, including lint, tests, web build,
  and Linux/amd64 compilation.
- `make deploy` changes the configured Unraid host and restarts Vault.
- `make verify` runs read-only live health, API, WebSocket, and MCP checks.
- `make redeploy` includes uninstall and can remove configured plugin state.

**Never run `make deploy`, `make verify`, or `make redeploy` without explicit
user approval and confirmed local deployment configuration.** Prefer
`make build deploy verify` for an approved routine release iteration.
Use `make redeploy` only when the user explicitly wants a clean reinstall.

## Validation and Release Gate

For ordinary implementation work:

1. Run targeted tests for the changed behavior.
2. Run applicable Go and web lint/tests/builds.
3. Run `make pre-commit-run` before committing.
4. Run CodeRabbit CLI review when changes are intended for integration.

For release-facing binary or UI changes, after user approval:

1. `make build`
2. `make deploy`
3. `make verify`
4. Verify affected UI flows with Playwright or browser tooling.
5. Run CodeRabbit CLI review and resolve validated findings.
6. Update `CHANGELOG.md` last.

Do not claim deployment, live verification, UI verification, or CodeRabbit
approval unless that step actually completed. Documentation-only changes do
not require the live release gate.

## Changelog

`CHANGELOG.md` is consumed by the application and release workflow. Add a
concise entry under `## [Unreleased]` for user-visible or release-facing
changes.

Supported section headings:

- `### Added`
- `### Changed`
- `### Fixed`
- `### Removed`
- `### Security`

Bullets must start with a dash followed by a space at column zero. At release
time, promote the section to `## [v<contents-of-VERSION>] - YYYY-MM-DD` before
pushing the matching `v<contents-of-VERSION>` tag. For example, `VERSION` value
`2026.07.10` uses heading `## [v2026.07.10] - 2026-07-24` and tag
`v2026.07.10`.

## Deployment Paths

- Binary: `/usr/local/sbin/vault`
- Database: `/boot/config/plugins/vault/vault.db`
- Configuration: `/boot/config/plugins/vault/vault.cfg`
- Plugin UI/assets: `/usr/local/emhttp/plugins/vault`
- Service script: `/etc/rc.d/rc.vault`
- Default API port: `24085`

Never commit `ansible/inventory.yml`, credentials, real hostnames, or private
network addresses.

## Versioning

`VERSION` is the release version source and uses `YYYY.MM.PATCH`. Build
metadata is injected with linker flags defined in the `Makefile`.

## Git

- Use Conventional Commits.
- Stage only the files required by the task.
- Do not discard, overwrite, or reformat unrelated user changes.
- Inspect branch and remote divergence before merge or push operations.
- Do not commit, push, merge, tag, release, or open a pull request unless the
  user requests it.

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues on `ruaan-deysel/vault` via the `gh` CLI.
See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical labels: `needs-triage`, `needs-info`, `ready-for-agent`,
`ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout: one `CONTEXT.md` at the repo root plus ADRs in
`docs/adr/`. See `docs/agents/domain.md`.

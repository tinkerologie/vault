## Description

<!-- Provide a clear, concise summary of what changes this PR introduces and why. -->

<!-- Reference related issue(s) using closing keywords (e.g., Closes #123, Fixes #456, Refs #789). -->

Closes #

## Type of Change

<!-- Mark the appropriate type of change with an "x": -->

- [ ] `fix`: Bug fix (non-breaking change which fixes an issue)
- [ ] `feat`: New feature (non-breaking change which adds functionality)
- [ ] `refactor`: Code refactoring without behavioral changes
- [ ] `perf`: Performance improvement
- [ ] `docs`: Documentation updates or additions
- [ ] `test`: Test suite additions or improvements
- [ ] `chore`: Build, CI, dependencies, or tooling changes

## Area & Target

<!-- Mark relevant affected areas and backup targets: -->

- **Area:**
  - [ ] `area: api` / `area: mcp` / `area: replication`
  - [ ] `area: engine` / `area: runner`
  - [ ] `area: storage` / `area: safepath`
  - [ ] `area: db` / `area: anomaly`
  - [ ] `area: scheduler`
  - [ ] `area: web` (Svelte UI)
  - [ ] `area: plugin` (Unraid integration / PHP / packaging)
  - [ ] `area: infra` / `area: docs`
- **Target (if backup-engine related):**
  - [ ] `target: containers` (Docker)
  - [ ] `target: vms` (Libvirt/KVM)
  - [ ] `target: folders` (Flash / Filesystems)
  - [ ] `target: plugins` (Unraid Plugins)
  - [ ] `target: flash` (Unraid USB Flash)
  - [ ] N/A

## Key Changes

<!-- Outline the main changes made across packages or components: -->

- **`path/to/component`**: Summary of change.
- **`path/to/component`**: Summary of change.

## Screenshots / Previews (UI Changes Only)

<!-- If this PR changes the Web UI or Unraid pages, include before/after screenshots or recordings. -->

| Before          | After           |
| --------------- | --------------- |
| _(Image / N/A)_ | _(Image / N/A)_ |

## Security & Sensitive Operations

<!-- Vault runs with privileged access on Unraid. Review sensitive touchpoints: -->

- [ ] No hardcoded secrets, API tokens, passwords, private keys, or private host addresses.
- [ ] Untrusted or storage-relative paths use `internal/safepath`.
- [ ] Sensitive operations (restore, deletion, database changes, remapping) preserve safety boundaries.
- [ ] N/A — No sensitive logic or path handling changed.

## Verification & Test Plan

<!-- Document the checks and automated tests performed to validate this PR: -->

- [ ] Targeted Go unit/integration tests run (`go test ./internal/...`)
- [ ] Full backend tests run (`make test`)
- [ ] Go linting and formatting clean (`make lint`, `gofmt`, `goimports`)
- [ ] Security scans passed (`make security-check`)
- [ ] Frontend tests passing (`npm --prefix web test`) _(if `web/` touched)_
- [ ] Frontend linting & build clean (`npm --prefix web run lint`, `npm --prefix web run build`) _(if `web/` touched)_
- [ ] Pre-commit hooks passed (`make pre-commit-run`)
- [ ] _(Maintainer / Approved)_ Live Unraid test completed (`make build deploy verify`)

## Checklist

- [ ] PR title follows [Conventional Commits](https://www.conventionalcommits.org/) (e.g., `feat(ui): ...`, `fix(engine): ...`, `docs: ...`).
- [ ] Linked relevant GitHub issue(s) above.
- [ ] `CHANGELOG.md` updated under `## [Unreleased]` for user-visible / release-facing changes (`### Added`, `### Changed`, `### Fixed`, `### Removed`, `### Security`).
- [ ] Pure Go maintained (`CGO_ENABLED=0`) with SQLite WAL mode and connection pragmas respected.
- [ ] CodeRabbit / automated review findings reviewed and addressed.

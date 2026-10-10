# Pull Request

## Description

<!-- Provide a clear, concise summary of what changes this PR introduces and why. -->

## Type of Change

<!-- Check all that apply: -->

- [ ] `fix`: Bug fix (non-breaking change which fixes an issue)
- [ ] `feat`: New feature (non-breaking change which adds functionality)
- [ ] `refactor`: Code refactoring without behavioral changes
- [ ] `perf`: Performance improvement
- [ ] `docs`: Documentation updates or additions
- [ ] `test`: Test suite additions or improvements
- [ ] `chore`: Build, CI, dependencies, or tooling changes

## Pre-Submission Governance

<!--
  REQUIRED for everyone, including AI agents and automation.
  Every box below is mandatory. If any box is left unchecked, the automated
  "PR Governance" check fails and the PR will not be reviewed or merged.
  Do not open multiple overlapping or back-to-back PRs for the same work;
  batch related changes together to avoid wasting CI runner capacity.
-->

- [ ] I built and ran the project locally and verified this change actually works (not just that it compiles)
- [ ] I ran `make test` locally and all backend tests pass
- [ ] I ran `make pre-commit-run` (lint + security checks) locally and it passes
- [ ] I pasted real local verification output under **Testing Performed** below (no placeholder text)
- [ ] This PR is self-contained and is not a duplicate; I have not opened other overlapping or back-to-back PRs for the same change
- [ ] If an AI agent created or assisted with this PR, a human reviewed and verified the changes before submission

## Related Issues

<!-- Link to related issues, or specify "None" for self-contained changes -->
<!-- Examples: Fixes #123, Closes https://github.com/..., Related to #456, Part of #789, or None -->

Fixes #

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

## Changes Made

<!-- Outline the main changes made across packages or components: -->

-
-
-

## Security & Sensitive Operations

<!-- Vault runs with privileged access on Unraid. Review sensitive touchpoints: -->

- [ ] No hardcoded secrets, API tokens, passwords, private keys, or private host addresses
- [ ] Untrusted or storage-relative paths use `internal/safepath`
- [ ] Sensitive operations (restore, deletion, database changes, remapping) preserve safety boundaries
- [ ] N/A — No sensitive logic or path handling changed

## Testing Performed

<!-- Document the checks and automated tests performed to validate this PR: -->

- [ ] Targeted Go unit/integration tests run (`go test ./internal/...`)
- [ ] Full backend tests run (`make test`)
- [ ] Go linting and formatting clean (`make lint`, `gofmt`, `goimports`)
- [ ] Security scans passed (`make security-check`)
- [ ] Frontend tests passing (`npm --prefix web test`) (if `web/` touched)
- [ ] Frontend linting & build clean (`npm --prefix web run lint`, `npm --prefix web run build`) (if `web/` touched)
- [ ] Pre-commit hooks passed (`make pre-commit-run`)
- [ ] Not applicable (documentation-only or metadata change)

### Test Results

```text
[Paste relevant local test command output and verification notes]
```

## Documentation

<!-- Check all that apply -->

- [ ] Code comments added/updated
- [ ] Documentation updated in `docs/`
- [ ] README.md updated (if needed)
- [ ] CHANGELOG.md updated under [Unreleased]
- [ ] No documentation needed

## Breaking Changes

<!-- If this PR introduces breaking changes, describe them and provide migration instructions -->

## Checklist

<!-- Ensure you've completed all required items before submitting -->

- [ ] I have updated CHANGELOG.md under [Unreleased] with details of this change
- [ ] PR title follows Conventional Commits (e.g., `feat(ui): ...`, `fix(engine): ...`, `docs: ...`)
- [ ] Linked relevant GitHub issue(s) above or noted "None"
- [ ] Pure Go maintained (`CGO_ENABLED=0`) with SQLite WAL mode and connection pragmas respected
- [ ] I have performed a self-review of my own code
- [ ] My changes generate no new warnings
- [ ] I have added tests that prove my fix is effective or that my feature works (if applicable)
- [ ] New and existing tests pass locally with my changes
- [ ] No sensitive information (tokens, passwords, personal data) is included

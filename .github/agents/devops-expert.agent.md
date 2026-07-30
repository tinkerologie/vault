---
name: Vault DevOps
description: Maintains Vault's Make, Ansible, Unraid packaging, and GitHub Actions delivery paths.
disable-model-invocation: true
user-invocable: true
---

# Vault DevOps

Read `AGENTS.md`, `Makefile`, the affected Ansible role or workflow, and
`plugin/vault.plg` before changing delivery behavior.

## Scope

- Local and Linux/amd64 builds
- Ansible build, deploy, and verify roles
- Unraid package layout and service lifecycle
- GitHub Actions build and release workflows
- VERSION, changelog embedding, package checksum, and release assets

## Rules

- Keep `CGO_ENABLED=0`.
- Preserve the package binary at `/usr/local/sbin/vault` and service script at
  `/etc/rc.d/rc.vault`.
- Keep credentials and real hosts out of tracked files.
- Use least-privilege workflow permissions.
- Verify action-version and pinning changes against current official action
  documentation and repository policy.
- Keep `CHANGELOG.md` as the root source; build paths must prepare
  `internal/release/CHANGELOG.md` before compiling.
- Inspect install, update, uninstall, and rollback behavior before changing the
  plugin lifecycle.

`make deploy`, `make verify`, and `make redeploy` affect an external Unraid
host. Run them only with explicit user approval and confirmed local deployment
configuration targeting the intended host. `make redeploy` includes uninstall
and must never be the default iteration command.

Report commands actually run and distinguish local validation from live-host
verification.

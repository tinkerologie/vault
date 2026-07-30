# GitHub Copilot Instructions

Read and follow [`../AGENTS.md`](../AGENTS.md). It is the repository-wide
source of truth. Apply matching files from `.github/instructions/` when
working in those paths.

- Verify contracts against current source before editing; do not trust copied
  interface, route, schema, or dependency lists.
- Preserve unrelated working-tree changes.
- Prefer the smallest change that fixes the root cause.
- Run targeted checks while iterating and the applicable Go or web checks
  before completion.
- Use `make test`, not bare `go test ./...`, when running the complete Go suite
  because the changelog embed must be prepared first.
- Do not run `make deploy`, `make verify`, or `make redeploy` without explicit
  user approval and confirmed local deployment configuration.
- Never use `make redeploy` for routine iteration; it includes uninstall.
- Add a valid `CHANGELOG.md` entry for user-visible or release-facing changes.
- Never commit secrets, `ansible/inventory.yml`, real hostnames, or private
  network addresses.

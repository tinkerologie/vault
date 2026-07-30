---
name: Vault Documentation
description: Writes and audits Vault documentation against current source and user-visible behavior.
---

# Vault Documentation

Read `AGENTS.md`, the relevant source, and the existing target document before
writing.

## Sources

Use these in order:

1. `AGENTS.md` and matching path-specific instructions
2. Current Go, Svelte, plugin, Ansible, and workflow source
3. Existing tests and observable UI/API behavior
4. Existing documentation style
5. Official upstream documentation for version-sensitive dependencies

## Rules

- Update the smallest affected section; preserve accurate surrounding text.
- Verify commands against `Makefile` and routes against
  `internal/api/routes.go`.
- Link to authoritative source instead of copying large contracts that will
  drift.
- Use placeholders such as `<unraid-server>`; never expose real credentials,
  hosts, or private addresses.
- Keep end-user language plain and distinguish validated behavior from planned
  behavior.
- Check relative links, headings, code fences, and Markdown formatting.
- Add a changelog entry only when the documentation change is user-visible or
  release-facing.

Return the changed paths, the claims verified against source, and any remaining
gap. Do not invent output logs or new documentation directories.

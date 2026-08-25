# Domain docs

Layout: **single-context**.

- `CONTEXT.md` at the repo root — the domain overview: what Vault is, its
  core concepts, and the vocabulary agents should use.
- `docs/adr/` — architecture decision records, one file per decision,
  numbered sequentially (for example `0001-sqlite-wal-mode.md`).

Neither exists yet; create them with the `domain-modeling` or
`codebase-design` skills when needed.

## Consumer rules

- Read `CONTEXT.md` before making changes that touch domain concepts.
- Check `docs/adr/` before reversing or working around an existing
  architectural decision. If a change supersedes an ADR, add a new ADR that
  references the old one — never rewrite an accepted ADR's decision in
  place.
- When documentation and source disagree, verify the source and update the
  documentation in the same change (per `AGENTS.md`).

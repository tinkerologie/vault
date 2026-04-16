---
description: "Refines Vault GitHub issues with acceptance criteria, technical considerations, edge cases, and NFRs grounded in the project's layered architecture."
name: "Refine Requirement or Issue"
tools:
  [
    "list_issues",
    "github_repo",
    "search",
    "create_issue_comment",
    "create_issue",
    "update_issue",
    "delete_issue",
    "get_issue",
    "search_issues",
  ]
---

# Refine Requirement or Issue — Vault

> Read [`../../AGENTS.md`](../../AGENTS.md) first. Any refinement must respect Vault's layering (CLI → API → Handlers → DB / Storage / Engine), build-tag constraints, and Ansible-driven deploy loop.

When activated, this mode analyzes an existing issue in `ruaan-deysel/vault` and enriches it with structured, Vault-aware detail.

## Enrichment Output

Every refined issue must end with these sections:

1. **Detailed description** — context, motivation, user impact
2. **Acceptance criteria** — testable checks, ideally mappable to `make test` / `make verify` / Playwright steps
3. **Technical considerations** — which layer(s) it touches, which interfaces (`storage.Adapter`, `engine.Handler`), which DB columns, build-tag implications, whether the plugin payload or `VERSION` changes
4. **Edge cases & risks** — concurrency, platform (Linux vs. macOS dev), backwards compatibility with existing jobs/restore points, migration safety (remember: `CREATE TABLE IF NOT EXISTS` + tolerant `ALTER TABLE`)
5. **Non-functional requirements** — performance, security (CGO=0, no secret logging), observability (WebSocket progress, `/api/v1/health`), upgrade safety

## Steps to Run

1. Fetch the issue with `get_issue` and read it verbatim
2. Search related code with `search` and `github_repo` — do not paraphrase from memory
3. Cross-reference `AGENTS.md` and the relevant `.github/instructions/*.md` file for the touched layer
4. Check for an existing prompt under `.github/prompts/` that already scaffolds the work (Add API Endpoint, Add Storage Adapter, Add Engine Handler, Add Scheduler Job Type, Debug Backup Issue) — link to it
5. Rewrite the issue description with the sections above — keep the original request verbatim at the top under "Original request"
6. Add suggested labels from `.github/labels.yml` where applicable
7. Propose effort (S / M / L / XL) with a one-sentence justification
8. Flag anything that needs maintainer input before work can start (scope, deprecation, breaking changes)

## Vault-Specific Refinement Heuristics

- Mention of "backup X" → specify which `engine.Handler` (container or VM or new type) and which build-tag file gets the real impl + stub
- Mention of "store to Y" → specify the `storage.Adapter` implementation, config struct, and factory case to add
- Mention of "schedule Z" → call out the cron expression, DB schema delta, and scheduler dispatch wiring
- Mention of "UI" → call out the Svelte page(s) in `web/`, the API it consumes, and the Playwright verification needed
- Mention of "release" → call out `VERSION`, `CHANGELOG.md`, and the `.plg` SHA update flow

## Usage

Reference an existing issue in your prompt:

```
refine ruaan-deysel/vault#<issue-number>
```

Or paste the issue URL.

## Output

The agent updates the issue body (via `update_issue`) with the enriched description and posts a comment (via `create_issue_comment`) summarizing what was added. It does not close, reassign, or label the issue without explicit instruction.

## Constraints

- Do not invent requirements the reporter did not express — if inference is needed, mark it "Assumption:"
- Do not reveal or embed secrets, credentials, or real hostnames
- Preserve the original report — never overwrite it
- Prefer linking to existing Vault prompts and instructions over restating them

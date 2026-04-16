---
description: "Debug mode for Vault. Systematically isolates failures across the Scheduler → Engine → Storage → DB → WebSocket layers and uses the project's existing reproduction commands."
name: "Debug Mode"
tools:
  [
    "edit/editFiles",
    "search",
    "execute/getTerminalOutput",
    "execute/runInTerminal",
    "read/terminalLastCommand",
    "read/terminalSelection",
    "search/usages",
    "read/problems",
    "execute/testFailure",
    "web/fetch",
    "web/githubRepo",
    "execute/runTests",
  ]
---

# Debug Mode — Vault

> Read [`../../AGENTS.md`](../../AGENTS.md) first for the architecture and build commands. For backup/restore failures, also read [`../prompts/Debug Backup Issue.prompt.md`](../prompts/Debug%20Backup%20Issue.prompt.md).

You are in debug mode. Your objective is to reproduce, isolate, and fix a bug without drifting into scope creep. Follow the phases below.

## Phase 1: Problem Assessment

1. **Gather context**
   - Read error messages, stack traces, failing test output
   - Identify which layer the symptom lives in (use the triage table below)
   - Note expected vs. actual behavior
   - Check recent git history (`git log -- <path>`) for changes that could have introduced the bug

2. **Reproduce the bug** before editing anything
   - Unit / table-driven test: `go test ./internal/<pkg>/... -run <TestName> -v`
   - Local daemon: `./build/vault daemon --db=vault.db --addr=:24085`
   - API: `curl` the failing endpoint against the local daemon or Unraid deployment
   - Cross-compile/deploy check: `make build` (full Ansible lint + test + web build + Linux/amd64 binary)

### Vault Triage Table

| Symptom                            | First place to look                                   |
| ---------------------------------- | ----------------------------------------------------- |
| Test fails on macOS but not Linux  | Missing build tag or stub in `internal/engine/`       |
| "connection refused" / timeout     | `internal/storage/` adapter (SFTP, SMB, NFS, local)   |
| "permission denied" on files       | Engine handler (Docker SDK or libvirt RPC)            |
| "no such container" / "no such VM" | Engine handler — `ListItems()` or runtime lookup      |
| "database is locked"               | `internal/db/` — WAL mode / busy timeout / open `rows`|
| Job stuck, no progress             | `internal/scheduler/` or `internal/ws/` hub           |
| 404 on a route that should exist   | `internal/api/routes.go` (Chi registration)           |
| WebSocket client never receives    | Hub in `internal/ws/` — register/broadcast ordering   |
| Panic in daemon                    | Recoverer middleware logs it — `internal/api/server.go` |

## Phase 2: Investigation

3. **Root cause analysis**
   - Trace the execution path across Scheduler → Engine → Storage → DB → WebSocket
   - Inspect variable state and error wrapping at each boundary — Vault wraps with `fmt.Errorf("context: %w", err)`, so unwrap to find the origin
   - Look for classic Go pitfalls: loop-variable capture, `defer` in loops, unclosed `rows`, missing context propagation, nil interface vs. nil concrete
   - For platform-specific code, check `//go:build linux` and its `vm_stub.go` / similar stub
   - Check if the issue only manifests on Unraid — the daemon runs under `rc.vault`; stdio goes through syslog

4. **Hypothesis formation**
   - Write each hypothesis as a one-liner with a verification step
   - Prioritize by likelihood × blast radius
   - Reject hypotheses that can't be tested with the tools you have

## Phase 3: Resolution

5. **Implement the fix**
   - Minimal, targeted change at the root cause — do not refactor adjacent code "while you're there"
   - Follow existing patterns: handler → repo → adapter layering, `respondJSON`/`respondError`, error wrapping with context
   - If a platform-specific code path changes, update its stub counterpart
   - Keep CGO_ENABLED=0 — no new C dependencies

6. **Verify**
   - Re-run the exact reproduction
   - `make test` (full suite) — catches regressions
   - `make lint` — zero tolerance for new lint findings
   - `make pre-commit-run` — gosec + govulncheck + go mod verify
   - For daemon-affecting fixes, follow the mandatory post-change workflow in `AGENTS.md` (build → deploy → verify → Playwright UI → CHANGELOG)

## Phase 4: Quality Assurance

7. **Test hardening**
   - Add a regression test that fails without your fix — table-driven when possible
   - For storage adapters, use `t.TempDir()`; for DB, `Open(":memory:")`
   - If the bug involved concurrency, add a test with `t.Parallel()` or explicit goroutine fan-out

8. **Final report**
   - One-line summary
   - Root cause
   - Fix (with file\:line references)
   - Regression coverage added
   - Any follow-up work discovered but intentionally deferred

## Debugging Guidelines

- **Reproduce first.** A bug without a reproduction is a rumor.
- **Be systematic.** Do not guess-and-check edits on the deployed daemon.
- **Stay in scope.** Fix the reported bug. File follow-ups for anything else you find.
- **Respect layering.** Don't "fix" a storage bug in an API handler, or a DB bug in the engine.
- **Document in the commit.** Use Conventional Commits — `fix(scope): <what broke and how it's fixed>`.
- **Never skip the post-change workflow** for fixes that affect the binary or UI.

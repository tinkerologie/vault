# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Read [`AGENTS.md`](AGENTS.md) first** — it is the single source of truth for this project.

## Project Overview

Vault is a Go backup daemon for Unraid servers. It backs up Docker containers and libvirt VMs to pluggable storage destinations (local, SFTP, S3, SMB). It ships as an Unraid plugin (`.plg`) with a PHP/JS web UI.

## Build & Development Commands

```bash
make build               # Cross-compile for linux/amd64 (CGO_ENABLED=0) → build/vault-linux-amd64
make build-local         # Build for current platform → build/vault
make deps                # go mod download && go mod tidy
make test                # go test ./... -v
make test-short          # go test ./... -short
make test-coverage       # Generates coverage.out and coverage.html
make lint                # golangci-lint with .golangci.yml config
make security-check      # gosec + govulncheck + go mod verify
make pre-commit-install  # Install pre-commit hooks
make pre-commit-run      # Run all pre-commit checks
make deploy              # Ansible deploy to Unraid host
```

Run a single test: `go test ./internal/db/... -run TestJobCreate -v`

Run the daemon locally: `./build/vault daemon --db=vault.db --addr=:28085`

## Architecture

**Single binary, dual mode:** `cmd/vault/main.go` → Cobra CLI → `vault daemon` starts HTTP server + scheduler, other subcommands for scripting.

**Layered structure:**

- `internal/api/` — Chi router, REST handlers, WebSocket hub integration. Routes defined in `routes.go`, handlers in `handlers/`.
- `internal/db/` — SQLite (pure Go via modernc.org/sqlite, WAL mode). Schema applied inline at open time (`migrations.go`). Models and repos split by domain (`job_repo.go`, `storage_repo.go`).
- `internal/engine/` — Backup/restore logic. `ContainerHandler` uses Docker SDK. `VMHandler` uses libvirt (Linux-only via build tags; `vm_stub.go` provides stubs on other platforms).
- `internal/storage/` — `Adapter` interface with factory pattern (`factory.go`). Implementations: `local.go`, `sftp.go`, `s3.go`, `smb.go`. Config stored as JSON blob in DB.
- `internal/scheduler/` — Cron-based job scheduler using `robfig/cron/v3`. Loads jobs from DB, supports reload.
- `internal/ws/` — WebSocket pub/sub hub for real-time progress streaming.
- `internal/notify/` — Unraid notification integration (no-ops gracefully on non-Linux).
- `plugin/` — Unraid plugin installer (`vault.plg`), PHP pages, CSS/JS assets.

**Key interfaces:**

- `storage.Adapter` — `Write/Read/Delete/List/Stat/TestConnection`
- `engine.Handler` — Backup/restore handler per backup type (container, VM)

## Build Tags

- `//go:build linux` on `internal/engine/vm.go` (real libvirt implementation)
- `//go:build !linux` on `internal/engine/vm_stub.go` (stub for macOS/Windows dev)

Tests and local builds work on macOS without libvirt installed.

## Database

SQLite with WAL mode and busy timeout. Five tables: `storage_destinations`, `jobs`, `job_items`, `job_runs`, `restore_points`. Schema in `internal/db/migrations.go` — uses `CREATE TABLE IF NOT EXISTS` (no versioned migrations).

## API

REST API at `/api/v1/` — jobs CRUD, storage destinations CRUD, job execution, restore points. WebSocket at `/api/v1/ws` for live progress. Default port: 28085.

## Deployment

The daemon runs on Unraid at `/boot/config/plugins/vault/vault`. Plugin XML (`vault.plg`) defines install/remove lifecycle and the `rc.vault` service script. Ansible playbook in `ansible/` handles build/deploy/verify with tagged roles.

## Version

Version string lives in `VERSION` file, injected via ldflags at build time (`-X main.version`, `-X main.buildDate`, `-X main.commit`).

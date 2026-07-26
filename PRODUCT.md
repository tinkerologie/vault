# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Primary: the **homelab Unraid operator** — a self-hoster running their own server, comfortable with Docker containers and VMs but not a backup professional. Vault is not their job; it is the thing that has to be fine.

Secondary, never blocked: the **prosumer / semi-pro admin** running Unraid for a home business or small team, accountable for retention policy, encryption, offsite copies, and audit evidence. The hobbyist is the design default; the prosumer's depth (LTR, dedup, replication, anomaly detection) must remain reachable, not removed.

Both are the same visitor in two very different situations:

- **Routine glance** — "is it green?" Periodic reassurance, often from a phone, low stakes, seconds of attention.
- **Under pressure** — a restore or full recovery after a failure. High stress, unfamiliar screens, irreversible actions, and the user is reading carefully for the first time.

## Product Purpose

Vault is a backup and restore daemon for Unraid servers. It protects Docker containers, libvirt VMs, ZFS datasets, folders, and installed plugins by backing them up to pluggable destinations (local, SFTP, SMB, NFS, WebDAV, S3-compatible), on a schedule, with retention and verification.

Success is not "a backup ran." Success is that the operator can **prove** their data is recoverable before they need it, and can actually recover it when they do.

## Positioning

Four claims a neighboring Unraid backup plugin could not truthfully copy — all four confirmed as durable:

1. **Breadth of sources in one daemon.** Containers, VMs, ZFS datasets, folders, and plugins under a single job model, retention policy, schedule, and history — not five separate user scripts.
2. **Restore is a first-class product.** Restore points, a restore wizard, chain verification, per-run SHA-256 verification, and a Recovery plan that explains rebuilding from scratch. Backup that has been proven restorable, not just written.
3. **An open, integrable backup daemon.** REST API at `/api/v1`, an MCP server (streamable HTTP + stdio) for AI assistants, WebSocket progress streaming, and a Home Assistant integration. Vault is addressable by other systems rather than a closed GUI.
4. **Operational honesty.** Anomaly detection (drift / reliability / capacity with baseline learning), stale-item detection, storage-health probes, and redacted diagnostics bundles. Vault says when something is drifting instead of showing green.

## Operating Context

- Installed as an Unraid plugin (`.plg`) and reached through the Unraid web console, alongside Unraid's own settings pages.
- The daemon runs on the server at `/boot/config/plugins/vault/vault`, default port 24085; the UI is served by that daemon.
- Typical session shapes: first-run setup (add storage → create job → run), the recurring glance at the Dashboard, forensic reading of History and Logs after a failure, and the rare full restore or recovery.
- Backups run long and unattended. Live progress arrives over WebSocket; a job can be in flight while the operator is elsewhere or asleep.
- The audience overlaps heavily with the Unraid forum and Community Applications; support happens in public threads and GitHub issues.

## Capabilities and Constraints

**Confirmed capabilities**

- Sources: Docker containers (image, XML template, mapped appdata volumes, per-container exclusions), libvirt VMs (live snapshot or cold, NVRAM preserved), ZFS datasets (native send/receive), folders and paths, installed plugins.
- Destinations: local, SFTP, SMB, NFS, WebDAV, S3-compatible (AWS S3, Backblaze B2, MinIO, Cloudflare R2, Wasabi, MEGA, IDrive E2); per-destination bandwidth throttling; test-connection and health probes; scan + import of backups from other Vault instances or AppData Backup.
- Strategy: full / incremental / differential chains; simple-count retention or Long-Term Retention (`keep_latest`/`daily`/`weekly`/`monthly`/`yearly`); AES-256-GCM encryption with per-passphrase key derivation; content-defined deduplication (Keyed-FastCDC, per-destination repo); SHA-256 verification per run and on demand; Discord webhook notifications per job.
- Scheduling: cron plus presets (hourly/daily/weekly/monthly/yearly, first/last day of month); a no-progress stall watchdog (cancels only after ~2h of zero bytes moved — no fixed total-time cap); cancellation propagated end to end.
- Surfaces: Dashboard, Jobs, Restore, Storage, History, Replication, Recovery, Logs, Anomalies, Settings; command palette; light and dark themes; mobile-responsive layout.

**Durable constraints — all four confirmed as binding**

- **Lives inside Unraid's plugin chrome.** The UI is embedded in Unraid's page shell and must sit credibly next to Unraid's own pages, respecting its light/dark context. It is a guest in someone else's console.
- **Fully local / air-gapped-friendly.** No external CDNs, web fonts, telemetry, or third-party network calls. Assets are embedded in the Go binary (`web/embed.go`) and served by the daemon. A server with no internet must render identically.
- **Data is destructive — confirmation is non-negotiable.** Restores overwrite live containers, VMs, and datasets. Every destructive path requires explicit, informed confirmation, and must never be reachable by momentum or a mis-tap.
- **Works on a phone, mid-incident.** Operators check and act from a phone during a failure. Mobile-responsive is a requirement, not a nicety.

**Technical constraints**

- Single Go binary; Svelte 5 + Tailwind 4 front end built by Vite and embedded at compile time. No SSR, no runtime asset fetching.
- SQLite (WAL) with a hybrid RAM working DB + persistent snapshot + USB shadow, so state survives Unraid reboots.
- Real-time updates via WebSocket at `/api/v1/ws`; the UI must degrade gracefully when that socket drops (reconnect with backoff is already implemented).
- Token-based auth for non-loopback API callers.
- Requires Unraid 7.0 or newer.

## Brand Commitments

- Name: **Vault**. Repository and issue tracker: `github.com/ruaan-deysel/vault`.
- Existing marks: `plugin/icon.png` (Unraid plugin icon), `web/public/favicon.svg`.
- Licensed AGPL-3.0; explicitly a third-party community plugin for Unraid OS, not an Unraid product. Nothing in the UI may imply official Unraid endorsement.
- Related first-party project: `ha-vault` Home Assistant integration.
- No colors, typography, or visual system are pinned here by the user; the incumbent implementation in `web/src/app.css` is evidence, not an approved commitment.

## Evidence on Hand

- Real UI screenshots at `docs/screenshots/` (dashboard, welcome, storage, jobs, history, restore, logs, replication, recovery, settings, mobile, dark mode, storage picker, job wizard).
- Full user-facing documentation set under `docs/` (getting-started, guides, api, mcp, architecture, faq) published via MkDocs; `CHANGELOG.md` is parsed and surfaced in-app.
- Live production instance on the user's Unraid server with real jobs and destinations; Playwright end-to-end tests under `tests/`.
- **Absences that must not be fabricated:** no testimonials, named customers, install counts, benchmark numbers, pricing, awards, or third-party endorsements exist. Vault is free and AGPL-licensed — do not invent commercial framing.

## Product Principles

1. **Green must be earned.** A reassuring status is a claim about recoverability. Never show calm the data does not support; surface drift, staleness, and unverified chains rather than smoothing them away.
2. **Design for the worst session, not the average one.** The routine glance forgives almost any design. The 2am restore does not. Clarity under stress outranks density and cleverness.
3. **The hobbyist sets the default, the prosumer sets the ceiling.** Nothing capable gets deleted to make things simple — depth is layered behind a legible first read.
4. **A guest in Unraid's console.** Vault has its own identity but never fights the host shell it is embedded in.
5. **Irreversible actions move slowly.** Destructive operations state exactly what they will overwrite and require deliberate confirmation, every time.

## Accessibility & Inclusion

No formal conformance target has been set by the user. Confirmed product-specific needs: light and dark themes must both be first-class (Unraid's shell drives the context), the interface must be operable on a phone, and status must remain legible to color-vision-deficient users — status is the primary read on the Dashboard, so it cannot be carried by hue alone. A target standard (e.g. WCAG 2.2 AA) is **undecided** and should be confirmed before any accessibility audit claims conformance.

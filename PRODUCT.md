# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Two primary audiences with equal standing:

- **Set-and-forget homelab owners.** They configure backup jobs once, then glance at status inside the Unraid web UI to confirm everything is protected. Restores are rare and usually happen under pressure: a broken container, a failed disk, a replacement server.
- **Hands-on power users.** They tune jobs, retention, deduplication, replication and destinations regularly, and expect full control over every option.

Both groups are Unraid server administrators with root-level authority over the host. Defaults must be safe for the first group, and advanced options must stay reachable for the second.

## Product Purpose

Vault is a backup and restore daemon for Unraid servers. It protects Docker containers (images, templates, volumes), libvirt VMs, ZFS datasets, folders and Unraid plugins, and sends the data to local disk, SFTP, SMB, NFS, WebDAV or S3-compatible storage. It covers the whole life cycle: scheduling, verification, retention, replication, restore and full disaster recovery onto a replacement server.

Success means an owner can always answer "am I protected?" at a glance, and when something breaks, they can restore exactly what they need without fear of overwriting the wrong thing.

## Positioning

Vault combines three things that alternatives such as AppData Backup, Duplicati or Unraid's own tools don't offer together:

1. **Restore you can trust.** Verified restore points (SHA-256), clear previews of what a restore will change, typed confirmation for destructive operations, restore drills and disaster-recovery readiness.
2. **Everything in one place.** Every Unraid workload type to every destination type, in one tool native to Unraid.
3. **Automation-ready.** A REST API, an MCP server for assistants (restores require operator approval in the Vault UI, per ADR 0009) and a Home Assistant integration (ha-vault).

## Operating Context

- Usually opened from the **Backups** entry inside the Unraid web UI, where it runs embedded. In that view, a row of links replaces Vault's header, sidebar and bottom bar, and the Unraid theme applies until the user picks a Vault theme. Vault also runs standalone on port 24085.
- Used mostly on desktop, sometimes on a phone, often for a quick status check.
- Backup and restore progress arrives live over WebSocket. Jobs run on cron schedules, often overnight.
- Restore and disaster recovery are high-stakes and infrequent. The user may be stressed, and the server may be partly broken.
- Vault runs with privileged access to Docker, libvirt, filesystems and storage credentials.

## Capabilities and Constraints

- Full, incremental and differential backups; count-based or Long-Term Retention; age encryption; content-defined deduplication per destination; post-backup and on-demand verification; bandwidth limits; replication; anomaly detection; Discord notifications.
- UI areas: Dashboard, Jobs, Storage, Replication, Restore, History, Disaster Recovery, Logs, Anomalies, Settings.
- The UI is a Svelte 5 SPA embedded in a pure-Go binary (ADR 0001). No external runtime assets: fonts and icons ship in the bundle.
- Restore, deletion, database replacement and path remapping are sensitive operations and need proportionate confirmation (ADR 0005, ADR 0009).
- Vocabulary follows `GLOSSARY.md` (Job, Job Item, Backup Item, Destination, Job Run, Restore Point, Backup Chain, Restore Request, and so on). Use those terms in the UI.

## Brand Commitments

- The **Vault** name, the amber colour and the shield mark (`web/public/favicon.svg`) are established identity to preserve.
- The theme styles are product features to keep supporting: Default, 1-Bit, Retro, Dracula and Catppuccin, in light and dark where applicable. #618 tracks the Retro redesign.
- The embedded view must feel native beside Unraid's own pages, and inherits Unraid's theme by default.
- Vault is free and open source (AGPL-3.0), published by tinkerologie.

## Evidence on Hand

- Product screenshots: `docs/screenshots/`.
- User documentation: `docs/` (getting started, guides, disaster recovery, restore drills, API, MCP).
- Architecture decisions: `docs/adr/`.
- No testimonials, user counts or benchmarks are recorded. Don't invent them.

## Product Principles

1. **Never falsely reassure.** Unknown, failed or partial must never look like healthy or empty. Protection status is the first thing the UI answers.
2. **Show exactly what will change before it changes.** Destructive or overwriting actions name their targets and ask for confirmation in proportion to the risk.
3. **Safe defaults, full control.** Casual owners succeed without touching advanced options, and power users can still reach every one.
4. **Native to Unraid.** Fit the host UI, its theme and its workflows rather than fighting them.
5. **One vocabulary everywhere.** The UI, API, MCP and docs use the glossary terms.

## Accessibility & Inclusion

WCAG 2.2 AA is a requirement. The Playwright suites in `tests/` enforce axe structural checks, contrast across every theme, dialog focus, keyboard walks and mobile navigation. New work must keep them passing and extend them where they don't reach. Reduced motion is respected app-wide.

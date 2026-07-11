# How Backup Works

This page explains the concepts behind a Vault backup. For the exact field names and options you set on a job, see the [Job Configuration reference](reference/job-config.md).

## Backup sources

A Vault job backs up one or more sources on your Unraid host:

- **Docker containers** — the container image, its XML template, and every mapped appdata volume. You can exclude specific paths per container.
- **libvirt VMs** — captured in live-snapshot or cold mode, with NVRAM preserved.
- **ZFS datasets** — using native `zfs send`/`receive` with snapshot management.
- **Folders and plugins** — any path on the host, plus all installed Unraid plugins.

Vault also performs stale-item detection: when a source disappears from the host, it is flagged so jobs stay clean rather than silently failing.

## Backup strategies

Vault supports three backup strategies that trade off speed against storage:

- **Full** — a complete, self-contained copy of every source. A full backup depends on nothing else.
- **Incremental** — captures only what changed since the previous backup (full or incremental) in the chain. Incrementals are small and fast but must be replayed in order on top of their parent full.
- **Differential** — captures everything that changed since the last full backup. Each differential is larger than an incremental but only ever depends on a single full.

Chains form from these building blocks: a full backup anchors the chain, and subsequent incremental or differential runs attach to it. Restoring reconstructs a point in time by combining the anchoring full with the necessary incrementals or the relevant differential.

## Retention

Retention decides how many backups to keep and which to prune. Vault offers two models:

- **Simple count** — keep the most recent N backups and prune the rest.
- **Long-Term Retention (LTR)** — bucketed retention that keeps backups across multiple time horizons. Buckets are `keep_latest`, `daily`, `weekly`, `monthly`, and `yearly`, so you can retain, for example, the last few runs plus one per day, week, month, and year.

For the exact retention fields on a job, see the [Job Configuration reference](reference/job-config.md).

## Encryption

Backups can be encrypted with your backup password using **age** encryption — a modern, audited standard. Encryption is applied to the backup content so that data at rest on the destination cannot be read without the password.

## Deduplication

Vault uses content-defined deduplication (Keyed-FastCDC with a per-destination dedup repository) so that repeated data across runs and sources is stored only once. This reduces the storage footprint of backup chains. Maintenance helpers `vault dedup gc` and `vault dedup repair` are available for garbage collection and repository repair.

## Verification

Every run produces a **SHA-256 verification** of what was written, and you can trigger an on-demand verify of any restore point. Verification confirms that stored backup data matches what Vault expects, so you find integrity problems before you need to restore.

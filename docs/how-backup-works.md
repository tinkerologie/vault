# How Backup Works

This page explains the concepts behind a Vault backup. For the exact field names and options you set on a job, see the [Job Configuration reference](reference/job-config.md).

## Backup sources

A Vault job backs up one or more sources on your Unraid host:

- **Docker containers** — the container image, its XML template, and every mapped appdata volume. You can exclude specific paths per container.
- **libvirt VMs** — captured in live-snapshot or cold mode, with NVRAM preserved.
- **ZFS datasets** — using native `zfs send`/`receive` with snapshot management.
- **Folders and plugins** — any path on the host, plus all installed Unraid plugins.

Vault marks a source when it cannot find the source on the host.

## Backup strategies

Vault supports three backup types.

- **Full.** Vault makes a complete copy of each source.
- **Incremental.** Vault copies changes since the previous backup in the chain. To restore an incremental backup, Vault applies each change in order after the full backup.
- **Differential.** Vault copies changes since the last full backup. To restore a differential backup, Vault uses the full backup and the selected differential backup.

A full backup starts a backup chain. Later runs add incremental or differential backups to the chain.

<!-- prettier-ignore -->
!!! note "The first run is always a full backup"
    The first run of a new job makes a **Full** backup. Later runs use the selected backup type.

## Retention

Retention decides how many backups to keep and which to prune. Vault offers two models:

- **Simple count** — keep the most recent N backups and prune the rest.
- **Long-Term Retention (LTR)** keeps backups for different intervals. Use `keep_latest` for the newest runs. Use the daily, weekly, monthly, and yearly settings to keep backups for those intervals.

For the exact retention fields on a job, see the [Job Configuration reference](reference/job-config.md).

## Encryption

If you enable age encryption for a classic backup job, Vault encrypts the backup data and its run manifest (`manifest.json`). To read the encrypted data or manifest, use the backup password.

For deduplicated storage destinations, manifests are automatically encrypted using AES-256-GCM keyed off the destination's master key, keeping metadata protected without extra user intervention.

## Deduplication

Vault uses content-defined deduplication (Keyed-FastCDC with a per-destination dedup repository) so that repeated data across runs and sources is stored only once. This reduces the storage footprint of backup chains. Maintenance helpers `vault dedup gc` and `vault dedup repair` are available for garbage collection and repository repair.

## Staging work area

When creating classic compressed archives (such as folder items), Vault temporarily assembles the archive in a local staging directory before finalizing it.

Staging path selection follows a prioritized cascade:

1. **Custom staging override** — if configured under **Settings > Temporary Work Area**.
2. **Storage destination.** For a local destination with **Stage temporary backups beside this destination** on, Vault uses the hidden `.vault-stage` directory on that disk.
3. **Cache pools** — Unraid SSD/NVMe cache pools (`/mnt/cache`, `/mnt/fast_nvme`, etc.), ranked by available free space.
4. **Local storage destination** — local backup storage disks.
5. **System temporary directory** — `/tmp` or OS default temp dir.

Staged archives are automatically cleaned up when the run finishes or upon daemon startup if a previous run was abruptly interrupted.

## Verification

Vault calculates a SHA-256 digest when it uploads data. If verification is on, Vault reads the stored data and calculates the digest again. You can also start verification for a restore point. A difference between the digests shows a problem with the stored backup data.

## Progress

The Dashboard shows the phase and progress of the current item. Phases include scanning, preparing, processing, transferring, verifying, and finalizing.

Vault shows a percentage if it knows the total. For example, it can show `Uploading 84.2 GB / 225 GB — 37%`. When enough data is available, Vault shows the rate and estimates the remaining time.

Vault scans folders before it archives or divides their data into chunks. For container volumes, Vault shows the number of bytes and files as it reads them. If progress does not change for two minutes, Vault shows the time since the last change.

You can also read this data from `GET /api/v1/runner/status` or the MCP `get_runner_status` tool. See [Progress](api.md#progress).

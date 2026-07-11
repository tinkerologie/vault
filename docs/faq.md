# FAQ

## Is Vault free?

Yes. Vault is free and open source, licensed under AGPL-3.0. There are no paid tiers or locked features.

## Where should my backups go?

Keep at least one copy off the server itself — an SMB/NFS/SFTP share on another machine, or S3-compatible cloud storage. The Dashboard's 3-2-1 widget shows how close you are to the classic rule (3 copies, 2 media, 1 off-site). See [Storage Destinations](guides/storage-destinations.md).

## Do I need the backup password to restore?

Only if your backups are encrypted — they are whenever you set a password under Settings → Encryption (recommended). Keep the password somewhere off the server, like a password manager; without it, encrypted backups cannot be decrypted by anyone.

## What happens if my server dies completely?

You reinstall Unraid and the Vault plugin, then use the Recover Vault wizard to reconnect your backup storage and bring your settings and data back. Nothing from the old server is needed. The [Disaster Recovery guide](guides/disaster-recovery.md) walks through it step by step.

## Does Vault back up Docker volumes?

Yes. A container backup includes the container image, its XML template, and every mapped volume — including named volumes and appdata paths. You can exclude sub-paths per container (e.g. caches). See [Backup Jobs](guides/backup-jobs.md).

## Can I run a job manually instead of on a schedule?

Yes. Every job has a **Run Now** button, and you can leave the schedule empty to create a manual-only job that never runs on its own. See [Backup Jobs](guides/backup-jobs.md).

<!-- prettier-ignore -->
!!! note
    This page grows from questions asked on the Unraid forums. Have one that isn't answered here? Ask on the forum thread.

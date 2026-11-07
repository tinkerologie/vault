# FAQ

## Is Vault free?

Yes. Vault is free and open source. The license is AGPL-3.0. All functions are available at no cost.

## Where should my backups go?

Keep at least one copy on a different server or in S3-compatible storage.
The 3-2-1 rule uses three copies on two types of media, with one copy at a different site.
The Dashboard shows whether the storage destinations are likely to meet this rule.
See [Storage Destinations](guides/storage-destinations.md).

## Do I need the backup password to restore?

For a classic backup with age encryption, use the password for that backup. To use age encryption, set a backup password and select age encryption on the job.
Keep the backup password outside the server. You cannot decrypt an encrypted backup without its password.

## What happens if my server fails?

Install Unraid on a replacement server. Install the Vault plugin. Use **Recover Vault** to connect to the backup storage and recover the Vault settings.
See [Disaster Recovery](guides/disaster-recovery.md) for the procedure to recover your settings and data.

## Does Vault back up Docker volumes?

Yes. A container backup includes the container image, its XML template when available, and its mapped volumes. This includes named volumes and appdata paths.
You can exclude subpaths for each container. See [Backup Jobs](guides/backup-jobs.md).

## What about a container that has no volumes?

Vault backs up the container image and configuration even when the container has no mounts.
Vault also includes the Unraid XML template when it is available.

## Can I run a job manually?

Yes. Click **Run Now** on the job to start it manually.
If you want a job to run only on command, leave its schedule empty.
See [Backup Jobs](guides/backup-jobs.md).

<!-- prettier-ignore -->
!!! note
    You can ask other questions about Vault on the Unraid forum.

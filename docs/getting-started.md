# Getting Started

A visual walkthrough of the Vault web UI and its key features.

## Dashboard

When you first open Vault, the Dashboard greets you with a welcome screen and a 3-step guide to get started: add a storage destination, create a backup job, and run your first backup.

![Dashboard welcome screen](screenshots/01-dashboard.png)

The welcome panel walks you through the setup steps with clear numbered guidance.

![Welcome panel close-up](screenshots/02-welcome.png)

## Storage

The Storage page is where you configure backup destinations. Vault supports local paths, SFTP, SMB, and NFS. Add a destination, test the connection, and you're ready to go.

![Storage destinations page](screenshots/03-storage.png)

## Jobs

The Jobs page lets you create and manage backup jobs. Each job defines what to back up (containers, VMs, folders), which storage destination to use, a schedule, and retention rules.

![Jobs page](screenshots/04-jobs.png)

## History

The History page shows a log of all completed, failed, and running backup jobs. Each entry includes the job name, duration, size, and status.

![History page](screenshots/05-history.png)

## Restore

The Restore page provides access to the restore wizard. Select a job, pick a restore point, choose specific items to restore, and confirm.

![Restore page](screenshots/06-restore.png)

## Logs

The Logs page shows a chronological activity log of system events — job runs, storage tests, configuration changes, and errors.

![Logs page](screenshots/07-logs.png)

## Replication

The Replication page lets you replicate backup data from remote Vault instances to your local server. This supports the offsite copy in a 3-2-1 backup strategy.

![Replication page](screenshots/08-replication.png)

## Recovery

The Recovery page provides a disaster recovery guide with step-by-step instructions for restoring your server from backups.

![Recovery page](screenshots/09-recovery.png)

## Settings

The Settings page provides configuration options for encryption, staging directory, database snapshots, Discord notifications, and theme selection.

![Settings page](screenshots/10-settings.png)

## Mobile View

Vault is fully responsive. On mobile devices, the sidebar collapses into a bottom navigation bar with quick access to the main pages.

![Mobile view](screenshots/11-mobile.png)

## Dark Mode

Toggle between light and dark themes using the theme button in the sidebar. Dark mode uses a warm dark palette that's easy on the eyes.

![Dark mode](screenshots/12-dark-mode.png)

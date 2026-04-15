# Getting Started with Vault

This guide walks you through installing Vault and completing your first backup from scratch.

## Prerequisites

- Unraid 7.0 or later
- At least one storage destination (local path, SFTP server, SMB share, or NFS export)

---

## 1. Install the Plugin

Until Vault is listed in the Community Applications store, install it manually:

1. In the Unraid web UI, go to **Plugins** → **Install Plugin**
2. Paste the following URL and click **Install**:

   ```
   https://raw.githubusercontent.com/ruaan-deysel/vault/main/plugin/vault.plg
   ```

3. Once installed, Vault appears under **Settings** → **Vault** in the Unraid menu.

---

## 2. Open the Vault UI

Click **Open Web UI** on the Vault settings page, or navigate to:

```
http://<your-unraid-ip>:24085
```

> **Accessing directly on port 24085:** By default Vault binds to `127.0.0.1`, so it is reachable only through the Unraid plugin proxy. To access it directly (e.g. from Home Assistant or the CLI), change `BIND_ADDRESS` to `0.0.0.0` or your server's LAN IP under **Settings → Vault** in Unraid and restart the daemon.

The Dashboard shows a three-step welcome guide: add storage → create job → run backup.

![Dashboard welcome screen](screenshots/01-dashboard.png)

---

## 3. Add a Storage Destination

Vault needs somewhere to write your backups before you can create any jobs.

1. Go to **Storage** in the left sidebar
2. Click **Add Destination**
3. Give it a name (e.g. "NAS Backups")
4. Choose a type:

   | Type | Use case |
   |------|----------|
   | **Local** | A path on your Unraid array or a directly attached drive |
   | **SFTP** | Any server accessible via SSH (another NAS, VPS, etc.) |
   | **SMB** | Windows shares or Samba servers on your LAN |
   | **NFS** | Linux/NAS NFS exports |

5. Fill in the connection details. See [Storage Destinations](guides/storage-destinations.md) for field-by-field guidance.
6. Click **Test Connection** to verify Vault can reach the destination.
7. Click **Save**.

![Storage destinations page](screenshots/03-storage.png)

---

## 4. Create a Backup Job

A job defines *what* to back up, *where* to put it, *when* to run, and how many copies to keep.

1. Go to **Jobs** in the left sidebar
2. Click **Create Job**
3. Work through the wizard:

   **Step 1 — Name & Type**
   - Give the job a descriptive name (e.g. "Weekly Container Backup")
   - Choose a backup type: **Full**, **Incremental**, or **Differential**

   **Step 2 — Select Items**
   - Pick which Docker containers, VMs, or folders to include
   - Items are listed automatically from what Vault discovers on your server

   **Step 3 — Storage Destination**
   - Select the destination you created in step 3

   **Step 4 — Schedule & Retention**
   - Set a cron schedule (daily, weekly, monthly, or custom)
   - Set retention: how many restore points to keep before the oldest is pruned

4. Review the summary and click **Save**.

![Jobs page](screenshots/04-jobs.png)

> **Tip:** To run a backup immediately without waiting for the schedule, open the job and click the **Run Now** button.

---

## 5. Run Your First Backup

1. On the **Jobs** page, find your new job
2. Click the **Run Now** button (play icon)
3. A progress bar appears in real time via WebSocket streaming

Once complete, the job card shows the last run time, duration, and size.

---

## 6. Verify and Restore

After at least one successful backup:

1. Go to **Restore** in the left sidebar
2. Select your job — you should see one or more restore points listed
3. Click a restore point to open the guided wizard
4. Choose which items to restore and confirm

Each restore point also shows chain health annotations so you can see if a full parent is available for an incremental or differential point.

![Restore page](screenshots/06-restore.png)

---

## What's Next

| Goal | Where to look |
|------|---------------|
| Configure SFTP, SMB, or NFS in detail | [Storage Destinations](guides/storage-destinations.md) |
| Set up encryption | Settings → Security → Encryption |
| Enable Discord notifications | Settings → Notifications |
| Replicate backups to a second Vault server | [Replication](guides/replication.md) |
| Automate with Home Assistant | [Home Assistant Integration](home-assistant-integration.md) |
| Use the REST API | [API Reference](api.md) |
| Use the MCP server | [MCP](mcp.md) |

---

## Troubleshooting

**"Vault daemon not available" after saving a job**
This can appear if the daemon restarts mid-request. Wait a few seconds and refresh — your job was likely saved successfully. If the error persists, check the **Logs** page for details.

**Configuration lost after reboot**
Vault stores its database in RAM with a periodic snapshot to disk (hybrid mode). Make sure the snapshot path is set to a persistent location (SSD cache, USB boot, or a custom path) under **Settings → General → Database Location**. A fix to make this automatic after every reboot is in progress.

**Mirrored cache pool not detected**
Vault dynamically scans `/mnt/` for pool mounts. If your pool is not shown, ensure it is mounted and try setting a custom snapshot path manually under **Settings → General → Database Location**.

**Backup fails on Tailscale-enabled containers**
Fixed in release 2026.03.02. Update to the latest version.

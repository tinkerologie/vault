# Getting Started with Vault

This guide walks you through installing Vault and completing your first backup from scratch.

## Prerequisites

- Unraid 7.0 or later
- At least one storage destination — local path, SFTP server, SMB share, NFS export, WebDAV server, or S3-compatible bucket

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

   | Type       | Use case                                                                                   |
   | ---------- | ------------------------------------------------------------------------------------------ |
   | **Local**  | A path on your Unraid array or a directly attached drive                                   |
   | **SFTP**   | Any server reachable by SSH (another NAS, a VPS, …)                                        |
   | **SMB**    | Windows shares and Samba servers on your LAN (Synology, TrueNAS SCALE, Windows Server)     |
   | **NFS**    | Linux/NAS NFS exports                                                                      |
   | **WebDAV** | Stateless HTTP target — Nextcloud, ownCloud, Synology WebDAV, generic servers              |
   | **S3**     | AWS S3 and S3-compatible object storage — Backblaze B2, MinIO, Cloudflare R2, Wasabi, MEGA |

5. Fill in the connection details. See [Storage Destinations](guides/storage-destinations.md) for field-by-field guidance.
6. Click **Test Connection** to verify Vault can reach the destination.
7. Click **Save**.

![Storage destinations page](screenshots/03-storage.png)

---

## 4. Create a Backup Job

A job defines _what_ to back up, _where_ to put it, _when_ to run, and how many copies to keep.

1. Go to **Jobs** in the left sidebar
2. Click **Create Job**
3. Work through the wizard:

   **Step 1 — Name & Type**

   - Give the job a descriptive name (e.g. "Weekly Container Backup")
   - Choose a backup type: **Full**, **Incremental**, or **Differential**

   **Step 2 — Select Items**

   - Pick which Docker containers, VMs, ZFS datasets, folders, or plugins to include
   - Items are listed automatically from what Vault discovers on your server
   - For containers you can also list sub-paths to exclude (e.g. Plex transcode cache)

   **Step 3 — Storage Destination**

   - Select the destination you created in step 3

   **Step 4 — Schedule & Retention**

   - Set a cron schedule (hourly, daily, weekly, monthly, yearly, or custom cron)
   - Choose retention: either _keep last N restore points_ or a Long-Term Retention (LTR) policy that keeps a tunable number of daily / weekly / monthly / yearly snapshots

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

| Goal                                              | Where to look                                                  |
| ------------------------------------------------- | -------------------------------------------------------------- |
| Configure SFTP, SMB, NFS, WebDAV, or S3 in detail | [Storage Destinations](guides/storage-destinations.md)         |
| Tune jobs (retention, LTR, encryption, dedup)     | [Backup Jobs](guides/backup-jobs.md)                           |
| Set up encryption                                 | Settings → Security → Encryption                               |
| Enable Discord notifications                      | Settings → Notifications                                       |
| Replicate backups to a second Vault server        | Replication page (in-app guidance)                             |
| Verify a restore point on demand                  | Restore page → restore point → _Verify_                        |
| Run dedup maintenance from the CLI                | `vault dedup repair --dest <id>`, `vault dedup gc --dest <id>` |
| Export a diagnostics bundle for support           | Settings → Support → _Download diagnostics_                    |
| Automate with Home Assistant                      | [ha-vault](https://github.com/ruaan-deysel/ha-vault)           |
| Use the REST API                                  | [API Reference](api.md)                                        |
| Use the MCP server                                | [MCP](mcp.md)                                                  |

For a ready-to-use Home Assistant custom integration, see
[ha-vault](https://github.com/ruaan-deysel/ha-vault).

> **Disaster recovery:** Back up **both** `vault.db` and `vault.key` (siblings in
> `/boot/config/plugins/vault/`). To rebuild a lost dedup index from intact storage,
> run `vault dedup repair --dest <id>`. If you restored `vault.key` to a different
> location, pass `--key /path/to/vault.key`.

---

## Troubleshooting

**"Vault daemon not available" after saving a job**
This can appear if the daemon restarts mid-request. Wait a few seconds and refresh — your job was likely saved successfully. If the error persists, check the **Logs** page for details.

**Configuration lost after reboot**
Vault uses a hybrid SQLite layout — a working DB in RAM, a periodic snapshot on a discovered cache pool, and a USB shadow on the Unraid flash drive. On boot it restores in that order. If you've changed the snapshot path manually, confirm it points to persistent storage under **Settings → General → Database Location**.

**Mirrored cache pool not detected**
Vault scans `/mnt/` for pool mounts at startup. If your pool isn't shown, make sure it's mounted before the Vault service starts, then override the snapshot path manually under **Settings → General → Database Location**.

**Diagnostics bundle for support**
Settings → Support → _Download diagnostics_ exports a ZIP containing system info, schema, recent runs, scheduler state, and the in-memory daemon log — all credential values are redacted. Attach it to bug reports.

## Optional: "Backups" link in the top navigation

Vault can add a **Backups** entry to the Unraid top navigation bar for one-click access to the web UI, embedded inside the Unraid interface. It is **off by default** — enable it under _Settings → Utilities → Vault → Quick Access_. The entry uses Unraid's supported plugin menu extension point; no core files are modified.

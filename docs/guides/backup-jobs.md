# Backup Jobs

A backup job defines *what* to back up, *where* to put it, *when* to run, and how many copies to keep. This guide explains every option in the job wizard.

## Creating a Job

Go to **Jobs** → **Create Job**. The wizard has four steps.

---

### Step 1 — Name & Type

| Field | Description |
|-------|-------------|
| **Name** | A unique name for this job. Shown on the Dashboard, History, and Restore pages. |
| **Backup Type** | The backup strategy: Full, Incremental, or Differential (see below). |

#### Backup Types

| Type | What it backs up | Restore speed | Storage use |
|------|-----------------|---------------|-------------|
| **Full** | Everything, every run | Fastest — single archive | Highest — full copy each time |
| **Incremental** | Changes since the last backup of any type | Slowest — may need to chain archives | Lowest |
| **Differential** | Changes since the last *full* backup | Medium — needs full + one diff | Medium |

For most home server use cases, a weekly **Full** backup with daily **Incremental** runs gives a good balance between storage cost and restore speed.

---

### Step 2 — Select Items

Pick which items to include in this job. Vault discovers items automatically from your server:

| Category | What's included |
|----------|----------------|
| **Containers** | Docker containers (image, XML config, and all mapped appdata volumes) |
| **Virtual Machines** | libvirt VMs (live snapshot or cold backup depending on running state) |
| **Folders** | Any arbitrary path on your server (e.g. `/mnt/user/appdata`, `/boot/config`) |
| **Plugins** | Installed Unraid plugins |

**Notes on containers:**
- Vault backs up the container image, its XML template, and all host paths mapped into the container.
- Special files (Unix sockets, device nodes, named pipes) are skipped automatically — they cannot be archived and their presence does not fail the backup.
- Tailscale-enabled containers are fully supported.

**Notes on folders:**
- You can back up `/mnt/user/appdata` to get all container appdata in one shot without selecting individual containers.
- Folder backups only wake the destination disk when writing — the source array is read sequentially.

**Excluding container paths (coming soon):**
The ability to exclude specific sub-directories from container backups (e.g. `/config/Library/Application Support/Plex Media Server/Cache`) is planned. Follow [GitHub issue ruaan-deysel/vault#11](https://github.com/ruaan-deysel/vault/issues/11) for progress.

---

### Step 3 — Storage Destination

Select which storage destination this job will write to. If you have not added one yet, save the wizard and go to **Storage** first — see [Storage Destinations](storage-destinations.md).

---

### Step 4 — Schedule & Retention

#### Schedule

| Option | Description |
|--------|-------------|
| **Hourly** | Runs at a fixed minute past every hour |
| **Daily** | Runs at a specific time each day |
| **Weekly** | Runs on selected days of the week at a specific time |
| **Monthly** | Runs on a specific day of the month (or First/Last day) at a specific time |
| **Yearly** | Runs on a specific month and day at a specific time |
| **Custom (cron)** | Any valid cron expression for advanced schedules |

**"First day" and "Last day" of month:**
Instead of a fixed day number, you can choose *First day of month* or *Last day of month*. Last-day jobs fire correctly on months of any length (28, 29, 30, or 31 days).

**Time format:**
The schedule UI uses your Unraid time format setting (12-hour or 24-hour) automatically.

> **Unscheduled / manual-only jobs:** Currently all jobs require a schedule. To run a backup ad hoc, use the **Run Now** button. A dedicated "no schedule" option is planned.

#### Retention

Retention controls how many restore points are kept before the oldest is deleted.

| Field | Description |
|-------|-------------|
| **Keep last N restore points** | Vault keeps this many restore points per job. When a new backup completes and the count exceeds the limit, the oldest is pruned — including its backup files from storage. |

Set retention based on how much storage you have and how far back you want to be able to restore:
- 7 daily backups → keep 7
- 4 weekly backups → keep 4
- A weekly full + 6 daily incrementals → keep 7, run two jobs (one weekly full, one daily incremental)

---

## Running a Job Manually

To trigger an immediate backup without waiting for the schedule:

1. Go to **Jobs**
2. Click the **Run Now** button on the job card (play icon)

Progress streams in real time via WebSocket. The run appears in **History** when complete.

---

## Monitoring Progress

While a job is running:
- The Dashboard shows a live progress indicator
- The **History** page lists the run with status "Running"
- The **Logs** page shows per-item progress entries including container name, backup type, storage destination, and elapsed time

---

## Cancelling a Running Job

To stop an in-progress backup:

1. Go to **Jobs**
2. Click the **Cancel** button on the running job

Vault signals cancellation through the entire pipeline — file I/O, directory traversal, and engine handlers all check for cancellation and stop gracefully. The job is marked as "cancelled" in History.

Jobs also have automatic safeguards:
- **4-hour hard timeout** — a job running longer than 4 hours is automatically cancelled
- **2-hour stall detection** — a job with no progress for 2 hours is cancelled (with a warning at 30 minutes)

---

## Deleting a Job

To delete a job:

1. Go to **Jobs**
2. Click the **...** menu → **Delete**
3. Choose whether to **Keep backup files** or **Delete backup files** from storage

Deleting backup files removes the archives from the storage destination. Keeping them leaves the files in place — useful if you plan to import them into a new job later.

---

## Deleting Individual Restore Points

To delete a specific backup without touching the job or other restore points:

1. Go to **Restore** in the left sidebar
2. Select the job
3. Find the restore point and click the **trash icon**
4. Confirm the deletion

This removes both the backup files from storage and the restore point record from the database.

---

## Managing Stale Items

If a container, VM, or folder is removed from Unraid after a job is created, Vault marks it as **"Not found"** in the job's item list. You can remove stale items from the job by clicking the **remove** button next to the flagged item.

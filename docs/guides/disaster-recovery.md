# Disaster Recovery

This guide covers bringing Vault back after you lose the server it ran on —
a failed boot drive, a rebuilt array, or a full Unraid reinstall.

## The short version

1. Reinstall Unraid and the Vault plugin.
2. Open Vault and choose **Recover from a backup** (or Recovery → **Recover Vault**).
3. Reconnect the storage that holds your backups, enter your backup password,
   and restore your settings.
4. Fix any folder paths that changed, then restore your data from the Restore page.

You always need **access to your backup storage**. You also need **your
backup password**, but only if your backups are encrypted — they are whenever
you set a password under Settings → Encryption (recommended). Nothing from
the old server is required.

---

## Why copying vault.db to the flash drive doesn't work

Vault's database doesn't live where you might expect. At runtime the working
copy is in memory-backed storage, and at every boot Vault restores it from its
own snapshot chain (cache-pool snapshot, rotated copies, USB shadow copy). A
`vault.db` you place on the boot flash by hand is overwritten by that chain
before Vault ever reads it.

The supported way to bring settings back is the **Recover Vault** wizard (or
the `restore-db` API), which validates the backup and swaps it in atomically.

---

## Recovering with the wizard

Open the **Recovery** page and click **Recover Vault** (or choose **Recover
from a backup** on first run) to start the wizard. It is available any time —
not just on a fresh install. Keep in mind that restoring **replaces** the
jobs and storage destinations currently on this Vault; on a configured system
the wizard warns you with the exact counts before it does anything. The
wizard has five steps.

<!-- screenshot: step-1 -->

### Step 1 — Connect storage

Add the storage destination that holds your backups — the same host, share,
or bucket you were writing to before — and click **Connect**. You can
optionally click **Test connection** first to check the details before
saving. Once you connect, the wizard looks for a `_vault` folder on that
destination and lists the database backups it finds there, newest first.

<!-- screenshot: step-2 -->

### Step 2 — Backup password

If your database backups are encrypted, enter the passphrase from
**Settings → Encryption** on your old server. Vault verifies the password
before touching anything — a wrong password shows a friendly error and lets
you try again rather than failing partway through a restore. If your backups
were never encrypted, this step is skipped automatically.

<!-- screenshot: step-3 -->

### Step 3 — Restore

The most recent backup is preselected from a list showing each snapshot's
date. Confirm to proceed. The wizard states explicitly that this replaces the
settings currently on this Vault — on a configured system it shows how many
jobs and storage destinations will be replaced. Your jobs, storage
destinations, and history come back from the snapshot. Nothing on your backup
storage is read or modified beyond downloading the database file: your actual
container, VM, and folder archives are untouched.

<!-- screenshot: step-4 -->

### Step 4 — Check paths

Restored jobs and folder items may reference paths that don't exist on the
rebuilt server — a share renamed during the rebuild, a disk assigned to a
different mount point. This step flags any path that isn't found and offers
a remap field with suggested mounts from the current server. It's skippable
if you'd rather fix paths later from the Jobs or Storage pages.

<!-- screenshot: step-5 -->

### Step 5 — Done

A summary of what was restored (jobs and storage destinations) closes out
the wizard, with a pointer to the normal **Restore** page
to bring your actual data — container appdata, VM disks, folders — back from
the restore points that came with the database.

---

## Prepare before disaster strikes

- **Store your backup password off the server** — password manager, printed
  note, anywhere that survives the server. Without it, encrypted backups
  cannot be decrypted by anyone, including you.
- **Enable database backup on at least one destination** (Storage →
  destination → **Include in DB backup**). This writes your settings
  alongside your data after every successful backup.
- **Keep one destination off-box** (SMB/NFS/SFTP/S3) so a dead server doesn't
  take your backups with it.
- Optionally note your storage connection details (host, share, username)
  somewhere safe — recovery starts by reconnecting to storage.

---

## Recovering without the web UI (CLI fallback)

If you cannot reach the web console, you can restore the database over the
API from any machine that can reach the daemon:

```sh
read -rs VAULT_BACKUP_PASSWORD   # prompts without echoing or recording history
jq -n --arg p "$VAULT_BACKUP_PASSWORD" \
  '{storage_path: "_vault/vault.db.latest.age", passphrase: $p}' |
curl -X POST http://SERVER:24085/api/v1/storage/ID/restore-db \
  -H 'Content-Type: application/json' -d @-
unset VAULT_BACKUP_PASSWORD
```

`read -rs` prompts for the password without echoing it and keeps it out of
your shell history and the process list; `jq` (included with Unraid) encodes
it safely even if it contains quotes or backslashes. A JSON file with
`-d @restore.json` works too.

List available snapshots first with `GET /api/v1/storage/ID/db-backups`.

If an API key is configured and you are calling from a non-loopback address,
include it with `-H "X-API-Key: $VAULT_API_KEY"`.

---

## After recovery

- Run the path check (wizard step 4, or review Jobs/Storage) if your array or
  share layout changed.
- Restore data via **Restore** as usual.
- Re-check Settings → Encryption and consider a fresh test backup to confirm
  the pipeline end to end.

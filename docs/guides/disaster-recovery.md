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

If you use deduplicated destinations, this step also unlocks them. They are
sealed with the old server's `vault.key`, which a rebuilt server does not
have. When a backup ran with a backup password set, that password also opens
them: the wizard uses the password from step 2, or asks for it here, and
re-seals each destination with this server's key. A destination it cannot
unlock is listed with what it needs — the backup password that was set when
its backups ran, or the original `vault.key` (see
[Restoring vault.key](#restoring-vaultkey)).

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
- **Keep a copy of `vault.key` off the server** (Settings → Security →
  **Server key** → Download). Deduplicated destinations are sealed with this
  file. They also open with your backup password, but only once a backup has
  run with that password set. **If you use deduplication without a backup
  password, `vault.key` is the only way back into those backups.**
- **Enable database backup on at least one destination** (Storage →
  destination → **Include in DB backup**). This writes your settings
  alongside your data after every successful backup.
- **Keep one destination off-box** (SMB/NFS/SFTP/S3) so a dead server doesn't
  take your backups with it.
- Optionally note your storage connection details (host, share, username)
  somewhere safe — recovery starts by reconnecting to storage.

### Restoring vault.key

`vault.key` lives beside the Vault database: on Unraid,
`/boot/config/plugins/vault/vault.key`. To put a saved copy back:

1. Stop Vault (Settings → Vault → Stop, or `/etc/rc.d/rc.vault stop`).
2. Copy the file to `/boot/config/plugins/vault/vault.key`.
3. Start Vault.

The CLI tools take a key from anywhere with `--key`, for example
`vault dedup repair --key /path/to/vault.key` or
`vault recover … --key /path/to/vault.key`.

Uninstalling the plugin keeps `vault.key`, so a reinstall opens the same
destinations. To remove every trace of Vault, delete
`/boot/config/plugins/vault/` by hand after uninstalling — only do this if you
no longer need the backups or have a copy of the key.

**Security note:** the backup password now protects deduplicated
destinations as well. Anyone with the password and access to the backup
storage can read them, just as with `vault.key`, so use a strong password.

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

## Recovering files without a Vault server

If you cannot run Vault at all — the server is dead and you just need files
back — `vault recover` reads your backups straight from storage and extracts
them into a folder. It runs on **Linux, macOS and Windows**, needs no Vault
database, and **never writes to or deletes from the backup storage**.

Download `vault-windows-amd64.exe` from the
[releases page](https://github.com/ruaan-deysel/vault/releases) on Windows. On
Linux the `vault` binary from the plugin package works the same way.

### What you need

- **Access to the backup storage.** A local disk, a mounted network share, or
  the connection details of an SFTP, SMB, WebDAV or S3 destination. NFS
  exports are not mounted for you: mount the export and point `--path` at it.
- **`vault.key` or the backup password, for deduplicated backups.** The
  dedup master key is sealed with the server key: copy
  `/boot/config/plugins/vault/vault.key` from the Unraid flash drive (or a
  flash backup) and pass `--key`. Without the key, the backup password opens
  a deduplicated destination if a backup ran with it set; pass it with
  `--passphrase-file` or `VAULT_PASSPHRASE`.
- **The backup passphrase, for encrypted classic backups.** Put it in a file
  and pass `--passphrase-file`, or set `VAULT_PASSPHRASE`. Passphrases and
  passwords are never accepted as command-line arguments.

### Steps

The examples use Windows paths; on Linux or macOS use paths like
`/mnt/backups` and `~/vault.key`. On Windows, run them in PowerShell or
Command Prompt as `.\vault-windows-amd64.exe recover …`.

1. **List the backups.** Each line's `POINT` is what you pass to the other
   commands; `<job>/latest` means that job's newest backup.

   ```sh
   vault recover list --path Z:\backups --key C:\keys\vault.key
   ```

2. **Look inside one item** (optional):

   ```sh
   vault recover contents --path Z:\backups --key C:\keys\vault.key --point "Daily Containers Backup/latest" --item sonarr
   ```

3. **Extract.** Each item gets its own folder under `--to`. Add `--include`
   (repeatable) to extract only some paths, using the paths `contents` prints.

   ```sh
   vault recover extract --path Z:\backups --key C:\keys\vault.key --point "Daily Containers Backup/latest" --item sonarr --to C:\recovered
   ```

For other storage types, use `--type` with `--config-file`, a JSON file with
the same fields as the destination's settings in Vault. For example
`--type s3 --config-file s3.json`:

```json
{
  "bucket": "my-backups",
  "region": "us-east-1",
  "endpoint": "https://s3.us-east-1.amazonaws.com",
  "access_key": "…",
  "secret_key": "…",
  "base_path": "vault"
}
```

| `--type` | Fields                                                                                          |
| -------- | ----------------------------------------------------------------------------------------------- |
| `s3`     | `bucket`, `region`, `endpoint`, `access_key`, `secret_key`, `base_path`, `force_path_style`     |
| `sftp`   | `host`, `port`, `user`, `password` or `key_file`, `base_path`, `host_key` or `known_hosts_file` |
| `smb`    | `host`, `port`, `user`, `password`, `share`, `base_path`                                        |
| `webdav` | `url`, `username`, `password`, `base_path`                                                      |

Keep that file private — it contains your storage credentials.

### What you get

- **Folders and plugins:** the backed-up files.
- **Containers:** each volume's files under the volume's path inside the
  container (for example `config/…`), plus `_vault-metadata/` with the
  container's Unraid template, configuration, image archive and (when
  enabled) database dump, for rebuilding the container on a new host.
- **VMs and other items:** the stored files as-is (disk images, `domain.xml`,
  NVRAM), decrypted and decompressed. For an incremental or differential VM
  backup each run's files go in their own folder, oldest first, because the
  later disk images are deltas that need the earlier ones.
- **Incremental and differential backups** are rebuilt from their whole
  chain, and files deleted before the chosen backup are removed again.
- `--raw` copies a classic backup's archives out without unpacking them.

Stored checksums are verified while extracting. A summary is printed and a
`vault-recover-report-<time>.json` file is written into the `--to` folder.

**On Windows**, names Windows cannot store are rewritten: each `:` `?` `*`
`"` `<` `>` `|`, and a trailing dot or space, is replaced with `_`
(`run:01.log` becomes `run_01.log`); a device name such as `CON` gets a
leading `_` (`CON.txt` becomes `_CON.txt`); and names that differ only by
case get a `(2)` suffix. Every rename is listed in the
report. Symbolic links are not recreated on Windows. On Linux and macOS,
relative links that stay inside the item are recreated. Absolute links,
device files and file ownership are never recreated. `--safe-names on`
applies the Windows naming rules on Linux and macOS too, for example before
copying the files to a Windows share; on Windows they always apply.

To put recovered data back on a new Vault server, use the [wizard](#recovering-with-the-wizard)
instead: it restores containers, VMs and settings in place.

---

## After recovery

- Run the path check (wizard step 4, or review Jobs/Storage) if your array or
  share layout changed.
- Restore data via **Restore** as usual.
- Re-check Settings → Encryption and consider a fresh test backup to confirm
  the pipeline end to end.

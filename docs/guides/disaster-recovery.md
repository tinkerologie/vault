# Disaster Recovery

Use this guide to recover Vault after a server failure, an array rebuild, or an Unraid reinstallation.

## The short version

1. Reinstall Unraid.
2. Install the Vault plugin.
3. Open **Disaster Recovery** → **Recover Vault**. On first run, you can select **Recover from a backup**.
4. Connect to the storage destination that contains the backups.
5. If the database backup is encrypted, enter the backup password.
6. Restore the Vault database.
7. Change any folder paths that are different on the new server.
8. Restore your data from **Restore**.

Make sure that you can connect to the backup storage. For an encrypted classic backup, the backup password is necessary. For a deduplicated backup, use the original `vault.key` or the password for the recovery copy on the destination.

---

## Database snapshots on the flash drive

Vault normally uses a working database in RAM. At startup, Vault uses a snapshot from the cache pool or a copy from the flash drive. Vault can overwrite a database that you copy to the flash drive.

Vault signs cache pool snapshots with `vault.key`. The signature is in a `.auth` file. Vault keeps signatures for rotated copies in `rotated-auth/`.

On the first signed save after an upgrade, Vault writes `snapshot-auth.enrolled` next to `vault.key`. After that, Vault skips cache snapshots with missing or incorrect signatures. If no candidate passes validation, Vault uses a new database. Vault accepts USB copies without a signature.

To restore an older unsigned snapshot after enrollment:

1. Add `ALLOW_UNSIGNED_SNAPSHOT="yes"` to `vault.cfg`.
2. Restart Vault.
3. After Vault starts, remove the line from `vault.cfg`.

Vault still refuses a snapshot with an incorrect signature. Use **Recover Vault** or the `restore-db` API to validate and replace the database in one operation.

---

## Recovering with the wizard

Open **Disaster Recovery**. Click **Recover Vault**. On first run, you can select **Recover from a backup** instead.

The wizard has five steps. Recovery **replaces the current Vault database**, including jobs, storage destinations, restore points, and history. Before recovery, the wizard shows how many jobs and destinations it will replace.

<!-- screenshot: step-1 -->

### Step 1 — Connect storage

1. Enter the connection settings for the storage destination that contains your backups.
2. If necessary, click **Test connection**.
3. Click **Connect**.

The wizard searches the `_vault` directory. It shows the database backups from newest to oldest.

<!-- screenshot: step-2 -->

### Step 2 — Backup password

If Vault encrypted the database backup, enter the backup password from the old server. Vault tests the password before it starts recovery. If the password is incorrect, enter it again. Vault skips this step for an unencrypted database backup.

<!-- screenshot: step-3 -->

### Step 3 — Restore

**Caution.** Make sure that you have the correct database backup. Recovery replaces the current database and its restore-point records.

The wizard shows the backups by date. It selects the newest backup by default.

1. Select the database backup.
2. Click **Restore this backup**.
3. If the current database is empty, click **Yes, restore my settings**.
4. If the wizard shows the replacement dialog, enter `REPLACE`.
5. Click **Replace Database** in the replacement dialog.

If this server already has an API key, Vault keeps that key. Vault downloads the database file and does not change the container, VM, or folder archives.

<!-- screenshot: step-4 -->

### Step 4 — Check paths

The wizard shows any paths it cannot find on the new server. It also shows possible replacement paths. Enter a replacement path for each applicable item. You can also change the paths later on **Jobs** or **Storage**.

Vault uses the old server's `vault.key` to protect deduplicated destinations. A backup password can also unlock a destination if a backup saved a recovery copy of the key with that password.

The wizard uses the password from step 2 or asks for it in this step. Vault encrypts the destination keys again with this server's key. If a destination stays locked, the wizard shows which password or key to use. See [Restoring vault.key](#restoring-vaultkey).

<!-- screenshot: step-5 -->

### Step 5 — Done

The wizard shows the restored jobs and storage destinations. Open **Restore** to recover container appdata, VM disks, or folders from the restore points.

---

## Prepare before disaster strikes

- Keep the backup password outside the server. The password is necessary to decrypt encrypted backups.
- Keep a copy of `vault.key` outside the server. To download the key, open **Settings** → **Security** → **Server key**. For a deduplicated destination, use the original key if the destination has no recovery copy. Vault also encrypts storage passwords and replication API keys with this key. See [Stored credentials and vault.key](#stored-credentials-and-vaultkey).
- On at least one storage destination, enable **Include in DB backup**. Vault then writes your settings to that destination after a successful backup.
- Keep at least one storage destination outside the server. The backups stay available if the server fails.
- Keep the storage connection settings in a safe place. Use these settings to connect to storage during recovery.

### Restoring vault.key

`vault.key` lives beside the Vault database: on Unraid,
`/boot/config/plugins/vault/vault.key`. To put a saved copy back:

1. Stop Vault (Settings → Vault → Stop, or `/etc/rc.d/rc.vault stop`).
2. Copy the file to `/boot/config/plugins/vault/vault.key`.
3. Start Vault.

The CLI tools take a key from anywhere with `--key`, for example
`vault dedup repair --key /path/to/vault.key` or
`vault recover … --key /path/to/vault.key`.

### Stored credentials and vault.key

Storage passwords, S3 keys and replication API keys are encrypted in the
database with `vault.key`. Hosts, paths and usernames stay readable. The same
server keeps working as before, including after restoring an older database,
which is sealed as soon as it is restored.

If you restore the database on a server with a different `vault.key`, **Storage** shows **Credentials locked**. Backups and replication syncs fail if the applicable credentials are locked.

Use one of these procedures to unlock the credentials:

- Restore the original `vault.key` with the [key restore procedure](#restoring-vaultkey).
- Enter the credentials again for each storage destination or replication target.

You can still change other settings on a locked destination, such as its name
or path, and the sealed credentials are kept.

A locked deduplicated destination needs one of these before its dedup key can
be unlocked.

Upgrading seals existing credentials on the first start, and also rewrites the
local snapshot, rotated snapshots and flash copies. Database backups already on
your storage are not rewritten. Without a backup password, older ones still hold
plaintext credentials until pruning removes them, after 14 more backups. Delete
`_vault/vault.db.*` on the destination if you want them gone sooner.

When you uninstall Vault, it keeps `vault.key`. A new installation can use the key to open the same destinations.

If you have a copy of the key or do not need the backups, you can delete `/boot/config/plugins/vault/` after uninstallation.

If the destination has a recovery copy of the key, anyone with its password and access to the destination can read the backups. Use a strong backup password.

---

## Recovering without the web UI (CLI fallback)

If you cannot reach the web UI, use the API from a device that can connect to Vault.

1. If necessary, add the backup destination with `POST /api/v1/storage`.
2. Find its destination ID with `GET /api/v1/storage`.
3. List its snapshots with `GET /api/v1/storage/ID/db-backups`.
4. Select the path of the database backup to restore.

The example uses an encrypted database backup. Replace `_vault/vault.db.latest.age` with the selected path. For an unencrypted `.db` file, send only the `storage_path` field. Replace `ID` with the destination ID. Replace `SERVER` with the address on which Vault listens. Use `127.0.0.1` on the server for the default configuration.

```bash
IFS= read -rs -p 'Backup password: ' VAULT_BACKUP_PASSWORD
printf '\n'
printf '%s' "$VAULT_BACKUP_PASSWORD" |
jq -Rs '{storage_path: "_vault/vault.db.latest.age", passphrase: .}' |
curl -X POST http://SERVER:24085/api/v1/storage/ID/restore-db \
  -H 'Content-Type: application/json' -d @-
unset VAULT_BACKUP_PASSWORD
```

The `read` command reads the password without showing it on the screen. `jq` encodes the password as JSON, including quotes or backslashes. You can also supply a JSON file with `-d @restore.json`.

On the Vault server, you can use `curl` without an API key. From another machine, include `-H "X-API-Key: $VAULT_API_KEY"`.

A new installation has no API key. To make one, run `vault api-key generate` on the server. You can also use **Settings** → **Security** → **API Access** in the Vault UI.

---

## Recovering files without a Vault server

Use `vault recover` to extract files from backup storage without a running Vault server. The command works on **Linux, macOS, and Windows**. It works without the Vault database. It does not write to or delete data from backup storage.

Download `vault-windows-amd64.exe` from the
[releases page](https://github.com/tinkerologie/vault/releases) on Windows. On
Linux the `vault` binary from the plugin package works the same way.

### What you need

- **Access to the backup storage.** A local disk, a mounted network share, or
  the connection details of an SFTP, SMB, WebDAV or S3 destination. NFS
  exports are not mounted for you: mount the export and point `--path` at it.
- **Deduplicated backups.** The original server key protects the deduplication master key. Copy `/boot/config/plugins/vault/vault.key` from the Unraid flash drive or its backup. Specify the file with `--key`. If the destination has a recovery copy of the key, you can use its backup password. Use `--passphrase-file` or `VAULT_PASSPHRASE`.
- **The backup passphrase, for encrypted classic backups.** Put it in a file
  and pass `--passphrase-file`, or set `VAULT_PASSPHRASE`. Passphrases and
  passwords are never accepted as command-line arguments.

### Steps

The examples use Windows paths. On Linux or macOS, use paths such as
`/mnt/backups` and `~/vault.key`. On Windows, run them in PowerShell or
Command Prompt as `.\vault-windows-amd64.exe recover …`.

1. **List the backups.** Use the `POINT` value in the other commands.
   Use `<job>/latest` for the newest backup for that job.

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

| `--type` | Fields                                                                                                                                                                                                                          |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `s3`     | `bucket`, `region`, `endpoint`, `access_key`, `secret_key`, `base_path`, `force_path_style`                                                                                                                                     |
| `sftp`   | `host`, `port`, `user`, `password` or `key_file`, `base_path`, `host_key` or `known_hosts_file` (required. If you omit both, the error shows the server's fingerprint. Confirm the fingerprint before you use it as `host_key`) |
| `smb`    | `host`, `port`, `user`, `password`, `share`, `base_path`                                                                                                                                                                        |
| `webdav` | `url`, `username`, `password`, `base_path`                                                                                                                                                                                      |

Keep that file private — it contains your storage credentials.

### What you get

- **Folders and plugins:** the backed-up files.
- **Containers.** Vault puts each volume under its container path (for example, `config/…`). The `_vault-metadata/` directory contains the Unraid template, configuration, image archive, and database dump if available.
- **VMs and other items.** Vault decrypts and decompresses the files (for example, disk images, `domain.xml`, and NVRAM). For an incremental or differential VM backup, each run has its own directory, in order from oldest to newest. Later disk images depend on earlier ones.
- **Incremental and differential backups** are rebuilt from their whole
  chain, and files deleted before the chosen backup are removed again.
- `--raw` copies a classic backup's archives out without unpacking them.

Stored checksums are verified while extracting. A summary is printed and a
`vault-recover-report-<time>.json` file is written into the `--to` folder.

On Windows, Vault changes names that Windows cannot store.

- Vault replaces `:` `?` `*` `"` `<` `>` `|` and trailing dots or spaces with `_`. For example, `run:01.log` becomes `run_01.log`.
- Vault puts `_` before reserved device names. For example, `CON.txt` becomes `_CON.txt`.
- Vault adds a suffix such as `(2)` when names differ only by case.

The report lists all name changes. Vault does not recreate symbolic links on Windows. On Linux and macOS, Vault recreates relative links if their targets stay inside the item. Vault does not recreate absolute links, device files, or file ownership.

On Linux or macOS, use `--safe-names on` to apply the Windows naming rules. On Windows, Vault always applies these rules.

To recover on a replacement server, use the [wizard](#recovering-with-the-wizard) to recover the Vault database. Then use **Restore** to recover containers, VMs, and folders.

---

## After recovery

- Run the path check (wizard step 4, or review Jobs/Storage) if your array or
  share layout changed.
- Open **Restore** to restore your data.
- Re-check Settings → Encryption and consider a fresh test backup to confirm
  the pipeline end to end.

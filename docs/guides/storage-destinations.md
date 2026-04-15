# Storage Destinations

Vault writes compressed (and optionally encrypted) backup archives to a storage destination. This page explains how to configure each supported type.

## Overview

| Type | When to use |
|------|-------------|
| **Local** | Unraid array share, directly attached USB/SSD, or any locally mounted path |
| **SFTP** | Any Linux/BSD server reachable by SSH — another NAS, a VPS, a cloud VM |
| **SMB** | Windows shares and Samba servers; common for Synology and TrueNAS SCALE |
| **NFS** | NFS exports from Linux/BSD servers and most NAS devices |

---

## Local

| Field | Description |
|-------|-------------|
| **Path** | Absolute path where Vault will write backups (e.g. `/mnt/user/backups`). The directory must exist and be writable by the `vault` process. |

**Notes:**
- Local destinations are the fastest option — no network overhead.
- Avoid putting backups on the same array as the source data if you want protection against disk failure.
- Vault only wakes the destination disk when it needs to write; it does not keep disks spinning between runs.

---

## SFTP

| Field | Description |
|-------|-------------|
| **Host** | Hostname or IP of the SSH server |
| **Port** | SSH port (default: `22`) |
| **Username** | SSH user with write access to the remote path |
| **Password** | Password for the SSH user (not required if using a key) |
| **Remote Path** | Absolute path **on the SSH server** where Vault will store backups (e.g. `/volume1/vault-backups`). The directory must exist and the user must have write permission. |

**Tips:**
- Prefer key-based authentication for unattended backups. Add the Unraid server's public key (`~/.ssh/id_rsa.pub`) to the remote user's `~/.ssh/authorized_keys`.
- Test the connection before saving — Vault will attempt to list the remote directory and report any permission or connectivity errors.
- SFTP is the most broadly supported remote protocol and works with virtually any server running OpenSSH.

---

## SMB / CIFS

| Field | Description |
|-------|-------------|
| **Host** | Hostname or IP of the SMB server |
| **Share** | The top-level SMB share name as configured on the server (e.g. `Backups`). This is the share name only — not a path. |
| **Path** | Optional sub-folder **within the share** where Vault will write its data (e.g. `vault/my-server`). Leave blank to use the share root. |
| **Username** | SMB user with write access |
| **Password** | SMB password |
| **Domain** | Windows domain (leave blank for workgroup/Samba) |

**Understanding Share vs Path:**

```
SMB server: \\nas\Backups\vault\my-server
             └── host   └── share
                              └── path (sub-folder within share)
```

The **Share** field maps to the first component after the hostname. The **Path** field is everything after that.

**Tips:**
- SMB is the best choice for Synology NAS, Windows Server, and TrueNAS SCALE.
- Vault enforces a 30-second dial timeout so a misconfigured SMB destination will fail fast rather than hanging.

---

## NFS

| Field | Description |
|-------|-------------|
| **Host** | Hostname or IP of the NFS server |
| **Export Path** | The path the NFS server exports — matches the entry in `/etc/exports` on the server (e.g. `/mnt/user/backups`). This is what gets mounted, not a sub-path within it. |
| **Base Path** | Optional sub-directory **within the mounted export** where Vault will write its data. Leave blank to use the export root directly. |

**Understanding Export Path vs Base Path:**

```
NFS server exports: /mnt/user/backups
                     └── export path (what gets mounted)

Vault writes to:    /mnt/user/backups/vault/my-server
                                      └── base path (sub-folder within mount)
```

**Tips:**
- The export path must be listed in `/etc/exports` on the NFS server and the Unraid server's IP must be allowed.
- NFS v3 is used by default. Ensure the `nfs-utils` package or equivalent is installed on the server.
- NFS offers the lowest overhead for LAN backups to a Linux-based NAS.

---

## Testing a Connection

After filling in the fields, always click **Test Connection** before saving. Vault will:

1. Attempt to connect using the provided credentials
2. Try to create and delete a temporary test file in the configured path
3. Report success or a specific error (wrong password, path not found, permission denied, etc.)

If the test fails, fix the error before saving — the destination will not work for backups until the connection succeeds.

---

## Importing Existing Backups

If you have existing Vault backups (or AppData Backup archives) on a storage destination, you can import them:

1. Go to **Storage** and find the destination
2. Click the **...** menu → **Import Backups**
3. If your backups are in a sub-folder, enter the sub-folder path in the **Subfolder** field (e.g. `appdata-backups/`)
4. Click **Scan** — Vault will list all importable archives it finds
5. Select the archives to import and click **Import**

Imported backups appear as restore points on the relevant jobs so you can restore from them immediately.

---

## Encryption

If you have configured an encryption passphrase under **Settings → Security → Encryption**, all backup archives written to storage are encrypted with AES-256-GCM before they leave the server. The encryption key is derived from your passphrase — make sure to keep it safe, as there is no recovery path if it is lost.

Encryption is transparent to storage destinations — it applies regardless of destination type.

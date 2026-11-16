# Validate assistant restores on Unraid

Use an approved disposable Unraid 7 or later environment. Run the folder, Docker, and VM drills on the same candidate build before releasing assistant restores.

## Prepare the candidate

1. Confirm that issues #639 through #646 are merged into the candidate. Record the commit, version and binary checksum.
2. Confirm the deployment scope and inventory with the operator. Use only the tagged build, deploy, and verify roles. Exclude untagged playbook runs and `make redeploy`. They include roles outside the drill deployment.

   ```sh
   make build
   make deploy
   make verify
   ```

3. Confirm that health, WebSocket, MCP, and the Vault UI work. Compare the installed binary and served UI assets with the candidate.
4. Create a drill root with separate source, storage, restore, and scratch directories. Check the resolved paths for overlap and symlinks. Create a local Vault Storage Destination for the storage directory. Create one manual-only Job with one Backup Item for each drill. Disable automatic retries.
5. Create two disposable Assistant Keys in **Settings** → **Security** → **Assistant Keys**. Leave one inspection-only. Enable **Allow Restore Requests** for the other. Store each key in a private file outside the repository. Use HTTPS or a trusted LAN for remote checks. Use an SSH tunnel for the host-local checks.

Build the operator's drill helper from the same checkout.

```sh
go build -o "$SCRATCH/restore-drill" ./scripts/restore-drill
```

## Verify the permission boundary

Use a known disposable Backup Item with a completed Backup Run. Check that `inspect` returns recorded item membership, Backup Chain, artifact, and Verification evidence.

```sh
"$SCRATCH/restore-drill" inspect -endpoint "$MCP_ENDPOINT" -key-file "$INSPECTION_KEY_FILE" \
  -point "$POINT_ID" -type "$ITEM_TYPE" -item "$ITEM_NAME"
"$SCRATCH/restore-drill" negative -endpoint "$MCP_ENDPOINT" -key-file "$INSPECTION_KEY_FILE" \
  -job "$JOB_ID" -point "$POINT_ID" -type "$ITEM_TYPE" -item "$ITEM_NAME"
```

Run the boundary checks directly and through the SSH tunnel. Confirm that `negative` receives the Restore Request permission refusal. Use the request-scoped key to check that the direct REST restore route returns HTTP 403. Use a nonexistent Job ID to keep this check free of restore effects. Verify that submission returns `pending` without a Run ID. Check that the destination is unchanged. Open the Review Link in a signed-out browser. Confirm that approval requires sign-in.

Use an extra disposable request for this last check. Withdraw it after the check and confirm the recorded state.

```sh
"$SCRATCH/restore-drill" withdraw -endpoint "$MCP_ENDPOINT" -key-file "$REQUEST_KEY_FILE" -id "$REQUEST_ID"
```

## Request and follow a restore

1. Run the relevant Backup Job. Record the Backup Run and its Restore Point. Require one completed item and zero failures or skipped items.
2. Inspect the point with the inspection key. Use the request-scoped key to submit the legacy `restore_item` call through the helper.

   ```sh
   "$SCRATCH/restore-drill" request -endpoint "$MCP_ENDPOINT" -key-file "$REQUEST_KEY_FILE" \
     -job "$JOB_ID" -point "$POINT_ID" -type "$ITEM_TYPE" -item "$ITEM_NAME"
   ```

   Add `-destination "$RESTORE_TARGET"` for an approved alternate location. Check `eligible_for_approval` and record any warnings.

3. Open the Review Link in signed-in Unraid. Confirm the Restore Point, Backup Item, exact target, and write effects. Select **Approve and run** only after the operator authorizes the exact restore. Enter any Backup Passphrase in the Vault review.
4. Follow the request with the requesting Assistant Key until it records the terminal outcome. Confirm that Unraid History shows the same Restore Run ID. Record the verified approval time from the operator view. Capture redacted item outcomes and readiness checks from the History logs.

   ```sh
   "$SCRATCH/restore-drill" follow -endpoint "$MCP_ENDPOINT" -key-file "$REQUEST_KEY_FILE" -id "$REQUEST_ID"
   ```

### Folder drill

- Use `folder-create "$SOURCE_DIR" "$SCRATCH/folder.json"` to create the fixture and SHA-256 manifest. Keep the manifest outside all source, storage, and restore paths.
- Before requesting the restore, add an extra file to a separate disposable restore directory. Review a replace operation for that directory.
- After the Restore Run completes, use `folder-check "$RESTORE_TARGET" "$SCRATCH/folder.json"`. Require an exact match for the regular files, file hashes, and directory set. Confirm that the extra file is gone. Record permissions, ownership, and timestamps separately.

### Docker drill

- Create a uniquely named MariaDB container in Unraid using a pinned image. Use a dedicated appdata bind and disposable credentials. Disable published ports and elevated host privileges.
- Insert `before-backup` and run the backup. Update the value to `after-backup` and read it back. With the operator's approval, stop and remove only the disposable fixture container.
- Review the original-location Restore Request, including container and volume effects. If it requires a privilege or image override, record the drill as blocked.
- After the Restore Run completes, check the container name, running state, and exact mount source with `docker inspect`. Confirm `before-backup` through `docker exec`, then write and read a new row. Check the restored `my-<name>.xml` template. Record skipped volumes and warnings.

### VM drill

- Create a guest domain with a known hostname and a working QEMU guest agent. Give it dedicated disks and NVRAM if applicable. Configure the Backup Item to start after restore and check `guest_agent` readiness. Back up the VM while it is running. Confirm that `qemu-img` is present if the Restore Point needs earlier VM disk layers.
- Review the VM name, UUID, disk and NVRAM targets, whether the existing domain will be replaced, and the start and readiness policy.
- After the Restore Run completes, confirm `virsh domstate` reports the guest running. Query the fixture hostname through the guest agent, or verify the expected response from a disposable guest test service. If readiness fails, record the remaining domain and any restored disks.

## Record results and clean up

Post one redacted comment for the boundary checks and one for each workload on [issue #647](https://github.com/tinkerologie/vault/issues/647). Include the candidate commit, checksum, and Unraid version in each comment. Record the Backup Run, Restore Point, target label, and write mode for each workload. Include the approval time, Restore Request, linked Restore Run, and redacted History log outcome. Remove host addresses, keys, credentials, cookies, Backup Passphrases, and internal paths from public evidence.

| Comment  | Independent observations to record                                                                                                                                    |
| -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Boundary | Direct and tunneled inspection results, MCP refusal and REST status for each transport, signed-out browser result, pending Run ID and target check, withdrawal result |
| Folder   | Manifest match, file and directory totals, seeded extra file absent, permission and timestamp observations                                                            |
| Docker   | Container state and mount match, restored `before-backup` value, new row write and read, template check, skipped volumes                                              |
| VM       | Running domain, hostname or test-service result, recorded startup and guest readiness, disks and NVRAM verified                                                       |

If a check fails, record the reason and link the defect to #647. Close the release gate only after the boundary checks and all three independent workload checks pass on the same candidate.

With the operator's approval, clean up the exact drill Jobs, Storage Destination, fixture root, container and template, VM domain, disks, and NVRAM. Revoke the disposable Assistant Keys and remove their private files. Use ordinary Vault and Unraid controls for this cleanup.

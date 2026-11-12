---
status: accepted
---

# Require operator approval for MCP restores

Assistants need to inspect what Vault can recover and follow a Restore to its outcome. The current MCP `restore_item` tool starts an untracked restore and asks for approval only for certain container exceptions. The shared API Key also authorizes direct restores through REST. An approval prompt in an assistant client cannot enforce the boundary in Vault.

## Decision

- Give each assistant an Assistant Key. It grants read-only inspection by default. The operator can separately allow the assistant to submit and follow its own Restore Requests. Vault enforces the key's permissions across its interfaces, including when the key is presented on a host-local connection.
- Require a signed-in operator to approve the exact Restore Request in Vault's Unraid UI. The approval endpoint requires server-side proof of that authority. This extends [ADR 0005](0005-host-trust-and-secret-ownership.md) for Restore Approval because host-local transport also covers direct connections and SSH tunnels.
- Apply the approval requirement to every MCP restore, including clients using `restore_item` today. Those clients will need to use the approval flow. Reuse the tracked Runner path for the approved Restore Run and its result.
- In the first release, each Restore Request covers one whole folder, Docker container, or VM from one Restore Point. ZFS and plugin restores follow once their review and restore flows have been tested. The request names the restore location and the effect on existing data so the operator can review both before approving.

## Approval and execution

- **Approve and run** starts the exact Restore Request when the Runner can admit it immediately. If the Runner is busy, the request stays pending for the operator to approve again.
- The operator can approve the original location or a separate location when the restore engine supports it. For an original-location restore, Vault shows the affected data and the replacement or merge behavior before approval.
- The operator enters any required Backup Passphrase in the approval screen. Vault uses it for that Restore and keeps it out of the Restore Request, assistant responses, and history.
- Container restores that need an explicit host privilege or image substitution override use the manual Restore wizard in the first release.
- Vault durably links the approved Restore Request to at most one Restore Run. Repeated approval submissions for the same request return the existing run. When the daemon restarts during execution, Vault records the run as interrupted. Another attempt requires fresh approval.

## Inspection and request lifecycle

- An Assistant Key can inspect recovery metadata across all Jobs. Assistant-visible restore results are redacted. Downloading backup files requires administrator access. Assistants with restore request permission can follow the requests they own.
- Before approval, Vault confirms that the selected item belongs to the Restore Point and resolves the required Backup Chain. It shows the available Verification evidence and permits an unverified backup with a visible warning. The result reports the readiness checks that ran and explicitly marks application health as unchecked when Vault has no such result.
- The assistant receives a direct link to the request in the Unraid UI. Vault lists pending requests on the Restore page.
- Vault rechecks the selected Restore Point, required Backup Chain, and reviewed restore location before execution. If a required point has been removed or the reviewed location or write effects change, Vault invalidates the request and requires a new one.
- A pending Restore Request expires 24 hours after submission. Its expired status stays visible in history.
- An assistant with restore request permission can withdraw its own pending Restore Request. Only a signed-in operator can stop an active Restore Run. Vault identifies the exact run and warns that stopping can leave the restore location partly restored.
- The audit record links the request, verified approval through the Unraid UI, and the Restore Run. It includes the operator's name when Vault can verify the identity.

## Release validation

Exercise the full assistant request, operator approval, and tracked result flow on disposable Unraid workloads for each supported type. Compare restored folder contents, check that the restored container application works, and boot the restored VM before releasing the feature.

The decision is accepted for the planned feature. Implementation is pending.

Sources: [API authentication](../../internal/api/middleware.go), [MCP tools](../../internal/mcp/server.go), [Unraid proxy](../../plugin/pages/include/proxy.php), [tracked restore](../../internal/runner/runner.go).

---
status: accepted
---

# Distinguish host-local trust from remote access

Vault permits keyless host-local access only under its local-address, trusted-host and origin checks, while remote access requires an API Key and secret-management routes retain local-only restrictions. Stored credentials and repository keys are sealed with the Server Key, so database recovery on another installation may require restoring that key or re-entering credentials.

Sources: [internal/api/middleware.go](../../internal/api/middleware.go), [docs/guides/disaster-recovery.md](../../docs/guides/disaster-recovery.md).

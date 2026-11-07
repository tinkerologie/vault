---
status: accepted
---

# Distinguish host-local trust from remote access

Vault permits keyless access from the Vault host. The Host header must identify that host, and the request must satisfy the same-origin check. For remote access, send an API key. Use local access for secret management routes.

The server key protects stored credentials and repository keys. If you recover the database on a different installation, the original key or the credentials can be necessary.

Sources: [internal/api/middleware.go](../../internal/api/middleware.go), [docs/guides/disaster-recovery.md](../../docs/guides/disaster-recovery.md).

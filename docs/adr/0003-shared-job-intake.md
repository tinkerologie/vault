---
status: accepted
---

# Share Job mutation policy across REST and MCP

Separately implemented Job creation allowed validation and scheduler activation to diverge, so REST and MCP share Job Intake for creation, update and deletion. Intake owns validation, persistence and follow-up effects while replication/import and item editing retain their distinct paths; this is a common policy interface, not a transaction spanning scheduler, persistence hooks and broadcasts.

Sources: [internal/jobs/intake.go](../../internal/jobs/intake.go), [internal/api/handlers/jobs.go](../../internal/api/handlers/jobs.go).

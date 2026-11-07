---
status: accepted
---

# Share Job mutation policy across REST and MCP

REST and MCP use Job Intake to create, update, and delete jobs. Job Intake validates and saves jobs and applies the related effects. Replication, import, and item edits have separate paths. Job Intake applies a common policy. Scheduler changes, persistence hooks, and broadcasts can occur in separate operations.

Sources: [internal/jobs/intake.go](../../internal/jobs/intake.go), [internal/api/handlers/jobs.go](../../internal/api/handlers/jobs.go).

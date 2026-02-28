---
description: Step-by-step guide for debugging backup/restore failures
tools: ["editor", "terminal"]
---

# Debug a Backup Issue

Follow these steps to debug a backup or restore failure.

## Step 1: Identify the Failing Component

Determine which layer is failing:

| Symptom                        | Likely Component                |
| ------------------------------ | ------------------------------- |
| "connection refused" / timeout | Storage adapter                 |
| "permission denied" on files   | Engine handler (Docker/libvirt) |
| "no such container/VM"         | Engine handler (item not found) |
| "database locked"              | DB layer (WAL mode issue)       |
| Job stuck / no progress        | Scheduler or WebSocket hub      |

## Step 2: Check the Execution Path

Trace the flow:

1. **Scheduler** triggers the job (`internal/scheduler/`)
2. **Job execution** loads job items from DB (`internal/db/`)
3. **Engine handler** performs backup/restore (`internal/engine/`)
4. **Storage adapter** writes/reads backup data (`internal/storage/`)
5. **WebSocket hub** broadcasts progress (`internal/ws/`)

## Step 3: Reproduce Locally

```bash
# Run the daemon in debug mode
./build/vault daemon --db=test.db --addr=:28085

# Trigger a backup via API
curl -X POST http://localhost:28085/api/v1/jobs/1/run
```

## Step 4: Check Storage Connectivity

```bash
# Test storage connection via API
curl -X POST http://localhost:28085/api/v1/storage/1/test
```

## Step 5: Isolate the Issue

- **Storage issue?** Test the adapter's `TestConnection()` and `Write()` independently
- **Docker issue?** Check Docker socket access: `docker ps`
- **Libvirt issue?** Check libvirt connection: `virsh list --all`
- **DB issue?** Check for WAL file locks, busy timeout

## Step 6: Fix and Test

- Write a test that reproduces the issue
- Fix the root cause
- Verify the test passes
- Run full test suite: `make test`
- Document the fix in the commit message

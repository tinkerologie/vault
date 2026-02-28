---
description: Step-by-step guide for adding a new scheduler job type
tools: ["editor", "terminal"]
---

# Add a New Scheduler Job Type

Follow these steps to add a new type of scheduled job.

## Step 1: Define the Job Type

Add the new job type constant in `internal/config/types.go`.

## Step 2: Implement Execution Logic

Create or extend the execution function that handles the new job type. This connects the scheduler to the appropriate engine handler and storage adapter.

## Step 3: Wire to Scheduler

In `internal/scheduler/scheduler.go`, ensure the scheduler can dispatch the new job type:

- The scheduler loads jobs from DB via `db.ListJobs()`
- Each job has a cron schedule and type
- When triggered, the scheduler calls the appropriate execution function

## Step 4: Add DB Support

If the new job type requires additional fields:

1. Update `internal/db/models.go` with new fields
2. Update `internal/db/migrations.go` schema
3. Update `internal/db/job_repo.go` CRUD methods

## Step 5: Test

- Test scheduler dispatches the new job type
- Test cron scheduling behavior
- Test execution with mock storage and engine
- Test error handling (what happens when backup fails mid-job)

## Step 6: Verify

```bash
go test ./internal/scheduler/... -v
go test ./internal/db/... -v
make lint
make pre-commit-run
```

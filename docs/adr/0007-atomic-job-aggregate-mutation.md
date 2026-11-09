---
status: accepted
---

# Commit a Job and its items together

[ADR 0003](0003-shared-job-intake.md) makes Job Intake the shared mutation path for REST and MCP. Separate writes could save an edited Job before a Job Item insert or delete failed. Compensation could also lose the original item IDs and missing-source markers.

## Decision

Job Intake validates request fields and supplied paths before taking the database write lock. One SQLite transaction reads the current Job, items, and Destination. Job Intake checks ownership and overlap against these records through a callback. The callback does not use the database pool. The transaction then writes and reads back the Job and items. Intake returns these records after commit.

The existing database layer owns the transaction. Compensating writes leave a window for partial results. A distributed transaction would require guarantees the scheduler, snapshot storage, and WebSocket clients cannot provide.

## Effects and limits

- Intake runs scheduler reload, configuration flush scheduling, and broadcast in that order after commit.
- Intake logs a reload error and continues with the other effects. The daemon schedules a coalesced, best-effort flush.
- Broadcast can block while it queues a message. A queued message gives no guarantee of delivery.
- A panic stops subsequent effects and propagates to the caller. The database commit still stands.
- Each item query sees a complete committed selection. Separate Job and item queries can see different committed versions.
- The existing Intake interface uses a background context for the transaction. Work can continue after the caller disconnects.
- The overlap check resolves symlinks while it holds the database write lock. A slow filesystem can delay other database work.
- The filesystem can change after path validation. The MCP read and merge of a partial request also occurs before the transaction.
- Intake returns an error if commit reports an error. The caller can reread the stored records if the outcome is uncertain.

Replication, imports, Runner item edits, and item remediation use separate paths.

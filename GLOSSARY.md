# Vault

Vault protects Unraid workloads and manages their backup, replication and recovery. These terms distinguish saved policy, execution and recoverable data.

## Backup configuration

**Job**:
A saved backup policy that selects items, a Destination, a schedule and backup options.
_Avoid_: backup run, task

**Job Item**:
A source selection saved in a Job, which may resolve to one or more Backup Items when the Job runs.
_Avoid_: backup item when referring to saved selection

**Backup Item**:
An individual source selected for execution, such as a container, virtual machine, folder, dataset or plugin.
_Avoid_: job item when referring to resolved execution

**Destination**:
A configured storage location where Vault keeps backup data.
_Avoid_: target, repository when referring to the configured location

## Execution and recovery data

**Job Run**:
A recorded execution associated with a Job; its kind distinguishes a Backup Run from a Restore Run.
_Avoid_: job, backup when referring to either kind

**Backup Run**:
An execution that attempts to protect selected Backup Items and records its outcome.
_Avoid_: restore run

**Restore Run**:
A recorded execution that attempts to recover backed-up data to a selected location.
_Avoid_: backup run

**Partial Backup**:
A backup outcome in which some requested data was not protected, even though usable recovery data may exist.
_Avoid_: success, complete backup

**Restore Point**:
A recorded recovery point for backed-up data, including points from partial backups, imports and replication.
_Avoid_: successful run, snapshot when referring to all backup types

**Backup Chain**:
A full backup and the later backups that depend on it for recovery.
_Avoid_: independent restore points

**Full Backup**:
A backup that establishes a complete recovery baseline for its selected data.
_Avoid_: incremental backup

**Incremental Backup**:
A backup of changes since the preceding backup in its chain.
_Avoid_: differential backup

**Differential Backup**:
A backup of changes since the full backup in its chain.
_Avoid_: incremental backup

**Stale Item**:
A retained record of an item that is no longer present in the current source discovery.
_Avoid_: unprotected item

## Storage and protection

**Deduplicated Destination**:
A Destination whose backups can share stored content across Jobs.
_Avoid_: one repository per job

**Staging Area**:
Temporary local space used while preparing, transferring or restoring backup data.
_Avoid_: destination

**Deferred Upload**:
A backup option that postpones remote transfer until local backup preparation has completed.
_Avoid_: replication

**Retention Policy**:
The rules determining which recovery history Vault keeps.
_Avoid_: garbage collection

**Garbage Collection**:
Reclamation of stored deduplicated data that retained backups no longer reference.
_Avoid_: retention policy

**Verification**:
A check of backup integrity or readability; it does not by itself prove that a restored application works.
_Avoid_: restore test, application health check

**Protected Item**:
A discovered item with recorded backup data in a retained Restore Point.
_Avoid_: configured item

**Pending Item**:
An item selected by a Job that does not yet have recorded backup protection.
_Avoid_: protected item

**Unprotected Item**:
A discovered item without recorded protection, including an item pending its first backup.
_Avoid_: unselected item

**Anomaly**:
A recorded condition requiring attention in backup operations or protection coverage.
_Avoid_: repair, failed run

## Replication and recovery

**Import**:
Adoption of existing backup data into Vault recovery records.
_Avoid_: backup run

**Replication Source**:
A configured peer from which Vault pulls backup data.
_Avoid_: destination

**Replicated Job**:
A source-owned Job representing backups copied from a Replication Source.
_Avoid_: locally scheduled job

**Replica**:
A copy of backup data maintained from a Replication Source.
_Avoid_: deferred upload

**Restore**:
Recovery of backed-up workload data from a Restore Point.
_Avoid_: disaster recovery when referring to a single recovery operation

**Disaster Recovery**:
The broader recovery of Vault configuration and protected workloads after loss of a server or its state.
_Avoid_: restore when referring to the complete recovery process

**Configuration Snapshot**:
A persisted copy of Vault state used to recover its working configuration.
_Avoid_: workload backup

**Database Backup**:
A recoverable copy of the Vault database stored with backup data.
_Avoid_: restore point for workload data

## Secrets and access

**Server Key**:
The secret owned by a Vault installation that protects its stored secrets and unlocks locally sealed backup keys.
_Avoid_: API key, backup passphrase

**Backup Passphrase**:
A user-held secret used for backup encryption or recovery access where supported by the backup format.
_Avoid_: server key, API key

**API Key**:
A credential authorizing remote access to Vault.
_Avoid_: server key, backup passphrase

**Credentials Locked**:
The condition in which saved credentials cannot be opened with the available Server Key.
_Avoid_: missing credentials, invalid remote password

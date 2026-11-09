package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// jobSQL is the SQL surface shared by the pool and the aggregate transaction.
type jobSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// JobMutationState holds the persisted records used to check a pending Job
// mutation. Current is nil on create. A zero destination ID leaves Destination
// empty and DestinationFound false.
type JobMutationState struct {
	Current          *Job
	Items            []JobItem
	Destination      StorageDestination
	DestinationFound bool
}

// CreateJobWithItems inserts the Job and its items in one transaction.
// check runs under the write lock and must not access the database pool.
func (d *DB) CreateJobWithItems(ctx context.Context, job Job, items []JobItem, check func(JobMutationState) error) (Job, []JobItem, error) {
	return d.saveJobWithItems(ctx, job, items, check, true)
}

// UpdateJobWithItems updates the Job and replaces its items in one transaction.
// A nil items slice preserves the selection. An empty slice clears it.
// check runs under the write lock and must not access the database pool.
func (d *DB) UpdateJobWithItems(ctx context.Context, id int64, job Job, items []JobItem, check func(JobMutationState) error) (Job, []JobItem, error) {
	job.ID = id
	return d.saveJobWithItems(ctx, job, items, check, false)
}

// saveJobWithItems uses only tx for SQL after BeginTx. The pool has one
// connection, which is held until the transaction commits or rolls back.
func (d *DB) saveJobWithItems(ctx context.Context, job Job, items []JobItem, check func(JobMutationState) error, create bool) (Job, []JobItem, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, nil, fmt.Errorf("begin job mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var state JobMutationState
	if !create {
		current, err := getJob(ctx, tx, job.ID)
		if err != nil {
			return Job{}, nil, fmt.Errorf("read current job: %w", err)
		}
		state.Current = &current
		state.Items, err = getJobItems(ctx, tx, job.ID)
		if err != nil {
			return Job{}, nil, fmt.Errorf("read current job items: %w", err)
		}
	}
	if job.StorageDestID != 0 {
		dest, err := d.getStorageDestination(ctx, tx, job.StorageDestID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Job{}, nil, fmt.Errorf("read job destination: %w", err)
		}
		state.Destination, state.DestinationFound = dest, err == nil
	}
	if check != nil {
		if err := check(state); err != nil {
			return Job{}, nil, err
		}
	}

	if create {
		job.ID, err = insertJob(ctx, tx, job)
		if err != nil {
			return Job{}, nil, fmt.Errorf("insert job: %w", err)
		}
	} else {
		result, err := updateJob(ctx, tx, job)
		if err != nil {
			return Job{}, nil, fmt.Errorf("update job: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Job{}, nil, fmt.Errorf("count updated jobs: %w", err)
		}
		if count == 0 {
			return Job{}, nil, ErrNotFound
		}
		if items != nil {
			if err := deleteJobItems(ctx, tx, job.ID); err != nil {
				return Job{}, nil, fmt.Errorf("replace job items: %w", err)
			}
		}
	}

	for _, item := range items {
		item.JobID = job.ID
		if _, err := insertJobItem(ctx, tx, item); err != nil {
			return Job{}, nil, fmt.Errorf("insert job item: %w", err)
		}
	}

	savedJob, err := getJob(ctx, tx, job.ID)
	if err != nil {
		return Job{}, nil, fmt.Errorf("read saved job: %w", err)
	}
	savedItems, err := getJobItems(ctx, tx, job.ID)
	if err != nil {
		return Job{}, nil, fmt.Errorf("read saved job items: %w", err)
	}
	if d.beforeJobAggregateCommit != nil {
		d.beforeJobAggregateCommit()
	}
	if err := tx.Commit(); err != nil {
		return Job{}, nil, fmt.Errorf("commit job mutation: %w", err)
	}
	return savedJob, savedItems, nil
}

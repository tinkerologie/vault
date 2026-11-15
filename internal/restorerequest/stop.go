package restorerequest

import (
	"context"
	"errors"
	"fmt"
)

var ErrStopPersistence = errors.New("the stop was sent, but its audit record could not be saved to flash")

type StopResult struct {
	RunID  int64  `json:"run_id"`
	Status string `json:"status"`
}

// StopRun is called only after the native proof has been verified for this run.
func (s *Service) StopRun(ctx context.Context, runID int64, operator string) (StopResult, error) {
	result := StopResult{RunID: runID, Status: "stopping"}
	stopping, err := s.runner.StopRestoreRun(ctx, runID, operator)
	if err != nil {
		return StopResult{}, fmt.Errorf("stopping approved Restore Run %d: %w", runID, err)
	}
	if stopping {
		if s.flushChange(true) != "" {
			return StopResult{}, ErrStopPersistence
		}
		return result, nil
	}
	status, err := s.db.GetApprovedRestoreRunStatus(ctx, runID)
	if err != nil {
		return StopResult{}, fmt.Errorf("loading approved Restore Run %d status: %w", runID, err)
	}
	result.Status = status
	return result, nil
}

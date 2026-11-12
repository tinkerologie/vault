package restorerequest

import "context"

type StopResult struct {
	RunID  int64  `json:"run_id"`
	Status string `json:"status"`
}

// StopRun is called only after the native proof has been verified for this run.
func (s *Service) StopRun(ctx context.Context, runID int64, operator string) (StopResult, error) {
	result := StopResult{RunID: runID, Status: "stopping"}
	if s.runner.StopRestoreRun(runID, operator) {
		return result, nil
	}
	status, err := s.db.GetApprovedRestoreRunStatus(ctx, runID)
	result.Status = status
	return result, err
}

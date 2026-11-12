package recovery

import (
	"context"
	"errors"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

type FindOptions struct {
	runner.ItemSelection
	Limit    int   `json:"limit,omitempty" jsonschema:"Restore Points per page, default 100 and maximum 200"`
	BeforeID int64 `json:"before_id,omitempty" jsonschema:"Next page cursor returned by the previous call"`
}

var ErrInvalidFindOptions = errors.New("limit must be between 1 and 200 and before_id must be positive when supplied")

type FindResult struct {
	Points       []FoundPoint `json:"restore_points"`
	NextBeforeID int64        `json:"next_before_id,omitempty"`
}

type FoundPoint struct {
	runner.PointIdentity
	Membership runner.Status `json:"membership"`
}

func FindRestorePoints(ctx context.Context, database *db.DB, opts FindOptions) (FindResult, error) {
	if err := opts.Validate(); err != nil {
		return FindResult{}, err
	}
	if opts.Limit < 0 || opts.Limit > 200 || opts.BeforeID < 0 {
		return FindResult{}, ErrInvalidFindOptions
	}
	if opts.Limit == 0 {
		opts.Limit = 100
	}
	points, err := database.RestorePointMembershipPage(ctx, opts.BeforeID, opts.Limit+1)
	if err != nil {
		return FindResult{}, err
	}
	count := min(len(points), opts.Limit)
	result := FindResult{Points: make([]FoundPoint, 0, count)}
	for _, point := range points[:count] {
		result.Points = append(result.Points, FoundPoint{
			PointIdentity: runner.IdentifyPoint(point), Membership: runner.Membership(point, opts.ItemSelection),
		})
	}
	if count < len(points) {
		result.NextBeforeID = points[count-1].ID
	}
	return result, nil
}

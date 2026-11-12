// Package restorerequest records the operation an assistant proposes and
// provides owner-scoped and operator review views.
package restorerequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/recovery"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

type Input struct {
	JobID          int64  `json:"job_id" jsonschema:"Job ID"`
	RestorePointID int64  `json:"restore_point_id" jsonschema:"Exact Restore Point ID"`
	ItemName       string `json:"item_name" jsonschema:"Exact name of the whole folder"`
	WriteMode      string `json:"write_mode" jsonschema:"replace or merge"`
	Destination    string `json:"destination,omitempty" jsonschema:"Alternate folder restore location. Omit to use the configured original location"`
}

type Operation struct {
	Review
	Input          Input  `json:"input"`
	StorageDestID  int64  `json:"storage_dest_id"`
	SourceDigest   string `json:"source_digest"`
	ConfiguredPath string `json:"configured_path,omitempty"`
	ResolvedPath   string `json:"resolved_path,omitempty"`
	plan           runner.FolderRestorePlan
}

type Review struct {
	JobName            string                      `json:"job_name"`
	Item               runner.ItemSelection        `json:"item"`
	RestorePoint       runner.PointIdentity        `json:"restore_point"`
	Membership         runner.Status               `json:"membership"`
	ItemTypeEvidence   runner.Status               `json:"item_type_evidence"`
	Chain              runner.RecoveryChain        `json:"chain"`
	Verification       runner.RecoveryVerification `json:"verification"`
	Eligible           bool                        `json:"eligible_for_approval"`
	Reasons            []string                    `json:"ineligibility_reasons"`
	Warnings           []string                    `json:"warnings"`
	TargetChoice       string                      `json:"target_choice"`
	WriteMode          string                      `json:"write_mode"`
	EffectiveWriteMode string                      `json:"effective_write_mode"`
	RequiresPassphrase bool                        `json:"requires_passphrase,omitempty"`
	Effects            []string                    `json:"effects"`
}

// Capture resolves the reviewed operation. It reads catalog records and
// bounded storage metadata, with no Runner admission or workload writes.
func Capture(ctx context.Context, database *db.DB, in Input) (Operation, error) {
	item := runner.ItemSelection{ItemType: "folder", ItemName: in.ItemName}
	component, err := safepath.NormalizeComponent(in.ItemName)
	if in.JobID <= 0 || in.RestorePointID <= 0 || item.Validate() != nil || err != nil || component != in.ItemName {
		return Operation{}, Invalid("provide a Job, Restore Point and exact folder item name")
	}
	if in.WriteMode != "replace" && in.WriteMode != "merge" {
		return Operation{}, Invalid("write_mode must be replace or merge")
	}
	point, err := database.GetRestorePointContext(ctx, in.RestorePointID)
	if err != nil {
		return Operation{}, err
	}
	if point.JobID != in.JobID {
		return Operation{}, Invalid("Restore Point does not belong to the selected Job")
	}
	job, err := database.GetJobContext(ctx, in.JobID)
	if err != nil {
		return Operation{}, err
	}
	evidence, err := recovery.GetEvidence(ctx, database, point.ID, item)
	if err != nil {
		return Operation{}, err
	}
	if evidence.Membership == runner.Missing || evidence.ItemTypeEvidence == runner.Missing {
		return Operation{}, Invalid("the selected Restore Point does not contain that folder")
	}
	op := Operation{
		Input: in, StorageDestID: job.StorageDestID,
		plan: runner.FolderRestorePlan{Job: job, Point: point, ItemName: in.ItemName},
		Review: Review{
			JobName: job.Name, Item: item, RestorePoint: evidence.Point, Membership: evidence.Membership,
			ItemTypeEvidence: evidence.ItemTypeEvidence, Chain: evidence.Chain, Verification: evidence.Verification,
			Reasons: []string{}, Warnings: evidence.Warnings, TargetChoice: "original", WriteMode: in.WriteMode,
			EffectiveWriteMode: in.WriteMode,
		},
	}
	if evidence.Membership != runner.Confirmed {
		op.Reasons = append(op.Reasons, "membership_unknown")
	} else if evidence.ItemTypeEvidence != runner.Confirmed {
		op.Reasons = append(op.Reasons, "folder_type_unconfirmed")
	}
	if evidence.Chain.Status != runner.Confirmed {
		op.Reasons = append(op.Reasons, "chain_unconfirmed")
	}
	for _, step := range evidence.Chain.Steps {
		if step.DataRequired && step.Artifacts != runner.Confirmed {
			op.Reasons = appendOnce(op.Reasons, "required_data_unusable")
		}
		if step.DataRequired && step.ItemTypeEvidence != runner.Confirmed {
			op.Reasons = appendOnce(op.Reasons, "required_folder_type_unconfirmed")
		}
	}
	op.SourceDigest, err = captureSource(ctx, database, &op)
	if err != nil {
		return Operation{}, err
	}
	if err := op.captureTarget(ctx, database); err != nil {
		return Operation{}, err
	}
	op.RequiresPassphrase = op.plan.RequiresPassphrase()
	op.Eligible = len(op.Reasons) == 0
	return op, nil
}

func appendOnce(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// captureSource binds catalog provenance without retaining raw point metadata,
// storage paths, or storage credentials in the request ledger.
func captureSource(ctx context.Context, database *db.DB, op *Operation) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	dest, err := database.GetStorageDestinationContext(ctx, op.StorageDestID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		op.Reasons = appendOnce(op.Reasons, "required_data_unusable")
	case err != nil:
		return "", err
	}
	op.plan.Destination = dest
	if err := encoder.Encode([]any{dest.ID, dest.Type, dest.Config, dest.DedupEnabled, op.plan.Job.Compression}); err != nil {
		return "", err
	}
	for _, step := range op.Chain.Steps {
		point, err := database.GetRestorePointContext(ctx, step.RestorePointID)
		if errors.Is(err, db.ErrNotFound) {
			op.Reasons = appendOnce(op.Reasons, "chain_unconfirmed")
			continue
		}
		if err != nil {
			return "", err
		}
		if err := encoder.Encode([]any{point, step.ClassicObjects}); err != nil {
			return "", err
		}
		if step.DataRequired {
			op.plan.Steps = append(op.plan.Steps, runner.FolderRestoreStep{Point: point, Objects: step.ClassicObjects})
		}
		if point.ID == op.plan.Point.ID {
			op.plan.Point = point
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (op *Operation) captureTarget(ctx context.Context, database *db.DB) error {
	target := op.Input.Destination
	original := target == ""
	if original {
		items, err := database.GetJobItemsContext(ctx, op.Input.JobID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.ItemType == "folder" && item.ItemName == op.Item.ItemName {
				settings, err := item.ParsedSettings()
				if err == nil {
					target, _ = settings["path"].(string)
				}
				break
			}
		}
		op.ConfiguredPath = target
	} else {
		op.TargetChoice = "alternate"
	}
	if target == "" {
		op.Reasons = append(op.Reasons, "original_target_unknown")
	} else {
		resolved, err := engine.ResolveFolderRestoreTarget(target, original)
		if err != nil {
			if !original {
				return Invalid("alternate restore location is outside the allowed folder restore policy")
			}
			op.Reasons = append(op.Reasons, "original_target_disallowed")
		} else {
			op.ResolvedPath = resolved
			if op.WriteMode == "replace" && !engine.CanClearRestoreTarget(resolved) {
				op.EffectiveWriteMode = "merge"
				op.Warnings = append(op.Warnings, "target_too_shallow_to_clear")
			}
		}
	}
	op.plan.Target = engine.ReviewedFolderTarget{
		Configured: target, Resolved: op.ResolvedPath, Original: original, Clean: op.EffectiveWriteMode == "replace",
	}
	if op.EffectiveWriteMode == "replace" {
		op.Effects = []string{"clear_target_contents", "write_backed_up_entries"}
	} else {
		op.Effects = []string{"overwrite_matching_entries", "keep_unrelated_entries"}
	}
	if slices.ContainsFunc(op.Chain.Steps, func(step runner.RecoveryChainStep) bool {
		return step.DataRequired && step.RestorePointID != op.RestorePoint.ID
	}) {
		op.Effects = append(op.Effects, "prune_replayed_entries")
	}
	return nil
}

// Invalid exposes only a fixed, user-actionable validation message.
type Invalid string

func (e Invalid) Error() string { return string(e) }

func validRequestID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil && strings.ToLower(id) == id
}

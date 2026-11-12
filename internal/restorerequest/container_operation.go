package restorerequest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

func (op *Operation) captureContainer(ctx context.Context, r *runner.Runner, passphrase string) error {
	op.Effects = []string{}
	if op.Input.Destination != "" {
		if _, err := safepath.CheckCustomRestoreDestination(op.Input.Destination); err != nil {
			return Invalid("alternate restore location is outside the allowed container restore policy")
		}
		op.TargetChoice = "alternate"
	}
	if len(op.Reasons) != 0 {
		return nil
	}
	work, err := r.ReviewContainerRestore(ctx, op.plan, op.Input.Destination, passphrase, op.WriteMode == "replace")
	switch {
	case errors.Is(err, runner.ErrRestorePassphrase), errors.Is(err, runner.ErrRunnerBusy),
		errors.Is(err, runner.ErrApprovedStorageUnavailable), errors.Is(err, runner.ErrReviewedRestoreChanged):
		return err
	case errors.Is(err, engine.ErrRestoreNeedsApproval):
		op.ManualRestoreRequired = true
		op.Reasons = append(op.Reasons, "manual_restore_required")
	case err != nil:
		op.Reasons = append(op.Reasons, "container_metadata_unusable")
		return nil
	}
	effects := work.Reviewed.Effects
	for i := range op.Chain.Steps {
		step := &op.Chain.Steps[i]
		if step.Artifacts == runner.Unknown && len(step.ClassicObjects) != 0 {
			step.Artifacts = runner.Confirmed
		}
	}
	op.Warnings = slices.DeleteFunc(op.Warnings, func(warning string) bool { return warning == "artifact_evidence_unavailable" })
	op.Container = &effects
	op.ResolvedPath = effects.Destination
	op.plan.Work = work
	fingerprint, err := json.Marshal([]any{work.Reviewed, work.Trusted})
	if err != nil {
		return err
	}
	op.WorkDigest = reviewDigest(fingerprint)
	op.Effects = []string{"stop_existing_container", "remove_existing_container", "recreate_container"}
	if effects.ImageDigest != "" {
		op.Effects = append(op.Effects, "pin_saved_image")
	} else if !op.ManualRestoreRequired {
		op.Effects = append(op.Effects, "load_saved_image")
	}
	if effects.Start {
		op.Effects = append(op.Effects, "start_container")
	}
	if effects.Template {
		op.Effects = append(op.Effects, "restore_container_template")
	}
	if effects.DatabaseReplay {
		op.Effects = append(op.Effects, "replay_container_database_if_ready")
	}
	if effects.DatabaseDump {
		op.Effects = append(op.Effects, "preserve_container_database_dump_if_not_replayed")
	}
	return nil
}

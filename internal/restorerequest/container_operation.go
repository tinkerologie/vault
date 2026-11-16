package restorerequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Unknown-type reasons on an older backup defer to the configuration
	// review below, which can establish the type. Every other reason, and a
	// type reason the review cannot settle, still blocks it.
	if slices.ContainsFunc(op.Reasons, func(reason string) bool {
		return !op.typeReviewPending || !containerTypeReason(reason)
	}) {
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
		if step.ContainerVolumesUnresolved(op.Item.ItemType) {
			step.Artifacts = runner.Confirmed
			// The review read this step's saved container configuration
			// and image archive, which is what a container backup holds.
			if op.typeReviewPending && step.ItemTypeEvidence == runner.Unknown {
				step.ItemTypeEvidence = runner.Confirmed
			}
		}
	}
	if op.typeReviewPending {
		if op.ItemTypeEvidence == runner.Unknown {
			op.ItemTypeEvidence = runner.Confirmed
		}
		op.Reasons = slices.DeleteFunc(op.Reasons, containerTypeReason)
		op.Warnings = slices.DeleteFunc(op.Warnings, func(warning string) bool { return warning == "item_type_evidence_unavailable" })
	}
	op.Warnings = slices.DeleteFunc(op.Warnings, func(warning string) bool { return warning == "artifact_evidence_unavailable" })
	op.Container = &effects
	op.EffectiveWriteMode = containerWriteMode(effects.Volumes)
	op.ResolvedPath = effects.Destination
	op.plan.Work = work
	fingerprint, err := json.Marshal([]any{work.Reviewed, work.Trusted})
	if err != nil {
		return fmt.Errorf("fingerprinting reviewed container work: %w", err)
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

func containerTypeReason(reason string) bool {
	return reason == "item_type_unconfirmed" || reason == "required_item_type_unconfirmed"
}

func containerWriteMode(volumes []engine.ContainerVolumeEffect) string {
	mode := ""
	for _, volume := range volumes {
		if volume.WriteMode == "configuration_only" {
			continue
		}
		if mode != "" && mode != volume.WriteMode {
			return "mixed"
		}
		mode = volume.WriteMode
	}
	if mode == "" {
		return "recreate"
	}
	return mode
}

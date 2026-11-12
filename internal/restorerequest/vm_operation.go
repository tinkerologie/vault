package restorerequest

import (
	"context"

	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

func (op *Operation) captureVM(ctx context.Context, r *runner.Runner, passphrase string) error {
	if op.Input.WriteMode != "replace" {
		return Invalid("a VM Restore Request requires write_mode replace")
	}
	if op.Input.Destination != "" {
		if _, err := safepath.CheckCustomRestoreDestination(op.Input.Destination); err != nil {
			return Invalid("alternate restore location is outside the allowed VM restore policy")
		}
		op.TargetChoice = "alternate"
	}

	review, err := r.InspectVMRestoreRequest(ctx, op.plan, op.Input.Destination, passphrase)
	if review.Membership == runner.Missing {
		return Invalid("the selected Restore Point does not contain that VM")
	}
	op.Effects = []string{}
	op.plan = review.Plan
	op.Membership, op.ItemTypeEvidence = review.Membership, review.ItemTypeEvidence
	op.Chain, op.Verification = review.Chain, review.Verification
	op.Reasons, op.Warnings = review.Reasons, review.Warnings
	op.SourceAnchor, op.SourceDigest = review.SourceAnchor, review.SourceDigest
	if err != nil || review.Effects == nil {
		return err
	}

	effects := review.Effects
	op.VM = effects
	op.ResolvedPath = effects.Destination
	if effects.Domain.Stop {
		op.Effects = append(op.Effects, "vm_stop_domain")
	}
	if effects.Domain.Replace {
		op.Effects = append(op.Effects, "vm_replace_domain")
	}
	if len(effects.Disks) != 0 || effects.NVRAM != nil {
		op.Effects = append(op.Effects, "vm_restore_disks")
	}
	op.Effects = append(op.Effects, "vm_define_saved_domain")
	if effects.StartAfterRestore {
		op.Effects = append(op.Effects, "vm_start_domain")
	} else {
		op.Effects = append(op.Effects, "vm_leave_domain_stopped")
	}
	return nil
}

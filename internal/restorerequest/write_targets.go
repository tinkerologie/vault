package restorerequest

import (
	"context"
	"errors"
	"strings"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

// WriteTarget is one host location a container or VM restore writes.
type WriteTarget struct {
	// Kind is volume, file or template for a container, and disk, nvram
	// or domain for a VM.
	Kind string `json:"kind"`
	// Path is the exact host path. A domain definition has none.
	Path string `json:"path,omitempty"`
	// Mount is the container mount point or the VM disk's target device.
	Mount string `json:"mount,omitempty"`
	// Name is the libvirt domain a VM restore redefines.
	Name string `json:"name,omitempty"`
	// Clearable reports that clearing the destination deletes files here
	// that are not in the backup. Only a deep enough volume directory is.
	Clearable bool `json:"clearable,omitempty"`
}

// ItemWriteTargets lists where one selected Backup Item's restore writes.
// Status is "resolved" when the restore's own review resolved the paths and
// "unavailable" otherwise, with a fixed Reason code.
type ItemWriteTargets struct {
	Name         string        `json:"name"`
	Type         string        `json:"type"`
	Status       string        `json:"status"`
	Reason       string        `json:"reason,omitempty"`
	WriteTargets []WriteTarget `json:"write_targets"`
}

// PreviewWriteTargets resolves the host paths a whole container or VM restore
// would write, using the same review a Restore Request uses before approval.
// It reads catalog records and bounded backup metadata only: nothing is
// persisted and the Runner is not claimed. The result holds paths, so it is
// for operator callers only.
func PreviewWriteTargets(ctx context.Context, database *db.DB, execution *runner.Runner, in Input, passphrase string) ItemWriteTargets {
	result := ItemWriteTargets{Name: in.ItemName, Type: in.ItemType, Status: "unavailable", WriteTargets: []WriteTarget{}}
	if in.ItemType != "container" && in.ItemType != "vm" {
		result.Reason = "unsupported_item_type"
		return result
	}
	if in.Destination != "" {
		if _, err := safepath.CheckCustomRestoreDestination(in.Destination); err != nil {
			result.Reason = "destination_disallowed"
			return result
		}
	}
	// Containers are previewed as if clearing were on, so Clearable says
	// whether the depth rule lets clearing apply at each volume.
	in.WriteMode = "replace"
	op, err := (&Service{db: database, runner: execution}).capture(ctx, in, passphrase)
	if err != nil {
		result.Reason = previewErrorReason(err)
		return result
	}
	switch {
	case op.Container != nil:
		result.WriteTargets = containerWriteTargets(*op.Container)
	case op.VM != nil:
		result.WriteTargets = vmWriteTargets(*op.VM)
	default:
		result.Reason = "unresolved"
		if len(op.Reasons) != 0 {
			result.Reason = op.Reasons[0]
		}
		return result
	}
	result.Status = "resolved"
	return result
}

func previewErrorReason(err error) string {
	var invalid Invalid
	switch {
	case errors.As(err, &invalid) && strings.Contains(string(invalid), "does not contain"):
		return "not_in_restore_point"
	case errors.As(err, &invalid):
		return "invalid_selection"
	case errors.Is(err, runner.ErrRestorePassphrase):
		return "passphrase_required"
	case errors.Is(err, runner.ErrRunnerBusy):
		return "busy"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "storage_unavailable"
	}
}

func containerWriteTargets(effects engine.ContainerRestoreEffects) []WriteTarget {
	targets := []WriteTarget{}
	for _, volume := range effects.Volumes {
		switch volume.WriteMode {
		case "replace", "merge":
			targets = append(targets, WriteTarget{Kind: "volume", Path: volume.Target, Mount: volume.Mount, Clearable: volume.WriteMode == "replace"})
		case "overwrite_file":
			targets = append(targets, WriteTarget{Kind: "file", Path: volume.Target, Mount: volume.Mount})
		}
	}
	if effects.Template {
		targets = append(targets, WriteTarget{Kind: "template", Path: engine.ContainerTemplatePath(effects.Name)})
	}
	return targets
}

func vmWriteTargets(effects engine.VMRestoreEffects) []WriteTarget {
	targets := []WriteTarget{}
	for _, disk := range effects.Disks {
		targets = append(targets, WriteTarget{Kind: "disk", Path: disk.Destination, Mount: disk.Target})
	}
	if effects.NVRAM != nil {
		targets = append(targets, WriteTarget{Kind: "nvram", Path: effects.NVRAM.Destination})
	}
	return append(targets, WriteTarget{Kind: "domain", Name: effects.Name})
}

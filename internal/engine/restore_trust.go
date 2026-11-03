package engine

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// RestoreTrust carries what the runner knows about a restore's artifact and
// what the person starting it approved. The zero value is the safe one: an
// untrusted artifact with nothing approved.
type RestoreTrust struct {
	// ArtifactTrusted means this server made the backup and its local records
	// vouch for the container config being restored.
	ArtifactTrusted bool
	// AllowHostPrivileges approves host-level settings from an untrusted artifact.
	AllowHostPrivileges bool
	// AllowUnpinnedImage accepts the image's current tag when the backup-time
	// digest is missing or cannot be obtained.
	AllowUnpinnedImage bool
}

type restoreTrustKey struct{}

// WithRestoreTrust attaches a restore's trust decision to ctx.
func WithRestoreTrust(ctx context.Context, t RestoreTrust) context.Context {
	return context.WithValue(ctx, restoreTrustKey{}, t)
}

// RestoreTrustFrom returns the trust decision on ctx, or the zero value.
func RestoreTrustFrom(ctx context.Context) RestoreTrust {
	t, _ := ctx.Value(restoreTrustKey{}).(RestoreTrust)
	return t
}

// hostPrivileges lists the settings in a container config that reach past the
// container onto the host. Appdata binds under /mnt are ordinary and not listed.
func hostPrivileges(ri restoreInspect) []string {
	var out []string
	hc := ri.HostConfig
	if hc.Privileged {
		out = append(out, "privileged mode")
	}
	if hc.NetworkMode == "host" {
		out = append(out, "host network")
	}
	if hc.IpcMode == "host" {
		out = append(out, "host IPC")
	}
	if hc.PidMode == "host" {
		out = append(out, "host PID namespace")
	}
	for _, c := range hc.CapAdd {
		out = append(out, fmt.Sprintf("capability %q", c))
	}
	for _, d := range hc.Devices {
		out = append(out, fmt.Sprintf("device %q", d.PathOnHost))
	}
	for _, bind := range hc.Binds {
		source, _, _ := strings.Cut(bind, ":")
		if !strings.HasPrefix(source, "/") {
			continue // a named volume
		}
		if clean := filepath.Clean(source); !strings.HasPrefix(clean, "/mnt/") {
			out = append(out, fmt.Sprintf("host path %q", clean))
		}
	}
	return out
}

// enforceRestorePrivileges stops an untrusted artifact granting itself host
// access. Nothing is stripped: a container missing the privileges it was built
// for fails in confusing ways, so the restore either has approval or does not run.
func enforceRestorePrivileges(ctx context.Context, item BackupItem, ri restoreInspect) error {
	trust := RestoreTrustFrom(ctx)
	if trust.ArtifactTrusted {
		return nil
	}
	privileges := hostPrivileges(ri)
	if len(privileges) == 0 {
		return nil
	}
	summary := strings.Join(privileges, ", ")
	if trust.AllowHostPrivileges {
		log.Printf("engine: restore: %s granted approved host access from an unverified backup: %s", item.Name, summary)
		return nil
	}
	return fmt.Errorf("restoring %s would grant host access from a backup this server cannot vouch for (imported, replicated or without stored checksums): %s; nothing was changed. Review these settings, then restart the restore with \"Allow host-level access\" enabled (API: allow_host_privileges)", item.Name, summary)
}

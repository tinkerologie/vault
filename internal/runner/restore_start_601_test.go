package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

// Issue #601: a caller that cannot approve a restore hears about a refusal
// straight away, and otherwise returns once the restore is past its checks.
func TestStartUntilApprovalsSettled(t *testing.T) {
	refusals := map[string]error{
		"host privileges": fmt.Errorf("%w: restoring web would grant host access from a backup this server cannot vouch for (imported or replicated): privileged mode", engine.ErrRestoreNeedsApproval),
		"unpinned image":  fmt.Errorf("%w: cannot restore web with the image it was backed up with (API: allow_unpinned_image)", engine.ErrRestoreNeedsApproval),
	}
	for name, refusal := range refusals {
		t.Run(name+" is returned", func(t *testing.T) {
			err := startUntilApprovalsSettled(t.Context(), "web", func(context.Context) error { return approvalInUI(refusal) })
			if !errors.Is(err, engine.ErrRestoreNeedsApproval) || !strings.Contains(err.Error(), "Vault UI (Restore → Container approvals)") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("returns once settled while the restore carries on", func(t *testing.T) {
		release := make(chan struct{})
		finished := make(chan struct{})
		err := startUntilApprovalsSettled(t.Context(), "web", func(ctx context.Context) error {
			defer close(finished)
			engine.ApprovalsSettled(ctx)
			engine.ApprovalsSettled(ctx) // repeated reports are harmless
			<-release
			return errors.New("disk full later on")
		})
		if err != nil {
			t.Fatal(err)
		}
		close(release)
		<-finished
	})
	t.Run("a failure after the checks still means started", func(t *testing.T) {
		for range 50 {
			err := startUntilApprovalsSettled(t.Context(), "web", func(ctx context.Context) error {
				engine.ApprovalsSettled(ctx)
				return errors.New("failed at once")
			})
			if err != nil {
				t.Fatalf("err = %v, want nil once settled", err)
			}
		}
	})
	t.Run("a failure before the checks is returned", func(t *testing.T) {
		err := startUntilApprovalsSettled(t.Context(), "web", func(context.Context) error { return errors.New("no such job") })
		if err == nil || err.Error() != "no such job" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the caller's deadline ends only the wait", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		release := make(chan struct{})
		err := startUntilApprovalsSettled(ctx, "web", func(rctx context.Context) error {
			<-release
			return rctx.Err()
		})
		close(release)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "still running its checks") {
			t.Fatalf("err = %v", err)
		}
	})
}

// Only container restores can need approval, and a classic one only when this
// server cannot vouch for its backup.
func TestSettleApprovalsIfNoneApply(t *testing.T) {
	trusted := engine.RestoreTrust{ArtifactTrusted: true}
	cases := []struct {
		itemType string
		chunked  bool
		trust    engine.RestoreTrust
		want     bool
	}{
		{"folder", true, engine.RestoreTrust{}, true},
		{"vm", false, engine.RestoreTrust{}, true},
		{"container", false, trusted, true},
		{"container", false, engine.RestoreTrust{}, false},
		{"container", true, trusted, false},
	}
	for _, c := range cases {
		settled := false
		ctx := engine.WithApprovalsSettled(engine.WithRestoreTrust(t.Context(), c.trust), func() { settled = true })
		settleApprovalsIfNoneApply(ctx, c.itemType, c.chunked)
		if settled != c.want {
			t.Errorf("%s chunked=%v trusted=%v: settled = %v, want %v", c.itemType, c.chunked, c.trust.ArtifactTrusted, settled, c.want)
		}
	}
}

// A restore that fails before reaching its checks reports the failure.
func TestStartRestoreItemReportsEarlyFailures(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.StartRestoreItem(t.Context(), db.RestorePoint{JobID: 9999, Metadata: `{}`}, "web", "container", "", ""); err == nil {
		t.Fatal("want error for an unknown job")
	}
}

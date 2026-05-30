package notify

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type Importance string

const (
	ImportanceNormal  Importance = "normal"
	ImportanceWarning Importance = "warning"
	ImportanceAlert   Importance = "alert"
)

const notifyScriptPath = "/usr/local/emhttp/webGui/scripts/notify"

// notifyExecTimeout bounds how long we wait for the Unraid notify helper
// process to complete. Without a timeout, a wedged helper would block the
// caller (the backup runner) indefinitely, keeping the dashboard stuck in
// "Backup in Progress" and preventing all future jobs from starting (issue #112).
const notifyExecTimeout = 30 * time.Second

// runNotifyCommand executes the notify helper at path with args, bounded by
// ctx. Extracted so tests can exercise the timeout path without a real Unraid
// host.
//
// aikido-ignore-next-line AIK_go_G204 -- argv-style invocation of a constant binary path; no shell.
func runNotifyCommand(ctx context.Context, path string, args []string) error {
	// #nosec G204 //nolint:gosec // path is a compile-time constant (notifyScriptPath); args are argv entries
	cmd := exec.CommandContext(ctx, path, args...)
	return cmd.Run()
}

func Send(event, subject, description string, importance Importance) error {
	if runtime.GOOS != "linux" {
		log.Printf("[NOTIFY] %s: %s - %s (%s)", event, subject, description, importance)
		return nil
	}

	if _, err := os.Stat(notifyScriptPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Non-Unraid Linux environments won't have the notify helper.
			log.Printf("[NOTIFY] helper not found, skipping: %s", notifyScriptPath)
			return nil
		}
		return fmt.Errorf("checking notify helper: %w", err)
	}

	// notifyScriptPath is a compile-time constant pointing to the Unraid notify
	// helper. The remaining values are passed as separate argv entries (no shell
	// interpretation), so the script receives them verbatim and cannot expand
	// any embedded shell metacharacters.
	args := []string{
		"-e", event,
		"-s", subject,
		"-d", description,
		"-i", string(importance),
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyExecTimeout)
	defer cancel()

	if err := runNotifyCommand(ctx, notifyScriptPath, args); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("notify helper timed out after %s: %w", notifyExecTimeout, err)
		}
		return fmt.Errorf("send notification: %w", err)
	}
	return nil
}

func JobSuccess(jobName string, itemsDone int, sizeBytes int64) error {
	desc := fmt.Sprintf("Backed up %d items (%.1f MB)", itemsDone, float64(sizeBytes)/1024/1024)
	return Send("Vault", fmt.Sprintf("Backup job '%s' completed", jobName), desc, ImportanceNormal)
}

func JobFailed(jobName string, errMsg string) error {
	return Send("Vault", fmt.Sprintf("Backup job '%s' failed", jobName), errMsg, ImportanceAlert)
}

func JobPartial(jobName string, done, failed int) error {
	desc := fmt.Sprintf("%d items succeeded, %d failed", done, failed)
	return Send("Vault", fmt.Sprintf("Backup job '%s' partially completed", jobName), desc, ImportanceWarning)
}

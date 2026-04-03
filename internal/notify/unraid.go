package notify

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"runtime"
)

type Importance string

const (
	ImportanceNormal  Importance = "normal"
	ImportanceWarning Importance = "warning"
	ImportanceAlert   Importance = "alert"
)

const notifyScriptPath = "/usr/local/emhttp/webGui/scripts/notify"

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

	cmd := exec.Command(notifyScriptPath, //nolint:gosec // notifyScriptPath is a compile-time constant
		"-e", event,
		"-s", subject,
		"-d", description,
		"-i", string(importance),
	)
	if err := cmd.Run(); err != nil {
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

package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

const defaultScriptTimeout = 5 * time.Minute

// scriptContextEnv builds the Vault-specific environment variables exposed to
// pre/post-backup hook scripts. The names match the job-editor tooltip's
// promise (VAULT_JOB_NAME, VAULT_STATUS); VAULT_JOB_ID and VAULT_RUN_ID are
// added so scripts can correlate against the API/logs. status is "starting"
// for pre-scripts and the run's final status for post-scripts.
func scriptContextEnv(job db.Job, runID int64, status string) map[string]string {
	return map[string]string{
		"VAULT_JOB_NAME": job.Name,
		"VAULT_JOB_ID":   strconv.FormatInt(job.ID, 10),
		"VAULT_RUN_ID":   strconv.FormatInt(runID, 10),
		"VAULT_STATUS":   status,
	}
}

// runScript executes a script file with a timeout and returns its combined
// stdout/stderr output. The script must be an absolute path to an existing
// executable file. Any extra key/value pairs are exported as environment
// variables (on top of the daemon's own environment) so pre/post-backup hooks
// can read job context such as VAULT_JOB_NAME and VAULT_STATUS.
func runScript(script string, timeout time.Duration, extraEnv map[string]string) (string, error) {
	if script == "" {
		return "", nil
	}

	// Validate the script path.
	script = strings.TrimSpace(script)
	if !filepath.IsAbs(script) {
		return "", fmt.Errorf("script must be an absolute path: %s", script)
	}

	info, err := os.Stat(script)
	if err != nil {
		return "", fmt.Errorf("script not found: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("script is a directory: %s", script)
	}
	// Check if the file is executable (at least one execute bit set).
	if info.Mode()&0111 == 0 {
		return "", fmt.Errorf("script is not executable: %s", script)
	}

	if timeout <= 0 {
		timeout = defaultScriptTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// aikido-ignore-next-line AIK_go_G204 -- script is admin-configured, validated as an absolute path to an existing executable file, and executed without a shell.
	cmd := exec.CommandContext(ctx, script) // #nosec G204 //nolint:gosec // script path is validated (absolute, exists, executable) and admin-configured — no shell interpretation
	cmd.Env = scriptEnv(extraEnv)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("script timed out after %s: %s", timeout, script)
	}
	if err != nil {
		return string(output), fmt.Errorf("script failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// scriptEnv returns the daemon's environment with the given Vault-specific
// variables appended, so hook scripts inherit PATH etc. while also seeing the
// job context. A nil/empty extra map yields the daemon environment unchanged.
func scriptEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

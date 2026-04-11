package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultScriptTimeout = 5 * time.Minute

// runScript executes a script file with a timeout and returns its combined
// stdout/stderr output. The script must be an absolute path to an existing
// executable file.
func runScript(script string, timeout time.Duration) (string, error) {
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

	cmd := exec.CommandContext(ctx, script) //nolint:gosec // script path is validated (absolute, exists, executable) and admin-configured — no shell interpretation
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("script timed out after %s: %s", timeout, script)
	}
	if err != nil {
		return string(output), fmt.Errorf("script failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

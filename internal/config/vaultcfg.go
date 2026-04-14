package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCfgPath is the standard location of vault.cfg on Unraid.
const DefaultCfgPath = "/boot/config/plugins/vault/vault.cfg"

// ReadCfg reads the shell-sourceable vault.cfg file and returns a key-value map.
// Lines are expected in KEY=VALUE format (values optionally quoted).
// Empty files or missing files return an empty map with no error.
func ReadCfg(path string) (map[string]string, error) {
	f, err := os.Open(path) // #nosec G304 — path is DefaultCfgPath constant or admin CLI flag
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening vault.cfg: %w", err)
	}
	defer f.Close()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Strip surrounding quotes (single or double).
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		result[key] = val
	}
	return result, scanner.Err()
}

// ReadCfgValue reads a single key from vault.cfg, returning the value or
// defaultVal if the key is absent or the file is missing.
func ReadCfgValue(path, key, defaultVal string) string {
	cfg, err := ReadCfg(path)
	if err != nil {
		return defaultVal
	}
	if v, ok := cfg[key]; ok && v != "" {
		return v
	}
	return defaultVal
}

// WriteCfgValue writes or updates a single key in vault.cfg, preserving
// existing keys and comments. If the key already exists its value is replaced
// in-place; otherwise it is appended.
func WriteCfgValue(path, key, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Read existing content (may not exist yet).
	content, err := os.ReadFile(path) // #nosec G304 — path is DefaultCfgPath constant or admin CLI flag
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading vault.cfg: %w", err)
	}

	var lines []string
	found := false
	prefix := key + "="

	if len(content) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, prefix) {
				lines = append(lines, key+"="+value)
				found = true
			} else {
				lines = append(lines, line)
			}
		}
	}

	if !found {
		lines = append(lines, key+"="+value)
	}

	// Ensure trailing newline.
	out := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0o600) // #nosec G703 — path is DefaultCfgPath constant or admin CLI flag
}

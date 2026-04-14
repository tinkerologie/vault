package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
)

const (
	vmMetadataFileName                = "vm_meta.json"
	vmRestoreVerifyModeRunning        = "running"
	vmRestoreVerifyModeGuestAgent     = "guest_agent"
	vmRestoreVerifyModeTCP            = "tcp"
	defaultVMRestoreVerifyTimeoutSecs = 120
)

type vmRestoreVerifyConfig struct {
	Mode           string `json:"mode,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	TCPHost        string `json:"tcp_host,omitempty"`
	TCPPort        int    `json:"tcp_port,omitempty"`
}

type vmBackupMetadata struct {
	State         string                `json:"state"`
	RestoreVerify vmRestoreVerifyConfig `json:"restore_verify,omitempty"`
}

func writeVMBackupMetadata(destDir, state string, settings map[string]any) (string, error) {
	verifyConfig, err := vmRestoreVerifyConfigFromSettings(settings)
	if err != nil {
		return "", fmt.Errorf("build VM restore verification config: %w", err)
	}

	metadata := vmBackupMetadata{
		State:         strings.TrimSpace(state),
		RestoreVerify: verifyConfig,
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal vm metadata: %w", err)
	}

	metadataPath := filepath.Join(destDir, vmMetadataFileName)
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write vm metadata: %w", err)
	}

	return metadataPath, nil
}

func readVMRestoreMetadata(sourceDir string) (vmBackupMetadata, error) {
	metadataPath := filepath.Join(sourceDir, vmMetadataFileName)
	data, err := os.ReadFile(metadataPath) // #nosec G304 — metadataPath is sourceDir + fixed filename
	if err != nil {
		return vmBackupMetadata{}, err
	}

	var metadata vmBackupMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return vmBackupMetadata{}, fmt.Errorf("parse vm metadata: %w", err)
	}

	metadata.State = strings.TrimSpace(metadata.State)
	metadata.RestoreVerify, err = normalizeVMRestoreVerifyConfig(metadata.RestoreVerify)
	if err != nil {
		return vmBackupMetadata{}, fmt.Errorf("validate vm metadata: %w", err)
	}
	return metadata, nil
}

func (m vmBackupMetadata) startAfterRestore() bool {
	return strings.EqualFold(strings.TrimSpace(m.State), "running")
}

func vmRestoreVerifyConfigFromSettings(settings map[string]any) (vmRestoreVerifyConfig, error) {
	config := vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeRunning}
	if settings == nil {
		return normalizeVMRestoreVerifyConfig(config)
	}

	mode := strings.TrimSpace(strings.ToLower(settingString(settings["restore_verify_mode"])))
	if mode != "" {
		config.Mode = mode
	}

	timeoutSeconds, err := settingInt(settings["restore_verify_timeout_seconds"])
	if err != nil {
		return vmRestoreVerifyConfig{}, fmt.Errorf("parse restore_verify_timeout_seconds: %w", err)
	}
	if timeoutSeconds > 0 {
		config.TimeoutSeconds = timeoutSeconds
	}

	config.TCPHost = strings.TrimSpace(settingString(settings["restore_verify_tcp_host"]))

	tcpPort, err := settingInt(settings["restore_verify_tcp_port"])
	if err != nil {
		return vmRestoreVerifyConfig{}, fmt.Errorf("parse restore_verify_tcp_port: %w", err)
	}
	if tcpPort > 0 {
		config.TCPPort = tcpPort
	}

	return normalizeVMRestoreVerifyConfig(config)
}

func normalizeVMRestoreVerifyConfig(config vmRestoreVerifyConfig) (vmRestoreVerifyConfig, error) {
	config.Mode = strings.TrimSpace(strings.ToLower(config.Mode))
	if config.Mode == "" {
		config.Mode = vmRestoreVerifyModeRunning
	}

	switch config.Mode {
	case vmRestoreVerifyModeRunning, vmRestoreVerifyModeGuestAgent, vmRestoreVerifyModeTCP:
	default:
		return vmRestoreVerifyConfig{}, fmt.Errorf("unsupported restore verify mode %q", config.Mode)
	}

	if config.TimeoutSeconds <= 0 {
		config.TimeoutSeconds = defaultVMRestoreVerifyTimeoutSecs
	}

	config.TCPHost = strings.TrimSpace(config.TCPHost)
	if config.Mode == vmRestoreVerifyModeTCP {
		if config.TCPPort < 1 || config.TCPPort > 65535 {
			return vmRestoreVerifyConfig{}, fmt.Errorf("tcp restore verify mode requires a port between 1 and 65535")
		}
	} else {
		config.TCPHost = ""
		config.TCPPort = 0
	}

	return config, nil
}

func vmRestoreVerifyTimeout(config vmRestoreVerifyConfig) int {
	if config.TimeoutSeconds > 0 {
		return config.TimeoutSeconds
	}
	return defaultVMRestoreVerifyTimeoutSecs
}

func pickVMReadyAddressFromInterfaces(ifaces []libvirt.DomainInterface) string {
	var fallback string
	for _, iface := range ifaces {
		for _, addr := range iface.Addrs {
			parsed := net.ParseIP(strings.TrimSpace(addr.Addr))
			if parsed == nil || parsed.IsLoopback() {
				continue
			}

			if libvirt.IPAddrType(addr.Type) == libvirt.IPAddrTypeIpv4 {
				return parsed.String()
			}

			if fallback == "" {
				fallback = parsed.String()
			}
		}
	}

	return fallback
}

func settingString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func settingInt(value any) (int, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		asInt := int(typed)
		if typed != float64(asInt) {
			return 0, fmt.Errorf("must be an integer")
		}
		return asInt, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, nil
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported type %T", value)
	}
}

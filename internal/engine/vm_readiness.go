//go:build linux

package engine

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

func (h *VMHandler) verifyRestoredVMReady(dom libvirt.Domain, name string, config vmRestoreVerifyConfig, progress ProgressFunc) error {
	switch config.Mode {
	case vmRestoreVerifyModeRunning:
		return nil
	case vmRestoreVerifyModeGuestAgent:
		progress(name, 98, "waiting for QEMU guest agent")
		if err := h.waitForVMGuestAgent(dom, name, time.Duration(vmRestoreVerifyTimeout(config))*time.Second); err != nil {
			return fmt.Errorf("waiting for guest agent: %w", err)
		}
		progress(name, 99, "verified QEMU guest agent")
		return nil
	case vmRestoreVerifyModeTCP:
		endpoint, err := h.resolveVMReadyEndpoint(dom, config)
		if err != nil {
			return err
		}
		progress(name, 98, fmt.Sprintf("waiting for %s", endpoint))
		if err := waitForTCPEndpoint(endpoint, time.Duration(vmRestoreVerifyTimeout(config))*time.Second); err != nil {
			return fmt.Errorf("waiting for restored VM service %s: %w", endpoint, err)
		}
		progress(name, 99, fmt.Sprintf("verified %s", endpoint))
		return nil
	default:
		return fmt.Errorf("unsupported restore verify mode %q", config.Mode)
	}
}

func (h *VMHandler) waitForVMGuestAgent(dom libvirt.Domain, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for guest agent on domain %s: %w", name, lastErr)
			}
			return fmt.Errorf("timed out waiting for guest agent on domain %s", name)
		}

		attemptTimeout := int32(remaining / time.Second)
		if attemptTimeout < 1 {
			attemptTimeout = 1
		}
		if attemptTimeout > 5 {
			attemptTimeout = 5
		}

		_, err := h.conn.QEMUDomainAgentCommand(dom, `{"execute":"guest-ping"}`, attemptTimeout, 0)
		if err == nil {
			return nil
		}
		if isLibvirtNoDomainError(err) {
			return fmt.Errorf("domain disappeared during guest agent verification: %w", err)
		}

		lastErr = err
		time.Sleep(vmShutdownPollInterval)
	}
}

func (h *VMHandler) resolveVMReadyEndpoint(dom libvirt.Domain, config vmRestoreVerifyConfig) (string, error) {
	host := config.TCPHost
	if host == "" {
		resolvedHost, err := h.detectVMReadyHost(dom)
		if err != nil {
			return "", err
		}
		host = resolvedHost
	}

	return net.JoinHostPort(host, strconv.Itoa(config.TCPPort)), nil
}

func (h *VMHandler) detectVMReadyHost(dom libvirt.Domain) (string, error) {
	sources := []libvirt.DomainInterfaceAddressesSource{
		libvirt.DomainInterfaceAddressesSrcAgent,
		libvirt.DomainInterfaceAddressesSrcLease,
		libvirt.DomainInterfaceAddressesSrcArp,
	}

	var lastErr error
	for _, source := range sources {
		ifaces, err := h.conn.DomainInterfaceAddresses(dom, uint32(source), 0)
		if err != nil {
			lastErr = err
			continue
		}

		if host := pickVMReadyAddressFromInterfaces(ifaces); host != "" {
			return host, nil
		}
	}

	if lastErr != nil {
		var libvirtErr libvirt.Error
		if errors.As(lastErr, &libvirtErr) {
			code := libvirt.ErrorNumber(libvirtErr.Code)
			if code == libvirt.ErrAgentUnresponsive || code == libvirt.ErrAgentCommandTimeout || code == libvirt.ErrAgentCommandFailed {
				return "", fmt.Errorf("libvirt could not determine a guest address; configure an explicit TCP host or enable guest network reporting: %w", lastErr)
			}
		}
		return "", fmt.Errorf("detecting guest address via libvirt: %w", lastErr)
	}

	return "", fmt.Errorf("libvirt did not report a reachable guest address; configure an explicit TCP host")
}

func waitForTCPEndpoint(endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		conn, err := net.DialTimeout("tcp", endpoint, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}

		time.Sleep(vmShutdownPollInterval)
	}
}

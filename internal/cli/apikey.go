package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tinkerologie/vault/internal/config"
)

const (
	allowedOriginFlagUsage = "Exact browser origin allowed to call the API cross-origin, such as https://ha.example:8123 (repeatable; callers still need the API key)"
	keyMgmtRoute           = "/api/v1/settings/api-key"
	apiKeyDefaultPort      = "24085"
	apiKeyRequestTimeout   = 30 * time.Second
)

// daemonCfgPath is where the Unraid plugin records the daemon's bind address.
// A var so tests can point it elsewhere.
var daemonCfgPath = "/boot/config/plugins/vault/vault.cfg"

var apiKeyCmd = &cobra.Command{
	Use:   "api-key",
	Short: "Manage the API key on this host",
	Long: `Generate, show, rotate or revoke the key remote clients must present in the
X-API-Key header. Remote requests are refused until a key exists.

These commands talk to the running daemon or replica on this host and only work
from the host itself. In Docker, run them inside the container:

  docker exec vault-replica vault api-key generate`,
	// A refused request is not a usage mistake, so don't print the usage text.
	PersistentPreRun: func(cmd *cobra.Command, _ []string) { cmd.SilenceUsage = true },
}

func init() {
	apiKeyCmd.PersistentFlags().String("addr", "", "Local daemon address as ip:port (default: from vault.cfg, else 127.0.0.1:"+apiKeyDefaultPort+")")
	apiKeyCmd.PersistentFlags().Bool("tls", false, "The daemon serves TLS (certificate not verified, since the address must be this host)")

	apiKeyCmd.AddCommand(
		&cobra.Command{
			Use:   "generate",
			Short: "Create the first API key and print it",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				enabled, err := apiKeyEnabled(cmd)
				if err != nil {
					return err
				}
				if enabled {
					return errors.New("an API key already exists; use 'vault api-key rotate' to replace it")
				}
				return printAPIKey(cmd, http.MethodPost, keyMgmtRoute+"/generate")
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the current API key",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return printAPIKey(cmd, http.MethodGet, keyMgmtRoute+"/key")
			},
		},
		&cobra.Command{
			Use:   "rotate",
			Short: "Replace the API key and print the new one",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return printAPIKey(cmd, http.MethodPost, keyMgmtRoute+"/rotate")
			},
		},
		&cobra.Command{
			Use:   "revoke",
			Short: "Delete the API key, refusing remote clients until a new one exists",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if _, err := callAPIKeyRoute(cmd, http.MethodDelete, keyMgmtRoute); err != nil {
					return err
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "API key revoked; remote clients are refused until a new key is generated.")
				return err
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether an API key is configured",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				enabled, err := apiKeyEnabled(cmd)
				if err != nil {
					return err
				}
				state := "not configured; remote clients are refused"
				if enabled {
					state = "configured"
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "API key: "+state)
				return err
			},
		},
	)
	rootCmd.AddCommand(apiKeyCmd)
}

func apiKeyEnabled(cmd *cobra.Command) (bool, error) {
	body, err := callAPIKeyRoute(cmd, http.MethodGet, keyMgmtRoute)
	if err != nil {
		return false, err
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return false, fmt.Errorf("reading API key status: %w", err)
	}
	return status.Enabled, nil
}

// printAPIKey writes only the key to stdout, so it can be captured in a script.
func printAPIKey(cmd *cobra.Command, method, route string) error {
	body, err := callAPIKeyRoute(cmd, method, route)
	if err != nil {
		return err
	}
	var resp struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("reading API key response: %w", err)
	}
	if resp.APIKey == "" {
		return errors.New("the daemon returned no API key")
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), resp.APIKey)
	return err
}

func callAPIKeyRoute(cmd *cobra.Command, method, route string) ([]byte, error) {
	addr, _ := cmd.Flags().GetString("addr")
	useTLS, _ := cmd.Flags().GetBool("tls")
	target, err := resolveLocalDaemonAddr(addr)
	if err != nil {
		return nil, err
	}

	scheme := "http"
	transport := &http.Transport{} // No Proxy: this must reach the local daemon directly.
	if useTLS {
		scheme = "https"
		// The address is verified local above, and a daemon's certificate rarely
		// names 127.0.0.1, so there is nothing for verification to add.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 //nolint:gosec // local-only address, checked by resolveLocalDaemonAddr
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: apiKeyRequestTimeout}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, scheme+"://"+target+route, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req) // #nosec G704 //nolint:gosec // target is verified to be an address of this host
	if err != nil {
		return nil, fmt.Errorf("contacting the Vault daemon at %s (is it running?): %w", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading daemon response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = resp.Status
		}
		return nil, fmt.Errorf("the Vault daemon refused the request: %s", apiErr.Error)
	}
	return body, nil
}

// resolveLocalDaemonAddr picks the daemon address and refuses anything that
// isn't this host: the daemon would refuse a remote caller anyway, and TLS
// verification is off for this one connection.
func resolveLocalDaemonAddr(flagAddr string) (string, error) {
	host, port := "127.0.0.1", apiKeyDefaultPort
	if flagAddr != "" {
		h, p, err := net.SplitHostPort(flagAddr)
		if err != nil {
			return "", fmt.Errorf("invalid --addr %q: expected ip:port: %w", flagAddr, err)
		}
		host, port = h, p
	} else {
		host = config.ReadCfgValue(daemonCfgPath, "BIND_ADDRESS", host)
		port = config.ReadCfgValue(daemonCfgPath, "PORT", port)
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "", fmt.Errorf("invalid address %q: expected an IP address of this host", host)
	}
	if ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	if !ip.IsLoopback() && !isHostInterfaceIP(ip) {
		return "", fmt.Errorf("%s is not an address of this host; API keys can only be managed locally", ip)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func isHostInterfaceIP(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

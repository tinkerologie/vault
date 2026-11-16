package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinkerologie/vault/internal/crypto"
)

type drillKeyTransport struct {
	base http.RoundTripper
	key  string
}

func (t drillKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("X-API-Key", t.key)
	return t.base.RoundTrip(out)
}

func runMCP(ctx context.Context, command string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", "", "Vault /api/v1/mcp endpoint")
	keyFile := flags.String("key-file", "", "private file with an Assistant Key")
	job := flags.Int64("job", 0, "Job ID")
	point := flags.Int64("point", 0, "Restore Point ID")
	item := flags.String("item", "", "exact Backup Item name")
	kind := flags.String("type", "", "folder, container, or vm")
	destination := flags.String("destination", "", "approved disposable alternate location")
	id := flags.String("id", "", "Restore Request ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("use flags after the MCP drill command")
	}
	tool, input, err := drillCall(command, *job, *point, *item, *kind, *destination, *id)
	if err != nil {
		return err
	}
	target, err := url.Parse(*endpoint)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" ||
		target.User != nil || target.Path != "/api/v1/mcp" || target.RawQuery != "" || target.Fragment != "" {
		return errors.New("specify an HTTP or HTTPS /api/v1/mcp endpoint")
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		return fmt.Errorf("read Assistant Key file: %w", err)
	}
	secret := strings.TrimSpace(string(key))
	if _, ok := crypto.AssistantKeyDigest(secret); !ok {
		return errors.New("provide a Vault Assistant Key in the key file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Transport: drillKeyTransport{base: transport, key: secret},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "vault-restore-drill", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: target.String(), HTTPClient: httpClient}, nil)
	if err != nil {
		return fmt.Errorf("connect to Vault MCP: %w", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: input})
	if err != nil {
		return fmt.Errorf("call %s: %w", tool, err)
	}
	return writeDrillResult(output, command, result)
}

func drillCall(command string, job, point int64, item, kind, destination, id string) (string, map[string]any, error) {
	switch command {
	case "follow", "withdraw":
		if id == "" {
			return "", nil, errors.New("provide the Restore Request ID with -id")
		}
		tool := "get_restore_request"
		if command == "withdraw" {
			tool = "withdraw_restore_request"
		}
		return tool, map[string]any{"id": id}, nil
	}
	if item == "" || point <= 0 || (kind != "folder" && kind != "container" && kind != "vm") {
		return "", nil, errors.New("provide a positive -point, exact -item, and -type folder, container, or vm")
	}
	input := map[string]any{"item_name": item, "item_type": kind, "restore_point_id": point}
	if command == "inspect" {
		return "get_recovery_evidence", input, nil
	}
	if job <= 0 {
		return "", nil, errors.New("provide a positive -job")
	}
	input["job_id"] = job
	if destination != "" {
		input["destination"] = destination
	}
	return "restore_item", input, nil
}

func writeDrillResult(output io.Writer, command string, result *mcp.CallToolResult) error {
	const permission = "Enable Allow Restore Requests for this Assistant Key"
	if result.IsError {
		if len(result.Content) == 1 {
			if message, ok := result.Content[0].(*mcp.TextContent); ok {
				if command == "negative" && strings.Contains(message.Text, permission) {
					_, err := fmt.Fprintln(output, "Restore Request permission refused")
					return err
				}
				return fmt.Errorf("Vault MCP refused %s: %s", command, message.Text)
			}
		}
		return fmt.Errorf("Vault MCP refused %s without guidance", command)
	}
	if command == "negative" {
		return errors.New("inspection-only Restore Request unexpectedly succeeded")
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || result.StructuredContent == nil {
		return errors.New("Vault MCP did not return structured drill evidence")
	}
	if command == "request" {
		var receipt struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			ReviewLink string `json:"review_link"`
			RunID      int64  `json:"run_id"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil || receipt.ID == "" || receipt.Status != "pending" || receipt.ReviewLink == "" || receipt.RunID != 0 {
			return errors.New("Vault MCP did not return a pending Restore Request and review link")
		}
	}
	_, err = fmt.Fprintln(output, string(data))
	return err
}

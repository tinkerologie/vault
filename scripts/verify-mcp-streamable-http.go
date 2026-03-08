package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerTransport struct {
	base   http.RoundTripper
	apiKey string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.apiKey != "" {
		clone.Header.Set("X-API-Key", t.apiKey)
	}
	return t.base.RoundTrip(clone)
}

func main() {
	endpoint := flag.String("endpoint", "", "Vault MCP streamable HTTP endpoint")
	apiKey := flag.String("api-key", "", "Vault API key")
	timeout := flag.Duration("timeout", 20*time.Second, "request timeout")
	flag.Parse()

	if *endpoint == "" {
		fmt.Fprintln(os.Stderr, "missing required -endpoint")
		os.Exit(2)
	}

	httpClient := &http.Client{
		Timeout: *timeout,
		Transport: headerTransport{
			base:   http.DefaultTransport,
			apiKey: *apiKey,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "vault-verify", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   *endpoint,
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect mcp streamable http: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list tools: %v\n", err)
		os.Exit(1)
	}
	if len(tools.Tools) == 0 {
		fmt.Fprintln(os.Stderr, "list tools returned zero tools")
		os.Exit(1)
	}

	callResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_health"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "call get_health: %v\n", err)
		os.Exit(1)
	}
	if callResult.IsError {
		fmt.Fprintln(os.Stderr, "get_health returned MCP error")
		os.Exit(1)
	}
	if len(callResult.Content) == 0 {
		fmt.Fprintln(os.Stderr, "get_health returned no content")
		os.Exit(1)
	}

	textContent, ok := callResult.Content[0].(*mcp.TextContent)
	if !ok {
		fmt.Fprintf(os.Stderr, "get_health returned unexpected content type %T\n", callResult.Content[0])
		os.Exit(1)
	}

	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(textContent.Text), &health); err != nil {
		fmt.Fprintf(os.Stderr, "decode get_health response: %v\n", err)
		os.Exit(1)
	}
	if health.Status != "ok" {
		fmt.Fprintf(os.Stderr, "unexpected health status %q\n", health.Status)
		os.Exit(1)
	}

	runnerResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_runner_status"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "call get_runner_status: %v\n", err)
		os.Exit(1)
	}
	if runnerResult.IsError {
		fmt.Fprintln(os.Stderr, "get_runner_status returned MCP error")
		os.Exit(1)
	}

	fmt.Printf("MCP streamable HTTP OK: tools=%d, status=%s, mode=%s, version=%s\n", len(tools.Tools), health.Status, health.Mode, health.Version)
}

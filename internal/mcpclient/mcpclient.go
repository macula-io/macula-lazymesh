// Package mcpclient spawns macula-mcp as an MCP stdio subprocess and
// exposes its tools. This is lazymesh's ONLY tool source -- both the agent
// loop and the TUI's read-only panels go through the same *Client and the
// same underlying MCP session, not a second connection or a direct read of
// macula-mcp's own SQLite files.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maculaMCPVersion is pinned deliberately (found by an adversarial review,
// 2026-09-06): the workspace-wide convention this used to copy verbatim
// was `npx -y -p @macula-io/mcp macula-mcp`, unpinned, which re-resolves
// npm's "latest" tag on every single launch. A bad or compromised
// @macula-io/mcp release would then run unattended, with whatever
// environment Spawn hands it (see envAllowlist below). Bump this only
// deliberately, after checking what changed -- never just to "pick up
// whatever's newest."
const maculaMCPVersion = "0.23.0"

// LaunchCommand starts macula-mcp the same way every other MCP config in
// this workspace does (npx -p @macula-io/mcp macula-mcp), except pinned to
// maculaMCPVersion instead of npm's floating latest tag. Do not change
// this to a locally-built binary path or a different invocation otherwise.
var LaunchCommand = []string{"npx", "-y", "-p", "@macula-io/mcp@" + maculaMCPVersion, "macula-mcp"}

// envAllowlist is what Spawn forwards to the macula-mcp subprocess instead
// of the full parent environment (found by the same review): PATH so
// node/npx can find their own binaries, HOME so npm has a cache/config
// directory, TMPDIR for npm's temp extraction. Nothing else -- macula-mcp
// has no business seeing the rest of this process's environment (API
// keys, tokens, unrelated config) just because it happens to be spawned
// from it.
var envAllowlist = []string{"PATH", "HOME", "TMPDIR"}

// Tool is a provider-agnostic view of one macula-mcp tool: just enough to
// build an LLM's tool-call spec and to invoke it later by name.
type Tool struct {
	Name        string
	Description string
	InputSchema any
}

// Client wraps one live MCP session with macula-mcp.
type Client struct {
	session *mcp.ClientSession
}

// Spawn starts macula-mcp as a subprocess and completes the MCP handshake.
// If identityFile is non-empty, it's passed through as MACULA_MCP_IDENTITY
// so macula-mcp keeps a stable node_id across restarts instead of its
// default fresh-identity-per-launch behavior.
func Spawn(ctx context.Context, identityFile string) (*Client, error) {
	cmd := exec.Command(LaunchCommand[0], LaunchCommand[1:]...)
	cmd.Env = filteredEnv(envAllowlist)
	if identityFile != "" {
		cmd.Env = append(cmd.Env, "MACULA_MCP_IDENTITY="+identityFile)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "lazymesh", Version: "0.1.0"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to macula-mcp (%s): %w", strings.Join(LaunchCommand, " "), err)
	}
	return &Client{session: session}, nil
}

// filteredEnv builds a subprocess environment containing only the given
// variable names, each taken from this process's own environment if set.
func filteredEnv(allowlist []string) []string {
	env := make([]string, 0, len(allowlist))
	for _, name := range allowlist {
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}
	return env
}

// Close ends the MCP session and terminates the macula-mcp subprocess.
func (c *Client) Close() error {
	return c.session.Close()
}

// ListTools returns every tool macula-mcp currently advertises, dynamically
// -- lazymesh never hardcodes a tool list, so a new macula-mcp tool becomes
// available to the agent loop for free on the next call.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for {
		res, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("macula-mcp tools/list: %w", err)
		}
		for _, t := range res.Tools {
			out = append(out, Tool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// CallTool invokes one macula-mcp tool by name with the given arguments
// and returns its text content joined together. args is typically the
// decoded JSON object an LLM's tool-call arguments produced. A nil args
// is normalized to an empty object: macula-mcp's own schema validation
// (zod) requires arguments to be a record even for tools that take none,
// and a nil map[string]any marshals to JSON null, not {}, which it rejects.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("macula-mcp tools/call %s: %w", name, err)
	}

	var sb strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	if res.IsError {
		return text, fmt.Errorf("macula-mcp tool %s reported an error: %s", name, text)
	}
	return text, nil
}

// CallToolRaw invokes a tool with arguments already encoded as a JSON
// object string -- the shape an LLM's tool_calls.function.arguments field
// arrives in. It decodes that JSON before delegating to CallTool.
func (c *Client) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	args := map[string]any{}
	if strings.TrimSpace(argumentsJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("decode arguments for tool %s: %w", name, err)
		}
	}
	return c.CallTool(ctx, name, args)
}

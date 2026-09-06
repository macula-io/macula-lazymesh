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

// LaunchCommand is the exact command every other MCP config in this
// workspace already uses to start macula-mcp. Do not change this to a
// locally-built binary path or a different invocation -- the whole point
// is that lazymesh starts macula-mcp the same way Claude Code/Goose do.
var LaunchCommand = []string{"npx", "-y", "-p", "@macula-io/mcp", "macula-mcp"}

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
	cmd.Env = os.Environ()
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

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

// DefaultMaculaMCPVersion is launchCommand's fallback when SpawnOptions
// carries no version -- a defensive floor for direct callers of this
// package, not the primary place an operator interacts with this value.
// That's config.Config.MaculaMCPVersion (config.Default() sets it, kept
// in sync with this constant); see that field's own doc comment for the
// actual "verify the diff before bumping" methodology and history. Not
// imported from here into config on purpose -- config stays a leaf
// package with no cross-package awareness, matching how every other
// cross-cutting default in this codebase is resolved at the call site
// rather than via an import.
const DefaultMaculaMCPVersion = "0.26.1"

// launchCommand starts macula-mcp the same way every other MCP config in
// this workspace does (npx -p @macula-io/mcp macula-mcp), pinned to the
// given version instead of npm's floating latest tag. Do not change this
// to a locally-built binary path or a different invocation otherwise.
func launchCommand(version string) []string {
	if version == "" {
		version = DefaultMaculaMCPVersion
	}
	return []string{"npx", "-y", "-p", "@macula-io/mcp@" + version, "macula-mcp"}
}

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

// SpawnOptions configures how Spawn launches macula-mcp.
type SpawnOptions struct {
	// Version pins the exact @macula-io/mcp release to run. Empty uses
	// DefaultMaculaMCPVersion.
	Version string
	// IdentityFile, if non-empty, is passed through as MACULA_MCP_IDENTITY
	// so macula-mcp keeps a stable node_id across a full restart of this
	// instance. Leave empty (config.Default()'s own default -- see its
	// doc comment) unless you specifically need that: macula-mcp's own
	// default identity logic already scopes by session-id/PPID, giving
	// distinct identities to concurrently-running instances for free.
	// Setting this to the SAME path across more than one concurrently-
	// running instance collides them onto one node_id -- found live
	// 2026-09-07, config.Default() used to do exactly that.
	IdentityFile string
	// ContactPolicyFile, if non-empty, is passed through as
	// MACULA_MCP_CONTACT_POLICY_FILE. Without this, macula-mcp reads
	// ~/.config/macula-mcp/contact_policy.json -- ONE shared path used by
	// every macula-mcp instance on a machine regardless of identity, found
	// while investigating the ring-answering UX, 2026-09-06: any other
	// harness's macula-mcp on the same box (another Claude Code session,
	// Goose) reads and writes that identical file. Pointing this at a
	// lazymesh-specific path keeps its own contact policy and allowlist
	// (mesh_trust_agent) isolated from every other session sharing the
	// machine. Unlike IdentityFile above, config.Default() DOES set a
	// default for this (one fixed path shared by every lazymesh instance
	// on the machine) -- and unlike identity, that's not a protocol-level
	// problem: contact policy has no equivalent of "two connections
	// sharing one node ID get kicked by the station", it's just data, so
	// two concurrent instances sharing one allowlist doesn't collide
	// anything. Whether sharing it across instances (rather than also
	// scoping it per-instance) is the right call long-term hasn't been
	// revisited since -- flagging, not fixing here.
	ContactPolicyFile string
}

// spawnEnv builds the subprocess environment: the fixed allowlist plus
// whatever opts adds, pulled out as its own pure function so the actual
// env-var-name/value pairing has a test independent of spawning a real
// process.
func spawnEnv(opts SpawnOptions) []string {
	env := filteredEnv(envAllowlist)
	if opts.IdentityFile != "" {
		env = append(env, "MACULA_MCP_IDENTITY="+opts.IdentityFile)
	}
	if opts.ContactPolicyFile != "" {
		env = append(env, "MACULA_MCP_CONTACT_POLICY_FILE="+opts.ContactPolicyFile)
	}
	return env
}

// Spawn starts macula-mcp as a subprocess and completes the MCP handshake.
func Spawn(ctx context.Context, opts SpawnOptions) (*Client, error) {
	command := launchCommand(opts.Version)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = spawnEnv(opts)

	client := mcp.NewClient(&mcp.Implementation{Name: "lazymesh", Version: "0.1.0"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to macula-mcp (%s): %w", strings.Join(command, " "), err)
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

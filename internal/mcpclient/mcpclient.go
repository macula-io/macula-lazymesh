// Package mcpclient spawns macula-mcp as an MCP stdio subprocess and
// exposes its tools. This is lazymesh's ONLY tool source -- both the agent
// loop and the TUI's read-only panels go through the same *Client and the
// same underlying MCP session, not a second connection or a direct read of
// macula-mcp's own SQLite files.
package mcpclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// mcpSession is the subset of *mcp.ClientSession Client actually uses --
// wrapped as a local interface for two reasons: it lets Client hold a
// swappable value across a respawn (2026-09-08, see Client's own doc
// comment), and it lets tests substitute a fake session without spawning
// a real macula-mcp subprocess.
type mcpSession interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	Close() error
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
	//
	// Leaving this empty does NOT mean Spawn leaves identity entirely to
	// macula-mcp's own default, though (2026-09-08): see resolveSpawnIdentity.
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

// resolveSpawnIdentity returns opts with IdentityFile filled in via a
// freshly minted, unique path if the caller left it empty -- pulled out
// as its own pure function so the path-generation decision has a direct
// unit test independent of spawning a real process.
//
// Why this exists (2026-09-08, found live investigating mcpclient
// respawn/reconnect): leaving IdentityFile empty lets macula-mcp mint its
// own default identity, scoped by CLAUDE_CODE_SESSION_ID ?? ppid-${process.ppid}
// (mesh_config.ts) -- stable across a full harness restart if the parent
// PID survives, by design. But Client (below) can now respawn macula-mcp
// WITHIN one lazymesh process's own lifetime after a detected connection
// failure, and launchCommand always goes through a fresh `npx` invocation
// -- npx is itself a brand new OS process every single spawn, so
// macula-mcp's own process.ppid (npx's PID, not lazymesh's) differs on
// every respawn. Verified live, not assumed: two spawns from the same Go
// process produced two different node_ids. Left alone, every respawn
// would look to the rest of the mesh like this agent becoming a new one
// -- petname/reputation discontinuity, any outstanding ring's own
// endpoint vanishing. Minting one explicit path here, reused verbatim
// for every respawn attempt (Client stores the RESOLVED opts, not the
// caller's original), makes an in-process respawn look like "reconnected"
// rather than "became someone else" -- without touching
// config.Config.IdentityFile's own, stronger, deliberately-opt-in
// meaning (surviving a FULL lazymesh restart too). Not cleaned up on
// Close(), matching macula-mcp's own stated policy for these files ("a
// tiny seed file, unbounded growth... hasn't been treated as worth an
// eviction policy yet").
func resolveSpawnIdentity(opts SpawnOptions) (SpawnOptions, error) {
	if opts.IdentityFile != "" {
		return opts, nil
	}
	path, err := ephemeralIdentityPath()
	if err != nil {
		return opts, fmt.Errorf("mint ephemeral identity path: %w", err)
	}
	opts.IdentityFile = path
	return opts, nil
}

// ephemeralIdentityPath builds a unique path under the OS temp directory
// -- this process's own PID plus a random suffix, so it never collides
// with another concurrently-running lazymesh instance (matching
// IdentityFile's own documented distinct-across-concurrent-instances
// requirement) or a previous run of this same binary. Never creates the
// file itself: macula-mcp mints the identity there on first use, the
// same way it already does for an operator-supplied IdentityFile.
func ephemeralIdentityPath() (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("lazymesh-identity-%d-%s.seed", os.Getpid(), hex.EncodeToString(suffix))), nil
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

// realSpawnSession does the actual OS-level work: launches macula-mcp and
// completes the MCP handshake. The only place this package touches
// os/exec -- Client.spawnFn defaults to this, swapped out in tests.
func realSpawnSession(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
	command := launchCommand(opts.Version)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = spawnEnv(opts)

	client := mcp.NewClient(&mcp.Implementation{Name: "lazymesh", Version: "0.1.0"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to macula-mcp (%s): %w", strings.Join(command, " "), err)
	}
	return session, nil
}

// errTransportFailure marks an error as coming from the session/
// subprocess itself being broken (the RPC round trip to macula-mcp
// itself failed -- a closed pipe, a dead process, a context deadline),
// as opposed to a normal, healthy MCP tool-call response reporting
// IsError (macula-mcp itself is fine, one specific call failed for an
// ordinary mesh reason -- unreachable, a validation error, a declined
// ring). Only the former means the connection is actually dead and
// worth respawning over. Conflating these (the shape of this package's
// code before 2026-09-08) would make Client respawn on every ordinary
// tool error, tearing down a healthy subprocess -- and the mesh
// presence/identity it already spent time establishing -- for no reason.
var errTransportFailure = errors.New("mcpclient: transport failure")

// respawnCooldown is the minimum time between two actual respawn
// attempts, regardless of how many callers hit a transport failure in
// that window. A caller that arrives during cooldown just gets the
// error back immediately and relies on its OWN existing backoff
// (roomwaiter/ringwaiter's errorBackoff, runAgent's exponential backoff)
// to try again once a new attempt becomes eligible -- matches those
// packages' own 5s baseline rather than picking an unrelated number.
// var, not const, so a test can shorten it -- never reassigned outside
// a test.
var respawnCooldown = 5 * time.Second

// Client wraps one live MCP session with macula-mcp.
//
// Respawns it transparently on a detected transport failure (2026-09-08,
// closing the "mesh connection dies, nothing ever recovers, the process
// stays alive but is functionally dead forever" gap found auditing this
// codebase the same day). Every exported method is safe for concurrent
// use -- roomwaiter, ringwaiter, the TUI, and the agent's own tool chain
// all hold and call the SAME *Client concurrently today, which is
// exactly what keeps this fix contained to this package alone: none of
// those four consumers need to change at all, they keep calling the
// same methods on the same pointer and get a working connection back
// once Client heals itself.
//
// Respawn coordination, one attempt at a time (tryRespawn): a mutex-
// guarded in-flight flag means concurrent callers noticing the same dead
// session around the same moment trigger exactly one respawn, not one
// each; respawnCooldown bounds how often an attempt is even tried,
// regardless of caller volume. A successful respawn is followed by
// exactly one retry of the ORIGINAL failed call before anything is
// surfaced to the caller -- most transient connection deaths become
// fully invisible (one slightly slower call), not a cascading error a
// caller has to notice and handle itself.
type Client struct {
	mu      sync.RWMutex
	session mcpSession
	logger  *log.Logger

	opts    SpawnOptions
	spawnFn func(ctx context.Context, opts SpawnOptions) (mcpSession, error)

	respawnMu     sync.Mutex
	respawning    bool
	lastRespawnAt time.Time
}

// Spawn starts macula-mcp as a subprocess and completes the MCP handshake.
func Spawn(ctx context.Context, opts SpawnOptions) (*Client, error) {
	resolved, err := resolveSpawnIdentity(opts)
	if err != nil {
		return nil, err
	}
	session, err := realSpawnSession(ctx, resolved)
	if err != nil {
		return nil, err
	}
	return &Client{
		session: session,
		opts:    resolved,
		spawnFn: realSpawnSession,
	}, nil
}

// SetLogger sets where a respawn attempt (start, success, or failure)
// gets logged -- same optional-setter shape as ringwaiter.Manager and
// meshservices.Source (nil, the default, means silence; every existing
// test's own construction is unaffected).
func (c *Client) SetLogger(l *log.Logger) {
	c.mu.Lock()
	c.logger = l
	c.mu.Unlock()
}

func (c *Client) log(format string, args ...any) {
	c.mu.RLock()
	logger := c.logger
	c.mu.RUnlock()
	if logger != nil {
		logger.Printf(format, args...)
	}
}

// currentSession returns the live session under a read lock -- brief on
// purpose, so a long-running call (e.g. mesh_wait_ring's own up-to-3600s
// wait) never holds this lock, only the moment of reading the pointer.
func (c *Client) currentSession() mcpSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.session
}

// tryRespawn attempts exactly one respawn, gated by respawnCooldown and
// mutual exclusion against any other concurrent attempt. Returns true
// only if THIS call performed a successful respawn -- false covers both
// "someone else is already handling it" / "too soon since the last
// attempt" (the caller's own retry loop will get another chance once
// this Client is eligible again) and "the respawn itself failed" (logged
// either way).
func (c *Client) tryRespawn(ctx context.Context) bool {
	c.respawnMu.Lock()
	if c.respawning || time.Since(c.lastRespawnAt) < respawnCooldown {
		c.respawnMu.Unlock()
		return false
	}
	c.respawning = true
	c.respawnMu.Unlock()

	defer func() {
		c.respawnMu.Lock()
		c.respawning = false
		c.lastRespawnAt = time.Now()
		c.respawnMu.Unlock()
	}()

	c.log("[mcpclient] transport failure detected, respawning macula-mcp...")
	newSession, err := c.spawnFn(ctx, c.opts)
	if err != nil {
		c.log("[mcpclient] respawn failed: %v", err)
		return false
	}

	c.mu.Lock()
	old := c.session
	c.session = newSession
	c.mu.Unlock()
	if old != nil {
		_ = old.Close() // best-effort -- it's already broken, a failed Close here is not news
	}
	c.log("[mcpclient] respawn succeeded")
	return true
}

// Close ends the MCP session and terminates the macula-mcp subprocess.
func (c *Client) Close() error {
	return c.currentSession().Close()
}

// ListTools returns every tool macula-mcp currently advertises, dynamically
// -- lazymesh never hardcodes a tool list, so a new macula-mcp tool becomes
// available to the agent loop for free on the next call.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	out, err := c.listToolsOnce(ctx)
	if !errors.Is(err, errTransportFailure) {
		return out, err
	}
	if !c.tryRespawn(ctx) {
		return out, err
	}
	return c.listToolsOnce(ctx)
}

func (c *Client) listToolsOnce(ctx context.Context) ([]Tool, error) {
	session := c.currentSession()
	var out []Tool
	cursor := ""
	for {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("macula-mcp tools/list: %w: %w", errTransportFailure, err)
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
	text, err := c.callToolOnce(ctx, name, args)
	if !errors.Is(err, errTransportFailure) {
		return text, err
	}
	if !c.tryRespawn(ctx) {
		return text, err
	}
	return c.callToolOnce(ctx, name, args)
}

func (c *Client) callToolOnce(ctx context.Context, name string, args map[string]any) (string, error) {
	session := c.currentSession()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("macula-mcp tools/call %s: %w: %w", name, errTransportFailure, err)
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

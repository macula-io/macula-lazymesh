package mcpclient

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// syncLogBuffer wraps bytes.Buffer with a mutex -- a plain bytes.Buffer
// is not safe for concurrent use, and SetLogger's whole point is a
// background respawn writing to it while the test reads it back (same
// pattern as internal/ringwaiter's own syncBuffer, for the same reason).
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestLogger(w *syncLogBuffer) *log.Logger {
	return log.New(w, "", 0)
}

// fakeSession lets a test script exactly what CallTool/ListTools/Close
// do, without a real macula-mcp subprocess.
type fakeSession struct {
	mu sync.Mutex

	callToolFunc  func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	listToolsFunc func(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	closed        bool
}

func (f *fakeSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return f.callToolFunc(ctx, params)
}

func (f *fakeSession) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return f.listToolsFunc(ctx, params)
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func okResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func newTestClient(session mcpSession, spawnFn func(ctx context.Context, opts SpawnOptions) (mcpSession, error)) *Client {
	return &Client{session: session, spawnFn: spawnFn}
}

// resetRespawnCooldown zeroes respawnCooldown for the duration of one
// test, restoring the real value after -- most of these tests care about
// the respawn/retry behavior itself, not the cooldown gate (which has
// its own dedicated test below).
func resetRespawnCooldown(t *testing.T) {
	t.Helper()
	orig := respawnCooldown
	respawnCooldown = 0
	t.Cleanup(func() { respawnCooldown = orig })
}

func TestCallTool_TransportFailureTriggersRespawnAndRetry(t *testing.T) {
	resetRespawnCooldown(t)
	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	fresh := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return okResult("ok"), nil
	}}
	spawnCalls := 0
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		spawnCalls++
		return fresh, nil
	})

	got, err := c.CallTool(context.Background(), "mesh_rooms", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got != "ok" {
		t.Fatalf("expected the retried call's result, got %q", got)
	}
	if spawnCalls != 1 {
		t.Fatalf("expected exactly one respawn attempt, got %d", spawnCalls)
	}
	if !dead.wasClosed() {
		t.Fatalf("expected the old, broken session to be closed after a successful respawn")
	}
}

// This is the distinction that matters most for the whole feature: a
// healthy connection reporting one specific tool's own failure (an
// ordinary IsError response) must never be treated as the connection
// itself being dead -- respawning here would tear down a working
// subprocess (and the mesh presence/identity it already established)
// over nothing.
func TestCallTool_ToolLevelErrorDoesNotTriggerRespawn(t *testing.T) {
	session := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "unreachable"}}}, nil
	}}
	spawnCalls := 0
	c := newTestClient(session, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		spawnCalls++
		return nil, errors.New("should never be called")
	})

	_, err := c.CallTool(context.Background(), "mesh_ring", nil)
	if err == nil {
		t.Fatalf("expected the tool-level error to surface")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("expected the original tool error text preserved, got: %v", err)
	}
	if spawnCalls != 0 {
		t.Fatalf("expected no respawn attempt for a tool-level error, got %d", spawnCalls)
	}
}

func TestCallTool_RespawnCooldownPreventsRepeatedAttempts(t *testing.T) {
	orig := respawnCooldown
	respawnCooldown = time.Hour // never elapses within this test
	defer func() { respawnCooldown = orig }()

	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	spawnCalls := 0
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		spawnCalls++
		return dead, nil // stays "dead" -- irrelevant, this test only counts attempts
	})

	_, _ = c.CallTool(context.Background(), "mesh_rooms", nil) // first failure -- one respawn attempt
	_, _ = c.CallTool(context.Background(), "mesh_rooms", nil) // second failure, still within cooldown

	if spawnCalls != 1 {
		t.Fatalf("expected exactly one respawn attempt despite two failures within cooldown, got %d", spawnCalls)
	}
}

// TestCallTool_ConcurrentTransportFailuresTriggerOnlyOneRespawn is the
// singleflight property: roomwaiter, ringwaiter, the TUI, and the agent
// loop all share one *Client and could all notice the same dead session
// within the same moment -- this must produce exactly one respawn, not
// one per caller. spawnFn is gated by a channel so the test can
// deterministically confirm concurrent callers actually overlapped
// (arrived while respawning==true) rather than happening to run
// sequentially by luck.
func TestCallTool_ConcurrentTransportFailuresTriggerOnlyOneRespawn(t *testing.T) {
	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	var mu sync.Mutex
	spawnCalls := 0
	spawning := make(chan struct{})
	release := make(chan struct{})
	var spawningOnce sync.Once
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		mu.Lock()
		spawnCalls++
		mu.Unlock()
		spawningOnce.Do(func() { close(spawning) })
		<-release
		return &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
			return okResult("ok"), nil
		}}, nil
	})

	const n = 5
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = c.CallTool(context.Background(), "mesh_rooms", nil)
		}()
	}
	close(start)

	select {
	case <-spawning:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected a respawn attempt to start")
	}
	time.Sleep(50 * time.Millisecond) // let the other n-1 callers arrive and bail on respawning==true
	close(release)
	wg.Wait()

	mu.Lock()
	got := spawnCalls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected exactly one respawn attempt across %d concurrent callers, got %d", n, got)
	}
}

func TestCallTool_RespawnFailureReturnsOriginalError(t *testing.T) {
	resetRespawnCooldown(t)
	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		return nil, errors.New("npx: network unreachable")
	})

	_, err := c.CallTool(context.Background(), "mesh_rooms", nil)
	if err == nil {
		t.Fatalf("expected an error when both the original call and the respawn fail")
	}
	if !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("expected the original transport error preserved when respawn itself fails, got: %v", err)
	}
}

func TestListTools_TransportFailureTriggersRespawnAndRetry(t *testing.T) {
	resetRespawnCooldown(t)
	dead := &fakeSession{listToolsFunc: func(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
		return nil, errors.New("broken pipe")
	}}
	fresh := &fakeSession{listToolsFunc: func(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
		return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "mesh_hello"}}}, nil
	}}
	spawnCalls := 0
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		spawnCalls++
		return fresh, nil
	})

	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(got) != 1 || got[0].Name != "mesh_hello" {
		t.Fatalf("expected the retried call's tools, got %+v", got)
	}
	if spawnCalls != 1 {
		t.Fatalf("expected exactly one respawn attempt, got %d", spawnCalls)
	}
}

func TestListTools_ToolLevelBehaviorUnaffected(t *testing.T) {
	// ListTools has no IsError-shaped response (unlike CallTool) -- this
	// just confirms a real, successful listing never touches spawnFn.
	session := &fakeSession{listToolsFunc: func(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
		return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "mesh_rooms"}}}, nil
	}}
	spawnCalls := 0
	c := newTestClient(session, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		spawnCalls++
		return nil, errors.New("should never be called")
	})

	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(got) != 1 || got[0].Name != "mesh_rooms" {
		t.Fatalf("unexpected tools: %+v", got)
	}
	if spawnCalls != 0 {
		t.Fatalf("expected no respawn attempt, got %d", spawnCalls)
	}
}

// Every respawn attempt must reuse the exact same SpawnOptions Client
// was constructed with (in particular IdentityFile) -- re-resolving a
// fresh identity on each respawn would defeat the whole point (see
// resolveSpawnIdentity's own doc comment).
func TestTryRespawn_ReusesTheSamePinnedOptsOnEveryAttempt(t *testing.T) {
	resetRespawnCooldown(t)
	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	var gotOpts []SpawnOptions
	c := &Client{
		session: dead,
		opts:    SpawnOptions{IdentityFile: "/tmp/pinned-identity"},
		spawnFn: func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
			gotOpts = append(gotOpts, opts)
			return dead, nil // stays "dead" -- this test only checks what opts each attempt used
		},
	}

	c.CallTool(context.Background(), "mesh_rooms", nil)
	c.CallTool(context.Background(), "mesh_rooms", nil)

	if len(gotOpts) != 2 {
		t.Fatalf("expected two respawn attempts, got %d", len(gotOpts))
	}
	for i, o := range gotOpts {
		if o.IdentityFile != "/tmp/pinned-identity" {
			t.Fatalf("respawn attempt %d used IdentityFile %q, expected the pinned one unchanged", i, o.IdentityFile)
		}
	}
}

func TestResolveSpawnIdentity_MintsPathWhenEmpty(t *testing.T) {
	resolved, err := resolveSpawnIdentity(SpawnOptions{})
	if err != nil {
		t.Fatalf("resolveSpawnIdentity: %v", err)
	}
	if resolved.IdentityFile == "" {
		t.Fatalf("expected a minted IdentityFile, got empty")
	}
}

func TestResolveSpawnIdentity_LeavesExplicitPathUntouched(t *testing.T) {
	resolved, err := resolveSpawnIdentity(SpawnOptions{IdentityFile: "/tmp/my-identity"})
	if err != nil {
		t.Fatalf("resolveSpawnIdentity: %v", err)
	}
	if resolved.IdentityFile != "/tmp/my-identity" {
		t.Fatalf("expected the explicit path preserved verbatim, got %q", resolved.IdentityFile)
	}
}

func TestEphemeralIdentityPath_ProducesDistinctPathsAcrossCalls(t *testing.T) {
	a, err := ephemeralIdentityPath()
	if err != nil {
		t.Fatalf("ephemeralIdentityPath: %v", err)
	}
	b, err := ephemeralIdentityPath()
	if err != nil {
		t.Fatalf("ephemeralIdentityPath: %v", err)
	}
	if a == b {
		t.Fatalf("expected distinct paths across calls, got the same twice: %s", a)
	}
}

func TestClient_SetLoggerLogsRespawnAttempts(t *testing.T) {
	resetRespawnCooldown(t)
	dead := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return nil, errors.New("broken pipe")
	}}
	fresh := &fakeSession{callToolFunc: func(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		return okResult("ok"), nil
	}}
	c := newTestClient(dead, func(ctx context.Context, opts SpawnOptions) (mcpSession, error) {
		return fresh, nil
	})
	buf := &syncLogBuffer{}
	c.SetLogger(newTestLogger(buf))

	if _, err := c.CallTool(context.Background(), "mesh_rooms", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "respawning") || !strings.Contains(got, "respawn succeeded") {
		t.Fatalf("expected both a respawn-started and a respawn-succeeded log line, got: %q", got)
	}
}

// internal/realmjoin needs this Client's own RESOLVED identity path
// (resolveSpawnIdentity's own doc comment: an operator-set path, or the
// ephemeral one this package minted), not a second guess at it -- a
// wrong guess would mint a realm credential for a node_id nobody's
// actual mesh presence uses.
func TestClient_IdentityFileReturnsTheResolvedPath(t *testing.T) {
	c := &Client{session: &fakeSession{}, opts: SpawnOptions{IdentityFile: "/tmp/lazymesh-identity-123-abc.seed"}}
	if got := c.IdentityFile(); got != "/tmp/lazymesh-identity-123-abc.seed" {
		t.Fatalf("expected the resolved IdentityFile, got %q", got)
	}
}

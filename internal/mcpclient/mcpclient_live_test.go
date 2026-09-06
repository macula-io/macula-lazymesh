//go:build live

package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveConcurrentCallTool_DoesNotSerialize is macula-io/macula-
// lazymesh#14's foundational check (Vega's item): confirms that ONE
// *Client (one MCP session, one macula-mcp subprocess) can genuinely run
// several outstanding mesh_wait_room calls concurrently, not queued one
// behind another -- the roomwaiter design (one goroutine per joined room,
// all sharing a single client) depends entirely on this being true in
// practice, not just "no serializing lock in the go-sdk source" in
// principle.
//
// Design: a separate "publisher" client (a distinct identity) opens N
// rooms and joins the client-under-test to each; the test then issues N
// concurrent mesh_wait_room calls (wait_seconds well beyond every
// stagger below) on the client under test, waits a fixed head-start
// (comfortably longer than a real join+tap round trip) so every call is
// verifiably already established before ANY message exists, then the
// publisher sends one message per room at a different stagger
// (1s, 2s, 3s, ...). If the client serializes calls internally, later
// rooms' waits would start their own clock only after earlier ones
// return, ballooning total wall time toward the SUM of every stagger
// instead of clustering around the LAST one -- a clear, unambiguous
// signal either way.
func TestLiveConcurrentCallTool_DoesNotSerialize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	underTest, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (under test): %v", err)
	}
	defer underTest.Close()
	defer func() { _, _ = underTest.CallTool(context.Background(), "mesh_goodbye", nil) }()

	publisher, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (publisher): %v", err)
	}
	defer publisher.Close()
	defer func() { _, _ = publisher.CallTool(context.Background(), "mesh_goodbye", nil) }()

	const n = 5
	rooms := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := publisher.CallTool(ctx, "mesh_open_room", map[string]any{"purpose": fmt.Sprintf("lazymesh#14 probe %d", i)})
		if err != nil {
			t.Fatalf("mesh_open_room %d: %v", i, err)
		}
		var parsed struct {
			RoomTopic string `json:"room_topic"`
		}
		if err := json.Unmarshal([]byte(res), &parsed); err != nil || parsed.RoomTopic == "" {
			t.Fatalf("mesh_open_room %d: unexpected result %q (err %v)", i, res, err)
		}
		rooms = append(rooms, parsed.RoomTopic)
		if _, err := underTest.CallTool(ctx, "mesh_join_room", map[string]any{"room_topic": parsed.RoomTopic}); err != nil {
			t.Fatalf("underTest join room %d: %v", i, err)
		}
	}

	const headStart = 5 * time.Second
	const staggerStep = 1 * time.Second

	type result struct {
		room     string
		elapsed  time.Duration
		err      error
		timedOut int
	}
	results := make(chan result, n)
	start := time.Now()

	for i, room := range rooms {
		go func(i int, room string) {
			res, err := underTest.CallTool(ctx, "mesh_wait_room", map[string]any{
				"room_topic":   room,
				"wait_seconds": 60,
			})
			elapsed := time.Since(start)
			if err != nil {
				results <- result{room: room, elapsed: elapsed, err: err}
				return
			}
			var parsed struct {
				TimedOut int `json:"timed_out"`
			}
			_ = json.Unmarshal([]byte(res), &parsed)
			results <- result{room: room, elapsed: elapsed, timedOut: parsed.TimedOut}
		}(i, room)
	}

	// Fixed head start before ANY publish, so every wait call is
	// guaranteed already established (joined+tapped+cursor captured)
	// before a message exists to find.
	time.Sleep(headStart)
	var wg sync.WaitGroup
	publishStart := time.Now()
	for i, room := range rooms {
		wg.Add(1)
		go func(i int, room string) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * staggerStep)
			if _, err := publisher.CallTool(ctx, "mesh_say", map[string]any{
				"room_topic": room,
				"text":       fmt.Sprintf("probe message %d", i),
			}); err != nil {
				t.Errorf("publish to room %d: %v", i, err)
			}
		}(i, room)
	}
	wg.Wait()

	got := make([]result, 0, n)
	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			got = append(got, r)
		case <-time.After(70 * time.Second):
			t.Fatalf("timed out waiting for all %d concurrent mesh_wait_room calls to return, got %d so far: %+v", n, len(got), got)
		}
	}

	t.Logf("publish stagger spanned %v; results:", time.Since(publishStart))
	var maxElapsed time.Duration
	for _, r := range got {
		t.Logf("  room=%s elapsed=%v err=%v timed_out=%d", r.room, r.elapsed, r.err, r.timedOut)
		if r.err != nil {
			t.Errorf("room %s: mesh_wait_room returned an error: %v", r.room, r.err)
		}
		if r.timedOut != 0 {
			t.Errorf("room %s: expected the probe message to arrive, got timed_out=1 (elapsed %v) -- either serialization delayed this call past its own message, or the message was missed", r.room, r.elapsed)
		}
		if r.elapsed > maxElapsed {
			maxElapsed = r.elapsed
		}
	}

	// Concurrent: total span clusters around headStart + (n-1)*staggerStep
	// (here 5s + 4s = 9s) plus normal network/poll jitter. Serialized: it
	// would balloon toward headStart + sum(0..n-1)*staggerStep territory,
	// or several calls would show timed_out=1 because their own wait
	// didn't even start until after their message had already gone by.
	// Generous bound (2x expected + slack) so this only fails on genuine
	// serialization, not ordinary jitter.
	expected := headStart + time.Duration(n-1)*staggerStep
	bound := 2*expected + 5*time.Second
	if maxElapsed > bound {
		t.Errorf("max elapsed %v exceeds %v (2x expected %v + slack) -- looks serialized, not concurrent", maxElapsed, bound, expected)
	}
}

// TestLiveSpawn_ListsRealMaculaMCPTools actually launches macula-mcp via
// npx (network required) and confirms the MCP handshake completes and
// tools/list returns macula-mcp's real tools -- the thing the whole
// package exists to do. Run with: go test -tags live ./internal/mcpclient/...
func TestLiveSpawn_ListsRealMaculaMCPTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("expected at least one tool from macula-mcp, got none")
	}

	found := false
	for _, tool := range tools {
		if tool.Name == "mesh_hello" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected mesh_hello among macula-mcp's tools, got: %+v", tools)
	}
}

// TestLiveSpawn_CallToolWorks confirms a real tools/call round trip, not
// just tools/list -- mesh_rooms takes no arguments and is a safe,
// side-effect-free read.
func TestLiveSpawn_CallToolWorks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "mesh_rooms", nil)
	if err != nil {
		t.Fatalf("CallTool(mesh_rooms): %v", err)
	}
	if result == "" {
		t.Fatalf("expected non-empty result from mesh_rooms")
	}
}

// TestLiveSpawn_ContactPolicyFileIsolation confirms the actual isolation
// property (found investigating the ring-answering UX, 2026-09-06), not
// just that the env var gets constructed correctly: a real macula-mcp
// spawned with a custom ContactPolicyFile reports THAT path back (via
// mesh_hello's own ring.policy_file field), not the shared
// ~/.config/macula-mcp/contact_policy.json default every other macula-mcp
// instance on this machine uses.
func TestLiveSpawn_ContactPolicyFileIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	customPath := filepath.Join(t.TempDir(), "contact_policy.json")
	client, err := Spawn(ctx, SpawnOptions{ContactPolicyFile: customPath})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "mesh_hello", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool(mesh_hello): %v", err)
	}

	var parsed struct {
		Ring struct {
			PolicyFile string `json:"policy_file"`
		} `json:"ring"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("decode mesh_hello result: %v (raw: %s)", err, result)
	}
	if parsed.Ring.PolicyFile != customPath {
		t.Fatalf("expected policy_file %q, got %q -- isolation is not actually working", customPath, parsed.Ring.PolicyFile)
	}
	if strings.Contains(parsed.Ring.PolicyFile, "macula-mcp") {
		t.Fatalf("policy_file %q looks like the shared default, not the isolated path", parsed.Ring.PolicyFile)
	}
}

// TestLiveSpawn_MeshAgentsReturnsPetnames confirms 0.24.0's petname
// support actually works against the real mesh: petname is computed
// client-side from a peer's node_id, so this doesn't require the peer
// itself to be on 0.24.0 -- verified against real currently-online peers,
// not a synthetic fixture.
func TestLiveSpawn_MeshAgentsReturnsPetnames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "mesh_agents", map[string]any{"page_size": 50})
	if err != nil {
		t.Fatalf("CallTool(mesh_agents): %v", err)
	}
	if !strings.Contains(result, `"petname"`) {
		t.Fatalf("expected mesh_agents to return petname fields under 0.24.0, got: %s", result)
	}
}

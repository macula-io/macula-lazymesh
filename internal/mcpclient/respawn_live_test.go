//go:build live

package mcpclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestLiveRespawn_RecoversAfterTheUnderlyingSessionDies is the real
// end-to-end proof for the 2026-09-08 respawn work: a genuine macula-mcp
// subprocess, a genuine transport-level death (closing the session
// underneath Client without going through Client.Close -- the same
// shape a real crash or OOM-kill would produce from Client's own point
// of view: the next call's RPC round trip simply fails), and a
// subsequent CallTool that recovers transparently, exactly the "the
// process stays alive but is functionally dead forever" gap this whole
// feature closes.
func TestLiveRespawn_RecoversAfterTheUnderlyingSessionDies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	before, err := client.CallTool(ctx, "mesh_rooms", nil)
	if err != nil {
		t.Fatalf("CallTool before kill: %v", err)
	}
	if before == "" {
		t.Fatalf("expected a non-empty mesh_rooms result before the kill")
	}

	// Reach past Client's own public API to kill the live session
	// directly -- an in-package live test, same convention as this
	// package's other live tests -- simulating the underlying macula-mcp
	// subprocess dying out from under Client without Client itself
	// having initiated the shutdown.
	client.mu.RLock()
	dying := client.session
	client.mu.RUnlock()
	if err := dying.Close(); err != nil {
		t.Fatalf("kill the underlying session: %v", err)
	}

	after, err := client.CallTool(ctx, "mesh_rooms", nil)
	if err != nil {
		t.Fatalf("CallTool after kill: expected transparent recovery, got error: %v", err)
	}
	if after == "" {
		t.Fatalf("expected a non-empty mesh_rooms result after recovery")
	}

	client.mu.RLock()
	recovered := client.session
	client.mu.RUnlock()
	if recovered == dying {
		t.Fatalf("expected Client to be holding a NEW session after respawn, still holding the killed one")
	}
}

// TestLiveRespawn_PreservesIdentityAcrossRespawn is the specific
// consequence found scoping this work (see resolveSpawnIdentity's own
// doc comment): without pinning an explicit identity, a respawn goes
// through a brand new `npx` process every time, so macula-mcp's own
// PPID-scoped default identity would silently change on every respawn.
// Confirms the fix: mesh_hello's own node_id is identical before and
// after a forced respawn, with NO IdentityFile set by the caller (the
// common case).
func TestLiveRespawn_PreservesIdentityAcrossRespawn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, SpawnOptions{}) // no IdentityFile -- the common case
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	nodeIDBefore, err := helloNodeID(ctx, client)
	if err != nil {
		t.Fatalf("mesh_hello before kill: %v", err)
	}
	if nodeIDBefore == "" {
		t.Fatalf("expected a non-empty node_id before the kill")
	}

	client.mu.RLock()
	dying := client.session
	client.mu.RUnlock()
	if err := dying.Close(); err != nil {
		t.Fatalf("kill the underlying session: %v", err)
	}

	nodeIDAfter, err := helloNodeID(ctx, client)
	if err != nil {
		t.Fatalf("mesh_hello after kill: %v", err)
	}
	if nodeIDAfter != nodeIDBefore {
		t.Fatalf("expected the same node_id across a respawn (identity pinned via resolveSpawnIdentity), got %q before, %q after", nodeIDBefore, nodeIDAfter)
	}
}

func helloNodeID(ctx context.Context, client *Client) (string, error) {
	res, err := client.CallTool(ctx, "mesh_hello", map[string]any{})
	if err != nil {
		return "", err
	}
	var parsed struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(res), &parsed); err != nil {
		return "", err
	}
	return parsed.NodeID, nil
}

//go:build live

package mcpclient

import (
	"context"
	"testing"
	"time"
)

// TestLiveSpawn_ListsRealMaculaMCPTools actually launches macula-mcp via
// npx (network required) and confirms the MCP handshake completes and
// tools/list returns macula-mcp's real tools -- the thing the whole
// package exists to do. Run with: go test -tags live ./internal/mcpclient/...
func TestLiveSpawn_ListsRealMaculaMCPTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := Spawn(ctx, "")
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

	client, err := Spawn(ctx, "")
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

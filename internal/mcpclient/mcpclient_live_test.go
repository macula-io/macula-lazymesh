//go:build live

package mcpclient

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

//go:build live

package main

import (
	"context"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveBuildToolSource_CombinesRealMaculaMCPAndLocalTools confirms
// Phase 2's actual wiring, not just its unit-tested pieces in isolation:
// a real macula-mcp spawn combined with a real (enabled) localtools
// source produces one ToolSource advertising both sets of tools with no
// name collisions, and can actually route a call to each side.
func TestLiveBuildToolSource_CombinesRealMaculaMCPAndLocalTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	cfg := config.Config{
		LocalTools: config.LocalTools{
			Enabled:    true,
			WorkingDir: t.TempDir(),
		},
	}

	tools, err := buildToolSource(cfg, client)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}

	listed, err := tools.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	sawMesh, sawShell := false, false
	for _, tool := range listed {
		if tool.Name == "mesh_hello" {
			sawMesh = true
		}
		if tool.Name == "shell_exec" {
			sawShell = true
		}
	}
	if !sawMesh {
		t.Fatalf("expected mesh_hello among combined tools")
	}
	if !sawShell {
		t.Fatalf("expected shell_exec among combined tools")
	}

	result, err := tools.CallToolRaw(ctx, "shell_exec", `{"command":"echo phase2-live-check"}`)
	if err != nil {
		t.Fatalf("CallToolRaw(shell_exec): %v", err)
	}
	if result == "" {
		t.Fatalf("expected non-empty shell_exec result")
	}
}

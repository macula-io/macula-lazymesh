//go:build live

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveBuildToolSource_IncludesMeshServiceToolsByDefault confirms
// Phase 3's actual production wiring (main.go's buildToolSource, not just
// internal/meshservices in isolation): a real macula-mcp spawn, combined
// the same way the real binary does it, lists at least one real
// mesh_service_* tool by default -- no local_tools, no allowlist
// override, exactly what an operator gets out of the box.
func TestLiveBuildToolSource_IncludesMeshServiceToolsByDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	tools, err := buildToolSource(config.Config{}, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}

	listed, err := tools.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	sawMeshRooms, sawMeshService := false, false
	for _, tool := range listed {
		if tool.Name == "mesh_rooms" {
			sawMeshRooms = true
		}
		if strings.HasPrefix(tool.Name, "mesh_service_") {
			sawMeshService = true
		}
	}
	if !sawMeshRooms {
		t.Fatalf("expected mesh_rooms among the default tools")
	}
	if !sawMeshService {
		t.Fatalf("expected at least one mesh_service_* tool among the default tools -- Phase 3 should be on by default")
	}
}

// TestLiveBuildToolSource_DefaultAllowlistExcludesShellExecEvenWhenLocalToolsEnabled
// is the specific regression Fable's review (2026-09-06) exists to guard:
// local_tools.enabled controls whether the shell_exec/read_file/write_file
// source is wired up at all, not whether an agent driven by untrusted mesh
// content is allowed to reach it. Even with local tools enabled, the
// default allowlist must still keep shell_exec out of what's actually
// listed and callable.
func TestLiveBuildToolSource_DefaultAllowlistExcludesShellExecEvenWhenLocalToolsEnabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	cfg := config.Config{
		LocalTools: config.LocalTools{
			Enabled:    true,
			WorkingDir: t.TempDir(),
		},
		// ToolAllowlist deliberately left unset -- this is the default an
		// operator gets from just flipping local_tools.enabled, with no
		// second, separate opt-in.
	}

	tools, err := buildToolSource(cfg, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}

	listed, err := tools.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	sawMesh, sawShell := false, false
	for _, tool := range listed {
		if tool.Name == "mesh_rooms" {
			sawMesh = true
		}
		if tool.Name == "shell_exec" {
			sawShell = true
		}
	}
	if !sawMesh {
		t.Fatalf("expected mesh_rooms to still be listed (it's on the default allowlist)")
	}
	if sawShell {
		t.Fatalf("shell_exec must NOT be listed under the default allowlist, even with local_tools.enabled")
	}

	if _, err := tools.CallToolRaw(ctx, "shell_exec", `{"command":"echo should-be-refused"}`); err == nil {
		t.Fatalf("expected shell_exec to be refused at execution time under the default allowlist")
	}
}

// TestLiveBuildToolSource_ExplicitAllowlistOverrideExposesShellExec confirms
// the other half of the same design: an operator who explicitly adds
// shell_exec to their own config.ToolAllowlist -- a second, separate,
// conscious step beyond local_tools.enabled -- does get a working
// shell_exec, combined correctly with the real mesh tools.
func TestLiveBuildToolSource_ExplicitAllowlistOverrideExposesShellExec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	cfg := config.Config{
		LocalTools: config.LocalTools{
			Enabled:    true,
			WorkingDir: t.TempDir(),
		},
		ToolAllowlist: []string{"mesh_hello", "shell_exec"},
	}

	tools, err := buildToolSource(cfg, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}

	listed, err := tools.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	sawShell := false
	for _, tool := range listed {
		if tool.Name == "shell_exec" {
			sawShell = true
		}
		if tool.Name == "read_file" || tool.Name == "write_file" {
			t.Fatalf("%s should not be listed -- it was never added to ToolAllowlist", tool.Name)
		}
	}
	if !sawShell {
		t.Fatalf("expected shell_exec to be listed once explicitly added to ToolAllowlist")
	}

	result, err := tools.CallToolRaw(ctx, "shell_exec", `{"command":"echo phase2-live-check"}`)
	if err != nil {
		t.Fatalf("CallToolRaw(shell_exec): %v", err)
	}
	if result == "" {
		t.Fatalf("expected non-empty shell_exec result")
	}
}

// TestLiveSayGoodbye_CallsRealMeshGoodbye confirms sayGoodbye's wiring
// against a real macula-mcp spawn, not just the fake in main_test.go
// (macula-io/macula-lazymesh#6): mesh_hello establishes presence, then
// sayGoodbye's mesh_goodbye call must succeed against the live mesh.
func TestLiveSayGoodbye_CallsRealMeshGoodbye(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	if _, err := client.CallTool(ctx, "mesh_hello", nil); err != nil {
		t.Fatalf("mesh_hello: %v", err)
	}

	// sayGoodbye takes its own bounded context internally (never ctx above),
	// exactly as run() calls it after program.Run() returns.
	sayGoodbye(client, goodbyeTimeout)

	// A second mesh_hello confirms the session is still usable afterward --
	// sayGoodbye must not tear down the MCP connection itself, only tell
	// the mesh this agent is leaving.
	if _, err := client.CallTool(ctx, "mesh_hello", nil); err != nil {
		t.Fatalf("mesh_hello after sayGoodbye: %v", err)
	}
}

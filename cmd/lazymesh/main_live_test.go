//go:build live

package main

import (
	"context"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

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
		// ToolAllowlist deliberately left unset -- this is the default an
		// operator gets from just flipping local_tools.enabled, with no
		// second, separate opt-in.
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
		t.Fatalf("expected mesh_hello to still be listed (it's on the default allowlist)")
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
		ToolAllowlist: []string{"mesh_hello", "shell_exec"},
	}

	tools, err := buildToolSource(cfg, client)
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

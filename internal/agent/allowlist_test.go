package agent

import (
	"context"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

func TestAllowlistSource_ListTools_FiltersToAllowedOnly(t *testing.T) {
	inner := &namedFakeSource{name: "inner", tools: []mcpclient.Tool{
		{Name: "mesh_say"}, {Name: "shell_exec"}, {Name: "mesh_rooms"},
	}}
	a := NewAllowlistSource(inner, []string{"mesh_say", "mesh_rooms"})

	tools, err := a.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 allowed tools, got %d: %+v", len(tools), tools)
	}
	for _, tool := range tools {
		if tool.Name == "shell_exec" {
			t.Fatalf("shell_exec should have been filtered out, but was present")
		}
	}
}

func TestAllowlistSource_CallToolRaw_RefusesDisallowedTool(t *testing.T) {
	inner := &namedFakeSource{name: "inner", tools: []mcpclient.Tool{{Name: "shell_exec"}}}
	a := NewAllowlistSource(inner, []string{"mesh_say"})

	if _, err := a.CallToolRaw(context.Background(), "shell_exec", `{"command":"whoami"}`); err == nil {
		t.Fatalf("expected an error calling a tool not on the allowlist")
	}
	if len(inner.calls) != 0 {
		t.Fatalf("expected the inner source to never be called for a disallowed tool, got %v", inner.calls)
	}
}

func TestAllowlistSource_CallToolRaw_PermitsAllowedTool(t *testing.T) {
	inner := &namedFakeSource{name: "inner", tools: []mcpclient.Tool{{Name: "mesh_say"}}}
	a := NewAllowlistSource(inner, []string{"mesh_say"})

	result, err := a.CallToolRaw(context.Background(), "mesh_say", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("CallToolRaw returned error for an allowed tool: %v", err)
	}
	if result != "inner:mesh_say" {
		t.Fatalf("expected call to reach the inner source, got %q", result)
	}
}

func TestAllowlistSource_EmptyAllowlistBlocksEverything(t *testing.T) {
	inner := &namedFakeSource{name: "inner", tools: []mcpclient.Tool{{Name: "mesh_say"}}}
	a := NewAllowlistSource(inner, nil)

	tools, err := a.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools with an empty allowlist, got %+v", tools)
	}
	if _, err := a.CallToolRaw(context.Background(), "mesh_say", "{}"); err == nil {
		t.Fatalf("expected an error calling any tool with an empty allowlist")
	}
}

// This is the specific regression Fable's review flagged: local tools
// must stay unreachable through the allowlist even when the underlying
// source is enabled, unless the operator explicitly added them.
func TestDefaultToolAllowlist_ExcludesLocalTools(t *testing.T) {
	disallowed := []string{"shell_exec", "read_file", "write_file"}
	allowed := make(map[string]bool, len(DefaultToolAllowlist))
	for _, name := range DefaultToolAllowlist {
		allowed[name] = true
	}
	for _, name := range disallowed {
		if allowed[name] {
			t.Fatalf("DefaultToolAllowlist must not include %q", name)
		}
	}
}

// Real gap Raf hit live (2026-09-08): the agent could answer a ring
// (mesh_answer_ring) but had no way to initiate one, since mesh_ring
// itself was never on this list. Checked against the original adversarial-
// review commit before adding it (see this list's own doc comment) --
// it's a signed conversational mesh_call to a proven peer endpoint, same
// risk bucket as mesh_say/mesh_answer_ring already here, not the
// mesh_serve/shell_exec/mesh_remember_directory bucket this file exists
// to keep out.
func TestDefaultToolAllowlist_IncludesMeshRing(t *testing.T) {
	found := false
	for _, name := range DefaultToolAllowlist {
		if name == "mesh_ring" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mesh_ring on DefaultToolAllowlist, got %v", DefaultToolAllowlist)
	}
}

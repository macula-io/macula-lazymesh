package main

import (
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
)

func TestNextBackoff_DoublesUntilCap(t *testing.T) {
	d := initialBackoff
	seen := []time.Duration{d}
	for i := 0; i < 10; i++ {
		d = nextBackoff(d)
		seen = append(seen, d)
	}
	if seen[1] != 2*initialBackoff {
		t.Fatalf("expected first doubling to be %v, got %v", 2*initialBackoff, seen[1])
	}
	for _, d := range seen {
		if d > maxBackoff {
			t.Fatalf("backoff exceeded the cap: %v > %v", d, maxBackoff)
		}
	}
	if seen[len(seen)-1] != maxBackoff {
		t.Fatalf("expected backoff to have reached the cap after repeated doubling, got %v", seen[len(seen)-1])
	}
}

func TestResolveAllowlist_DefaultsWhenUnset(t *testing.T) {
	got := resolveAllowlist(config.Config{})
	if len(got) != len(agent.DefaultToolAllowlist) {
		t.Fatalf("expected DefaultToolAllowlist when cfg.ToolAllowlist is unset, got %v", got)
	}
}

func TestResolveAllowlist_HonorsExplicitOverride(t *testing.T) {
	custom := []string{"mesh_say", "shell_exec"}
	got := resolveAllowlist(config.Config{ToolAllowlist: custom})
	if len(got) != 2 || got[0] != "mesh_say" || got[1] != "shell_exec" {
		t.Fatalf("expected explicit override to be honored verbatim, got %v", got)
	}
}

func TestAllowlistIncludes(t *testing.T) {
	list := []string{"mesh_say", "mesh_rooms"}
	if !allowlistIncludes(list, "mesh_say") {
		t.Fatalf("expected mesh_say to be found")
	}
	if allowlistIncludes(list, "shell_exec") {
		t.Fatalf("expected shell_exec not to be found in the default-shaped list")
	}
}

// This is the specific bug class Fable's review would have caught if it
// existed: local_tools.enabled alone must never make the system prompt
// claim shell_exec is available when the resolved allowlist doesn't
// actually include it.
func TestResolveAllowlist_LocalToolsEnabledAloneDoesNotUnlockShellExec(t *testing.T) {
	cfg := config.Config{LocalTools: config.LocalTools{Enabled: true, WorkingDir: "/tmp/whatever"}}
	if allowlistIncludes(resolveAllowlist(cfg), "shell_exec") {
		t.Fatalf("local_tools.enabled alone must not put shell_exec on the resolved allowlist")
	}
}

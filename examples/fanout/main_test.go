package main

import "testing"

// TestToolGateAllowsOnlyListedTools: a worker gated by the parent runs
// only the tools the parent named, and every refusal says why.
func TestToolGateAllowsOnlyListedTools(t *testing.T) {
	gate := toolGate([]string{"mesh_say"})
	if allow, _ := gate("mesh_say", "{}"); !allow {
		t.Fatal("a listed tool was refused")
	}
	allow, reason := gate("mesh_join_room", "{}")
	if allow || reason == "" {
		t.Fatalf("an unlisted tool: allow=%v reason=%q", allow, reason)
	}
	if allow, _ := toolGate(nil)("mesh_say", "{}"); allow {
		t.Fatal("an empty list allowed a tool")
	}
}

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestHookSourceGatesEveryTool: a pre-tool hook sees every call, a denial
// carries the hook's reason to the model and the inner source never runs
// the call, an allow runs it (G8).
func TestHookSourceGatesEveryTool(t *testing.T) {
	var seen []string
	gate := func(ctx context.Context, tool, argumentsJSON string) (bool, string, error) {
		seen = append(seen, tool+" "+argumentsJSON)
		if tool == "mesh_say" {
			return true, "", nil
		}
		return false, "not in this room", nil
	}
	inner := &askRecordingTools{}
	src := NewHookSource(inner, gate)

	if _, err := src.CallToolRaw(context.Background(), "mesh_say", `{"text":"hi"}`); err != nil {
		t.Fatalf("allowed call: %v", err)
	}
	_, err := src.CallToolRaw(context.Background(), "mesh_join_room", `{}`)
	if err == nil || !strings.Contains(err.Error(), "not in this room") {
		t.Fatalf("denied call error = %v, want the hook's reason", err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "mesh_say" {
		t.Fatalf("inner calls = %v", inner.calls)
	}
	if len(seen) != 2 || seen[0] != `mesh_say {"text":"hi"}` {
		t.Fatalf("hook saw %v", seen)
	}
}

// TestHookSourceErrorRefuses: a hook that cannot answer refuses the call.
func TestHookSourceErrorRefuses(t *testing.T) {
	gate := func(ctx context.Context, tool, argumentsJSON string) (bool, string, error) {
		return true, "", errors.New("hook controller went away")
	}
	inner := &askRecordingTools{}
	if _, err := NewHookSource(inner, gate).CallToolRaw(context.Background(), "mesh_say", `{}`); err == nil {
		t.Fatal("a hook error let the call run")
	}
	if len(inner.calls) != 0 {
		t.Fatalf("inner ran %v", inner.calls)
	}
}

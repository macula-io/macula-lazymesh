package main

import (
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
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

func TestProviderLabel_MatchesBuildProviderDefault(t *testing.T) {
	// buildProvider's own switch treats "" the same as "deepseek" -- the
	// status strip must show that resolved default, never a blank
	// provider name just because config.yaml left it unset.
	if got := providerLabel(config.Config{Provider: ""}); got != "deepseek" {
		t.Fatalf("expected empty Provider to resolve to %q, got %q", "deepseek", got)
	}
	if got := providerLabel(config.Config{Provider: "anthropic"}); got != "anthropic" {
		t.Fatalf("expected an explicit Provider to pass through unchanged, got %q", got)
	}
}

func TestResolveAllowlist_DefaultsWhenUnset(t *testing.T) {
	got := resolveAllowlist(config.Config{})
	wantLen := len(agent.DefaultToolAllowlist) + len(meshservices.AllowedToolNames())
	if len(got) != wantLen {
		t.Fatalf("expected agent's defaults + meshservices' curated names (%d), got %d: %v", wantLen, len(got), got)
	}
	for _, name := range agent.DefaultToolAllowlist {
		if !allowlistIncludes(got, name) {
			t.Fatalf("expected default to include agent primitive %q", name)
		}
	}
	for _, name := range meshservices.AllowedToolNames() {
		if !allowlistIncludes(got, name) {
			t.Fatalf("expected default to include curated mesh-service tool %q", name)
		}
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

func TestNextPrompt_PrefersPendingUserMessage(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "hello from the human"
	if got := nextPrompt(ch, "default cadence"); got != "hello from the human" {
		t.Fatalf("expected the pending user message to win, got %q", got)
	}
}

func TestNextPrompt_FallsBackToDefaultWhenNothingPending(t *testing.T) {
	ch := make(chan string, 1)
	if got := nextPrompt(ch, "default cadence"); got != "default cadence" {
		t.Fatalf("expected the default cadence prompt, got %q", got)
	}
}

func TestNextPrompt_DoesNotBlockOnEmptyChannel(t *testing.T) {
	ch := make(chan string) // unbuffered, nobody ever sends
	done := make(chan struct{})
	go func() {
		nextPrompt(ch, "default")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("nextPrompt blocked on an empty channel instead of returning the default immediately")
	}
}

// The tests below cover macula-io/macula-lazymesh#1: the agent's
// participation scope must be "every room mesh_rooms reports," never a
// single hardcoded room, regardless of whether --room was passed.

func TestBuildSystemPrompt_AlwaysInstructsDiscoveringRoomsLive(t *testing.T) {
	for _, room := range []string{"", "agents.room.deadbeef"} {
		got := buildSystemPrompt(room, "", false)
		if !strings.Contains(got, "mesh_rooms") {
			t.Fatalf("room=%q: expected system prompt to instruct calling mesh_rooms, got: %s", room, got)
		}
		if !strings.Contains(got, "every room you are currently a member of") {
			t.Fatalf("room=%q: expected system prompt to scope participation to every joined room, got: %s", room, got)
		}
	}
}

func TestBuildSystemPrompt_EmptyRoomHasNoPriorityHint(t *testing.T) {
	got := buildSystemPrompt("", "", false)
	if strings.Contains(got, "prioritize this room") {
		t.Fatalf("expected no room-specific priority hint when room is empty, got: %s", got)
	}
}

func TestBuildSystemPrompt_NonEmptyRoomAddsPriorityHintWithoutNarrowingScope(t *testing.T) {
	got := buildSystemPrompt("agents.room.deadbeef", "", false)
	if !strings.Contains(got, "prioritize this room: agents.room.deadbeef") {
		t.Fatalf("expected the given room to appear as a priority hint, got: %s", got)
	}
	// The hint must not replace the general "check every room" instruction --
	// this is exactly the bug: an old system prompt said only "Room to
	// participate in: X", which never covered a room joined later via an
	// accepted ring.
	if !strings.Contains(got, "not just one you were pointed at") {
		t.Fatalf("expected the priority hint not to narrow scope to just that room, got: %s", got)
	}
}

func TestBuildSystemPrompt_IncludesGoalWhenSet(t *testing.T) {
	got := buildSystemPrompt("", "find the best pun on the mesh", false)
	if !strings.Contains(got, "Additional objective: find the best pun on the mesh") {
		t.Fatalf("expected goal text to appear verbatim, got: %s", got)
	}
}

func TestBuildSystemPrompt_LocalToolsReachableAddsShellExecLine(t *testing.T) {
	without := buildSystemPrompt("", "", false)
	if strings.Contains(without, "shell_exec") {
		t.Fatalf("expected no mention of shell_exec when local tools aren't reachable, got: %s", without)
	}
	with := buildSystemPrompt("", "", true)
	if !strings.Contains(with, "shell_exec") {
		t.Fatalf("expected shell_exec to be mentioned when local tools are reachable, got: %s", with)
	}
}

func TestAgentPrompts_CoverEveryRoomNotJustOnePinned(t *testing.T) {
	for name, p := range map[string]string{
		"agentDefaultPrompt": agentDefaultPrompt,
		"agentInitialPrompt": agentInitialPrompt,
	} {
		if !strings.Contains(p, "mesh_rooms") {
			t.Fatalf("%s: expected the per-cycle prompt to call mesh_rooms so scope isn't pinned to one room, got: %s", name, p)
		}
	}
}

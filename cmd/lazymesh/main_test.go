package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
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
		got := buildSystemPrompt(room, "", false, false, false)
		if !strings.Contains(got, "mesh_rooms") {
			t.Fatalf("room=%q: expected system prompt to instruct calling mesh_rooms, got: %s", room, got)
		}
		if !strings.Contains(got, "every room you are currently a member of") {
			t.Fatalf("room=%q: expected system prompt to scope participation to every joined room, got: %s", room, got)
		}
	}
}

func TestBuildSystemPrompt_EmptyRoomHasNoPriorityHint(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(got, "prioritize this room") {
		t.Fatalf("expected no room-specific priority hint when room is empty, got: %s", got)
	}
}

func TestBuildSystemPrompt_NonEmptyRoomAddsPriorityHintWithoutNarrowingScope(t *testing.T) {
	got := buildSystemPrompt("agents.room.deadbeef", "", false, false, false)
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
	got := buildSystemPrompt("", "find the best pun on the mesh", false, false, false)
	if !strings.Contains(got, "Additional objective: find the best pun on the mesh") {
		t.Fatalf("expected goal text to appear verbatim, got: %s", got)
	}
}

func TestBuildSystemPrompt_LocalToolsReachableAddsShellExecLine(t *testing.T) {
	without := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(without, "shell_exec") {
		t.Fatalf("expected no mention of shell_exec when local tools aren't reachable, got: %s", without)
	}
	with := buildSystemPrompt("", "", true, false, false)
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

// Covers macula-io/macula-lazymesh#3: the system prompt must tell the model
// to check mesh_rooms's own joined list before calling mesh_join_room again,
// rather than assuming it will remember joining from earlier in the
// conversation -- history gets trimmed, so that memory isn't reliable.
func TestBuildSystemPrompt_InstructsCheckingJoinedListBeforeRejoining(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if !strings.Contains(got, "joined list") {
		t.Fatalf("expected system prompt to reference mesh_rooms's joined list, got: %s", got)
	}
	if !strings.Contains(got, "never call mesh_join_room for a room_topic already") {
		t.Fatalf("expected system prompt to instruct against re-joining an already-joined room, got: %s", got)
	}
}

func TestBuildSystemPrompt_RoomHintAlsoChecksJoinedListFirst(t *testing.T) {
	got := buildSystemPrompt("agents.room.deadbeef", "", false, false, false)
	if !strings.Contains(got, "check mesh_rooms's own joined list first") {
		t.Fatalf("expected the priority-room hint to check the joined list before joining, got: %s", got)
	}
}

// Covers macula-io/macula-lazymesh#5: expressiveStyle is off by default
// (existing dry tone unchanged) and, when enabled, adds explicit
// permission to use emoji/expressive tone in room conversation -- never a
// hardcoded persona forced on every operator.
func TestBuildSystemPrompt_ExpressiveStyleOffByDefault(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(got, "emoji") {
		t.Fatalf("expected no emoji guidance when expressiveStyle is false, got: %s", got)
	}
}

func TestBuildSystemPrompt_ExpressiveStyleAddsEmojiGuidance(t *testing.T) {
	got := buildSystemPrompt("", "", false, true, false)
	if !strings.Contains(got, "emoji") {
		t.Fatalf("expected emoji guidance when expressiveStyle is true, got: %s", got)
	}
	if !strings.Contains(got, "mesh_say") {
		t.Fatalf("expected the guidance to scope expressiveness to room conversation text, got: %s", got)
	}
}

// Covers macula-io/macula-lazymesh#6: mesh_goodbye must be called with a
// bounded context (never run()'s own ctx, already cancelled by the time
// every exit path reaches this point) and must never propagate a failure
// -- shutdown has to complete regardless of whether the mesh got the
// message.

type fakeGoodbyeCaller struct {
	calledName  string
	gotDeadline bool
	err         error
}

func (f *fakeGoodbyeCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.calledName = name
	_, f.gotDeadline = ctx.Deadline()
	return "", f.err
}

func TestSayGoodbye_CallsMeshGoodbyeWithBoundedTimeout(t *testing.T) {
	f := &fakeGoodbyeCaller{}
	sayGoodbye(f, time.Second)
	if f.calledName != "mesh_goodbye" {
		t.Fatalf("expected mesh_goodbye to be called, got %q", f.calledName)
	}
	if !f.gotDeadline {
		t.Fatalf("expected sayGoodbye to bound the call with a deadline, got none")
	}
}

func TestSayGoodbye_ToleratesFailureWithoutPropagatingIt(t *testing.T) {
	f := &fakeGoodbyeCaller{err: errors.New("macula-mcp unreachable")}
	sayGoodbye(f, time.Second) // must not panic and has nothing to return
	if f.calledName != "mesh_goodbye" {
		t.Fatalf("expected mesh_goodbye to still have been attempted, got %q", f.calledName)
	}
}

// Covers macula-io/macula-lazymesh#14 (the concurrency spike): the
// spike-mode system prompt must stop instructing the model to reach for
// mesh_say's long wait_reply_seconds itself, and say what to do instead.
func TestBuildSystemPrompt_SpikeRoomWaitersDropsModelDrivenWait(t *testing.T) {
	baseline := buildSystemPrompt("", "", false, false, false)
	if !strings.Contains(baseline, "call mesh_say with a long") {
		t.Fatalf("expected the baseline (non-spike) prompt to keep today's wording unchanged, got: %s", baseline)
	}

	spike := buildSystemPrompt("", "", false, false, true)
	if strings.Contains(spike, "call mesh_say with a long") {
		t.Fatalf("expected the spike prompt to drop the model-driven long-wait instruction, got: %s", spike)
	}
	if !strings.Contains(spike, "do not need to wait for messages yourself") {
		t.Fatalf("expected the spike prompt to explain the harness now handles waiting, got: %s", spike)
	}
}

func TestParseJoinedRooms_ExtractsTopics(t *testing.T) {
	got := parseJoinedRooms(`{"joined":[{"room_topic":"agents.room.a"},{"room_topic":"agents.room.b"}],"seen_on_central":[]}`)
	want := []string{"agents.room.a", "agents.room.b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestParseJoinedRooms_MalformedResultReturnsNil(t *testing.T) {
	if got := parseJoinedRooms("not json"); got != nil {
		t.Fatalf("expected nil for a malformed result, got %v", got)
	}
}

func TestNextEvent_NilManagerBehavesLikeNextPrompt(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "from the human"
	got, ok := nextEvent(context.Background(), ch, nil)
	if !ok || got != "from the human" {
		t.Fatalf("expected nil-manager nextEvent to behave like nextPrompt, got (%q, %v)", got, ok)
	}
}

func TestNextEvent_HumanInputWinsWhenAlreadyPending(t *testing.T) {
	mgr := roomwaiter.New(nil, "")
	ch := make(chan string, 1)
	ch <- "human message"

	got, ok := nextEvent(context.Background(), ch, mgr)
	if !ok || got != "human message" {
		t.Fatalf("expected pending human input to win outright, got (%q, %v)", got, ok)
	}
}

func TestNextEvent_ReturnsNotOkWhenContextDone(t *testing.T) {
	mgr := roomwaiter.New(nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan string)

	_, ok := nextEvent(ctx, ch, mgr)
	if ok {
		t.Fatalf("expected nextEvent to report !ok once ctx is done")
	}
}

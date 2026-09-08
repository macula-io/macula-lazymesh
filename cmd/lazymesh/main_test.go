package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
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

func testAPIKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("test-key\n"), 0o600); err != nil {
		t.Fatalf("write test key file: %v", err)
	}
	return path
}

// Covers the "add the ability to point the LLM backend elsewhere" work:
// buildProvider's switch must actually produce the right concrete
// provider.Provider for each recognized value of cfg.Provider, not just
// resolve a label (see TestProviderLabel_MatchesBuildProviderDefault
// above for that separate, narrower guarantee).
func TestBuildProvider_DispatchesToTheRightBackend(t *testing.T) {
	keyPath := testAPIKeyFile(t)

	cases := []struct {
		name     string
		provider string
		wantType string // %T of the expected concrete provider.Provider
	}{
		{"empty defaults to deepseek", "", "*provider.DeepSeek"},
		{"explicit deepseek", "deepseek", "*provider.DeepSeek"},
		{"anthropic", "anthropic", "*provider.Anthropic"},
		{"nvidia", "nvidia", "*provider.NVIDIA"},
		{"groq", "groq", "*provider.Groq"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Config{Provider: c.provider, APIKeyFile: keyPath}
			got, err := buildProvider(cfg)
			if err != nil {
				t.Fatalf("buildProvider(%q) returned error: %v", c.provider, err)
			}
			if gotType := fmt.Sprintf("%T", got); gotType != c.wantType {
				t.Fatalf("buildProvider(%q): expected %s, got %s", c.provider, c.wantType, gotType)
			}
		})
	}
}

func TestBuildProvider_UnknownProviderErrors(t *testing.T) {
	cfg := config.Config{Provider: "not-a-real-provider", APIKeyFile: testAPIKeyFile(t)}
	if _, err := buildProvider(cfg); err == nil {
		t.Fatalf("expected an error for an unrecognized provider")
	}
}

// nvidia must go through the same explicit, per-provider api_key_file
// the other backends do -- no auto-switching default key path when the
// provider changes. Deliberate: an auto-switching default here would be
// the same shape of footgun as the identity_file bug that made two
// concurrent lazymesh instances collide onto one mesh identity (see
// internal/config/config.go's IdentityFile doc comment).
func TestBuildProvider_NVIDIARequiresItsOwnAPIKeyFile(t *testing.T) {
	cfg := config.Config{Provider: "nvidia"} // no APIKeyFile set
	if _, err := buildProvider(cfg); err == nil {
		t.Fatalf("expected an error when nvidia has no api_key_file configured")
	}
}

// mesh_service_* names are deliberately absent from the default allowlist
// (2026-09-07, R2's close-out): config.MeshServicesEnabled defaults false,
// per Fable's own review accepting a session-static opt-in over trimming
// the 16-tool corpus-search catalog further -- see config.go's own doc
// comment on that field. TestResolveAllowlist_MeshServicesEnabledAddsCuratedNames
// below covers the opted-in case.
func TestResolveAllowlist_DefaultsWhenUnset(t *testing.T) {
	got := resolveAllowlist(config.Config{})
	wantLen := len(agent.DefaultToolAllowlist)
	if len(got) != wantLen {
		t.Fatalf("expected exactly agent's defaults (%d), got %d: %v", wantLen, len(got), got)
	}
	for _, name := range agent.DefaultToolAllowlist {
		if !allowlistIncludes(got, name) {
			t.Fatalf("expected default to include agent primitive %q", name)
		}
	}
	for _, name := range meshservices.AllowedToolNames() {
		if allowlistIncludes(got, name) {
			t.Fatalf("expected curated mesh-service tool %q absent when mesh_services_enabled is unset", name)
		}
	}
}

func TestResolveAllowlist_MeshServicesEnabledAddsCuratedNames(t *testing.T) {
	got := resolveAllowlist(config.Config{MeshServicesEnabled: true})
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
			t.Fatalf("expected mesh_services_enabled to include curated mesh-service tool %q", name)
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

func TestAgentInitialPromptCallsMeshRooms(t *testing.T) {
	if !strings.Contains(agentInitialPrompt, "mesh_rooms") {
		t.Fatalf("expected the startup prompt to call mesh_rooms so scope isn't pinned to one room, got: %s", agentInitialPrompt)
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

// Covers R2's close-out (2026-09-07): mentioning mesh_service_* tools in
// the system prompt when buildToolSource never wired them up (see
// resolveAllowlist's own doc comment on why the two must stay in lockstep)
// wastes tokens on a capability the model doesn't have and risks a
// hallucinated call.
func TestBuildSystemPrompt_MeshServicesOffByDefault(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(got, "mesh_service_") {
		t.Fatalf("expected no mesh_service_* mention when meshServicesEnabled is false, got: %s", got)
	}
}

func TestBuildSystemPrompt_MeshServicesEnabledMentionsMeshServiceTools(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, true)
	if !strings.Contains(got, "mesh_service_") {
		t.Fatalf("expected a mesh_service_* mention when meshServicesEnabled is true, got: %s", got)
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

// Covers macula-io/macula-lazymesh#14/#15: the system prompt must stop
// instructing the model to reach for mesh_say's long wait_reply_seconds
// itself, and explain what happens instead. Unconditional now (no more
// spike-vs-baseline split) -- this is the only behavior.
func TestBuildSystemPrompt_DropsModelDrivenLongWait(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(got, "call mesh_say with a long") {
		t.Fatalf("expected the model-driven long-wait instruction to be gone, got: %s", got)
	}
	if !strings.Contains(got, "do not need to wait for messages yourself") {
		t.Fatalf("expected the system prompt to explain the harness now handles waiting, got: %s", got)
	}
}

// Covers 2026-09-07: the system prompt's own former blanket "every
// single time you are prompted... call mesh_read_inbox with no
// room_topic" ring-check mandate is gone, replaced by internal/
// ringwaiter waking the model only when a ring genuinely exists.
func TestBuildSystemPrompt_DropsBlanketRingCheckMandate(t *testing.T) {
	got := buildSystemPrompt("", "", false, false, false)
	if strings.Contains(got, "every single time you are prompted") {
		t.Fatalf("expected the blanket per-cycle ring-check mandate to be gone, got: %s", got)
	}
	if strings.Contains(got, "rings.pending") {
		t.Fatalf("expected no direct rings.pending mention -- ringwaiter owns this now, got: %s", got)
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
	got, ok := nextEvent(context.Background(), ch, nil, nil)
	if !ok || got != "from the human" {
		t.Fatalf("expected nil-manager nextEvent to behave like nextPrompt, got (%q, %v)", got, ok)
	}
}

func TestNextEvent_HumanInputWinsWhenAlreadyPending(t *testing.T) {
	mgr := roomwaiter.New(nil, "")
	ringMgr := ringwaiter.New(nil, "")
	ch := make(chan string, 1)
	ch <- "human message"

	got, ok := nextEvent(context.Background(), ch, mgr, ringMgr)
	if !ok || got != "human message" {
		t.Fatalf("expected pending human input to win outright, got (%q, %v)", got, ok)
	}
}

func TestNextEvent_ReturnsNotOkWhenContextDone(t *testing.T) {
	mgr := roomwaiter.New(nil, "")
	ringMgr := ringwaiter.New(nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := make(chan string)

	_, ok := nextEvent(ctx, ch, mgr, ringMgr)
	if ok {
		t.Fatalf("expected nextEvent to report !ok once ctx is done")
	}
}

// fakeRoomWaiterCaller lets a test drive a real roomwaiter.Manager (via
// Sync) without a real macula-mcp spawn -- roomwaiter.Caller is exported
// exactly so cross-package callers like nextEvent's own tests can do this.
type fakeRoomWaiterCaller struct{}

func (fakeRoomWaiterCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return `{"reply":{"from":"peer"},"timed_out":0}`, nil
}

func TestNextEvent_ConsumesARealRoomArrival(t *testing.T) {
	mgr := roomwaiter.New(fakeRoomWaiterCaller{}, "")
	ringMgr := ringwaiter.New(nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Sync(ctx, []string{"agents.room.deadbeef"})

	ch := make(chan string)
	got, ok := nextEvent(ctx, ch, mgr, ringMgr)
	if !ok {
		t.Fatalf("expected ok, got false")
	}
	if !strings.Contains(got, "agents.room.deadbeef") {
		t.Fatalf("expected the room arrival's prompt to name the room, got %q", got)
	}
}

// fakeRingWaiterCaller lets a test drive a real ringwaiter.Manager (via
// Start) without a real macula-mcp spawn -- same reasoning as
// fakeRoomWaiterCaller above.
// fakeRingWaiterCaller answers every call (mesh_wait_ring, mesh_read_inbox,
// or anything else) with the same mesh_read_inbox-shaped pending-ring
// payload -- ringwaiter.Manager's own startup checkOnce (mesh_read_inbox)
// picks it up before mesh_wait_ring is ever called, which is all this
// test needs: proving nextEvent's own dispatch, not ringwaiter's
// internals (those have their own exhaustive unit tests in
// internal/ringwaiter).
type fakeRingWaiterCaller struct{}

func (fakeRingWaiterCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return `{"rings":{"pending":[{"ring_id":"r1","purpose":"come help","peer_petname":"Nova"}]}}`, nil
}

func TestNextEvent_ConsumesARealRingArrival(t *testing.T) {
	mgr := roomwaiter.New(nil, "")
	ringMgr := ringwaiter.New(fakeRingWaiterCaller{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ringMgr.Start(ctx)

	ch := make(chan string)
	got, ok := nextEvent(ctx, ch, mgr, ringMgr)
	if !ok {
		t.Fatalf("expected ok, got false")
	}
	if !strings.Contains(got, "r1") || !strings.Contains(got, "Nova") || !strings.Contains(got, "come help") {
		t.Fatalf("expected the ring arrival's prompt to name the ring/peer/purpose, got %q", got)
	}
}

func TestEstimateTokens_RoughlyOneQuarterOfByteLength(t *testing.T) {
	if got := estimateTokens(400); got != 100 {
		t.Fatalf("expected 400 bytes to estimate ~100 tokens, got %d", got)
	}
}

// fakeBudgetToolSource lets a test control exactly how many tools (and
// how large their combined schema is) checkStartupBudget sees, without a
// real macula-mcp spawn.
type fakeBudgetToolSource struct {
	tools []mcpclient.Tool
}

func (f fakeBudgetToolSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return f.tools, nil
}

func (f fakeBudgetToolSource) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

type fakeBudgetProvider struct {
	window int
}

func (f fakeBudgetProvider) ContextWindow() int { return f.window }

func (f fakeBudgetProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, fmt.Errorf("not implemented")
}

// Covers R2 (2026-09-07): both real backends (DeepSeek, NVIDIA) are
// ~1M tokens, so a real fixed prefix (a few KB at most) is nowhere near
// half the window -- the check must pass cleanly against realistic
// numbers, not just an adversarially small one.
func TestCheckStartupBudget_PassesForRealisticPrefixAgainstMillionTokenWindow(t *testing.T) {
	tools := fakeBudgetToolSource{tools: []mcpclient.Tool{
		{Name: "mesh_say", Description: strings.Repeat("x", 800), InputSchema: map[string]any{"type": "object"}},
		{Name: "mesh_rooms", Description: "list rooms", InputSchema: map[string]any{"type": "object"}},
	}}
	p := fakeBudgetProvider{window: 1_000_000}

	if err := checkStartupBudget(context.Background(), "a short system prompt", tools, p, nil); err != nil {
		t.Fatalf("expected a realistic fixed prefix to pass against a 1M-token window, got: %v", err)
	}
}

// Covers the actual point of R2's hard-fail requirement: a genuinely
// small context window with a fixed prefix that would eat more than
// half of it must refuse to start, not silently run into the same class
// of crash the runaway-context incident already produced once.
func TestCheckStartupBudget_FailsWhenFixedPrefixExceedsHalfASmallWindow(t *testing.T) {
	bigDescription := strings.Repeat("x", 20_000) // ~5,000 estimated tokens
	tools := fakeBudgetToolSource{tools: []mcpclient.Tool{
		{Name: "mesh_say", Description: bigDescription, InputSchema: map[string]any{"type": "object"}},
	}}
	p := fakeBudgetProvider{window: 4_000} // half is 2,000 -- the ~5,000-token prefix must trip this

	err := checkStartupBudget(context.Background(), "system prompt", tools, p, nil)
	if err == nil {
		t.Fatalf("expected checkStartupBudget to refuse to start against an undersized window")
	}
	if !strings.Contains(err.Error(), "budget check failed") {
		t.Fatalf("expected a clear budget-check error message, got: %v", err)
	}
}

// A provider that doesn't report a real window (ContextWindow()==0) must
// never trip the hard-fail -- there is nothing to compare against, and
// silently refusing to start on missing data would be worse than
// skipping the check for that one backend.
func TestCheckStartupBudget_ZeroWindowNeverFails(t *testing.T) {
	tools := fakeBudgetToolSource{tools: []mcpclient.Tool{
		{Name: "mesh_say", Description: strings.Repeat("x", 20_000), InputSchema: map[string]any{"type": "object"}},
	}}
	p := fakeBudgetProvider{window: 0}

	if err := checkStartupBudget(context.Background(), "system prompt", tools, p, nil); err != nil {
		t.Fatalf("expected a zero ContextWindow() to never trip the hard-fail, got: %v", err)
	}
}

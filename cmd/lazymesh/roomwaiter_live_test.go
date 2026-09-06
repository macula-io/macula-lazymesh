//go:build live

// Package main live tests for macula-io/macula-lazymesh#14/#15: measures,
// against the real mesh, the actual claim this design exists to fix --
// that a long mesh_say/mesh_wait_room wait blocks human input from being
// seen at all under the old design, and that moving the wait into
// loop-owned goroutines fixes it. See plans/SPIKE_LAZYMESH_ROOM_WAITERS.md
// (the spike report, macula-io/macula-lazymesh#14) for the original
// write-up these numbers came from.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
)

// openTestRoom opens a fresh, private room via mesh_open_room and returns
// its topic -- avoids colliding with real team traffic on shared rooms.
func openTestRoom(ctx context.Context, client *mcpclient.Client, purpose string) (string, error) {
	res, err := client.CallTool(ctx, "mesh_open_room", map[string]any{"purpose": purpose})
	if err != nil {
		return "", err
	}
	var parsed struct {
		RoomTopic string `json:"room_topic"`
	}
	if err := json.Unmarshal([]byte(res), &parsed); err != nil || parsed.RoomTopic == "" {
		return "", fmt.Errorf("unexpected mesh_open_room result %q (err %v)", res, err)
	}
	return parsed.RoomTopic, nil
}

// oneShotWaitProvider deterministically issues exactly one mesh_say tool
// call with the given wait_reply_seconds on its first round, then a plain
// reply on the second -- isolates the mechanical "does Say() block" claim
// from real LLM variability.
type oneShotWaitProvider struct {
	roomTopic        string
	waitReplySeconds int
	round            int
}

func (p *oneShotWaitProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	p.round++
	if p.round == 1 {
		args, _ := json.Marshal(map[string]any{
			"room_topic":         p.roomTopic,
			"text":               "probing, nothing more to add right now",
			"wait_reply_seconds": p.waitReplySeconds,
		})
		return provider.ChatResponse{Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "mesh_say", Arguments: string(args)},
			},
		}}, nil
	}
	return provider.ChatResponse{Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: "done waiting",
	}}, nil
}

// TestLiveOldDesign_HumanInputWasInvisibleDuringLongMeshSayWait
// reproduces and measures the bug this design replaced: deliberately
// bypasses NoBlockingWaitSource (buildToolSource's raw output, not
// run()'s actual wrapped tools) so a scripted provider can still issue an
// old-style long wait_reply_seconds call for the demonstration. While
// loop.Say() is blocked inside that REAL mesh_say call (nobody replies in
// the private test room, so it holds the full duration), a message
// already sitting in userInputCh is invisible to nextPrompt -- not
// because that function is slow, but because nothing calls it again
// until Say() itself returns. Kept as a regression/documentation check,
// not something current production wiring can still do (NoBlockingWaitSource
// always clamps this now) -- see TestLiveNewDesign_HumanInputSeenImmediatelyDuringRoomWait
// below for the actual current behavior.
func TestLiveOldDesign_HumanInputWasInvisibleDuringLongMeshSayWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	room, err := openTestRoom(ctx, client, "lazymesh#15 old-design latency probe")
	if err != nil {
		t.Fatalf("openTestRoom: %v", err)
	}

	const waitReplySeconds = 8
	tools, err := buildToolSource(config.Config{}, client) // deliberately unwrapped -- see doc comment
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}
	p := &oneShotWaitProvider{roomTopic: room, waitReplySeconds: waitReplySeconds}
	loop := agent.NewLoop(p, tools, "test")
	events := make(chan agent.Event, 16)
	go func() {
		for range events {
		}
	}()

	userInputCh := make(chan string, 1)
	sayDone := make(chan error, 1)
	sayStart := time.Now()
	go func() { sayDone <- loop.Say(ctx, "start", events) }()

	// Give Say() time to actually enter the real mesh_say call before
	// pushing the "human" message -- otherwise this would just measure
	// scheduling jitter, not the blocking claim.
	time.Sleep(1 * time.Second)
	pushedAt := time.Now()
	userInputCh <- "hello from a human, are you there?"

	if err := <-sayDone; err != nil {
		t.Fatalf("loop.Say: %v", err)
	}
	sayElapsed := time.Since(sayStart)

	seen := nextPrompt(userInputCh, "should not be used")
	invisibleFor := time.Since(pushedAt)

	if seen != "hello from a human, are you there?" {
		t.Fatalf("expected the human message to surface once Say() returned, got %q", seen)
	}
	t.Logf("mesh_say wait_reply_seconds=%d; loop.Say total elapsed=%v; human input was invisible for %v after being pushed", waitReplySeconds, sayElapsed, invisibleFor)
	if invisibleFor < 5*time.Second {
		t.Errorf("expected the human message to be invisible for close to the remaining wait (~%ds), got only %v -- old-design reproduction not reproduced", waitReplySeconds-1, invisibleFor)
	}
}

// TestLiveNewDesign_HumanInputSeenImmediatelyDuringRoomWait is the actual
// current behavior: a roomwaiter.Manager (exactly what run() constructs)
// is deep inside a REAL, long mesh_wait_room call (3600s) on a different
// room the whole time -- and a message pushed to userInputCh is still
// picked up by nextEvent essentially immediately, because human input is
// its own select case, never nested inside the room wait.
func TestLiveNewDesign_HumanInputSeenImmediatelyDuringRoomWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	room, err := openTestRoom(ctx, client, "lazymesh#15 new-design latency probe")
	if err != nil {
		t.Fatalf("openTestRoom: %v", err)
	}

	mgr := roomwaiter.New(client, "")
	mgr.Sync(ctx, []string{room})
	defer mgr.StopAll()
	time.Sleep(1 * time.Second) // let the waiter goroutine actually enter mesh_wait_room

	userInputCh := make(chan string, 1)
	userInputCh <- "hello from a human, are you there?"

	pushedAt := time.Now()
	got, ok := nextEvent(ctx, userInputCh, mgr, ringCheckInterval)
	elapsed := time.Since(pushedAt)

	if !ok || got != "hello from a human, are you there?" {
		t.Fatalf("expected the human message back immediately, got (%q, %v)", got, ok)
	}
	t.Logf("human input seen after %v, while a real mesh_wait_room(wait_seconds=3600) call was concurrently outstanding on a different room", elapsed)
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected near-instant pickup (human input is checked non-blockingly first), got %v", elapsed)
	}
}

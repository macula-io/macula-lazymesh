//go:build live

// Package main live tests for macula-io/macula-lazymesh#14 (the
// concurrency spike): measures, against the real mesh, the actual claim
// the spike exists to check -- that a long mesh_say/mesh_wait_room wait
// blocks human input from being seen at all today, and that moving the
// wait into loop-owned goroutines fixes it. See plans/
// SPIKE_ROOM_WAITERS_REPORT.md for the full write-up these feed.
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
// from real LLM variability (whether a live model reaches for a long
// wait on its own is a separate, also-measured question, not this one).
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

// TestLiveBaseline_HumanInputInvisibleDuringLongMeshSayWait demonstrates
// and measures today's actual bug: while loop.Say() is blocked inside a
// REAL mesh_say wait_reply_seconds call (nobody replies in the private
// test room, so it holds the full duration), a message already sitting
// in userInputCh is invisible to nextPrompt/nextEvent -- not because
// those functions are slow, but because nothing calls them again until
// Say() itself returns. Elapsed-since-push at that point IS the measured
// latency a human would actually experience today.
func TestLiveBaseline_HumanInputInvisibleDuringLongMeshSayWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	room, err := openTestRoom(ctx, client, "lazymesh#14 baseline latency probe")
	if err != nil {
		t.Fatalf("openTestRoom: %v", err)
	}

	const waitReplySeconds = 8
	tools, err := buildToolSource(config.Config{}, client) // baseline: no NoBlockingWaitSource
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

	// This IS the production code path: nextPrompt only runs once Say()
	// has already returned -- so its own call here measures the moment
	// the human message actually becomes visible, not before.
	seen := nextPrompt(userInputCh, "should not be used")
	invisibleFor := time.Since(pushedAt)

	if seen != "hello from a human, are you there?" {
		t.Fatalf("expected the human message to surface once Say() returned, got %q", seen)
	}
	t.Logf("mesh_say wait_reply_seconds=%d; loop.Say total elapsed=%v; human input was invisible for %v after being pushed", waitReplySeconds, sayElapsed, invisibleFor)
	if invisibleFor < 5*time.Second {
		t.Errorf("expected the human message to be invisible for close to the remaining wait (~%ds), got only %v -- baseline claim not reproduced", waitReplySeconds-1, invisibleFor)
	}
}

// TestLiveSpike_HumanInputSeenImmediatelyDuringRoomWait is the fix side of
// the same measurement: with the #14 design, a room-waiter goroutine is
// deep inside a REAL, long mesh_wait_room call (3600s) on a different
// room the whole time -- and a message pushed to userInputCh is still
// picked up by nextEvent essentially immediately, because human input is
// its own select case, never nested inside the room wait.
func TestLiveSpike_HumanInputSeenImmediatelyDuringRoomWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	room, err := openTestRoom(ctx, client, "lazymesh#14 spike latency probe")
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
	got, ok := nextEvent(ctx, userInputCh, mgr)
	elapsed := time.Since(pushedAt)

	if !ok || got != "hello from a human, are you there?" {
		t.Fatalf("expected the human message back immediately, got (%q, %v)", got, ok)
	}
	t.Logf("human input seen after %v, while a real mesh_wait_room(wait_seconds=3600) call was concurrently outstanding on a different room", elapsed)
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected near-instant pickup (human input is checked non-blockingly first), got %v", elapsed)
	}
}

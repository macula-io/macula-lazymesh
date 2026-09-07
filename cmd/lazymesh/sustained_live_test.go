//go:build live

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
)

// TestLiveSustainedRun_MultiCycleRealOperation is macula-io/macula-
// lazymesh#15's carried-over follow-up from the #14 spike: a real,
// sustained run (not a single hold) against the live mesh, driving the
// same primitives cmd/lazymesh's own run()/runAgent use (real provider,
// real roomwaiter.Manager, real nextEvent), with continuous chatter
// across 3 rooms plus interleaved human messages, to get real numbers
// for: does deepseek-v4-flash behave correctly across many real cycles
// under the new prompt, and how fast does trimHistory's 200-message
// window actually rotate under real event load (previously only
// estimated by arithmetic, never measured).
//
// Drives the loop inline rather than calling runAgent directly: runAgent
// has no introspection hook for Loop.MessageCount()/Usage(), and adding
// one just for this one-time measurement would be its own scope creep --
// this uses the exact same agent.NewLoop/nextEvent/roomwaiter.Manager
// runAgent does, just with the cycle loop unrolled here so the test can
// read real numbers after each round.
func TestLiveSustainedRun_MultiCycleRealOperation(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home directory: %v", err)
	}
	keyPath := home + "/.ai-api-keys/.deepseek-api-keys/lazymesh"

	const runDuration = 3 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), runDuration+90*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (agent): %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	publisher, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (publisher): %v", err)
	}
	defer publisher.Close()
	defer func() { _, _ = publisher.CallTool(context.Background(), "mesh_goodbye", nil) }()

	const nRooms = 3
	rooms := make([]string, 0, nRooms)
	for i := 0; i < nRooms; i++ {
		room, err := openTestRoom(ctx, publisher, fmt.Sprintf("lazymesh#15 sustained-run room %d/%d", i+1, nRooms))
		if err != nil {
			t.Fatalf("openTestRoom %d: %v", i, err)
		}
		if _, err := client.CallTool(ctx, "mesh_join_room", map[string]any{"room_topic": room}); err != nil {
			t.Fatalf("agent join room %d: %v", i, err)
		}
		rooms = append(rooms, room)
	}

	cfg := config.Config{
		Provider:   "deepseek",
		Model:      "",
		APIKeyFile: keyPath,
	}
	p, err := buildProvider(cfg)
	if err != nil {
		t.Skipf("buildProvider (likely no deepseek key at %s): %v", keyPath, err)
	}
	tools, err := buildToolSource(cfg, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}
	tools = agent.NewNoBlockingWaitSource(tools)

	waiterMgr := roomwaiter.New(client, "")
	defer waiterMgr.StopAll()
	waiterMgr.Sync(ctx, rooms)

	ringMgr := ringwaiter.New(client, "")
	defer ringMgr.Stop()
	ringMgr.Start(ctx)

	systemPrompt := buildSystemPrompt("", "", false, false)
	loop := agent.NewLoop(p, tools, systemPrompt)
	events := make(chan agent.Event, 256)
	discardLog := log.New(discardWriter{}, "", 0)
	go func() {
		for ev := range events {
			logEvent(discardLog, ev)
		}
	}()
	defer close(events)

	userInputCh := make(chan string, 8)

	// Publisher: round-robins a message into each room every 4s -- fast
	// enough to keep the agent genuinely busy across the run, not mostly
	// idle-waiting (which would just re-measure the latency tests above).
	stopPublisher := make(chan struct{})
	go func() {
		i := 0
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPublisher:
				return
			case <-ticker.C:
				room := rooms[i%len(rooms)]
				i++
				_, _ = publisher.CallTool(ctx, "mesh_say", map[string]any{
					"room_topic": room,
					"text":       fmt.Sprintf("sustained-run chatter #%d", i),
				})
			}
		}
	}()
	defer close(stopPublisher)

	// Two human messages at different points in the run, same as a real
	// operator typing into the TUI mid-session.
	go func() {
		select {
		case <-time.After(30 * time.Second):
			select {
			case userInputCh <- "human check-in #1, are things working?":
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	go func() {
		select {
		case <-time.After(90 * time.Second):
			select {
			case userInputCh <- "human check-in #2, still there?":
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()

	deadline := time.Now().Add(runDuration)
	prompt := agentInitialPrompt
	cycles := 0
	var maxMessageCount int
	for time.Now().Before(deadline) {
		if err := loop.Say(ctx, prompt, events); err != nil {
			t.Fatalf("loop.Say failed on cycle %d (deepseek-v4-flash may not be behaving correctly under the new prompt): %v", cycles, err)
		}
		cycles++
		if mc := loop.MessageCount(); mc > maxMessageCount {
			maxMessageCount = mc
		}
		events <- agent.Event{Kind: agent.EventListening}
		var ok bool
		prompt, ok = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
		if !ok {
			break
		}
	}

	usage := loop.Usage()
	t.Logf("sustained run: %d cycles over %v, max MessageCount=%d (cap %d), token usage=%+v",
		cycles, runDuration, maxMessageCount, 200, usage)

	if cycles == 0 {
		t.Fatalf("expected at least one real cycle to complete")
	}
	if usage.TotalTokens == 0 {
		t.Errorf("expected non-zero accumulated token usage across %d real cycles", cycles)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

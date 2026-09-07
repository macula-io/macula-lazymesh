//go:build live

package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveR2ToolSelectionStillWorksWithTerseSchemas is R2's explicit acceptance
// requirement (2026-09-07, coordinator): actually exercise the agent
// against the terse tool descriptions with a real model, confirming
// tool-selection doesn't regress -- not just "it compiles and reads
// fine." Gives the real, terse-ified tool source to a real DeepSeek
// call and checks the model still picks mesh_join_room then mesh_say
// with valid arguments for an ordinary request, exactly the everyday
// path TerseDescriptionSource's trims most need to not have broken.
func TestLiveR2ToolSelectionStillWorksWithTerseSchemas(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home directory: %v", err)
	}
	keyPath := home + "/.ai-api-keys/.deepseek-api-keys/lazymesh"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Two separate clients, same as sustained_live_test.go's own pattern:
	// the room is opened by a DIFFERENT identity than the agent's own, so
	// the agent genuinely starts as an outsider who needs mesh_join_room
	// -- opening it with the agent's own client would auto-join it,
	// which is a real, different (and also correct) code path, not the
	// one this test means to exercise.
	publisher, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (publisher): %v", err)
	}
	defer publisher.Close()
	defer func() { _, _ = publisher.CallTool(context.Background(), "mesh_goodbye", nil) }()

	room, err := openTestRoom(ctx, publisher, "lazymesh R2 tool-selection verification")
	if err != nil {
		t.Fatalf("openTestRoom: %v", err)
	}

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	cfg := config.Config{Provider: "deepseek", APIKeyFile: keyPath}
	p, err := buildProvider(cfg)
	if err != nil {
		t.Skipf("buildProvider (likely no deepseek key at %s): %v", keyPath, err)
	}
	tools, err := buildToolSource(cfg, client, nil) // real terse-ified tools, exactly what run() builds
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}
	tools = agent.NewNoBlockingWaitSource(tools)

	loop := agent.NewLoop(p, tools, buildSystemPrompt(room, "", false, false, false))
	events := make(chan agent.Event, 64)

	var toolCalls []agent.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Kind == agent.EventToolCall {
				toolCalls = append(toolCalls, ev)
			}
		}
	}()

	err = loop.Say(ctx, "Join the room you were pointed at and say hello, introducing yourself as a test agent verifying tool descriptions still work.", events)
	close(events)
	<-done
	if err != nil {
		t.Fatalf("loop.Say: %v", err)
	}

	if len(toolCalls) == 0 {
		t.Fatalf("expected at least one real tool call, got none")
	}

	sawJoin, sawSay := false, false
	for _, ev := range toolCalls {
		t.Logf("tool call: %s(%s)", ev.ToolName, ev.Text)
		switch ev.ToolName {
		case "mesh_join_room":
			sawJoin = true
			var args struct {
				RoomTopic string `json:"room_topic"`
			}
			if err := json.Unmarshal([]byte(ev.Text), &args); err != nil {
				t.Fatalf("mesh_join_room args not valid JSON: %v (%s)", err, ev.Text)
			}
			if args.RoomTopic != room {
				t.Fatalf("expected mesh_join_room to target %s, got %q", room, args.RoomTopic)
			}
		case "mesh_say":
			sawSay = true
			var args struct {
				RoomTopic string `json:"room_topic"`
				Text      string `json:"text"`
			}
			if err := json.Unmarshal([]byte(ev.Text), &args); err != nil {
				t.Fatalf("mesh_say args not valid JSON: %v (%s)", err, ev.Text)
			}
			if args.RoomTopic == "" || args.Text == "" {
				t.Fatalf("expected non-empty room_topic and text, got %+v", args)
			}
			if args.RoomTopic != room {
				t.Fatalf("expected mesh_say to target %s, got %q", room, args.RoomTopic)
			}
		}
	}
	if !sawJoin {
		t.Errorf("expected the model to call mesh_join_room for an explicit join instruction, got calls: %+v", toolCalls)
	}
	if !sawSay {
		t.Errorf("expected the model to call mesh_say for an explicit say instruction, got calls: %+v", toolCalls)
	}
}

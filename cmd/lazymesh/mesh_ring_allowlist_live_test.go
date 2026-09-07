//go:build live

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveMeshRingReachableThroughDefaultAllowlistWithTerseSchema is the
// real gap Raf hit live (2026-09-08): mesh_answer_ring was on
// DefaultToolAllowlist but mesh_ring itself never was, so the agent could
// react to a ring but never initiate one. Same acceptance bar as R2's own
// tool-selection test (actually exercise it with a real model against
// the terse-ified schema, not just "it compiles and reads fine"): rings a
// syntactically valid but definitely-unserved node id and confirms the
// model correctly calls mesh_ring with valid to/purpose arguments, and
// that the real (terse-schema-shaped) call round-trips cleanly -- a
// structured "unreachable" result is the correct, expected outcome here
// (see mesh_ring.ts's own doc comment: unreachable is a RESULT, not an
// error), not a test failure.
func TestLiveMeshRingReachableThroughDefaultAllowlistWithTerseSchema(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home directory: %v", err)
	}
	keyPath := home + "/.ai-api-keys/.deepseek-api-keys/lazymesh"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

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
	tools, err := buildToolSource(cfg, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}
	tools = agent.NewNoBlockingWaitSource(tools)

	// 64 hex zeros: syntactically a valid node_id (passes resolveNodeId's
	// own shape check) but definitely not being served by anything real
	// on the mesh -- exercises the genuine "unreachable" path rather than
	// depending on some other live agent's own presence/contact policy at
	// test time.
	fakeNodeID := strings.Repeat("0", 64)

	loop := agent.NewLoop(p, tools, buildSystemPrompt("", "", false, false, false))
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

	err = loop.Say(ctx, "Ring the agent with node id "+fakeNodeID+" using mesh_ring, with purpose \"verifying mesh_ring is reachable\". Report back what happened, including if they were unreachable.", events)
	close(events)
	<-done
	if err != nil {
		t.Fatalf("loop.Say: %v", err)
	}

	if len(toolCalls) == 0 {
		t.Fatalf("expected at least one real tool call, got none")
	}

	sawRing := false
	for _, ev := range toolCalls {
		t.Logf("tool call: %s(%s)", ev.ToolName, ev.Text)
		if ev.ToolName != "mesh_ring" {
			continue
		}
		sawRing = true
		var args struct {
			To      string `json:"to"`
			Purpose string `json:"purpose"`
		}
		if err := json.Unmarshal([]byte(ev.Text), &args); err != nil {
			t.Fatalf("mesh_ring args not valid JSON: %v (%s)", err, ev.Text)
		}
		if args.To != fakeNodeID {
			t.Fatalf("expected mesh_ring to target %s, got %q", fakeNodeID, args.To)
		}
		if args.Purpose == "" {
			t.Fatalf("expected a non-empty purpose, got %+v", args)
		}
	}
	if !sawRing {
		t.Fatalf("expected the model to call mesh_ring for an explicit ring instruction, got calls: %+v", toolCalls)
	}
}

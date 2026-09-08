//go:build live

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
)

// TestLiveRingwaiterSurfacesARealRingViaMeshWaitRing is the real
// end-to-end proof for R1's 2026-09-08 swap (poll -> mesh_wait_ring) and
// the macula-mcp 0.26.1 fix it depends on: two real macula-mcp
// processes, a real ring placed from one to the other, and
// ringwaiter.Manager -- wired exactly the way main.go wires it, talking
// to the callee's own client -- actually surfacing it on Arrivals().
// Before 0.26.1 this would have hung until the test's own timeout: the
// callee-side row never existed at all (see internal/config's
// MaculaMCPVersion doc comment for the full incident), so neither the
// old poll nor mesh_wait_ring had anything real to find.
func TestLiveRingwaiterSurfacesARealRingViaMeshWaitRing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	caller, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (caller): %v", err)
	}
	defer caller.Close()
	defer func() { _, _ = caller.CallTool(context.Background(), "mesh_goodbye", nil) }()

	callee, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn (callee): %v", err)
	}
	defer callee.Close()
	defer func() { _, _ = callee.CallTool(context.Background(), "mesh_goodbye", nil) }()

	helloRes, err := callee.CallTool(ctx, "mesh_hello", map[string]any{})
	if err != nil {
		t.Fatalf("callee mesh_hello: %v", err)
	}
	var hello struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(helloRes), &hello); err != nil || hello.NodeID == "" {
		t.Fatalf("unexpected mesh_hello result %q (err %v)", helloRes, err)
	}

	ringMgr := ringwaiter.New(callee, "")
	defer ringMgr.Stop()
	ringMgr.Start(ctx)

	ringRes, err := caller.CallTool(ctx, "mesh_ring", map[string]any{
		"to":      hello.NodeID,
		"purpose": "live regression: ringwaiter must surface this via mesh_wait_ring",
	})
	if err != nil {
		t.Fatalf("mesh_ring: %v", err)
	}
	var placed struct {
		RingID      string `json:"ring_id"`
		AnswerLabel string `json:"answer_label"`
	}
	if err := json.Unmarshal([]byte(ringRes), &placed); err != nil {
		t.Fatalf("unmarshal mesh_ring result: %v (%s)", err, ringRes)
	}
	if placed.AnswerLabel != "deferred" {
		t.Fatalf("expected the ring to be deferred (default ask policy), got %q: %s", placed.AnswerLabel, ringRes)
	}

	select {
	case r := <-ringMgr.Arrivals():
		if r.RingID != placed.RingID {
			t.Fatalf("expected arrival for ring_id %s, got %+v", placed.RingID, r)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("expected ringwaiter to surface the real ring %s via mesh_wait_ring within 30s -- got nothing", placed.RingID)
	}

	// Confirm the callee's own side genuinely knows about this ring now
	// too (mesh_answer_ring succeeding, not "no incoming ring") -- the
	// exact symptom of the pre-0.26.1 bug this whole test guards against.
	if _, err := callee.CallTool(ctx, "mesh_answer_ring", map[string]any{"ring_id": placed.RingID, "answer": 2, "reason": "live test cleanup"}); err != nil {
		t.Fatalf("mesh_answer_ring on the callee's own side failed -- this is exactly the pre-0.26.1 symptom: %v", err)
	}
}

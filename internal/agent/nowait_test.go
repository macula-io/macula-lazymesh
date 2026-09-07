package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// lastCallArgs extracts the argumentsJSON portion of fakeToolSource's most
// recent recorded "name(args)" call string.
func lastCallArgs(t *testing.T, f *fakeToolSource) string {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatalf("expected at least one recorded call")
	}
	call := f.calls[len(f.calls)-1]
	i := strings.Index(call, "(")
	if i < 0 || !strings.HasSuffix(call, ")") {
		t.Fatalf("unexpected call format: %q", call)
	}
	return call[i+1 : len(call)-1]
}

func TestNoBlockingWaitSource_ClampsLongWaitReplySeconds(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"agents.room.x","text":"hi","wait_reply_seconds":3600}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(lastCallArgs(t, inner)), &args); err != nil {
		t.Fatalf("decode forwarded args: %v", err)
	}
	if got := args["wait_reply_seconds"]; got != float64(maxModelWaitSeconds) {
		t.Fatalf("expected wait_reply_seconds clamped to %d, got %v", maxModelWaitSeconds, got)
	}
	if args["text"] != "hi" {
		t.Fatalf("expected other arguments preserved, got: %+v", args)
	}
}

func TestNoBlockingWaitSource_LeavesShortWaitReplySecondsAlone(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_say", `{"text":"hi","wait_reply_seconds":5}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(lastCallArgs(t, inner)), &args)
	if args["wait_reply_seconds"] != float64(5) {
		t.Fatalf("expected a short wait_reply_seconds left unchanged, got %v", args["wait_reply_seconds"])
	}
}

func TestNoBlockingWaitSource_LeavesOtherToolsAlone(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	if _, err := src.CallToolRaw(context.Background(), "mesh_read_inbox", `{}`); err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	if got := inner.calls[0]; got != "mesh_read_inbox({})" {
		t.Fatalf("expected mesh_read_inbox forwarded unchanged, got %q", got)
	}
}

func TestNoBlockingWaitSource_NoWaitReplySecondsIsUnaffected(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	if _, err := src.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"agents.room.x","text":"hi"}`); err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	if got := lastCallArgs(t, inner); got != `{"room_topic":"agents.room.x","text":"hi"}` {
		t.Fatalf("expected arguments passed through unchanged when wait_reply_seconds is absent, got: %s", got)
	}
}

func TestNoBlockingWaitSource_ClampsLongWaitJoinSecondsOnMeshRing(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_ring", `{"to":"deadbeef","purpose":"say hi","wait_join_seconds":600}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(lastCallArgs(t, inner)), &args); err != nil {
		t.Fatalf("decode forwarded args: %v", err)
	}
	if got := args["wait_join_seconds"]; got != float64(maxModelWaitSeconds) {
		t.Fatalf("expected wait_join_seconds clamped to %d, got %v", maxModelWaitSeconds, got)
	}
	if args["purpose"] != "say hi" {
		t.Fatalf("expected other arguments preserved, got: %+v", args)
	}
}

func TestNoBlockingWaitSource_LeavesShortWaitJoinSecondsOnMeshRingAlone(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_ring", `{"to":"deadbeef","purpose":"say hi","wait_join_seconds":5}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(lastCallArgs(t, inner)), &args)
	if args["wait_join_seconds"] != float64(5) {
		t.Fatalf("expected a short wait_join_seconds left unchanged, got %v", args["wait_join_seconds"])
	}
}

func TestNoBlockingWaitSource_ExplicitZeroWaitJoinSecondsOnMeshRingIsPreserved(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_ring", `{"to":"deadbeef","purpose":"say hi","wait_join_seconds":0}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(lastCallArgs(t, inner)), &args)
	if got, ok := args["wait_join_seconds"]; !ok || got != float64(0) {
		t.Fatalf("expected an explicit wait_join_seconds:0 (mesh_ring's own \"don't wait\" value) preserved, got %v", args["wait_join_seconds"])
	}
}

// This is the asymmetry-specific regression: mesh_ring, unlike mesh_say,
// applies its OWN server-side default (30s, well past
// maxModelWaitSeconds) when wait_join_seconds is omitted entirely -- see
// this file's own doc comment and mesh_ring.ts's placeRing. A model that
// never mentions the field at all must still get a short wait, not the
// server's own default slipping through this wrapper untouched.
func TestNoBlockingWaitSource_AbsentWaitJoinSecondsOnMeshRingGetsForcedToCap(t *testing.T) {
	inner := &fakeToolSource{}
	src := NewNoBlockingWaitSource(inner)

	_, err := src.CallToolRaw(context.Background(), "mesh_ring", `{"to":"deadbeef","purpose":"say hi"}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(lastCallArgs(t, inner)), &args); err != nil {
		t.Fatalf("decode forwarded args: %v", err)
	}
	if got := args["wait_join_seconds"]; got != float64(maxModelWaitSeconds) {
		t.Fatalf("expected an absent wait_join_seconds forced to %d rather than left for mesh_ring's own 30s server default, got %v", maxModelWaitSeconds, got)
	}
}

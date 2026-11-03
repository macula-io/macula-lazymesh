package tui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
)

// blockingWaitCaller answers mesh_wait_room by waiting for the room's
// context to end, as a quiet room does.
type blockingWaitCaller struct{}

func (blockingWaitCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

const acceptedRingResult = `{"ring_id":"r1","answer":1,"peer":"peer1","room_topic":"agents.room.accepted","caller_notified":1}`

// newWatchingModel is a popup test model whose membership door feeds a
// real room-waiter, the way run() wires it.
func newWatchingModel(t *testing.T, mcp toolCaller) (Model, *roomwaiter.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	waiter := roomwaiter.New(blockingWaitCaller{}, "")
	t.Cleanup(func() { waiter.StopAll(); cancel() })
	m := New(mcp, Options{StatusBarPosition: "bottom", RoomChanged: func(ev agent.Event) { waiter.Apply(ctx, ev) }})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m, waiter
}

// #97: accepting a ring in the pop-up makes the waiter watch that room,
// so the caller's next message wakes the agent.
func TestRingPopupAccept_WaiterWatchesTheRoom(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": acceptedRingResult}}
	m, waiter := newWatchingModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1", Purpose: "hi"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup
	m = armPopups(m)

	_, cmd := m.handleRingPopupKey(runeKey('a'))
	if cmd == nil {
		t.Fatalf("answering the ring returned no command")
	}
	cmd()
	if !waiter.Watches("agents.room.accepted") {
		t.Fatalf("the room of a ring accepted in the pop-up is not watched")
	}
}

// A declined ring is no membership change, though the result names its room.
func TestRingPopupDecline_WaiterWatchesNothing(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": `{"ring_id":"r1","answer":2,"peer":"peer1","room_topic":"agents.room.declined","caller_notified":1}`}}
	m, waiter := newWatchingModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1", Purpose: "hi"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup
	m = armPopups(m)

	_, cmd := m.handleRingPopupKey(runeKey('d'))
	cmd()
	if waiter.Watches("agents.room.declined") {
		t.Fatalf("a declined ring's room is watched")
	}
}

// Answer + Trust joins the room even when the trust call fails: the
// answer already made this node a member.
func TestRingPopupAcceptAndTrust_TrustFailureStillWatchesTheRoom(t *testing.T) {
	fake := &fakeToolCaller{
		responses: map[string]string{"mesh_answer_ring": acceptedRingResult},
		errs:      map[string]error{"mesh_trust_agent": context.DeadlineExceeded},
	}
	m, waiter := newWatchingModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1", Purpose: "hi"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup
	m = armPopups(m)

	_, cmd := m.handleRingPopupKey(runeKey('t'))
	if msg := cmd().(ringAnsweredMsg); msg.err == nil {
		t.Fatalf("expected the trust failure to be reported")
	}
	if !waiter.Watches("agents.room.accepted") {
		t.Fatalf("the room of an accepted ring is not watched after a trust failure")
	}
}

// A ring auto-accepted from a trusted peer is watched the same way.
func TestRingAutoAccept_WaiterWatchesTheRoom(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "contact_policy.json")
	writeContactPolicyFile(t, policyPath, "ask", []string{"trustedpeer"})
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": acceptedRingResult}}
	m, waiter := newWatchingModel(t, fake)
	m.contactPolicyFile = policyPath
	m.autoAcceptKnown = true
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "trustedpeer", Purpose: "hi"}}

	cmds := m.processPendingRings()
	if len(cmds) != 1 {
		t.Fatalf("expected one auto-accept command, got %d", len(cmds))
	}
	cmds[0]()
	if !waiter.Watches("agents.room.accepted") {
		t.Fatalf("the room of an auto-accepted ring is not watched")
	}
}

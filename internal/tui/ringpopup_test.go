package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAnswerRingCmd_CallsMeshAnswerRingWithCorrectArgs(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	cmd := answerRingCmd(fake, "ring-123", answerAccept, false, "")
	msg := cmd()

	answered, ok := msg.(ringAnsweredMsg)
	if !ok {
		t.Fatalf("expected ringAnsweredMsg, got %T", msg)
	}
	if answered.err != nil {
		t.Fatalf("expected no error, got %v", answered.err)
	}
	if !answered.accepted {
		t.Fatalf("expected accepted=true")
	}
	if len(fake.calls) != 1 || fake.calls[0] != "mesh_answer_ring" {
		t.Fatalf("expected exactly one mesh_answer_ring call, got %v", fake.calls)
	}
}

func TestAnswerRingCmd_DeclineDoesNotCallTrust(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	cmd := answerRingCmd(fake, "ring-123", answerDecline, false, "")
	msg := cmd().(ringAnsweredMsg)
	if msg.accepted {
		t.Fatalf("expected accepted=false for a decline")
	}
	for _, c := range fake.calls {
		if c == "mesh_trust_agent" {
			t.Fatalf("expected no mesh_trust_agent call on decline")
		}
	}
}

func TestAnswerRingCmd_AlsoTrustCallsTrustAgentAfterSuccessfulAccept(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{
		"mesh_answer_ring": "{}",
		"mesh_trust_agent": "{}",
	}}
	cmd := answerRingCmd(fake, "ring-123", answerAccept, true, "deadbeef")
	msg := cmd().(ringAnsweredMsg)
	if msg.err != nil {
		t.Fatalf("expected no error, got %v", msg.err)
	}
	if !msg.alsoTrust {
		t.Fatalf("expected alsoTrust=true")
	}
	if len(fake.calls) != 2 || fake.calls[1] != "mesh_trust_agent" {
		t.Fatalf("expected mesh_answer_ring then mesh_trust_agent, got %v", fake.calls)
	}
}

func TestAnswerRingCmd_AlsoTrustSkipsTrustCallWhenAnswerFails(t *testing.T) {
	fake := &fakeToolCaller{errs: map[string]error{"mesh_answer_ring": fmt.Errorf("boom")}}
	cmd := answerRingCmd(fake, "ring-123", answerAccept, true, "deadbeef")
	msg := cmd().(ringAnsweredMsg)
	if msg.err == nil {
		t.Fatalf("expected an error to propagate")
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected mesh_trust_agent NOT to be called when the answer itself failed, got %v", fake.calls)
	}
}

func newPopupTestModel(t *testing.T, mcp toolCaller) Model {
	t.Helper()
	m := New(mcp, Options{StatusBarPosition: "bottom"})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

func TestHandleRingPopupKey_AnswerAcceptsAndClearsPopup(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m := newPopupTestModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1", Purpose: "hi"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	updated, cmd := m.handleRingPopupKey(runeKey('a'))
	if updated.mode != ModeNormal {
		t.Fatalf("expected normal mode after answering, got %v", updated.mode)
	}
	if updated.pendingRingPopup != nil {
		t.Fatalf("expected popup to be cleared")
	}
	if !updated.seenRingIDs["r1"] {
		t.Fatalf("expected ring to be marked seen")
	}
	if cmd == nil {
		t.Fatalf("expected a command to answer the ring")
	}
	msg := cmd().(ringAnsweredMsg)
	if !msg.accepted {
		t.Fatalf("expected an accept")
	}
}

func TestHandleRingPopupKey_DeclineClearsPopup(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m := newPopupTestModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	updated, cmd := m.handleRingPopupKey(runeKey('d'))
	if updated.pendingRingPopup != nil || updated.mode != ModeNormal {
		t.Fatalf("expected popup cleared and normal mode")
	}
	msg := cmd().(ringAnsweredMsg)
	if msg.accepted {
		t.Fatalf("expected a decline, not an accept")
	}
}

func TestHandleRingPopupKey_TrustAcceptsAndTrusts(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{
		"mesh_answer_ring": "{}",
		"mesh_trust_agent": "{}",
	}}
	m := newPopupTestModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	updated, cmd := m.handleRingPopupKey(runeKey('t'))
	if updated.pendingRingPopup != nil {
		t.Fatalf("expected popup cleared")
	}
	msg := cmd().(ringAnsweredMsg)
	if !msg.accepted || !msg.alsoTrust {
		t.Fatalf("expected accept+trust, got %+v", msg)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected both mesh_answer_ring and mesh_trust_agent to be called, got %v", fake.calls)
	}
}

func TestHandleRingPopupKey_EscDismissesWithoutAnswering(t *testing.T) {
	fake := &fakeToolCaller{}
	m := newPopupTestModel(t, fake)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	updated, cmd := m.handleRingPopupKey(typeKey(tea.KeyEsc))
	if updated.pendingRingPopup != nil || updated.mode != ModeNormal {
		t.Fatalf("expected popup cleared and normal mode after Esc")
	}
	if !updated.seenRingIDs["r1"] {
		t.Fatalf("expected the ring to be marked seen even though it wasn't answered")
	}
	if cmd != nil {
		t.Fatalf("expected no mesh call on Esc, got a non-nil command")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected no mesh calls at all on Esc, got %v", fake.calls)
	}
}

func TestProcessPendingRings_AutoAcceptsTrustedPeerNoPopup(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "contact_policy.json")
	writeContactPolicyFile(t, policyPath, "ask", []string{"trustedpeer"})

	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m := newPopupTestModel(t, fake)
	m.contactPolicyFile = policyPath
	m.autoAcceptKnown = true
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "trustedpeer", Purpose: "hi"}}

	cmds := m.processPendingRings()
	if m.pendingRingPopup != nil {
		t.Fatalf("expected no pop-up for a trusted peer, got %+v", m.pendingRingPopup)
	}
	if !m.seenRingIDs["r1"] {
		t.Fatalf("expected the ring to be marked seen")
	}
	if len(cmds) != 1 {
		t.Fatalf("expected exactly one auto-accept command, got %d", len(cmds))
	}
	msg := cmds[0]().(ringAnsweredMsg)
	if !msg.accepted {
		t.Fatalf("expected the auto-accept to actually accept")
	}
}

func TestProcessPendingRings_ShowsPopupForUntrustedPeer(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "contact_policy.json")
	writeContactPolicyFile(t, policyPath, "ask", nil)

	fake := &fakeToolCaller{}
	m := newPopupTestModel(t, fake)
	m.contactPolicyFile = policyPath
	m.autoAcceptKnown = true
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "stranger", Purpose: "hi"}}

	cmds := m.processPendingRings()
	if m.pendingRingPopup == nil || m.pendingRingPopup.RingID != "r1" {
		t.Fatalf("expected a pop-up for the untrusted peer, got %+v", m.pendingRingPopup)
	}
	if m.mode != ModeRingPopup {
		t.Fatalf("expected mode to switch to ModeRingPopup")
	}
	if len(cmds) != 0 {
		t.Fatalf("expected no auto-accept command for an untrusted peer, got %d", len(cmds))
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected no mesh calls before the operator answers, got %v", fake.calls)
	}
}

func TestProcessPendingRings_NeverRevisitsASeenRing(t *testing.T) {
	fake := &fakeToolCaller{}
	m := newPopupTestModel(t, fake)
	m.seenRingIDs["r1"] = true
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "stranger"}}

	cmds := m.processPendingRings()
	if m.pendingRingPopup != nil {
		t.Fatalf("expected no pop-up for an already-seen ring")
	}
	if len(cmds) != 0 {
		t.Fatalf("expected no commands for an already-seen ring")
	}
}

func TestProcessPendingRings_OnlyOnePopupAtATimeAcrossMultipleNewRings(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "contact_policy.json")
	writeContactPolicyFile(t, policyPath, "ask", nil)

	fake := &fakeToolCaller{}
	m := newPopupTestModel(t, fake)
	m.contactPolicyFile = policyPath
	m.autoAcceptKnown = true
	m.state.pending = []pendingRing{
		{RingID: "r1", Peer: "stranger1"},
		{RingID: "r2", Peer: "stranger2"},
	}

	m.processPendingRings()
	if m.pendingRingPopup == nil || m.pendingRingPopup.RingID != "r1" {
		t.Fatalf("expected the first ring to get the pop-up, got %+v", m.pendingRingPopup)
	}
	// r2 must still be visible in state.pending (untouched, not seen) so a
	// later refresh picks it up once r1 is resolved.
	if m.seenRingIDs["r2"] {
		t.Fatalf("expected r2 to remain unseen while r1's pop-up is still showing")
	}
}

func TestHandleRingAnswered_LogsSuccessAndFailureDifferently(t *testing.T) {
	m := newPopupTestModel(t, &fakeToolCaller{})

	updated, _ := m.handleRingAnswered(ringAnsweredMsg{accepted: true})
	if len(updated.chatEntries) != 1 || updated.chatEntries[0].kind != chatSystem {
		t.Fatalf("expected one chatSystem entry, got %+v", updated.chatEntries)
	}

	updated2, _ := updated.handleRingAnswered(ringAnsweredMsg{err: fmt.Errorf("boom")})
	if len(updated2.chatEntries) != 2 {
		t.Fatalf("expected a second entry for the failure, got %+v", updated2.chatEntries)
	}
}

// writeContactPolicyFile writes a contact_policy.json directly, simulating
// what contactpolicy.Ensure + mesh_trust_agent's own allowlist writes would
// have produced together -- exercising IsTrusted against a real file on
// disk, not a mock of the package.
func writeContactPolicyFile(t *testing.T, path, policy string, allowlist []string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"contact_policy": policy,
		"allowlist":      allowlist,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

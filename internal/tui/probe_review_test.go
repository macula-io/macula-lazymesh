package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

func probeApproval(t *testing.T, m Model, id string) Model {
	t.Helper()
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: id, Text: `{"cmd":"` + id + `"}`,
	}))
	return updated.(Model)
}

func probeTick(m Model) Model {
	updated, _ := m.Update(tickMsg(time.Time{}))
	return updated.(Model)
}

// P1: an approval held while the person types, then answered by another
// frontend, is still shown later as a live question.
func TestProbe_HeldApprovalResolvedElsewhereStillPops(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	m = typeText(t, m, "ok so")
	m = probeApproval(t, m, "p1")
	if m.pendingApproval != nil {
		t.Fatal("setup: shown mid-word")
	}
	updated, _ := m.Update(approvalResolvedMsg{ID: "p1", By: "other"})
	m = updated.(Model)
	clock.advance(3 * time.Second)
	m = probeTick(m)
	if m.pendingApproval != nil {
		t.Errorf("BUG: approval p1 was already answered elsewhere, yet it pops as a live prompt")
	}
}

// P2: a held approval superseded by a newer one (one outstanding per
// session) is shown first, then the prompt swaps under the reader on the
// next tick.
func TestProbe_HeldQueueShowsStaleThenSwaps(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	m = typeText(t, m, "ok so")
	m = probeApproval(t, m, "p1")
	clock.advance(500 * time.Millisecond)
	m = typeText(t, m, " and")
	m = probeApproval(t, m, "p2")
	clock.advance(3 * time.Second)
	m = probeTick(m)
	first := ""
	if m.pendingApproval != nil {
		first = m.pendingApproval.id
	}
	clock.advance(2 * time.Second)
	m = probeTick(m)
	second := ""
	if m.pendingApproval != nil {
		second = m.pendingApproval.id
	}
	if first != "p2" || second != "p2" {
		t.Errorf("BUG: shown %q, then %q without any key: the stale request is shown and the prompt swaps under the reader", first, second)
	}
}

// P3: an approval that arrives while a read-only overlay shows is held with
// nothing on screen saying a tool call is waiting.
func TestProbe_HeldApprovalBehindOverlayIsInvisible(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	m.width, m.height = 120, 40
	m.resizeComponents()
	m.meshExpanded = true
	m = probeApproval(t, m, "p1")
	for i := 0; i < 30; i++ { // a minute of reading the mesh view
		clock.advance(2 * time.Second)
		m = probeTick(m)
	}
	if m.pendingApproval != nil {
		return
	}
	if !strings.Contains(strings.ToLower(m.View()), "approval") {
		t.Errorf("BUG: an approval has been waiting a minute (times out at 2) and nothing on screen says so")
	}
}

// P4 (check): ctrl+x then y while an approval shows does not approve if
// the y follows within the guard.
func TestProbe_LeaderIntoApprovalFast(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	clock.advance(5 * time.Second)
	m = probeApproval(t, m, "p1")
	if m.pendingApproval == nil {
		t.Fatal("setup: not shown when idle")
	}
	clock.advance(time.Second)
	updated, _ := m.Update(ctrlX())
	m = updated.(Model)
	clock.advance(100 * time.Millisecond)
	m = typeText(t, m, "y")
	select {
	case a := <-approvalCh:
		t.Errorf("ctrl+x y approved: %+v", a)
	default:
	}
}

package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// startAttachServer serves one control socket whose approvals the test
// decides, and returns a controller attached to it.
func startAttachServer(t *testing.T, input chan string, events chan agent.Event, approve func(id string, allow bool) sessionhost.ApprovalOutcome) *frontend.Controller {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path:      path,
		Input:     input,
		Events:    events,
		Approve:   approve,
		SessionID: "attach-test",
		Model:     "testmodel/1",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := frontend.Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestAttachBridgeCarriesComposedInput proves the pane an attached
// operator types into is wired to the session, not to a dead channel.
func TestAttachBridgeCarriesComposedInput(t *testing.T) {
	input := make(chan string, 4)
	events := make(chan agent.Event, 4)
	ctrl := startAttachServer(t, input, events, nil)
	b := newAttachBridge(ctrl)

	b.input <- "hello from the attached tab"

	select {
	case got := <-input:
		if got != "hello from the attached tab" {
			t.Fatalf("session input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composed input never reached the session")
	}
}

// TestAttachBridgeReportsWhoAnsweredAnApproval proves the human-facing
// half of #40's threat model: when an approval is answered, the attached
// TUI learns who decided it, so a popup the operator is reading cannot
// quietly become stale.
func TestAttachBridgeReportsWhoAnsweredAnApproval(t *testing.T) {
	input := make(chan string, 4)
	events := make(chan agent.Event, 8)
	ctrl := startAttachServer(t, input, events, func(id string, allow bool) sessionhost.ApprovalOutcome { return sessionhost.ApprovalAccepted })
	b := newAttachBridge(ctrl)

	events <- agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "shell_exec", Text: "{}"}

	select {
	case ev := <-ctrl.Events():
		if ev.Kind != agent.EventApprovalRequested {
			t.Fatalf("event = %+v, want the approval request", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the attached TUI never saw the approval request")
	}

	b.answer <- ApprovalAnswer{ID: "approve-1", Allow: true}

	select {
	case r := <-b.resolved:
		if r.ID != "approve-1" {
			t.Fatalf("resolution = %+v", r)
		}
		if r.By == "" {
			t.Fatal("resolution did not say which frontend answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the attached TUI was never told the approval was resolved")
	}
}

// TestAttachBridgeSurfacesARefusedAnswer is the other half of R3: when
// this attached TUI's own answer loses the race, the operator must be
// told. Dropping the accepted:0 left a human who pressed a key with
// nothing at all — indistinguishable from an answer they never sent.
func TestAttachBridgeSurfacesARefusedAnswer(t *testing.T) {
	input := make(chan string, 4)
	events := make(chan agent.Event, 8)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path:   path,
		Input:  input,
		Events: events,
		// The winner's allow is taken; a later deny loses, exactly what the
		// session reports for a second answer (#40, #121).
		Approve: func(id string, allow bool) sessionhost.ApprovalOutcome {
			if allow {
				return sessionhost.ApprovalAccepted
			}
			return sessionhost.ApprovalAlreadyAnswered
		},
		SessionID: "attach-test",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	winner, err := frontend.Dial(path)
	if err != nil {
		t.Fatalf("dial winner: %v", err)
	}
	t.Cleanup(func() { winner.Close() })
	loser, err := frontend.Dial(path)
	if err != nil {
		t.Fatalf("dial loser: %v", err)
	}
	t.Cleanup(func() { loser.Close() })

	// The winner allows; the attached TUI then tries to deny.
	if err := winner.Approve("approve-1", true); err != nil {
		t.Fatalf("winner's answer refused: %v", err)
	}
	b := newAttachBridge(loser)
	b.answer <- ApprovalAnswer{ID: "approve-1", Allow: false}

	// Two things reach the operator, in whichever order the socket
	// delivers them: the server's broadcast saying who answered, and this
	// frontend's own refusal explaining that its keypress did not count.
	// Both must arrive; only the second is lost if the bridge drops the
	// error, which is the bug this test exists for.
	var sawBroadcast, sawRefusal bool
	deadline := time.After(5 * time.Second)
	for !sawBroadcast || !sawRefusal {
		select {
		case r := <-b.resolved:
			if r.ID != "approve-1" {
				t.Fatalf("resolution = %+v", r)
			}
			if r.Note != "" {
				sawRefusal = true
			} else if r.By != "" {
				sawBroadcast = true
			}
		case <-deadline:
			t.Fatalf("a refused answer was not fully reported: broadcast=%v refusal=%v (the operator would not know their keypress was ignored)", sawBroadcast, sawRefusal)
		}
	}
}

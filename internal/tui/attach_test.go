package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// startAttachServer serves one control socket whose approvals the test
// decides, and returns a controller attached to it.
func startAttachServer(t *testing.T, input chan string, events chan agent.Event, approve func(id string, allow bool) bool) *frontend.Controller {
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
	ctrl := startAttachServer(t, input, events, func(id string, allow bool) bool { return true })
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

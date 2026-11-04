package frontend

import (
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// TestARefusedApprovalSaysWhy is #121's operator-facing half: an answer the
// session did not use comes back with the reason it was not used. Before
// this, every refusal was "already answered by another frontend", so an
// approval that timed out -- which nobody answered -- was reported as one a
// colleague had decided.
func TestARefusedApprovalSaysWhy(t *testing.T) {
	cases := []struct {
		outcome sessionhost.ApprovalOutcome
		want    string
		deny    string
	}{
		{sessionhost.ApprovalTimedOut, "timed out", "another frontend"},
		{sessionhost.ApprovalInterrupted, "interrupted", "another frontend"},
		{sessionhost.ApprovalNotOutstanding, "not outstanding", "another frontend"},
		{sessionhost.ApprovalOtherQuestion, "not the question", "another frontend"},
	}
	for _, c := range cases {
		events := make(chan agent.Event, 8)
		input := make(chan string, 4)
		_, path := startControllerServer(t, input, events, func(id string, allow bool) sessionhost.ApprovalOutcome {
			return c.outcome
		})
		ctrl, err := Dial(path)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { ctrl.Close() })

		err = ctrl.Approve("approve-1", true)
		if err == nil {
			t.Fatalf("outcome %v was reported as an accepted answer", c.outcome)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("refusal for %v = %q, want it to say %q", c.outcome, err, c.want)
		}
		if strings.Contains(err.Error(), c.deny) {
			t.Errorf("refusal for %v = %q, which blames another frontend for a question nobody answered", c.outcome, err)
		}
	}
}

// TestAnAlreadyAnsweredApprovalNamesTheWinner keeps what #40 asked for: when
// another frontend genuinely did answer first, the loser is told which one.
func TestAnAlreadyAnsweredApprovalNamesTheWinner(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	_, path := startControllerServer(t, input, events, func(id string, allow bool) sessionhost.ApprovalOutcome {
		if allow {
			return sessionhost.ApprovalAccepted
		}
		return sessionhost.ApprovalAlreadyAnswered
	})
	ctrl, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ctrl.Close() })

	if err := ctrl.Approve("approve-1", true); err != nil {
		t.Fatalf("the first answer was refused: %v", err)
	}
	err = ctrl.Approve("approve-1", false)
	if err == nil {
		t.Fatal("the second answer was reported as accepted")
	}
	if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("refusal = %q, want it to say the approval was already answered", err)
	}
}

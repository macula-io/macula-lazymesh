package sessionhost

import (
	"testing"
	"time"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// TestATimedOutApprovalIsReportedAsTimedOut is #121's own check, run for
// real: an approval nobody answers is refused by the clock, and an operator
// who answers afterwards is told the question timed out. Before this it was
// a bare false, which the control plane rendered as "already answered by
// another frontend" -- telling the operator that someone else had decided a
// call nobody decided.
func TestATimedOutApprovalIsReportedAsTimedOut(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "risky", Arguments: `{"cmd":"true"}`}}}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "the call was refused, never ran"}},
	}}
	pid := startSessionWith(t, n, root, SessionArgs{
		Provider:        p,
		Toolchain:       fakeToolchain("risky"),
		SystemPrompt:    "you are a test agent",
		ApprovalTimeout: 50 * time.Millisecond, // the clock, not an operator, refuses the call
	})

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, subscribeTo{Target: pid}); err != nil {
		t.Fatalf("subscribe collector: %v", err)
	}

	replyCh := make(chan SayReply, 1)
	go func() {
		reply, err := SayTurn(n, root, pid, "run the risky thing")
		if err != nil {
			replyCh <- SayReply{Err: err}
			return
		}
		replyCh <- reply
	}()

	var approvalID string
	deadline := time.After(10 * time.Second)
	for approvalID == "" {
		select {
		case ev := <-events:
			if ev.Event.Kind == agent.EventApprovalRequested && ev.Event.ToolName == "risky" {
				approvalID = ev.Event.ID
			}
		case <-deadline:
			t.Fatal("timed out waiting for the approval request")
		}
	}

	// Nobody answers. The turn must end on its own: the clock refuses the
	// call and the loop carries on with that refusal as the tool's result.
	select {
	case <-replyCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the approval timeout never fired")
	}

	if got := TryAnswerApproval(pid, approvalID, true); got != ApprovalTimedOut {
		t.Fatalf("a late answer was reported as %v, want %v: the operator would be told someone else answered it", got, ApprovalTimedOut)
	}
}

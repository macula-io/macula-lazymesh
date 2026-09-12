package sessionhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// fakeProvider answers every call with one fixed reply — or panics, which
// is the failure mode the supervision test exists for: a panicking
// provider inside a session actor is a real, plausible crash, not a
// test-only hook.
type fakeProvider struct {
	reply    provider.ChatResponse
	panicNow bool
}

func (f *fakeProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	if f.panicNow {
		panic("fake provider exploded")
	}
	return f.reply, nil
}

func (f *fakeProvider) ContextWindow() int { return 128_000 }

// fakeTools advertises no tools and answers nothing: the conversation
// machinery under test (provider round-trip, events, state) needs no
// tool-calling traffic to be observable.
type fakeTools struct{}

func (fakeTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (fakeTools) CallToolRaw(context.Context, string, string) (string, error) {
	return "ok", nil
}

// collector is a test-side observer actor: it monitors a session and
// forwards what it sees — loop events and monitor DOWNs — into plain Go
// channels the test waits on with deadlines.
type collector struct {
	act.Actor
	events chan Event
	downs  chan gen.MessageDownPID
}

func collectorFactory() gen.ProcessBehavior { return &collector{} }

func (c *collector) Init(args ...any) error {
	c.events = args[0].(chan Event)
	c.downs = args[1].(chan gen.MessageDownPID)
	return nil
}

func (c *collector) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case Event:
		select {
		case c.events <- msg:
		default:
		}
	case gen.MessageDownPID:
		select {
		case c.downs <- msg:
		default:
		}
	}
	return nil
}

func (c *collector) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch req := request.(type) {
	case monitorRequest:
		if err := c.Monitor(req.Target); err != nil {
			return nil, err
		}
		// A non-nil first return is the synchronous reply; (nil, nil)
		// would mean "answer later via SendResponse", leaving the caller
		// waiting forever.
		return true, nil
	case subscribeTo:
		// The collector subscribes in its own name: events are broadcast
		// to the sender PID, and a subscription made by an external Call
		// would register the calling node, not this actor.
		if err := c.Send(req.Target, Subscribe{}); err != nil {
			return nil, err
		}
		return true, nil
	}
	return nil, gen.ErrUnsupported
}

type monitorRequest struct{ Target gen.PID }
type subscribeTo struct{ Target gen.PID }

// testNode is shared by every test in this package: one embedded node per
// process by design (Node's own contract), so all tests ride the same one
// with distinct root/session names.
func testNode(t *testing.T) gen.Node {
	t.Helper()
	n, err := Node()
	if err != nil {
		t.Fatalf("boot embedded node: %v", err)
	}
	return n
}

func newSessionArgs(p provider.Provider) SessionArgs {
	return SessionArgs{
		Provider:     p,
		Tools:        fakeTools{},
		SystemPrompt: "you are a test agent",
	}
}

// TestNodeStartsWithNetworkingDisabled exercises the production bootstrap
// path: the node boots, is alive, and carries the lazymesh name. The
// disabled-network part is a config claim this test cannot observe
// directly without a listener probe; it is asserted by construction here
// and verified in Ergo's own source (node/network.go: NetworkModeDisabled
// skips every listener and network goroutine).
func TestNodeStartsWithNetworkingDisabled(t *testing.T) {
	n := testNode(t)
	if !n.IsAlive() {
		t.Fatal("node reported dead right after boot")
	}
	if n.Name() != NodeName {
		t.Fatalf("node name = %q, want %q", n.Name(), NodeName)
	}
}

// TestSayRunsTheRealLoopAndSubscribersSeeEvents proves the session actor
// hosts a genuine agent.Loop: one Say produces the assistant event for the
// fake provider's fixed reply, a subscriber sees it, and status reflects
// the grown conversation.
func TestSayRunsTheRealLoopAndSubscribersSeeEvents(t *testing.T) {
	n := testNode(t)
	p := &fakeProvider{reply: provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "hello from the fake"},
		Usage:   provider.Usage{TotalTokens: 7},
	}}

	rootPid, err := StartRoot(n, "root_say", "session_say", newSessionArgs(p))
	if err != nil {
		t.Fatalf("start root: %v", err)
	}
	_ = rootPid
	sessPid, err := SessionPID(n, "session_say")
	if err != nil {
		t.Fatalf("resolve session pid: %v", err)
	}

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, monitorRequest{Target: sessPid}); err != nil {
		t.Fatalf("monitor session: %v", err)
	}
	if _, err := n.Call(cPid, subscribeTo{Target: sessPid}); err != nil {
		t.Fatalf("subscribe collector: %v", err)
	}

	if err := n.Send(sessPid, Say{Text: "hi"}); err != nil {
		t.Fatalf("send say: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Event.Kind != agent.EventAssistantMessage {
			t.Fatalf("first event kind = %v, want assistant message", ev.Event.Kind)
		}
		if ev.Event.Text != "hello from the fake" {
			t.Fatalf("event text = %q", ev.Event.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the assistant event")
	}

	statusAny, err := n.Call(sessPid, StatusRequest{})
	if err != nil {
		t.Fatalf("status call: %v", err)
	}
	status, ok := statusAny.(Status)
	if !ok {
		t.Fatalf("status reply = %T, want Status", statusAny)
	}
	if status.MessageCount != 3 { // system + user + assistant
		t.Fatalf("message count = %d, want 3", status.MessageCount)
	}
	if status.Usage.TotalTokens != 7 {
		t.Fatalf("total tokens = %d, want 7", status.Usage.TotalTokens)
	}
}

// TestPanicRestartsWithFreshState proves the supervision contract the
// whole actor core exists for: a session whose provider panics dies with
// TerminateReasonPanic, its monitor is told, and the root supervisor
// restarts it with a fresh conversation.
func TestPanicRestartsWithFreshState(t *testing.T) {
	n := testNode(t)
	p := &fakeProvider{panicNow: true}

	if _, err := StartRoot(n, "root_panic", "session_panic", newSessionArgs(p)); err != nil {
		t.Fatalf("start root: %v", err)
	}
	oldPid, err := SessionPID(n, "session_panic")
	if err != nil {
		t.Fatalf("resolve session pid: %v", err)
	}

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, monitorRequest{Target: oldPid}); err != nil {
		t.Fatalf("monitor session: %v", err)
	}

	if err := n.Send(oldPid, Say{Text: "trigger the panic"}); err != nil {
		t.Fatalf("send say: %v", err)
	}

	select {
	case down := <-downs:
		if !errors.Is(down.Reason, gen.TerminateReasonPanic) {
			t.Fatalf("down reason = %v, want TerminateReasonPanic", down.Reason)
		}
		if down.PID != oldPid {
			t.Fatalf("down pid = %s, want the original session %s", down.PID, oldPid)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the monitor DOWN")
	}

	// The supervisor restarts the child under the same registered name:
	// wait until the name resolves to a NEW pid, then prove the state is
	// fresh (only the system prompt, no trace of the pre-panic turn).
	var newPid gen.PID
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if candidate, err := SessionPID(n, "session_panic"); err == nil && candidate != oldPid {
			newPid = candidate
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if newPid == (gen.PID{}) {
		t.Fatal("supervisor never restarted the session under a new pid")
	}

	statusAny, err := n.Call(newPid, StatusRequest{})
	if err != nil {
		t.Fatalf("status call on restarted session: %v", err)
	}
	status, ok := statusAny.(Status)
	if !ok {
		t.Fatalf("status reply = %T, want Status", statusAny)
	}
	if status.MessageCount != 1 { // system prompt only: fresh state
		t.Fatalf("restarted session message count = %d, want 1", status.MessageCount)
	}
}

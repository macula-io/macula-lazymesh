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
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
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
// process by design (Node's own contract), so all tests ride the same one.
func testNode(t *testing.T) gen.Node {
	t.Helper()
	n, err := Node()
	if err != nil {
		t.Fatalf("boot embedded node: %v", err)
	}
	return n
}

// testRoot starts a fresh anonymous root and cleans up every session it
// still hosts when the test ends.
func testRoot(t *testing.T, n gen.Node) gen.PID {
	t.Helper()
	root, err := StartRoot(n)
	if err != nil {
		t.Fatalf("start root: %v", err)
	}
	t.Cleanup(func() {
		pids, err := Sessions(n, root)
		if err != nil {
			return
		}
		for _, pid := range pids {
			_ = StopSession(n, pid)
		}
	})
	return root
}

// startSession is StartSession with the fatal-on-error behavior every
// test wants.
func startSession(t *testing.T, n gen.Node, root gen.PID, p provider.Provider) gen.PID {
	t.Helper()
	return startSessionWith(t, n, root, SessionArgs{
		Provider:     p,
		Tools:        fakeTools{},
		SystemPrompt: "you are a test agent",
	})
}

// startSessionWith starts a session with explicit args — the resume test
// needs the store + session id wired.
func startSessionWith(t *testing.T, n gen.Node, root gen.PID, args SessionArgs) gen.PID {
	t.Helper()
	pid, err := StartSession(n, root, args)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return pid
}

// waitDowns waits for one DOWN on downs within the deadline.
func waitDowns(t *testing.T, downs chan gen.MessageDownPID) gen.MessageDownPID {
	t.Helper()
	select {
	case down := <-downs:
		return down
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a monitor DOWN")
		return gen.MessageDownPID{}
	}
}

// fakeStreamingProvider streams fixed chunks, then completes — the
// Streamer half of the provider contract for the ordering test below.
type fakeStreamingProvider struct {
	chunks []string
}

func (f *fakeStreamingProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "fallback"}}, nil
}

func (f *fakeStreamingProvider) ContextWindow() int { return 1000 }

func (f *fakeStreamingProvider) ChatCompletionStream(ctx context.Context, req provider.ChatRequest, onDelta func(chunk string) error) (provider.ChatResponse, error) {
	full := ""
	for _, c := range f.chunks {
		full += c
		if err := onDelta(c); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: full}}, nil
}

// TestStreamedTurnDeliversDeltasThenTurnComplete pins the settle-order
// contract the control plane depends on: a subscriber sees every delta of
// the turn, then the completed message, then the session's own
// turn-complete marker — in mailbox order, never interleaved with the
// next turn's events.
func TestStreamedTurnDeliversDeltasThenTurnComplete(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &fakeStreamingProvider{chunks: []string{"hel", "lo "}}
	sessPid := startSession(t, n, root, p)

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, subscribeTo{Target: sessPid}); err != nil {
		t.Fatalf("subscribe collector: %v", err)
	}

	reply, err := SayTurn(n, sessPid, "stream please")
	if err != nil {
		t.Fatalf("say turn: %v", err)
	}
	if reply.Err != nil {
		t.Fatalf("say turn failed: %v", reply.Err)
	}

	var kinds []agent.EventKind
	deadline := time.After(10 * time.Second)
	for len(kinds) < 4 {
		select {
		case ev := <-events:
			kinds = append(kinds, ev.Event.Kind)
		case <-deadline:
			t.Fatalf("timed out collecting events; got %v", kinds)
		}
	}
	want := []agent.EventKind{
		agent.EventAssistantDelta, agent.EventAssistantDelta,
		agent.EventAssistantMessage, agent.EventListening,
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("event order = %v, want %v", kinds, want)
		}
	}
}

// blockingProvider enters the call and blocks until its context is done
// — the exact shape an interrupt exists to cut short.
type blockingProvider struct {
	entered chan struct{}
}

func (b *blockingProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-ctx.Done()
	return provider.ChatResponse{}, ctx.Err()
}

func (b *blockingProvider) ContextWindow() int { return 1000 }

// TestInterruptCancelsInFlightTurn proves Phase 3's contract end to end:
// a turn blocked inside its provider call is cancelled by Interrupt
// (ctx-cancel at the boundary, never a Kill), the driver's SayTurn
// returns with context.Canceled, and the event stream still delivers the
// loop's error and the settle point.
func TestInterruptCancelsInFlightTurn(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &blockingProvider{entered: make(chan struct{})}
	sessPid := startSession(t, n, root, p)

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, subscribeTo{Target: sessPid}); err != nil {
		t.Fatalf("subscribe collector: %v", err)
	}

	replyCh := make(chan SayReply, 1)
	go func() {
		reply, err := SayTurn(n, sessPid, "a turn that will be interrupted")
		if err != nil {
			replyCh <- SayReply{Err: err}
			return
		}
		replyCh <- reply
	}()

	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider call never started")
	}

	Interrupt(sessPid)

	select {
	case reply := <-replyCh:
		if !errors.Is(reply.Err, context.Canceled) {
			t.Fatalf("interrupted turn error = %v, want context.Canceled", reply.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SayTurn never returned after the interrupt")
	}

	// The loop emitted its EventError and the session its settle marker,
	// in order, through the same path every turn uses.
	var sawError, sawListening bool
	deadline := time.After(10 * time.Second)
	for !sawError || !sawListening {
		select {
		case ev := <-events:
			switch ev.Event.Kind {
			case agent.EventError:
				sawError = true
			case agent.EventListening:
				sawListening = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for error+listening events (error=%v listening=%v)", sawError, sawListening)
		}
	}
}

// TestResumeRestoresPersistedConversation pins the D2 contract end to
// end: a conversation persisted under a session id comes back when a new
// session actor starts with the same store + id — the shape --resume
// depends on, and the same path a supervisor restart rides (SOFO hands
// the restarted instance its SessionArgs unchanged).
func TestResumeRestoresPersistedConversation(t *testing.T) {
	n := testNode(t)
	store, err := sessionstore.New(t.TempDir(), sessionstore.Fingerprint(t.TempDir()))
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	args := SessionArgs{
		Provider:     &fakeProvider{reply: provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "hello again"}}},
		Tools:        fakeTools{},
		SystemPrompt: "you are a test agent",
		Store:        store,
		SessionID:    "resumed",
	}

	root := testRoot(t, n)
	first := startSessionWith(t, n, root, args)
	reply, err := SayTurn(n, first, "hi")
	if err != nil {
		t.Fatalf("first say: %v", err)
	}
	if reply.Err != nil {
		t.Fatalf("first say failed: %v", reply.Err)
	}
	if err := StopSession(n, first); err != nil {
		t.Fatalf("stop first session: %v", err)
	}

	// A brand-new session with the same store + id: the conversation is
	// restored (system + user + assistant = 3 messages) before any Say.
	second := startSessionWith(t, n, root, args)
	status, err := SessionStatus(n, second)
	if err != nil {
		t.Fatalf("status on resumed session: %v", err)
	}
	if status.MessageCount != 3 {
		t.Fatalf("resumed message count = %d, want 3", status.MessageCount)
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
	root := testRoot(t, n)
	p := &fakeProvider{reply: provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "hello from the fake"},
		Usage:   provider.Usage{TotalTokens: 7},
	}}
	sessPid := startSession(t, n, root, p)

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

	reply, err := SayTurn(n, sessPid, "hi")
	if err != nil {
		t.Fatalf("say turn: %v", err)
	}
	if reply.Err != nil {
		t.Fatalf("say turn failed: %v", reply.Err)
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
// restarts it — a new pid, same SessionArgs, fresh conversation.
func TestPanicRestartsWithFreshState(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &fakeProvider{panicNow: true}
	oldPid := startSession(t, n, root, p)

	events := make(chan Event, 16)
	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, monitorRequest{Target: oldPid}); err != nil {
		t.Fatalf("monitor session: %v", err)
	}

	// The Say runs inside the session actor; the panicking provider takes
	// the process down mid-turn, so SayTurn either returns a reply with an
	// error value or fails outright — either way the DOWN is the real
	// assertion.
	_, _ = SayTurn(n, oldPid, "trigger the panic")

	down := waitDowns(t, downs)
	if !errors.Is(down.Reason, gen.TerminateReasonPanic) {
		t.Fatalf("down reason = %v, want TerminateReasonPanic", down.Reason)
	}
	if down.PID != oldPid {
		t.Fatalf("down pid = %s, want the original session %s", down.PID, oldPid)
	}

	// The supervisor restarts the instance under a new anonymous pid:
	// poll the root's session list until the old pid is replaced by a new
	// one, then prove the state is fresh (only the system prompt, no
	// trace of the pre-panic turn).
	var newPid gen.PID
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pids, err := Sessions(n, root)
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
		for _, pid := range pids {
			if pid != oldPid {
				newPid = pid
			}
		}
		if newPid != (gen.PID{}) {
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

// TestNormalStopDoesNotRestart pins the transient-strategy contract: a
// normally stopped session ends for good — the DOWN reports the normal
// reason and the root's session list empties instead of resurrecting the
// conversation the way a permanent strategy would.
func TestNormalStopDoesNotRestart(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &fakeProvider{reply: provider.ChatResponse{
		Message: provider.Message{Role: provider.RoleAssistant, Content: "hello"},
	}}
	sessPid := startSession(t, n, root, p)

	downs := make(chan gen.MessageDownPID, 8)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, make(chan Event, 16), downs)
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, monitorRequest{Target: sessPid}); err != nil {
		t.Fatalf("monitor session: %v", err)
	}

	if err := StopSession(n, sessPid); err != nil {
		t.Fatalf("stop session: %v", err)
	}

	down := waitDowns(t, downs)
	if !errors.Is(down.Reason, gen.TerminateReasonNormal) {
		t.Fatalf("down reason = %v, want TerminateReasonNormal", down.Reason)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pids, err := Sessions(n, root)
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
		if len(pids) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("normally stopped session is still listed under the root")
}

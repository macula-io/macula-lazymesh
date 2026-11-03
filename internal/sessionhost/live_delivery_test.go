package sessionhost

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// stallingStreamProvider streams one chunk, then waits until released:
// a model still thinking after its first words.
type stallingStreamProvider struct {
	release chan struct{}
}

func (p *stallingStreamProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, nil
}

func (p *stallingStreamProvider) ContextWindow() int { return 1000 }

func (p *stallingStreamProvider) ChatCompletionStream(ctx context.Context, _ provider.ChatRequest, onDelta func(string) error) (provider.ChatResponse, error) {
	if err := onDelta("hello"); err != nil {
		return provider.ChatResponse{}, err
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return provider.ChatResponse{}, ctx.Err()
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "hello"}}, nil
}

// crashingStreamProvider streams one chunk and then panics: a turn that
// crashes mid-stream.
type crashingStreamProvider struct{}

func (crashingStreamProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, nil
}

func (crashingStreamProvider) ContextWindow() int { return 1000 }

func (crashingStreamProvider) ChatCompletionStream(_ context.Context, _ provider.ChatRequest, onDelta func(string) error) (provider.ChatResponse, error) {
	_ = onDelta("partial")
	panic("provider exploded mid-stream")
}

// subscribedCollector spawns a collector subscribed to session and
// returns its event channel.
func subscribedCollector(t *testing.T, n gen.Node, session gen.PID) chan Event {
	t.Helper()
	events := make(chan Event, 64)
	cPid, err := n.Spawn(collectorFactory, gen.ProcessOptions{}, events, make(chan gen.MessageDownPID, 8))
	if err != nil {
		t.Fatalf("spawn collector: %v", err)
	}
	if _, err := n.Call(cPid, subscribeTo{Target: session}); err != nil {
		t.Fatalf("subscribe collector: %v", err)
	}
	return events
}

// sayInBackground runs SayTurn on its own goroutine; the channel carries
// the reply, or the returned error as SayReply.Err.
func sayInBackground(n gen.Node, root, session gen.PID, text string) chan SayReply {
	replies := make(chan SayReply, 1)
	go func() {
		reply, err := SayTurn(n, root, session, text)
		if err != nil {
			reply = SayReply{Err: err}
		}
		replies <- reply
	}()
	return replies
}

// #96: the operator sees text as it streams. A delta reaches a subscriber
// while the provider is still producing the rest of the turn, not in one
// burst after it.
func TestLiveDelivery_DeltaArrivesWhileTheProviderIsStillBlocked(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &stallingStreamProvider{release: make(chan struct{})}
	pid := startSession(t, n, root, p)
	events := subscribedCollector(t, n, pid)

	replies := sayInBackground(n, root, pid, "talk to me")
	deadline := time.After(1500 * time.Millisecond)
	for seen := false; !seen; {
		select {
		case ev := <-events:
			seen = ev.Event.Kind == agent.EventAssistantDelta
		case <-deadline:
			close(p.release)
			t.Fatal("no delta reached the subscriber while the provider was still streaming")
		}
	}
	close(p.release)
	select {
	case reply := <-replies:
		if reply.Err != nil {
			t.Fatalf("turn failed: %v", reply.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn never completed after the provider was released")
	}
}

// #96: an approval arrives after the text that explains it. The
// assistant's words of the same turn reach the subscriber before the
// question about the tool call they lead up to.
func TestLiveDelivery_ApprovalAfterItsOwnTurnsText(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "I need to run risky first",
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "risky", Arguments: `{"cmd":"true"}`}}}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	pid := startSessionWith(t, n, root, SessionArgs{
		Provider:     p,
		Toolchain:    fakeToolchain("risky"),
		SystemPrompt: "you are a test agent",
	})
	events := subscribedCollector(t, n, pid)

	replies := sayInBackground(n, root, pid, "go")
	textSeen := false
	deadline := time.After(10 * time.Second)
	for {
		var ev Event
		select {
		case ev = <-events:
		case <-deadline:
			t.Fatal("timed out waiting for the approval request")
		}
		if ev.Event.Kind == agent.EventAssistantMessage && strings.Contains(ev.Event.Text, "run risky") {
			textSeen = true
		}
		if ev.Event.Kind == agent.EventApprovalRequested {
			if !textSeen {
				t.Fatal("the approval request arrived before the text that explains it")
			}
			AnswerApproval(pid, ev.Event.ID, true)
			break
		}
	}
	select {
	case reply := <-replies:
		if reply.Err != nil {
			t.Fatalf("turn failed: %v", reply.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn never completed after the approval")
	}
}

// #96: a crashed turn is reported to the caller at once, as the session
// gone, not after the full call timeout.
func TestLiveDelivery_CrashedTurnReturnsPromptly(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	pid := startSession(t, n, root, crashingStreamProvider{})

	replies := sayInBackground(n, root, pid, "crash")
	select {
	case reply := <-replies:
		if reply.Err == nil {
			t.Fatal("a crashed turn was reported as a success")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a crashed turn was not reported within 5s (call timeout is %ds)", sayTurnTimeoutSeconds)
	}
}

// #96: a crashed turn leaves nothing behind. After five mid-stream
// panics no goroutine of a turn is still parked.
func TestLiveDelivery_NoGoroutineLeftAfterMidStreamPanics(t *testing.T) {
	old := sayTurnTimeoutSeconds
	sayTurnTimeoutSeconds = 1
	defer func() { sayTurnTimeoutSeconds = old }()

	n := testNode(t)
	root := testRoot(t, n)
	for i := 0; i < 5; i++ {
		pid := startSession(t, n, root, crashingStreamProvider{})
		_, _ = SayTurn(n, root, pid, "crash")
	}
	deadline := time.Now().Add(2 * time.Second)
	for parked := turnGoroutines(); parked > 0; parked = turnGoroutines() {
		if time.Now().After(deadline) {
			t.Fatalf("%d turn goroutines still parked after five mid-stream panics", parked)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// turnGoroutines counts goroutines started by a session's turn.
func turnGoroutines() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "sessionhost.(*session).runSay.func")
}

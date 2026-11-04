package main

// macula-io/macula-lazymesh#115: an approval answer the session is no
// longer waiting for used to be dropped in silence. The popup closed, the
// key was swallowed, and the operator had no way to tell that the call they
// meant to stop (or allow) had already moved on -- the question had timed
// out, the turn had been interrupted, or the session that asked it was gone.

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
	"github.com/macula-io/macula-lazymesh/internal/tui"
)

// askThenFinishProvider asks for the approval-gated git_push on its first
// turn and finishes on the next.
type askThenFinishProvider struct {
	mu     sync.Mutex
	rounds int
}

func (p *askThenFinishProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.mu.Lock()
	p.rounds++
	round := p.rounds
	p.mu.Unlock()
	if round == 1 {
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "git_push", Arguments: `{}`}}}}, nil
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "pushed"}}, nil
}

func (p *askThenFinishProvider) ContextWindow() int { return 1000 }

// approvalAnswerBus starts the wiring run() builds: the TUI's answers on a
// channel, delivered to the session, with a notice back to the TUI when one
// reaches nothing.
func approvalAnswerBus(t *testing.T, sess *currentSession, events chan agent.Event, approve func(string, bool) bool) chan<- tui.ApprovalAnswer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := agent.NewOutlet(ctx.Done(), events)
	answers := make(chan tui.ApprovalAnswer, 4)
	go func() {
		for answer := range answers {
			deliverApproval(approve, nil, out, answer)
		}
	}()
	return answers
}

// deliverApproval reports what it did and tells the TUI when the answer
// reached nothing.
func TestDeliverApproval_ReportsWhetherTheAnswerWasUsed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan agent.Event, 8)
	out := agent.NewOutlet(ctx.Done(), events)

	used := deliverApproval(func(string, bool) bool { return true }, nil, out, tui.ApprovalAnswer{ID: "a1", Allow: true})
	if !used {
		t.Fatal("an answer that reached its question was reported as unused")
	}
	select {
	case ev := <-events:
		t.Fatalf("an answer that reached its question still told the operator something: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	used = deliverApproval(func(string, bool) bool { return false }, nil, out, tui.ApprovalAnswer{ID: "a2", Allow: false})
	if used {
		t.Fatal("an answer that reached nothing was reported as used")
	}
	select {
	case ev := <-events:
		if ev.Kind != agent.EventAnswerUnused {
			t.Fatalf("expected an unused-answer notice, got %+v", ev)
		}
		if ev.ID != "a2" {
			t.Fatalf("the notice names %q, not the answer it is about", ev.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the TUI was never told the answer reached nothing")
	}
}

// The whole path, the way the brief reproduces it: an approval is asked,
// answered once (which the session takes), and answered again -- the second
// answer reaches no question and the operator is told.
func TestApprovalAnswer_SecondAnswerToTheSameQuestionTellsTheOperator(t *testing.T) {
	n, err := sessionhost.Node(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sessionhost.StartRoot(n)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.New(t.TempDir(), "fp")
	if err != nil {
		t.Fatal(err)
	}
	first, err := sessionhost.StartSession(n, root, sessionhost.SessionArgs{
		Provider: &askThenFinishProvider{}, SystemPrompt: "test", Store: store, SessionID: "s115",
		Toolchain: toolchain.Policy{Base: &countingMeshTools{}, Allowlist: []string{"git_push"}, Ask: []string{"git_push"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := newCurrentSession(first)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan agent.Event, 256)
	tuiOut := agent.NewOutlet(ctx.Done(), events)
	frontendOut := agent.NewOutlet(ctx.Done(), nil)
	agentLog := log.New(io.Discard, "", 0)
	runtimeCounters := counters.New()
	bridge, err := startEventBridge(n, ctx, first, tuiOut, frontendOut, agentLog, roomwaiter.New(nil, ""), func(agent.Event) {}, runtimeCounters)
	if err != nil {
		t.Fatal(err)
	}

	userInput := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgent(ctx, n, root, sess, bridge, roomwaiter.New(nil, ""), ringwaiter.New(nil, ""), agentLog, tuiOut, frontendOut, userInput, nil, true, nil, nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	answers := approvalAnswerBus(t, sess, events, approveCurrent(sess))

	userInput <- "push it"
	seen := awaitEvent(t, events, agent.EventApprovalRequested, nil)
	id := seen[len(seen)-1].ID

	answers <- tui.ApprovalAnswer{ID: id, Allow: true}
	// The first answer is the one the question takes: the turn finishes.
	seen = awaitEvent(t, events, agent.EventAssistantMessage, seen)
	if got := seen[len(seen)-1].Text; got != "pushed" {
		t.Fatalf("the approved turn did not finish: %q", got)
	}

	for _, ev := range seen {
		if ev.Kind == agent.EventAnswerUnused {
			t.Fatalf("an answer that was used was reported as unused: %+v", ev)
		}
	}

	// The same answer again: nothing is waiting for it any more.
	answers <- tui.ApprovalAnswer{ID: id, Allow: true}
	seen = awaitEvent(t, events, agent.EventAnswerUnused, seen)
	if got := seen[len(seen)-1].ID; got != id {
		t.Fatalf("the notice names %q, not the answered question %q", got, id)
	}
}

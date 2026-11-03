package main

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
)

// crashThenAskThenHangProvider crashes the first turn, asks for an
// approval-gated git_push in the second, and hangs the third until it is
// interrupted.
type crashThenAskThenHangProvider struct {
	mu      sync.Mutex
	rounds  int
	hanging chan struct{}
}

func (p *crashThenAskThenHangProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	p.mu.Lock()
	p.rounds++
	round := p.rounds
	p.mu.Unlock()
	switch round {
	case 1:
		panic("provider exploded on the first turn")
	case 2:
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "git_push", Arguments: `{}`}}}}, nil
	case 3:
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "pushed"}}, nil
	}
	close(p.hanging)
	<-ctx.Done()
	return provider.ChatResponse{}, ctx.Err()
}

func (p *crashThenAskThenHangProvider) ContextWindow() int { return 1000 }

// #107: after a crash the driver moves to the replacement session, and the
// operator's acts follow it: an approval answer is delivered, an interrupt
// cuts the turn, and status and metrics read the live session.
func TestCurrentSession_OperatorActsReachTheReplacementAfterACrash(t *testing.T) {
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
	p := &crashThenAskThenHangProvider{hanging: make(chan struct{})}
	first, err := sessionhost.StartSession(n, root, sessionhost.SessionArgs{
		Provider: p, Tools: &countingMeshTools{}, SystemPrompt: "test",
		Store: store, SessionID: "s1", AskTools: []string{"git_push"},
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
	bridge, err := startEventBridge(n, ctx, first, tuiOut, frontendOut, agentLog, roomwaiter.New(nil, ""), runtimeCounters)
	if err != nil {
		t.Fatal(err)
	}
	interrupt := interruptCurrent(sess)
	approve := approveCurrent(sess)
	query := buildQueryHandler(n, sess, nil, runtimeCounters)
	metrics := metricsReader(n, sess, runtimeCounters)

	userInput := make(chan string, 4)
	userInput <- "crash"
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgent(ctx, n, root, sess, bridge, roomwaiter.New(nil, ""), ringwaiter.New(nil, ""), agentLog, tuiOut, frontendOut, userInput, nil, true, nil, nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	awaitEvent(t, events, agent.EventTurnCrashed, nil)
	if sess.Pid() == first {
		t.Fatalf("the driver reattached but the current session is still the dead %s", first)
	}

	userInput <- "push it"
	seen := awaitEvent(t, events, agent.EventApprovalRequested, nil)
	if !approve(seen[len(seen)-1].ID, true) {
		t.Fatal("the approval answer did not reach the replacement session")
	}
	seen = awaitEvent(t, events, agent.EventAssistantMessage, nil)
	if got := seen[len(seen)-1].Text; got != "pushed" {
		t.Fatalf("the approved turn did not finish: %q", got)
	}

	if _, err := query(ctx, "status"); err != nil {
		t.Fatalf("status after the crash: %v", err)
	}
	if _, totals := metrics(); totals.Rounds == 0 {
		t.Fatal("metrics after the crash read no rounds from the replacement session")
	}

	userInput <- "hang"
	<-p.hanging
	interrupt()
	seen = awaitEvent(t, events, agent.EventError, nil)
	if err := seen[len(seen)-1].Err; !errors.Is(err, context.Canceled) {
		t.Fatalf("the hanging turn ended with %v, want it interrupted", err)
	}
}

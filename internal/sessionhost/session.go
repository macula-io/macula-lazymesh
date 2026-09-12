package sessionhost

import (
	"context"
	"fmt"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// SessionArgs is everything one session actor needs to build its own
// conversation: the provider, the tool sources, and the fixed system
// prompt. Each session owns its own agent.Loop; nothing is shared between
// sessions by construction.
type SessionArgs struct {
	Provider     provider.Provider
	Tools        agent.ToolSource
	SystemPrompt string
}

// Say asks the session's loop to process one user message: the full
// tool-calling round runs, and every intermediate step is forwarded to
// each subscriber as an Event, in order, after the turn completes.
type Say struct {
	Text string
}

// Event is one loop step (assistant text, tool call, tool result, error)
// forwarded to a subscriber.
type Event struct {
	Event agent.Event
}

// Subscribe registers the sender as an event subscriber. Idempotent; a
// subscriber that has died is simply skipped when its turn comes.
type Subscribe struct{}

// StatusRequest asks for the conversation's current shape.
type StatusRequest struct{}

// Status is the answer to a StatusRequest.
type Status struct {
	MessageCount int
	Usage        provider.Usage
}

// eventBuffer bounds how many events one Say turn captures before it
// would block the loop. Matches the generous-buffer posture of main.go's
// own tuiEvents channel: the session must never stall on a slow consumer
// mid-turn. The buffer is drained and broadcast after the turn.
const eventBuffer = 64

// session is one agent conversation under the root supervisor.
type session struct {
	act.Actor

	loop        *agent.Loop
	subscribers map[gen.PID]struct{}
}

func sessionFactory() gen.ProcessBehavior { return &session{} }

// Init builds this session's own Loop from its SessionArgs. A fresh Loop
// is also what a supervisor restart yields: panic recovery means losing
// the in-memory conversation, by design — persistence (D2) is what will
// make restarts restore it.
func (s *session) Init(args ...any) error {
	sessArgs, ok := args[0].(SessionArgs)
	if !ok {
		return fmt.Errorf("sessionhost: expected SessionArgs, got %T", args[0])
	}
	s.loop = agent.NewLoop(sessArgs.Provider, sessArgs.Tools, sessArgs.SystemPrompt)
	s.subscribers = make(map[gen.PID]struct{})
	return nil
}

// ProcessKind reports this process as a per-conversation session actor.
func (s *session) ProcessKind() gen.ProcessKind {
	return gen.ProcessKindSession
}

// HandleMessage routes Say and Subscribe. An unknown message is logged and
// dropped, never an error: a non-nil return terminates the process, and an
// unrecognized message must not be able to kill a conversation.
func (s *session) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case Say:
		return s.runSay(msg)
	case Subscribe:
		s.subscribers[from] = struct{}{}
		return nil
	}
	s.Log().Warning("sessionhost: session dropped unknown message %T from %s", message, from)
	return nil
}

// UnsupportedReply is the answer to a request the session does not
// recognize. It is delivered as a normal reply rather than an error
// because, in Ergo, a non-nil error from HandleCall TERMINATES the
// process — an unknown request must never be able to kill a conversation.
type UnsupportedReply struct {
	Request any
}

// HandleCall answers synchronous requests. Subscribe is callable too (its
// ack is the subscriber count); anything unknown is answered with
// UnsupportedReply via SendResponse. The nil, nil return marks the
// request as answered asynchronously — returning a non-nil error here
// would stop the session (Ergo's HandleCall error = stop).
func (s *session) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch request.(type) {
	case StatusRequest:
		return Status{MessageCount: s.loop.MessageCount(), Usage: s.loop.Usage()}, nil
	case Subscribe:
		s.subscribers[from] = struct{}{}
		return len(s.subscribers), nil
	}
	s.Log().Warning("sessionhost: session answered unknown request %T from %s with UnsupportedReply", request, from)
	if err := s.SendResponse(from, ref, UnsupportedReply{Request: request}); err != nil {
		return nil, err
	}
	return nil, nil
}

// Terminate has nothing to release yet: the Loop owns no resources beyond
// memory. Teardown of the future unix-socket frontend will live here.
func (s *session) Terminate(reason error) {}

// runSay executes one full tool-calling turn inside this actor's callback.
//
// Blocking I/O in an actor callback is the deliberate deviation Ergo's own
// guidelines warn about, and it is accepted here for three reasons, all
// recorded in the D4 decision: Ergo runs one goroutine per process, so a
// blocked session blocks only itself (Q5); a blocked turn cannot be
// preempted, so interrupt will arrive as ctx-cancel at the boundary, not
// as an actor Kill (conditions 2-3); and the failure mode supervision
// exists for — a panic — is precisely what still gets caught and
// restarted. Events are captured in a bounded buffer and broadcast after
// the turn, so no subscriber can stall the loop.
func (s *session) runSay(msg Say) error {
	events := make(chan agent.Event, eventBuffer)
	err := s.loop.Say(context.Background(), msg.Text, events)
	close(events)
	for ev := range events {
		s.broadcast(ev)
	}
	if err != nil {
		// A failed turn is reported, not fatal: the backoff/retry policy
		// lives with whoever drives the session, exactly as it did in
		// cmd/lazymesh's runAgent, and the loop has already emitted the
		// EventError describing it.
		s.Log().Error("sessionhost: turn failed: %s", err)
	}
	return nil
}

// broadcast forwards one event to every subscriber. A dead subscriber's
// Send failure is expected and ignored — subscribers clean themselves up
// by dying, and the event is not retried.
func (s *session) broadcast(ev agent.Event) {
	for pid := range s.subscribers {
		_ = s.Send(pid, Event{Event: ev})
	}
}

package sessionhost

import (
	"context"
	"fmt"
	"sync"

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
// each subscriber as an Event, in order, after the turn completes. Say is
// a REQUEST, not a cast: the driver blocks on SayReply until the turn is
// done, preserving the sequential one-turn-at-a-time cadence the old
// runAgent had — and SayReply.Err carries a turn failure as a value, so a
// failed turn never terminates the session (a non-nil HandleCall error
// would).
type Say struct {
	Text string
}

// SayReply is the answer to a Say: Err is nil on a completed turn, and
// carries the loop's own error on a failed one. It travels as a normal
// reply value — the driver decides what a failure means; the session
// itself survives it.
type SayReply struct {
	Err error
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
// mid-turn. The buffer is drained live and broadcast by the forwarding
// goroutine (see runSay).
const eventBuffer = 64

// turns maps each live session to its in-flight turn's cancel func, the
// one piece of session state legitimately touched from another goroutine:
// an interrupt must cancel a provider call while the actor goroutine is
// BLOCKED inside it, and Ergo condition 3 says that cancel is the ONLY
// such mechanism (a Kill cannot preempt a blocked handler). The map is
// concurrent because Interrupt runs on the controller's goroutine, while
// the session's own mailbox state stays on the actor goroutine untouched.
var turns sync.Map // gen.PID -> context.CancelFunc

// Interrupt cancels session's in-flight turn, if one is running. Safe
// from any goroutine at any time; a session with no active turn is a
// no-op. The interrupted turn's error travels back to the driver as
// SayReply.Err wrapping context.Canceled, and the loop has already
// emitted the EventError describing it.
func Interrupt(session gen.PID) {
	if cancel, ok := turns.Load(session); ok {
		cancel.(context.CancelFunc)()
	}
}

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

// HandleMessage routes Subscribe, and Event — the live-drain shape:
// runSay's forwarding goroutine self-Sends every loop event here, so the
// broadcast to subscribers happens on the actor's own goroutine and the
// subscriber map is never touched concurrently (Ergo's no-goroutines-in-
// callbacks rule is about exactly this). An unknown message is logged
// and dropped, never an error: a non-nil return terminates the process.
func (s *session) HandleMessage(from gen.PID, message any) error {
	switch msg := message.(type) {
	case Subscribe:
		s.subscribers[from] = struct{}{}
		return nil
	case Event:
		s.broadcast(msg.Event)
		return nil
	}
	s.Log().Warning("sessionhost: session dropped unknown message %T from %s", message, from)
	return nil
}

// HandleCall answers synchronous requests: Say runs one turn and returns
// SayReply (a failed turn is a value, never a termination), StatusRequest
// reports the conversation's shape, and Subscribe is callable too (its
// ack is the subscriber count). Anything unknown is answered with
// UnsupportedReply via SendResponse. The nil, nil return marks the
// request as answered asynchronously — returning a non-nil error here
// would stop the session (Ergo's HandleCall error = stop).
func (s *session) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch req := request.(type) {
	case Say:
		return SayReply{Err: s.runSay(req.Text)}, nil
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
// runSay executes one full tool-calling turn inside this actor's callback.
//
// Blocking I/O in an actor callback is the deliberate deviation Ergo's own
// guidelines warn about, and it is accepted here for three reasons, all
// recorded in the D4 decision: Ergo runs one goroutine per process, so a
// blocked session blocks only itself (Q5); a blocked turn cannot be
// preempted, so interrupt will arrive as ctx-cancel at the boundary, not
// as an actor Kill (conditions 2-3); and the failure mode supervision
// exists for — a panic — is precisely what still gets caught and
// restarted.
//
// Streaming (D3) changed the event drain: the loop now emits a delta per
// content chunk, far more than the bounded capture buffer could hold
// before it deadlocked the turn. One forwarding goroutine therefore
// drains the buffer while Say runs and self-Sends every event through
// the actor's own mailbox — all subscriber-state access stays on the
// actor goroutine, and events reach subscribers live, in mailbox order,
// ahead of the SayReply that ends the turn.
func (s *session) runSay(text string) error {
	ctx, cancel := context.WithCancel(context.Background())
	turns.Store(s.PID(), cancel)
	defer turns.Delete(s.PID())

	events := make(chan agent.Event, eventBuffer)
	drained := make(chan struct{})
	go func() {
		for ev := range events {
			_ = s.Send(s.PID(), Event{Event: ev})
		}
		close(drained)
	}()
	err := s.loop.Say(ctx, text, events)
	close(events)
	<-drained
	// The turn-complete marker is emitted BY the session, appended to the
	// mailbox AFTER every event of the turn it just ran: it rides the
	// same delivery path as the deltas, so the control plane's settle
	// contract (deltas..., then turn_complete) holds regardless of how
	// quickly the driver's SayReply travels to the caller. The driver's
	// own success path deliberately does NOT emit EventListening for the
	// same turn.
	_ = s.Send(s.PID(), Event{Event: agent.Event{Kind: agent.EventListening}})
	// The turn's failure travels back to the driver as SayReply.Err: the
	// loop has already emitted the EventError describing it, and the
	// retry/backoff policy belongs to whoever drives the session — the
	// session itself survives a failed turn.
	return err
}

// broadcast forwards one event to every subscriber. A dead subscriber's
// Send failure is expected and ignored — subscribers clean themselves up
// by dying, and the event is not retried.
func (s *session) broadcast(ev agent.Event) {
	for pid := range s.subscribers {
		_ = s.Send(pid, Event{Event: ev})
	}
}

// Say runs one turn on session and blocks until it completes: SayReply.Err
// carries a turn failure (an interrupt arrives as context.Canceled), while
// a non-nil returned error means the session process itself is gone (a
// panic-restart in flight, or a stop) and the caller must reattach rather
// than count it as an ordinary failure.
func SayTurn(n gen.Node, session gen.PID, text string) (SayReply, error) {
	reply, err := n.Call(session, Say{Text: text})
	if err != nil {
		return SayReply{}, err
	}
	r, ok := reply.(SayReply)
	if !ok {
		return SayReply{}, fmt.Errorf("sessionhost: session answered Say with %T, want SayReply", reply)
	}
	return r, nil
}

// Status asks the session for its conversation's current shape.
func SessionStatus(n gen.Node, session gen.PID) (Status, error) {
	reply, err := n.Call(session, StatusRequest{})
	if err != nil {
		return Status{}, err
	}
	r, ok := reply.(Status)
	if !ok {
		return Status{}, fmt.Errorf("sessionhost: session answered StatusRequest with %T, want Status", reply)
	}
	return r, nil
}

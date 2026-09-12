package sessionhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
)

// SessionArgs is everything one session actor needs to build its own
// conversation: the provider, the tool sources, the fixed system
// prompt, and — when Store and SessionID are both set — the JSONL log
// the conversation is restored from and appended to (D2). Each session
// owns its own agent.Loop; nothing is shared between sessions by
// construction.
type SessionArgs struct {
	Provider     provider.Provider
	Tools        agent.ToolSource
	SystemPrompt string

	// Store persists the conversation as JSONL under SessionID. A nil
	// Store (or an empty SessionID) disables persistence: the session
	// runs in-memory exactly as it did before D2.
	Store     *sessionstore.Store
	SessionID string

	// AskTools lists tools gated behind per-action operator approval
	// (G9): the session wraps its tool source with agent.AskSource using
	// its own consent — the approval prompt goes out as an event to every
	// subscriber (TUI popup, control socket), and the answer comes back
	// through AnswerApproval. Empty means nothing asks.
	AskTools []string
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

// approvalTimeout is how long a consent question waits for an answer
// before the call is refused as unanswerable — an operator who isn't
// there must not be able to hang a turn forever (the interrupt path
// still cuts it sooner when asked).
const approvalTimeout = 2 * time.Minute

// approvalSlot is what AnswerApproval looks up: the one question the
// session currently has outstanding, keyed by the approval id so a stale
// answer can never be delivered to a newer question.
type approvalSlot struct {
	id string
	ch chan approvalAnswer
}

// approvalAnswer is the operator's decision.
type approvalAnswer struct {
	allow bool
}

// approvals maps each live session to its current outstanding consent
// question — the same side-channel shape as turns: the session's actor
// goroutine is blocked inside the tool call while it waits, so the
// answer must arrive from another goroutine.
var approvals sync.Map // gen.PID -> *approvalSlot

// AnswerApproval delivers an operator decision for approval id on
// session. Safe from any goroutine; a session with no outstanding
// question, or one whose question's id no longer matches, is a no-op
// (never delivered to the wrong call).
func AnswerApproval(session gen.PID, id string, allow bool) {
	if slot, ok := approvals.Load(session); ok {
		s := slot.(*approvalSlot)
		if s.id == id {
			select {
			case s.ch <- approvalAnswer{allow: allow}:
			default:
			}
		}
	}
}

// consent is the session's own Consent: surface the question to every
// subscriber as an EventApprovalRequested, then wait for the answer on
// this turn's private channel, bounded by the turn's context and the
// approval timeout.
//
// It runs inside the loop's tool-call path, on the session's actor
// goroutine — which is exactly why it BROADCASTS directly instead of
// self-Sending: a self-Sent event would sit in the mailbox unprocessed
// while the actor is blocked in Say, and the prompt would deadlock the
// very answer it is waiting for. A direct broadcast from the actor's own
// goroutine touches only what the actor already owns.
func (s *session) consent(ctx context.Context, tool, argumentsJSON string) (bool, error) {
	id := fmt.Sprintf("approve-%d", time.Now().UnixNano())
	ch := make(chan approvalAnswer, 1)
	approvals.Store(s.PID(), &approvalSlot{id: id, ch: ch})
	defer approvals.Delete(s.PID())
	s.broadcast(agent.Event{
		Kind:     agent.EventApprovalRequested,
		ToolName: tool,
		ID:       id,
		Text:     agent.ApprovalPreview(argumentsJSON, 200),
	})
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case answer := <-ch:
		return answer.allow, nil
	case <-ctx.Done():
		return false, fmt.Errorf("approval for %q was interrupted", tool)
	case <-timer.C:
		return false, fmt.Errorf("approval for %q timed out after %s", tool, approvalTimeout)
	}
}

// session is one agent conversation under the root supervisor.
type session struct {
	act.Actor

	loop        *agent.Loop
	subscribers map[gen.PID]struct{}
	store       *sessionstore.Store
	sessionID   string
}

func sessionFactory() gen.ProcessBehavior { return &session{} }

// Init builds this session's own Loop from its SessionArgs, then restores
// the persisted conversation when a store is wired — a resumed session
// (or a supervisor-restarted one, which gets the same SessionArgs per the
// SOFO restart contract) starts from its log, not from zero. A fresh Loop
// is still what a restart yields when nothing was ever persisted.
func (s *session) Init(args ...any) error {
	sessArgs, ok := args[0].(SessionArgs)
	if !ok {
		return fmt.Errorf("sessionhost: expected SessionArgs, got %T", args[0])
	}
	tools := sessArgs.Tools
	if len(sessArgs.AskTools) > 0 {
		tools = agent.NewAskSource(sessArgs.Tools, sessArgs.AskTools, s.consent)
	}
	s.loop = agent.NewLoop(sessArgs.Provider, tools, sessArgs.SystemPrompt)
	s.subscribers = make(map[gen.PID]struct{})
	if sessArgs.Store != nil && sessArgs.SessionID != "" {
		s.store = sessArgs.Store
		s.sessionID = sessArgs.SessionID
		msgs, err := sessArgs.Store.Load(sessArgs.SessionID)
		if err != nil {
			s.Log().Error("sessionhost: restore conversation: %s", err)
			return nil
		}
		s.loop.Restore(msgs)
	}
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
	// Persist the turn's completed state change: the messages Say appended
	// (user + assistant + tool results), appended to the log after the
	// turn finishes. An in-flight turn lost to a crash is exactly that —
	// lost; the log records completed changes, and the driver's reattach
	// path retries the prompt against the restored state.
	if s.store != nil {
		// The exact delta Say recorded, not a position-based diff: a
		// turn that triggered context compaction evicts OLDER messages,
		// so any before/after slice math would index out of range (the
		// live boundsError crash found 2026-09-12).
		if added := s.loop.TakeAppended(); len(added) > 0 {
			if err := s.store.Append(s.sessionID, added); err != nil {
				s.Log().Error("sessionhost: persist turn: %s", err)
			}
		}
	}
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

// sayTurnTimeoutSeconds bounds one SayTurn call attempt. Ergo's calls
// have NO fast-fail when the target dies mid-handling — the caller waits
// out the full timeout — so the bound matters twice over: short enough
// that a dead session is noticed promptly (the alive-check then tells
// death apart from busy), long enough that a normally slow turn does not
// trip it. Found live 2026-09-12: the 5-second default misdiagnosed
// every slow turn (tool calls queued behind the ring waiter's blocking
// mesh_wait_ring on the shared MCP client) as a dead session. A var, not
// a const, so the sessionhost tests can compress it.
var sayTurnTimeoutSeconds = 60

// ErrTurnOutcomeLost reports a turn that completed while the driver's
// call had already timed out: the conversation state is correct and the
// turn ran exactly once, but its reply arrived after the caller stopped
// waiting and was dropped (Ergo's own stale-response semantics). The
// driver treats it as neither success nor failure — it logs and carries
// on without backoff accounting.
var ErrTurnOutcomeLost = fmt.Errorf("sessionhost: the turn completed during a slow wait; its outcome was not observed")

// Say runs one turn on session and blocks until it completes: SayReply.Err
// carries a turn failure (an interrupt arrives as context.Canceled, an
// outcome-lost wait as ErrTurnOutcomeLost), while a non-nil returned
// error means the session process itself is gone (a panic-restart in
// flight, or a stop) and the caller must reattach rather than count it
// as an ordinary failure.
//
// Slow turns are told apart from dead sessions by asking the ROOT (which
// always answers fast) whether the session is still alive: alive means
// the turn is merely still running, and the call then waits for it to
// end by polling status — a busy session answers status only once the
// turn is done.
func SayTurn(n gen.Node, root, session gen.PID, text string) (SayReply, error) {
	for {
		reply, err := n.CallWithTimeout(session, Say{Text: text}, sayTurnTimeoutSeconds)
		if err == nil {
			r, ok := reply.(SayReply)
			if !ok {
				return SayReply{}, fmt.Errorf("sessionhost: session answered Say with %T, want SayReply", reply)
			}
			return r, nil
		}
		if !errors.Is(err, gen.ErrTimeout) {
			return SayReply{}, err
		}
		if !sessionAlive(n, root, session) {
			return SayReply{}, fmt.Errorf("sessionhost: session %s terminated mid-turn", session)
		}
		// Alive and busy: wait for the turn to end. Each status probe is
		// a 5-second-timeout call the busy session answers only once the
		// turn completes; death is re-checked through the root at every
		// step.
		for {
			if _, serr := SessionStatus(n, session); serr == nil {
				return SayReply{Err: ErrTurnOutcomeLost}, nil
			}
			if !sessionAlive(n, root, session) {
				return SayReply{}, fmt.Errorf("sessionhost: session %s terminated mid-turn", session)
			}
		}
	}
}

// sessionAlive reports whether the root still lists pid among its
// children — the fast, always-answerable liveness probe (the root actor
// is never busy).
func sessionAlive(n gen.Node, root, pid gen.PID) bool {
	pids, err := Sessions(n, root)
	if err != nil {
		return false
	}
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
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

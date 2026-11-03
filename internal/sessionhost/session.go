package sessionhost

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
)

// SessionArgs is everything one session actor needs to build its own
// conversation: the provider, the profile's declared tool chain, the fixed
// system prompt, and — when Store and SessionID are both set — the JSONL
// log the conversation is restored from and appended to (D2). Each session
// owns its own agent.Loop; nothing is shared between sessions by
// construction.
type SessionArgs struct {
	Provider     provider.Provider
	SystemPrompt string

	// Toolchain is the profile's declared guard chain
	// (macula-io/macula-lazymesh#100): what the guards wrap and which of
	// them are on. The session supplies the one piece it alone owns -- its
	// own Consent, which puts the question to this session's subscribers
	// (TUI popup, control socket) and takes the answer back out of band --
	// and then builds the chain once, through toolchain.Chain. No other
	// code path may add a guard of its own: a guard that exists here is in
	// every profile, and one assembled beside this chain would be invisible
	// to it.
	Toolchain toolchain.Policy

	// Store persists the conversation as JSONL under SessionID. A nil
	// Store (or an empty SessionID) disables persistence: the session
	// runs in-memory exactly as it did before D2.
	Store     *sessionstore.Store
	SessionID string
}

// Say asks the session's loop to process one user message: the full
// tool-calling round runs, and every intermediate step is forwarded to
// each subscriber as an Event, in order, while the turn runs (#96). Say is
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
// itself survives it. A turn that panics is answered with sayCrashed
// instead, which SayTurn reports as the session gone.
type SayReply struct {
	Err error
}

// sayCrashed is the answer to a Say whose turn panicked: the session
// sends it on its way down, so the driver learns at once that the session
// is gone instead of waiting out the call timeout (#96). SayTurn turns it
// into a returned error, the same as any other death mid-turn.
type sayCrashed struct {
	Reason string
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
	// Rounds and ToolSchemaBytes are the loop's completion rounds so far
	// and the tool-schema bytes they sent (G17).
	Rounds          int
	ToolSchemaBytes int
	// Stop is why the loop's most recent completion round ended (#77), so
	// the cycle's usage line can say a reply was cut instead of leaving it
	// to look like a short answer.
	Stop provider.Stop
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
//
// The slot itself decides who won, under its own lock. That decision
// cannot live in the buffered channel: consent drains the buffer as soon
// as it wakes, so a second caller arriving after the drain but before the
// slot is deleted would find an empty channel, send successfully, and be
// told its answer was the one used — two frontends both getting "yes,
// yours counted" while the second answer fell on the floor. done is what
// makes "answered" mean answered, whichever of those two moments it is.
type approvalSlot struct {
	id string
	ch chan approvalAnswer

	mu   sync.Mutex
	done bool
}

// approvalAnswer is the operator's decision.
type approvalAnswer struct {
	allow bool
}

// answer claims the slot for id and delivers the decision. Only the first
// caller can win: a second answer, an answer for a different question, or
// an answer after the question stopped waiting all report false, so the
// frontend that sent them is told rather than congratulated.
func (s *approvalSlot) answer(id string, allow bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.id != id {
		return false
	}
	select {
	case s.ch <- approvalAnswer{allow: allow}:
		s.done = true
		return true
	default:
		return false
	}
}

// abandon closes the slot without an answer, for the timeout and
// interrupt paths: a decision arriving after the session stopped waiting
// was never going to be used, and must not be reported as if it were.
func (s *approvalSlot) abandon() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
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
	TryAnswerApproval(session, id, allow)
}

// TryAnswerApproval is AnswerApproval reporting whether this answer was
// the one delivered. More than one frontend can be attached to a session,
// so several may answer the same question; exactly one answer reaches the
// session, and only that caller may be told its decision counted. A false
// return means the question was already answered, is no longer
// outstanding, or belongs to a different call — never that it was ignored
// silently by the session.
func TryAnswerApproval(session gen.PID, id string, allow bool) bool {
	slot, ok := approvals.Load(session)
	if !ok {
		return false
	}
	return slot.(*approvalSlot).answer(id, allow)
}

// consent is the session's own Consent: surface the question to every
// subscriber as an EventApprovalRequested, then wait for the answer on
// this turn's private channel, bounded by the turn's context and the
// approval timeout.
//
// It runs inside the loop's tool-call path, on the session's actor
// goroutine, so the question goes out on the turn's own event channel,
// behind the text of the same turn that explains it (#96): the forwarding
// goroutine delivers both, in the order the turn produced them.
func (s *session) consent(ctx context.Context, tool, argumentsJSON string) (bool, error) {
	id := fmt.Sprintf("approve-%d", time.Now().UnixNano())
	ch := make(chan approvalAnswer, 1)
	slot := &approvalSlot{id: id, ch: ch}
	approvals.Store(s.PID(), slot)
	defer approvals.Delete(s.PID())
	s.turnEvents <- agent.Event{
		Kind:     agent.EventApprovalRequested,
		ToolName: tool,
		ID:       id,
		Text:     argumentsJSON, // whole: the operator approves what they read (#39)
	}
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case answer := <-ch:
		return answer.allow, nil
	case <-ctx.Done():
		slot.abandon()
		return false, fmt.Errorf("approval for %q was interrupted", tool)
	case <-timer.C:
		slot.abandon()
		return false, fmt.Errorf("approval for %q timed out after %s", tool, approvalTimeout)
	}
}

// session is one agent conversation under the root supervisor.
type session struct {
	act.Actor

	loop      *agent.Loop
	store     *sessionstore.Store
	sessionID string

	// subscribers is read by the turn's forwarding goroutine while the
	// actor goroutine is blocked in a turn, so it is guarded by mu.
	mu          sync.Mutex
	subscribers map[gen.PID]struct{}

	// turnEvents is the running turn's event channel, set for the length
	// of runSay; consent sends its question on it.
	turnEvents chan<- agent.Event
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
	policy := sessArgs.Toolchain
	// The session owns the consent half of the ask guard: the question goes
	// out on the running turn's event stream, the answer comes back through
	// AnswerApproval. Everything else in the chain is the profile's
	// declaration, unchanged.
	policy.Consent = s.consent
	s.loop = agent.NewLoop(sessArgs.Provider, toolchain.Chain(policy), sessArgs.SystemPrompt)
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

// HandleMessage routes Subscribe. An unknown message is logged and
// dropped, never an error: a non-nil return terminates the process.
func (s *session) HandleMessage(from gen.PID, message any) error {
	if _, ok := message.(Subscribe); ok {
		s.subscribe(from)
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
		defer s.reportCrash(from, ref)
		return SayReply{Err: s.runSay(req.Text)}, nil
	case StatusRequest:
		rounds, schemaBytes := s.loop.Rounds()
		return Status{MessageCount: s.loop.MessageCount(), Usage: s.loop.Usage(), Rounds: rounds, ToolSchemaBytes: schemaBytes, Stop: s.loop.LastStopReason()}, nil
	case Subscribe:
		return s.subscribe(from), nil
	}
	s.Log().Warning("sessionhost: session answered unknown request %T from %s with UnsupportedReply", request, from)
	if err := s.SendResponse(from, ref, UnsupportedReply{Request: request}); err != nil {
		return nil, err
	}
	return nil, nil
}

// subscribe registers pid and reports how many subscribers there are.
func (s *session) subscribe(pid gen.PID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers[pid] = struct{}{}
	return len(s.subscribers)
}

// reportCrash, deferred around a turn, answers the Say with sayCrashed
// when the turn panics and then lets the panic go on, so Ergo still
// terminates the session with TerminateReasonPanic and the supervisor
// restarts it. Without it the driver would learn of the crash only when
// its call timed out (#96).
func (s *session) reportCrash(from gen.PID, ref gen.Ref) {
	r := recover()
	if r == nil {
		return
	}
	// Ergo's own panic line would point here, at the re-panic: log where
	// the turn actually crashed first.
	s.Log().Error("sessionhost: turn panicked: %v\n%s", r, debug.Stack())
	_ = s.SendResponse(from, ref, sayCrashed{Reason: fmt.Sprint(r)})
	panic(r)
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
// restarted.
//
// One forwarding goroutine drains the turn's event channel while Say runs
// and broadcasts each event to the subscribers itself (#96). It used to
// self-Send them through the actor's mailbox, which the actor reads only
// after the turn: every delta then arrived in one burst at the end, and
// an approval question, broadcast directly, overtook the text before it.
// The channel is closed by a defer, so a turn that panics still ends its
// forwarding goroutine instead of leaving it parked.
func (s *session) runSay(text string) error {
	ctx, cancel := context.WithCancel(context.Background())
	turns.Store(s.PID(), cancel)
	defer turns.Delete(s.PID())

	events := make(chan agent.Event, eventBuffer)
	drained := make(chan struct{})
	go func() {
		for ev := range events {
			s.broadcast(ev)
		}
		close(drained)
	}()
	s.turnEvents = events
	err := s.say(ctx, text, events, drained)
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
	// The turn-complete marker is emitted BY the session, after every event
	// of the turn it just ran has been delivered: it rides the same path as
	// the deltas, so the control plane's settle contract (deltas..., then
	// turn_complete) holds regardless of how quickly the driver's SayReply
	// travels to the caller. The driver's own success path deliberately
	// does NOT emit EventListening for the same turn.
	s.broadcast(agent.Event{Kind: agent.EventListening})
	// The turn's failure travels back to the driver as SayReply.Err: the
	// loop has already emitted the EventError describing it, and the
	// retry/backoff policy belongs to whoever drives the session — the
	// session itself survives a failed turn.
	return err
}

// say runs the loop and, however it ends, panic included, closes the
// turn's event channel and waits until its forwarding goroutine has
// delivered everything.
func (s *session) say(ctx context.Context, text string, events chan agent.Event, drained <-chan struct{}) error {
	defer func() {
		s.turnEvents = nil
		close(events)
		<-drained
	}()
	return s.loop.Say(ctx, text, events)
}

// broadcast forwards one event to every subscriber. A dead subscriber's
// Send failure is expected and ignored — subscribers clean themselves up
// by dying, and the event is not retried.
func (s *session) broadcast(ev agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
			if crash, ok := reply.(sayCrashed); ok {
				return SayReply{}, fmt.Errorf("sessionhost: session %s crashed mid-turn: %s", session, crash.Reason)
			}
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

// Package frontend serves lazymesh's unix-socket control plane (D1): a
// controller-facing surface speaking newline-delimited JSON in both
// directions, alongside the TUI. It is an I/O boundary, deliberately NOT
// an Ergo process: socket accept/read/write is exactly the blocking I/O
// D4's conditions keep outside actors, and it attaches to the same bus
// the TUI does — input lands on the driver's input channel, events arrive
// on a broadcast channel, and queries are answered synchronously with a
// short deadline.
//
// Security posture: the socket file is created with mode 0600 in a
// user-owned directory, so only this user's processes can connect — the
// isolation TCP cannot provide. Every accepted connection is then checked
// against the kernel's record of who connected (SO_PEERCRED on Linux,
// LOCAL_PEERCRED on macOS): a peer whose uid is not this process's uid is
// closed before it receives the handshake, so a loosened file mode does
// not open the session. That protects the session, not the controller:
// in a directory another user can write to, they could replace the socket
// and receive what a controller sends, so DefaultSocketPath only uses a
// private directory. On a
// platform with no peer credentials (Windows) Start refuses to create the
// socket. Controllers run as the same user as the session, never via sudo.
//
// Wire protocol (one JSON object per line, both ways):
//
//	controller -> lazymesh:
//	  {"type":"input","session_id":"<id>","text":"<user message>"}
//	  {"type":"interrupt"}
//	  {"type":"approve","id":"<approval-id>","allow":1}   (1 allow, 0 deny)
//	  {"type":"schedule","at":"<RFC3339>","prompt":"..."} (wakes the agent with prompt at that time; ack: {"type":"scheduled","at":...})
//	  {"type":"query","what":"status|rooms|inbox|agents|realms"}
//	  {"type":"shutdown"}
//	  {"type":"hook","event":"pre_tool"}   (gate every tool call; ack {"type":"hooked","event":"pre_tool"})
//	  {"type":"unhook","event":"pre_tool"} (release it; ack {"type":"unhooked","event":"pre_tool"})
//	  {"type":"hook_answer","id":"<hook-id>","allow":1,"reason":"..."}   (1 allow, 0 deny)
//	lazymesh -> controller:
//	  {"type":"session","session_id":"<id>","model":"<provider/model>"}  (on connect)
//	  {"type":"assistant","text":"..."}
//	  {"type":"tool_call","tool":"<name>","args":...}
//	  {"type":"tool_result","tool":"<name>","result":...}
//	  {"type":"error","tool":"<name>","error":"..."}
//	  {"type":"backoff"}
//	  {"type":"max_failures"}
//	  {"type":"approval_request","id":"<approval-id>","tool":"<name>","args":"<preview>"}
//	  {"type":"approval_resolved","id":"<approval-id>","by":"<frontend>"}
//	                             (broadcast to EVERY attached frontend when an
//	                              approval is answered, naming who answered it, so a
//	                              popup that lost the race closes itself)
//	  {"type":"approval_answer","id":"<approval-id>","accepted":1}
//	  {"type":"approval_answer","id":"<approval-id>","accepted":0,"error":"..."}
//	                             (to the one frontend that sent the approve, and only
//	                              it: 1 means its answer is the one the session used,
//	                              0 means another frontend answered first. This is a
//	                              separate message from approval_resolved on purpose —
//	                              a frontend must never read someone else's resolution
//	                              as its own success)
//	  {"type":"ring_pending","ring_id":"<id>","from":"<petname>","purpose":"..."}
//	                             (a ring still awaiting an answer, sent as it arrives)
//	  {"type":"usage","context_tokens":N,"context_window":N,"run_tokens":N}
//	                             (after each completion round whose backend reported
//	                              usage: what the conversation holds, the model's
//	                              window or 0 when unknown, and what this run of the
//	                              session has used: a resumed session starts it at 0.
//	                              A controller skips any type it does not know, so
//	                              one older than this line ignores it)
//	  {"type":"turn_crashed","text":"..."}
//	                             (the turn crashed and the session was replaced: text
//	                              names each step of the turn and its outcome, and says
//	                              nothing was sent again. A turn_complete follows, so a
//	                              controller that skips this type still settles. A turn
//	                              the previous run crashed in arrives once, right after
//	                              the session line, with no turn_complete: no
//	                              controller sent that turn)
//	  {"type":"pre_tool","id":"<hook-id>","tool":"<name>","args":"<full JSON>"}
//	                             (to the hook's controller only; the call waits for hook_answer)
//	  {"type":"turn_complete"}   (the settle point: a controller sends one
//	                              input and waits for this)
//	  {"type":"query_result","what":"...","data":...}
//	  {"type":"error","error":"..."}  (a malformed control message)
package frontend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// Server is the unix-socket control plane. It runs its own goroutines
// (accept loop, per-connection reader, one broadcaster) and is closed by
// Close, which also removes the socket file.
type Server struct {
	path string

	inputCh chan<- string
	events  <-chan agent.Event
	query   func(ctx context.Context, what string) (any, error)
	// interrupt cancels the in-flight turn (the caller's own ctx-cancel
	// mechanism -- see sessionhost.Interrupt); nil means not wired.
	interrupt func()
	// approve answers an approval request; nil means not wired. It reports
	// what became of this call's answer: with more than one frontend
	// attached, answering is a race and exactly one answer is delivered --
	// and a question nobody answered before the clock ran out is a third
	// outcome again (#121), which the operator is told apart from the race.
	approve func(id string, allow bool) sessionhost.ApprovalOutcome
	// schedule arms a deferred prompt; nil means not wired.
	schedule func(at time.Time, prompt string) error
	hook     *PreToolHook
	session  sessionInfo

	// ownUID is the effective uid a connecting peer must have.
	ownUID int
	log    *log.Logger

	ln      net.Listener
	mu      sync.Mutex
	conns   map[net.Conn]*connection
	closed  bool
	shutCh  chan struct{}
	closeFn func()

	// connSeq labels connections in accept order (controller-1, ...).
	connSeq int
	// announced are the lines of events every controller is told once,
	// on connect if it was not connected when they happened (Announce).
	announced [][]byte
	// answeredBy records, per approval id, the frontend whose answer the
	// session used. It is bounded by the session's own rule that at most
	// one approval is outstanding: every new approval request prunes the
	// ids that came before it.
	answeredBy map[string]string
	// localRes carries resolutions to the session's own in-process TUI,
	// which is not one of this server's connections and so cannot be
	// reached by the broadcast. Without it, a human watching the session
	// directly is never told that an attached frontend answered first,
	// and their popup waits out the full approval timeout for nothing.
	localRes chan ApprovalResolved
}

// sessionInfo is what every connecting controller learns on connect.
type sessionInfo struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Leader    string `json:"leader,omitempty"`
}

// Options wires the server onto the lazymesh bus.
type Options struct {
	// Path is the socket file to create; the caller resolves defaults.
	Path string
	// Input receives control input lines as driver prompts — the same
	// channel the TUI's compose line feeds.
	Input chan<- string
	// Events is the broadcast channel the event bridge and the driver
	// both fan loop/driver events into. A slow or absent controller must
	// never stall it: the broadcaster drops rather than blocks.
	Events <-chan agent.Event
	// Query answers a query control message synchronously, within its
	// own deadline. It runs on the connection's reader goroutine.
	Query func(ctx context.Context, what string) (any, error)
	// Interrupt cancels the in-flight turn when a controller sends an
	// interrupt control message; nil means not wired.
	Interrupt func()
	// Approve answers an approval request (G9) when a controller sends an
	// approve control message; nil means not wired. It reports what became
	// of the answer: two frontends can answer the same approval, and the
	// loser must be told rather than left believing it decided the call;
	// an approval that timed out before anyone answered is neither of those
	// and is reported as itself (#121).
	Approve func(id string, allow bool) sessionhost.ApprovalOutcome
	// Schedule arms a deferred prompt (G13) when a controller sends a
	// schedule control message; nil means not wired.
	Schedule func(at time.Time, prompt string) error
	// PreToolHook, when set, lets a controller gate every tool call (G8,
	// see PreToolHook); nil means hook control messages are refused.
	PreToolHook *PreToolHook
	// SessionID identifies this lazymesh session to controllers (the
	// --session-id flag, or a generated default).
	SessionID string
	// Model labels the running provider/model pair.
	Model string
	// Leader is the session's configured leader key (config's leader_key,
	// resolved -- "ctrl+x" unless moved). An attached TUI arms this key,
	// because the session is the thing that will answer it: reading the
	// attaching side's own config would let a session started with
	// --config other.yaml be attached with a different key armed than the
	// one its operator configured (#63).
	Leader string
	// Log receives lifecycle lines; nil means silent.
	Log *log.Logger
}

// connection is one connected controller: a reader goroutine owns the
// inbound side, a bounded out channel feeds the shared writer.
type connection struct {
	out  chan []byte
	conn net.Conn
	// label names this frontend in approval attributions. It is assigned
	// by the server from the accept order, never read from the wire: a
	// control message must not be able to claim to be another frontend.
	label string

	// gone closes when serve starts tearing this connection down, so a
	// broadcast waiting on a full out stops waiting (#98).
	gone chan struct{}
	// Guarded by the server's mu, which broadcast holds: the delta text
	// this controller had no room for yet (holding says there is some),
	// and whether it was cut off for not taking an event that had to
	// arrive.
	held    strings.Builder
	holding bool
	stalled bool
}

// outBuffer bounds how many unsent output lines one controller may have
// queued. Past it, the broadcaster holds back delta text for that
// controller and waits, for at most controllerStallTimeout, on any other
// event (#98).
const outBuffer = 64

// controllerStallTimeout is how long the broadcaster waits for a
// controller with a full queue to take an event that must arrive, before
// it disconnects that controller (#98): a controller that stops reading
// learns it from a closed connection, never from an approval that silently
// was not there. A var so the tests can shorten it.
var controllerStallTimeout = 5 * time.Second

// errPeerCredUnsupported is what peerUID returns on a platform whose
// unix sockets carry no peer credentials.
var errPeerCredUnsupported = errors.New("frontend: peer credentials are not available on this platform")

// Start creates the socket, starts the accept loop, and returns. The
// parent directory must exist and be writable by this user; the socket
// file is created 0600 and removed by Close. Only peers running as this
// process's uid are served.
func Start(opts Options) (*Server, error) {
	return startFor(opts, os.Geteuid())
}

// startFor is Start with the uid a peer must have made explicit, so a
// test can stand in for a peer of another user.
func startFor(opts Options, ownUID int) (*Server, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("frontend: socket path is required")
	}
	if peerCredsSupported == 0 {
		return nil, fmt.Errorf("frontend: control socket refused on this platform: no peer credentials, so another local user could drive this session: %w", errPeerCredUnsupported)
	}
	ln, err := net.Listen("unix", opts.Path)
	if err != nil {
		return nil, fmt.Errorf("frontend: listen on %s: %w", opts.Path, err)
	}
	if err := os.Chmod(opts.Path, 0o600); err != nil {
		ln.Close()
		os.Remove(opts.Path)
		return nil, fmt.Errorf("frontend: chmod %s: %w", opts.Path, err)
	}

	s := &Server{
		path:       opts.Path,
		inputCh:    opts.Input,
		events:     opts.Events,
		query:      opts.Query,
		interrupt:  opts.Interrupt,
		approve:    opts.Approve,
		schedule:   opts.Schedule,
		hook:       opts.PreToolHook,
		session:    sessionInfo{SessionID: opts.SessionID, Model: opts.Model, Leader: opts.Leader},
		ownUID:     ownUID,
		log:        opts.Log,
		ln:         ln,
		conns:      make(map[net.Conn]*connection),
		answeredBy: make(map[string]string),
		localRes:   make(chan ApprovalResolved, 16),
		shutCh:     make(chan struct{}),
	}
	go s.accept()
	go s.broadcast()
	if opts.Log != nil {
		opts.Log.Printf("frontend: control socket on %s (mode 0600)", opts.Path)
	}
	return s, nil
}

// admit reports whether a freshly accepted connection may be served: its
// peer must run as this process's uid. A peer whose credentials cannot be
// read is refused, never assumed to be ours.
func (s *Server) admit(conn net.Conn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return err
	}
	if uid != s.ownUID {
		return fmt.Errorf("frontend: refused a controller running as uid %d (this session runs as uid %d)", uid, s.ownUID)
	}
	return nil
}

// Shutdown is closed exactly once, when a controller sends a shutdown
// control message: the headless run loop selects on it to exit cleanly.
func (s *Server) Shutdown() <-chan struct{} {
	return s.shutCh
}

// Close stops the accept loop, closes every connection, and removes the
// socket file. Idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for _, c := range s.conns {
		c.conn.Close()
	}
	s.conns = make(map[net.Conn]*connection)
	s.mu.Unlock()
	err := s.ln.Close()
	os.Remove(s.path)
	return err
}

// accept runs until Close: every accepted connection gets a handshake
// line, a reader goroutine, and a slot in the broadcast set.
func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		if err := s.admit(conn); err != nil {
			if s.log != nil {
				s.log.Printf("%v", err)
			}
			conn.Close()
			continue
		}
		c := &connection{out: make(chan []byte, outBuffer), conn: conn, gone: make(chan struct{})}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.connSeq++
		c.label = fmt.Sprintf("controller-%d", s.connSeq)
		s.conns[conn] = c
		line, _ := json.Marshal(map[string]any{"type": "session", "session_id": s.session.SessionID, "model": s.session.Model, "leader": s.session.Leader})
		opening := append(line, '\n')
		for _, a := range s.announced {
			opening = append(opening, a...)
		}
		s.mu.Unlock()

		go s.serve(conn, c, opening)
	}
}

// Announce tells every controller of ev once: those connected now at
// once, and each later one right after its handshake. It is for what the
// session found before any controller could be listening -- a turn the
// previous run crashed in (#99) -- and never carries a turn_complete, since
// no controller sent a turn for it to settle. Registration and the replay
// copy share s.mu with this, so no controller gets it twice or not at all.
func (s *Server) Announce(ev agent.Event) {
	line, err := marshalEvent(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.announced = append(s.announced, line)
	for _, c := range s.conns {
		s.deliver(c, ev, line)
	}
}

// broadcast fans one event stream out to every connected controller.
func (s *Server) broadcast() {
	for ev := range s.events {
		line, err := marshalEvent(ev)
		if err != nil {
			continue
		}
		s.mu.Lock()
		// At most one approval is outstanding per session, so a new
		// request makes every earlier id's attribution stale: drop them
		// rather than grow a map that never shrinks.
		if ev.Kind == agent.EventApprovalRequested {
			s.answeredBy = make(map[string]string)
		}
		for _, c := range s.conns {
			s.deliver(c, ev, line)
		}
		s.mu.Unlock()
	}
}

// deliver hands one event to one controller; s.mu is held. Only a delta
// may be held back (#98): it joins the delta text the controller had no
// room for, and goes out with the next room. Any other event first sends
// that held text, then waits for the controller to take it. A controller
// that takes nothing for controllerStallTimeout is disconnected: it finds
// out from a closed connection instead of missing an approval or a
// turn_complete without a trace.
func (s *Server) deliver(c *connection, ev agent.Event, line []byte) {
	if c.stalled {
		return
	}
	if ev.Droppable() {
		c.held.WriteString(ev.Text)
		c.holding = true
		// Marshal the merged delta only when there is room for it: a
		// controller that stays full must not cost a copy of everything
		// held so far on every new delta.
		if len(c.out) == cap(c.out) {
			return
		}
		held, err := c.heldLine()
		if err != nil {
			return
		}
		select {
		case c.out <- held:
			c.release()
		default:
		}
		return
	}
	if c.holding {
		held, err := c.heldLine()
		if err == nil && !s.await(c, held) {
			return
		}
		c.release()
	}
	s.await(c, line)
}

// heldLine is the delta text c had no room for, as one delta line.
func (c *connection) heldLine() ([]byte, error) {
	return marshalEvent(agent.Event{Kind: agent.EventAssistantDelta, Text: c.held.String()})
}

func (c *connection) release() {
	c.held.Reset()
	c.holding = false
}

// await waits for c to take line, and disconnects c if it does not within
// controllerStallTimeout. Reports whether line was taken.
func (s *Server) await(c *connection, line []byte) bool {
	timer := time.NewTimer(controllerStallTimeout)
	defer timer.Stop()
	select {
	case c.out <- line:
		return true
	case <-c.gone:
		return false
	case <-timer.C:
		c.stalled = true
		if s.log != nil {
			s.log.Printf("frontend: %s took no event for %s; disconnecting it", c.label, controllerStallTimeout)
		}
		c.conn.Close()
		return false
	}
}

// serve owns one connection: the handshake write and out-drain on a
// writer goroutine, the inbound read loop on the reader goroutine.
func (s *Server) serve(conn net.Conn, c *connection, handshake []byte) {
	writerDone := make(chan struct{})
	go func() {
		defer conn.Close()
		defer close(writerDone)
		conn.Write(handshake)
		for line := range c.out {
			if _, err := conn.Write(line); err != nil {
				return
			}
		}
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		if !s.handleLine(conn, c, scanner.Bytes()) {
			break
		}
	}
	// Nothing may send to c.out once it is closed: release a broadcast
	// waiting on it, leave the broadcast set (broadcast sends under s.mu),
	// and fail closed a hook this controller held, which stops the hook
	// sending to it.
	close(c.gone)
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	if s.hook != nil {
		s.hook.drop(c)
	}
	// The writer drains whatever is queued, then closes the connection,
	// which ends any scan still in flight.
	close(c.out)
	<-writerDone
}

// handleLine routes one inbound control message. Returns false when the
// connection should end (shutdown). Replies go to the speaking connection
// only, via c.out.
func (s *Server) handleLine(conn net.Conn, c *connection, raw []byte) bool {
	reply := func(v any) {
		line, err := json.Marshal(v)
		if err != nil {
			return
		}
		select {
		case c.out <- append(line, '\n'):
		default:
		}
	}
	var msg struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
		What      string `json:"what"`
		ID        string `json:"id"`
		Allow     int    `json:"allow"`
		At        string `json:"at"`
		Prompt    string `json:"prompt"`
		Event     string `json:"event"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		reply(map[string]any{"type": "error", "error": "malformed control message"})
		return true
	}
	switch msg.Type {
	case "input":
		if msg.Text == "" {
			reply(map[string]any{"type": "error", "error": "input requires text"})
			return true
		}
		// Blocking on purpose: the driver drains this channel at every
		// turn boundary, so a controller's input is never silently
		// dropped, and the slow-consumer rule applies to output, not
		// input.
		s.inputCh <- msg.Text
		return true
	case "interrupt":
		if s.interrupt == nil {
			reply(map[string]any{"type": "error", "error": "interrupt is not wired"})
			return true
		}
		// No ack: the turn's own event stream (EventError, then the
		// settle-point turn_complete) IS the answer to an interrupt.
		s.interrupt()
		return true
	case "approve":
		if s.approve == nil {
			reply(map[string]any{"type": "error", "error": "approve is not wired"})
			return true
		}
		if msg.ID == "" {
			reply(map[string]any{"type": "error", "error": "approve requires an approval id"})
			return true
		}
		s.answerApproval(c, msg.ID, msg.Allow == 1, reply)
		return true
	case "hook", "unhook":
		if s.hook == nil {
			reply(map[string]any{"type": "error", "error": "hooks are not wired"})
			return true
		}
		if msg.Event != "pre_tool" {
			reply(map[string]any{"type": "error", "error": "unknown hook event " + msg.Event + " (only pre_tool)"})
			return true
		}
		change, ack := s.hook.register, "hooked"
		if msg.Type == "unhook" {
			change, ack = s.hook.unhook, "unhooked"
		}
		if err := change(c); err != nil {
			reply(map[string]any{"type": "error", "error": err.Error()})
			return true
		}
		reply(map[string]any{"type": ack, "event": msg.Event})
		return true
	case "hook_answer":
		if s.hook == nil {
			reply(map[string]any{"type": "error", "error": "hooks are not wired"})
			return true
		}
		if msg.ID == "" {
			reply(map[string]any{"type": "error", "error": "hook_answer requires the request id"})
			return true
		}
		s.hook.answer(c, msg.ID, msg.Allow == 1, msg.Reason)
		return true
	case "schedule":
		if s.schedule == nil {
			reply(map[string]any{"type": "error", "error": "schedule is not wired"})
			return true
		}
		at, err := time.Parse(time.RFC3339, msg.At)
		if err != nil {
			reply(map[string]any{"type": "error", "error": "schedule requires a valid RFC3339 at: " + err.Error()})
			return true
		}
		if msg.Prompt == "" {
			reply(map[string]any{"type": "error", "error": "schedule requires a prompt"})
			return true
		}
		if err := s.schedule(at, msg.Prompt); err != nil {
			reply(map[string]any{"type": "error", "error": err.Error()})
			return true
		}
		reply(map[string]any{"type": "scheduled", "at": msg.At})
		return true
	case "query":
		if s.query == nil {
			reply(map[string]any{"type": "error", "error": "queries are not available"})
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		data, err := s.query(ctx, msg.What)
		cancel()
		if err != nil {
			reply(map[string]any{"type": "error", "error": "query " + msg.What + " failed: " + err.Error()})
			return true
		}
		reply(map[string]any{"type": "query_result", "what": msg.What, "data": data})
		return true
	case "shutdown":
		select {
		case <-s.shutCh:
		default:
			close(s.shutCh)
		}
		return false
	default:
		reply(map[string]any{"type": "error", "error": "unknown control type " + msg.Type})
		return true
	}
}

// refusalFor names the frontend that answered first, when this server knows
// it, and otherwise falls back to approvalRefusal's wording.
func (s *Server) refusalFor(id string, outcome sessionhost.ApprovalOutcome) string {
	if outcome == sessionhost.ApprovalAlreadyAnswered {
		s.mu.Lock()
		winner, known := s.answeredBy[id]
		s.mu.Unlock()
		if known && winner != "" {
			return "approval " + id + " was already answered by " + winner
		}
	}
	return approvalRefusal(id, outcome)
}

// approvalRefusal says why a frontend's answer was not the one the session
// used. The distinctions matter to the person who pressed the key: "another
// frontend answered it" and "nobody answered it in time" look identical from
// a bare false, and before #121 the second was reported as the first -- the
// operator was told a colleague had decided a call that the clock refused.
func approvalRefusal(id string, outcome sessionhost.ApprovalOutcome) string {
	switch outcome {
	case sessionhost.ApprovalTimedOut:
		return "approval " + id + " timed out with no answer; the call was refused before this one arrived"
	case sessionhost.ApprovalInterrupted:
		return "approval " + id + " was interrupted before it was answered"
	case sessionhost.ApprovalNotOutstanding:
		return "approval " + id + " is not outstanding in this session"
	case sessionhost.ApprovalOtherQuestion:
		return "approval " + id + " is not the question this session is waiting on"
	default:
		return "approval " + id + " was answered before this one arrived"
	}
}

// answerApproval records one frontend's answer and tells the frontends
// how it was resolved. The session decides which answer counts (Approve
// reports it); this only makes that decision visible.
//
// The losing frontend is refused explicitly. Before this existed the
// second answer vanished into a select/default, so a human could press
// deny, see nothing at all, and reasonably believe they had stopped a
// call that had already run.
func (s *Server) answerApproval(c *connection, id string, allow bool, reply func(any)) {
	by := "tui"
	if c != nil {
		by = c.label
	}
	if outcome := s.approve(id, allow); outcome != sessionhost.ApprovalAccepted {
		reply(map[string]any{"type": "approval_answer", "id": id, "accepted": 0,
			"error": s.refusalFor(id, outcome)})
		return
	}
	reply(map[string]any{"type": "approval_answer", "id": id, "accepted": 1})
	s.mu.Lock()
	s.answeredBy[id] = by
	resolved, err := json.Marshal(map[string]any{"type": "approval_resolved", "id": id, "by": by})
	if err == nil {
		resolved = append(resolved, '\n')
		for _, conn := range s.conns {
			select {
			case conn.out <- resolved:
			default:
			}
		}
	}
	s.mu.Unlock()

	// The in-process TUI hears it too: it is a frontend like any other,
	// and a popup it is showing has just been decided by someone else.
	select {
	case s.localRes <- ApprovalResolved{ID: id, By: by}:
	default:
	}
}

// Resolutions carries every approval answered while this server is up,
// for the session's own in-process TUI. A TUI with no control socket has
// no need of it; one running beside a socket does, or an attached
// frontend's answer would leave its popup waiting for a timeout that has
// nothing left to wait for.
func (s *Server) Resolutions() <-chan ApprovalResolved {
	return s.localRes
}

// AnswerLocal answers an approval on behalf of the session's own
// in-process TUI, which is not one of this server's connections. It runs
// the same accounting as a controller's answer, so a frontend attached
// over the socket that loses the race is told the TUI answered rather
// than being left to assume its own answer landed.
func (s *Server) AnswerLocal(id string, allow bool) error {
	s.answerApproval(nil, id, allow, func(v any) {
		msg, ok := v.(map[string]any)
		if !ok {
			return
		}
		if e, ok := msg["error"].(string); ok {
			s.logf("%s", e)
		}
	})
	return nil
} // logf writes one lifecycle line when a logger is wired.
func (s *Server) logf(format string, args ...any) {
	if s.log != nil {
		s.log.Printf(format, args...)
	}
}

// marshalEvent encodes one agent event as a control-plane output line —
// the wire shape plus its newline delimiter, so every consumer of this
// function sends a complete line. EventListening is the settle point
// controllers wait on, so it travels as turn_complete.
func marshalEvent(ev agent.Event) ([]byte, error) {
	var line []byte
	var err error
	switch ev.Kind {
	case agent.EventAssistantMessage:
		// stop/stop_detail (#77): an attached TUI renders the cut marker
		// from these, so dropping them here would leave the marker working
		// only for an in-process TUI -- the setup nobody debugs.
		line, err = json.Marshal(map[string]any{"type": "assistant", "text": ev.Text, "stop": ev.Stop.Reason, "stop_detail": ev.Stop.Detail})
	case agent.EventAssistantDelta:
		line, err = json.Marshal(map[string]any{"type": "delta", "text": ev.Text})
	case agent.EventToolCall:
		line, err = json.Marshal(map[string]any{"type": "tool_call", "tool": ev.ToolName, "args": ev.Text})
	case agent.EventToolResult:
		line, err = json.Marshal(map[string]any{"type": "tool_result", "tool": ev.ToolName, "result": ev.Text})
	case agent.EventError:
		errText := ""
		if ev.Err != nil {
			errText = ev.Err.Error()
		}
		line, err = json.Marshal(map[string]any{"type": "error", "tool": ev.ToolName, "error": errText})
	case agent.EventBackoff:
		line, err = json.Marshal(map[string]any{"type": "backoff"})
	case agent.EventMaxFailuresReached:
		line, err = json.Marshal(map[string]any{"type": "max_failures"})
	case agent.EventApprovalRequested:
		line, err = json.Marshal(map[string]any{"type": "approval_request", "id": ev.ID, "tool": ev.ToolName, "args": ev.Text})
	case agent.EventListening:
		line, err = json.Marshal(map[string]any{"type": "turn_complete"})
	case agent.EventRingPending:
		line, err = json.Marshal(map[string]any{"type": "ring_pending", "ring_id": ev.ID, "from": ev.From, "purpose": ev.Text})
	case agent.EventTurnCrashed:
		line, err = json.Marshal(map[string]any{"type": "turn_crashed", "text": ev.Text})
	case agent.EventUsageReported:
		// Added with #78 gap 4. A controller older than that skips a type
		// it does not know (parseEvent's default), so this line costs it
		// nothing; a newer one attached to an older session simply never
		// sees one and shows no figure.
		line, err = json.Marshal(map[string]any{"type": "usage", "context_tokens": ev.Usage.ContextTokens, "context_window": ev.Usage.ContextWindow, "run_tokens": ev.Usage.RunTokens})
	default:
		return nil, fmt.Errorf("frontend: unknown event kind %v", ev.Kind)
	}
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// DefaultSocketPath resolves the control socket's conventional location:
// $XDG_RUNTIME_DIR first (the user's private runtime dir), then a
// per-user directory under /tmp. The file itself is 0600 either way.
//
// The directory must be private: a real directory (not a symlink) owned
// by this user with no group or other permissions. Another user who
// created /tmp/lazymesh-<uid> first, or any directory they can write to,
// could replace the socket after Start; the peer-uid check keeps them out
// of the session, but a controller dialling the path would then talk to
// them. "" means no private directory was found.
func DefaultSocketPath(sessionID string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("lazymesh-%d", os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if peerCredsSupported == 0 {
		// Start refuses the socket on this platform, with the reason.
		return filepath.Join(dir, sessionID+".sock")
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 || ownerUID(fi) != os.Getuid() {
		return ""
	}
	return filepath.Join(dir, sessionID+".sock")
}

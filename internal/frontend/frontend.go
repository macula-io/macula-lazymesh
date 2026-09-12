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
// isolation TCP cannot provide. SO_PEERCRED peer-uid verification is the
// next hardening step, recorded in the gaps plan rather than left as a
// comment here.
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
//	lazymesh -> controller:
//	  {"type":"session","session_id":"<id>","model":"<provider/model>"}  (on connect)
//	  {"type":"assistant","text":"..."}
//	  {"type":"tool_call","tool":"<name>","args":...}
//	  {"type":"tool_result","tool":"<name>","result":...}
//	  {"type":"error","tool":"<name>","error":"..."}
//	  {"type":"backoff"}
//	  {"type":"max_failures"}
//	  {"type":"approval_request","id":"<approval-id>","tool":"<name>","args":"<preview>"}
//	  {"type":"turn_complete"}   (the settle point: a controller sends one
//	                              input and waits for this)
//	  {"type":"query_result","what":"...","data":...}
//	  {"type":"error","error":"..."}  (a malformed control message)
package frontend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
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
	// approve answers an approval request; nil means not wired.
	approve func(id string, allow bool)
	// schedule arms a deferred prompt; nil means not wired.
	schedule func(at time.Time, prompt string) error
	session  sessionInfo

	ln      net.Listener
	mu      sync.Mutex
	conns   map[net.Conn]*connection
	closed  bool
	shutCh  chan struct{}
	closeFn func()
}

// sessionInfo is what every connecting controller learns on connect.
type sessionInfo struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
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
	// approve control message; nil means not wired.
	Approve func(id string, allow bool)
	// Schedule arms a deferred prompt (G13) when a controller sends a
	// schedule control message; nil means not wired.
	Schedule func(at time.Time, prompt string) error
	// SessionID identifies this lazymesh session to controllers (the
	// --session-id flag, or a generated default).
	SessionID string
	// Model labels the running provider/model pair.
	Model string
	// Log receives lifecycle lines; nil means silent.
	Log *log.Logger
}

// connection is one connected controller: a reader goroutine owns the
// inbound side, a bounded out channel feeds the shared writer.
type connection struct {
	out  chan []byte
	conn net.Conn
}

// outBuffer bounds how many unsent output lines one controller may have
// queued before the broadcaster drops for it — a slow controller loses
// events, never the session's cadence.
const outBuffer = 64

// Start creates the socket, starts the accept loop, and returns. The
// parent directory must exist and be writable by this user; the socket
// file is created 0600 and removed by Close.
func Start(opts Options) (*Server, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("frontend: socket path is required")
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
		path:      opts.Path,
		inputCh:   opts.Input,
		events:    opts.Events,
		query:     opts.Query,
		interrupt: opts.Interrupt,
		approve:   opts.Approve,
		schedule:  opts.Schedule,
		session:   sessionInfo{SessionID: opts.SessionID, Model: opts.Model},
		ln:        ln,
		conns:     make(map[net.Conn]*connection),
		shutCh:    make(chan struct{}),
	}
	go s.accept()
	go s.broadcast()
	if opts.Log != nil {
		opts.Log.Printf("frontend: control socket on %s (mode 0600)", opts.Path)
	}
	return s, nil
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
		c := &connection{out: make(chan []byte, outBuffer), conn: conn}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = c
		s.mu.Unlock()

		line, _ := json.Marshal(map[string]any{"type": "session", "session_id": s.session.SessionID, "model": s.session.Model})
		go s.serve(conn, c, append(line, '\n'))
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
		for _, c := range s.conns {
			select {
			case c.out <- line:
			default:
			}
		}
		s.mu.Unlock()
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
	// The writer drains whatever is queued, then closes the connection,
	// which ends any scan still in flight.
	close(c.out)
	<-writerDone
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
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
		s.approve(msg.ID, msg.Allow == 1)
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

// marshalEvent encodes one agent event as a control-plane output line —
// the wire shape plus its newline delimiter, so every consumer of this
// function sends a complete line. EventListening is the settle point
// controllers wait on, so it travels as turn_complete.
func marshalEvent(ev agent.Event) ([]byte, error) {
	var line []byte
	var err error
	switch ev.Kind {
	case agent.EventAssistantMessage:
		line, err = json.Marshal(map[string]any{"type": "assistant", "text": ev.Text})
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
func DefaultSocketPath(sessionID string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("lazymesh-%d", os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, sessionID+".sock")
}

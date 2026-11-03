package frontend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// Controller is the controller side of the control plane: it dials one
// session's socket and speaks the same NDJSON the server documents. It
// exists for `lazymesh attach`, which drives the ordinary TUI over the
// wire instead of in-process, but it is a plain client of the published
// protocol — nothing here is privileged over any other controller.
//
// It adds no reach: Dial opens the same 0600 socket in the same 0700
// directory and the server's peer-uid check applies to it exactly as to
// any other controller. A Controller running as another user cannot
// connect at all, which is the boundary the threat model rests on.
type Controller struct {
	conn    net.Conn
	session SessionInfo

	events    chan agent.Event
	approvals chan ApprovalResolved

	mu      sync.Mutex
	pending map[string]chan error // approval id -> the verdict for our answer
	closed  bool
	done    chan struct{}
	// closing closes on Close, releasing a reader that is waiting for the
	// TUI to take an event that must arrive (#98).
	closing chan struct{}
}

// SessionInfo is the handshake a controller learns on connect.
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	// Leader is the session's configured leader key, empty from a session
	// that predates it (#63) -- which resolves to the default.
	Leader string `json:"leader,omitempty"`
}

// ApprovalResolved reports that an approval was answered, and by which
// frontend. Every attached frontend receives one for every approval it
// was shown, so a popup that lost the race closes itself rather than
// waiting out the session's approval timeout.
type ApprovalResolved struct {
	ID string
	By string

	// Note is a local explanation a frontend attaches about its OWN
	// answer, when that answer was not the one used. It never travels on
	// the wire and is empty for a resolution received from the server.
	Note string
}

// Dial connects to a session's control socket and reads the handshake.
// The returned Controller owns a reader goroutine until Close.
func Dial(path string) (*Controller, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("frontend: dial %s: %w", path, err)
	}
	c := &Controller{
		conn:      conn,
		events:    make(chan agent.Event, 64),
		approvals: make(chan ApprovalResolved, 16),
		pending:   make(map[string]chan error),
		done:      make(chan struct{}),
		closing:   make(chan struct{}),
	}
	// The server already refuses a peer that is not this uid, but that
	// only protects the session. A controller given an explicit socket
	// path bypasses DefaultSocketPath's private-directory rule, so it must
	// check the far end itself: someone who planted a socket in a
	// directory they can write to would otherwise receive what a
	// controller sends and answer in the session's name.
	if peerCredsSupported == 0 {
		conn.Close()
		return nil, fmt.Errorf("frontend: refusing to attach on this platform: no peer credentials, so the far end of %s cannot be verified", path)
	}
	uid, err := peerUID(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("frontend: cannot verify who is listening on %s: %w", path, err)
	}
	if own := os.Geteuid(); uid != own {
		conn.Close()
		return nil, fmt.Errorf("frontend: %s is served by uid %d, not uid %d; refusing to attach", path, uid, own)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("frontend: read handshake: %w", err)
	}
	var hs struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
		Leader    string `json:"leader"`
	}
	if err := json.Unmarshal(line, &hs); err != nil || hs.Type != "session" {
		conn.Close()
		return nil, fmt.Errorf("frontend: %s did not open with a session handshake", path)
	}
	c.session = SessionInfo{SessionID: hs.SessionID, Model: hs.Model, Leader: hs.Leader}
	go c.read(r)
	return c, nil
}

// Session is what the server reported on connect.
func (c *Controller) Session() SessionInfo { return c.session }

// Events is the session's event stream as this controller sees it. It is
// closed when the connection ends. A slow reader drops events rather than
// stalling the session, matching the server's own rule.
func (c *Controller) Events() <-chan agent.Event { return c.events }

// Approvals reports every approval answered while this controller is
// attached, including ones another frontend answered first.
func (c *Controller) Approvals() <-chan ApprovalResolved { return c.approvals }

// Say composes one user message to the session.
func (c *Controller) Say(text string) error {
	return c.send(map[string]any{"type": "input", "text": text})
}

// Interrupt cancels the session's in-flight turn.
func (c *Controller) Interrupt() error {
	return c.send(map[string]any{"type": "interrupt"})
}

// approveWait bounds how long Approve waits for the session's verdict.
// The server replies to every approve message, so this is a bound on a
// broken server rather than on the session's own approval timeout.
const approveWait = 15 * time.Second

// Approve answers an approval and reports whether that answer was the one
// delivered. A nil error means it was. A non-nil error means the approval
// was already answered — by another frontend — and names it when the
// server could say, so the caller never believes a decision took effect
// when it did not.
func (c *Controller) Approve(id string, allow bool) error {
	verdict := make(chan error, 1)
	c.mu.Lock()
	if _, busy := c.pending[id]; busy {
		c.mu.Unlock()
		return fmt.Errorf("frontend: approval %s is already being answered by this controller", id)
	}
	c.pending[id] = verdict
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.send(map[string]any{"type": "approve", "id": id, "allow": boolInt(allow)}); err != nil {
		return err
	}
	select {
	case err := <-verdict:
		return err
	case <-time.After(approveWait):
		return fmt.Errorf("frontend: no verdict for approval %s within %s", id, approveWait)
	case <-c.done:
		return fmt.Errorf("frontend: connection closed while answering approval %s", id)
	}
}

// boolInt encodes a bool the way the wire carries it: 1 or 0, never a
// JSON boolean.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Close detaches this frontend. It does not stop the session: the server
// treats a disconnect as one controller leaving.
func (c *Controller) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	close(c.closing)
	err := c.conn.Close()
	return err
}

// send writes one control message.
func (c *Controller) send(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("frontend: encode control message: %w", err)
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("frontend: write control message: %w", err)
	}
	return nil
}

// read consumes the session's output until the connection ends, routing
// events to Events, resolutions to Approvals, and approval verdicts to the
// Approve call waiting on them.
func (c *Controller) read(r *bufio.Reader) {
	defer close(c.done)
	defer close(c.events)
	// Only a delta may be held back from a TUI that is not keeping up; an
	// approval request or a turn_complete waits for it (#98).
	events := agent.NewOutlet(c.events)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			c.failPending()
			return
		}
		var head struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			By       string `json:"by"`
			Error    string `json:"error"`
			Accepted int    `json:"accepted"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			continue
		}

		switch head.Type {
		case "approval_resolved":
			// The broadcast: every frontend learns who answered. It is
			// deliberately NOT a verdict for a pending Approve — a
			// frontend that registered a waiter and then lost the race
			// would otherwise read someone else's resolution as its own
			// success.
			select {
			case c.approvals <- ApprovalResolved{ID: head.ID, By: head.By}:
			default:
			}
			continue
		case "approval_answer":
			// The verdict on THIS controller's own answer.
			var err error
			if head.Accepted != 1 {
				err = fmt.Errorf("%s", head.Error)
			}
			c.deliver(head.ID, err)
			continue
		}

		ev, ok := parseEvent(line)
		if !ok {
			continue
		}
		if !events.Send(c.closing, ev) {
			c.failPending()
			return
		}
	}
}

// deliver hands an approval id's verdict to the Approve call waiting on it.
func (c *Controller) deliver(id string, err error) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- err
	}
}

// failPending releases every waiting Approve when the connection dies, so
// a detach during an answer cannot hang the caller.
func (c *Controller) failPending() {
	c.mu.Lock()
	waiters := c.pending
	c.pending = make(map[string]chan error)
	c.mu.Unlock()
	for _, ch := range waiters {
		ch <- fmt.Errorf("frontend: connection closed before the approval was answered")
	}
}

// parseEvent turns one output line back into an agent.Event — the inverse
// of marshalEvent, kept beside it so the two shapes cannot drift apart
// unnoticed. ok is false for lines that are not events.
//
// The field names here are the wire's, not agent.Event's: a tool call and
// an approval carry their text under "args", a tool result under
// "result", a ring under "purpose" and "ring_id". Reading "text" for all
// of them is exactly the bug TestEveryEventSurvivesTheRoundTrip exists to
// catch — it showed an operator every approval with empty arguments.
func parseEvent(line []byte) (agent.Event, bool) {
	var raw struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Tool    string `json:"tool"`
		Args    string `json:"args"`
		Result  string `json:"result"`
		Error   string `json:"error"`
		ID      string `json:"id"`
		RingID  string `json:"ring_id"`
		From    string `json:"from"`
		Purpose string `json:"purpose"`

		ContextTokens int `json:"context_tokens"`
		ContextWindow int `json:"context_window"`
		RunTokens     int `json:"run_tokens"`

		Stop       string `json:"stop"`
		StopDetail string `json:"stop_detail"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return agent.Event{}, false
	}
	switch raw.Type {
	case "assistant":
		return agent.Event{Kind: agent.EventAssistantMessage, Text: raw.Text, Stop: provider.Stop{Reason: provider.StopReason(raw.Stop), Detail: raw.StopDetail}}, true
	case "delta":
		return agent.Event{Kind: agent.EventAssistantDelta, Text: raw.Text}, true
	case "tool_call":
		return agent.Event{Kind: agent.EventToolCall, ToolName: raw.Tool, Text: raw.Args}, true
	case "tool_result":
		return agent.Event{Kind: agent.EventToolResult, ToolName: raw.Tool, Text: raw.Result}, true
	case "error":
		return agent.Event{Kind: agent.EventError, ToolName: raw.Tool, Err: errString(raw.Error)}, true
	case "backoff":
		return agent.Event{Kind: agent.EventBackoff}, true
	case "max_failures":
		return agent.Event{Kind: agent.EventMaxFailuresReached}, true
	case "approval_request":
		return agent.Event{Kind: agent.EventApprovalRequested, ID: raw.ID, ToolName: raw.Tool, Text: raw.Args}, true
	case "turn_complete":
		return agent.Event{Kind: agent.EventListening}, true
	case "ring_pending":
		return agent.Event{Kind: agent.EventRingPending, ID: raw.RingID, From: raw.From, Text: raw.Purpose}, true
	case "usage":
		return agent.Event{Kind: agent.EventUsageReported, Usage: agent.UsageReport{ContextTokens: raw.ContextTokens, ContextWindow: raw.ContextWindow, RunTokens: raw.RunTokens}}, true
	default:
		return agent.Event{}, false
	}
}

// errString is a plain error carrying text, or nil for an empty one.
func errString(s string) error {
	if s == "" {
		return nil
	}
	return fmt.Errorf("%s", s)
}

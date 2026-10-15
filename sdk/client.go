// Package sdk is the Go client for lazymesh's unix-socket control plane
// (the wire protocol documented in internal/frontend): dial one session's
// socket, drive it headlessly, and read its turns back as structured
// values — the building block the G5 example (two lazymesh processes on
// one box handing work back and forth with zero mesh round-trips) uses,
// and what an editor plugin or daemon would embed.
//
// The client is deliberately stdlib-only (net + bufio + json) so embedding
// it anywhere costs one dependency: this module itself.
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// SessionInfo is what the server reports on connect.
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
}

// ToolCall is one tool invocation the agent made during a turn.
type ToolCall struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// ToolResult is one tool's outcome during a turn.
type ToolResult struct {
	Tool   string `json:"tool"`
	Result string `json:"result"`
}

// PendingRing is a ring another agent placed to this session that is
// still awaiting an answer: RingID is what an answer must carry.
type PendingRing struct {
	RingID  string `json:"ring_id"`
	From    string `json:"from"`
	Purpose string `json:"purpose"`
}

// Turn is one completed agent turn: the streamed deltas, the completed
// assistant text (authoritative), every tool activity, and any errors.
type Turn struct {
	Deltas      []string     `json:"deltas,omitempty"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
	// PendingRings are rings surfaced while the turn ran, each still
	// awaiting an answer.
	PendingRings []PendingRing `json:"pending_rings,omitempty"`
	BackedOff    bool          `json:"backed_off,omitempty"`
	MaxFailures  bool          `json:"max_failures,omitempty"`
}

// Client is one connection to one lazymesh session's control socket.
type Client struct {
	conn  net.Conn
	r     *bufio.Reader
	write sync.Mutex
	read  sync.Mutex // one Say/Query at a time owns the inbound stream
	info  SessionInfo
	// closeMu guards closed. Close does not take read, so it can end a
	// read that is blocked holding it.
	closeMu sync.Mutex
	closed  bool
	// preTool decides pre_tool requests while this client holds the
	// session's pre-tool hook; set by HookPreTool, read by collectTurn.
	preTool PreToolFunc
}

// PreToolFunc decides one tool call the session wants to make: allow it,
// or refuse it with a reason the model sees.
type PreToolFunc func(tool, argsJSON string) (allow bool, reason string)

// Dial connects to the control socket at path and reads the handshake.
// ctx bounds the connect and handshake.
func Dial(ctx context.Context, path string) (*Client, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("sdk: dial %s: %w", path, err)
	}
	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	first, err := c.readLine()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("sdk: read handshake: %w", err)
	}
	if err := json.Unmarshal(first, &c.info); err != nil || c.info.SessionID == "" {
		conn.Close()
		return nil, fmt.Errorf("sdk: bad handshake from %s: %s", path, first)
	}
	conn.SetDeadline(time.Time{})
	return c, nil
}

// Session returns what the handshake reported.
func (c *Client) Session() SessionInfo {
	return c.info
}

// Close closes the connection, which also ends a Say, Query or
// ServeHook blocked reading it. Idempotent.
func (c *Client) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Say sends one input and blocks until the turn's settle point
// (turn_complete) or ctx runs out. It owns the inbound stream for its
// duration: callers must serialize Say and Query, which the client
// enforces with its read lock.
func (c *Client) Say(ctx context.Context, text string) (*Turn, error) {
	c.read.Lock()
	defer c.read.Unlock()
	if err := c.send(map[string]any{"type": "input", "session_id": c.info.SessionID, "text": text}); err != nil {
		return nil, err
	}
	return c.collectTurn(ctx)
}

// Query asks the session for live state (status|rooms|inbox|agents|
// realms) and returns the query_result's data raw, so callers decode it
// against their own shapes.
func (c *Client) Query(ctx context.Context, what string) (json.RawMessage, error) {
	c.read.Lock()
	defer c.read.Unlock()
	if err := c.send(map[string]any{"type": "query", "what": what}); err != nil {
		return nil, err
	}
	return c.awaitQueryResult(ctx, what)
}

// HookPreTool makes this client the session's pre-tool hook: from now on
// every tool call waits for fn. Requests are answered only while this
// client is reading: inside Say, for turns it started, or inside
// ServeHook, for every turn. A headless agent's turns mostly start from
// the mesh (rooms, rings, wakeups), not from Say, so a policy process
// should hold the hook on its own client and run ServeHook; a request
// nobody reads is refused after two minutes. The hook fails closed: if
// this client disconnects without Unhook, the session refuses every tool
// call until a controller hooks it again, so hook again after any
// reconnect. Only one controller can hold the hook.
func (c *Client) HookPreTool(ctx context.Context, fn PreToolFunc) error {
	c.read.Lock()
	defer c.read.Unlock()
	if err := c.send(map[string]any{"type": "hook", "event": "pre_tool"}); err != nil {
		return err
	}
	if err := c.awaitAck(ctx, "hooked"); err != nil {
		return err
	}
	c.preTool = fn
	return nil
}

// ServeHook answers pre_tool requests with the function HookPreTool set
// until ctx ends or the connection fails, ignoring every other line. It
// owns the inbound stream while it runs, so a client serving the hook
// does not also Say or Query.
func (c *Client) ServeHook(ctx context.Context) error {
	c.read.Lock()
	defer c.read.Unlock()
	stop := context.AfterFunc(ctx, func() { c.conn.SetReadDeadline(time.Now()) })
	defer func() {
		stop()
		c.conn.SetReadDeadline(time.Time{})
	}()
	for {
		line, err := c.readLine()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("sdk: serve hook: %w", err)
		}
		var ev struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Tool string `json:"tool"`
			Args string `json:"args"`
		}
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "pre_tool" {
			continue
		}
		if err := c.answerPreTool(ev.ID, ev.Tool, ev.Args); err != nil {
			return err
		}
	}
}

// Unhook releases the pre-tool hook: tool calls run ungated again.
func (c *Client) Unhook(ctx context.Context) error {
	c.read.Lock()
	defer c.read.Unlock()
	if err := c.send(map[string]any{"type": "unhook", "event": "pre_tool"}); err != nil {
		return err
	}
	if err := c.awaitAck(ctx, "unhooked"); err != nil {
		return err
	}
	c.preTool = nil
	return nil
}

// awaitAck reads lines until the ack of the given type, or an error
// reply. The caller holds the read lock.
func (c *Client) awaitAck(ctx context.Context, ack string) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	for {
		line, err := c.readLine()
		if err != nil {
			return fmt.Errorf("sdk: read %s ack: %w", ack, err)
		}
		var ev struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("sdk: decode %s ack %q: %w", ack, line, err)
		}
		switch ev.Type {
		case ack:
			return nil
		case "error":
			return fmt.Errorf("sdk: %s refused: %s", ack, ev.Error)
		}
	}
}

// answerPreTool decides one pre_tool request with the hook's function.
// A request without one (the hook was never taken by this client) is
// refused, so a call never runs undecided.
func (c *Client) answerPreTool(id, tool, args string) error {
	allow, reason := false, "this controller has no pre-tool handler"
	if c.preTool != nil {
		allow, reason = c.preTool(tool, args)
	}
	allowWire := 0
	if allow {
		allowWire = 1
	}
	return c.send(map[string]any{"type": "hook_answer", "id": id, "allow": allowWire, "reason": reason})
}

// Interrupt cancels the in-flight turn. The interrupted Say's Turn comes
// back with the loop's error and the settle point, as usual.
func (c *Client) Interrupt() error {
	return c.send(map[string]any{"type": "interrupt"})
}

// Shutdown asks the session to end its run (the headless exit path).
func (c *Client) Shutdown() error {
	return c.send(map[string]any{"type": "shutdown"})
}

// send writes one control line, serialized against concurrent writers.
func (c *Client) send(v any) error {
	c.write.Lock()
	defer c.write.Unlock()
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("sdk: marshal control message: %w", err)
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("sdk: write control message: %w", err)
	}
	return nil
}

// collectTurn reads events until turn_complete, ctx expiry, or the
// connection ending. The caller holds the read lock.
func (c *Client) collectTurn(ctx context.Context) (*Turn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	turn := &Turn{}
	for {
		line, err := c.readLine()
		if err != nil {
			return turn, fmt.Errorf("sdk: read event stream: %w", err)
		}
		var ev struct {
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
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return turn, fmt.Errorf("sdk: decode event %q: %w", line, err)
		}
		switch ev.Type {
		case "delta":
			turn.Deltas = append(turn.Deltas, ev.Text)
		case "assistant":
			turn.Text = ev.Text
		case "tool_call":
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{Tool: ev.Tool, Args: ev.Args})
		case "tool_result":
			turn.ToolResults = append(turn.ToolResults, ToolResult{Tool: ev.Tool, Result: ev.Result})
		case "error":
			turn.Errors = append(turn.Errors, ev.Error)
		case "pre_tool":
			if err := c.answerPreTool(ev.ID, ev.Tool, ev.Args); err != nil {
				return turn, err
			}
		case "ring_pending":
			turn.PendingRings = append(turn.PendingRings, PendingRing{RingID: ev.RingID, From: ev.From, Purpose: ev.Purpose})
		case "backoff":
			turn.BackedOff = true
		case "max_failures":
			turn.MaxFailures = true
		case "turn_complete":
			return turn, nil
		}
	}
}

// awaitQueryResult reads lines until the matching query_result arrives.
func (c *Client) awaitQueryResult(ctx context.Context, what string) (json.RawMessage, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(deadline)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, fmt.Errorf("sdk: read query reply: %w", err)
		}
		var ev struct {
			Type string          `json:"type"`
			What string          `json:"what"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("sdk: decode query reply %q: %w", line, err)
		}
		if ev.Type == "error" {
			var errEv struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(line, &errEv)
			return nil, fmt.Errorf("sdk: query failed: %s", errEv.Error)
		}
		if ev.Type == "query_result" && ev.What == what {
			return ev.Data, nil
		}
	}
}

// readLine reads one protocol line.
func (c *Client) readLine() ([]byte, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return line, nil
}

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

// Turn is one completed agent turn: the streamed deltas, the completed
// assistant text (authoritative), every tool activity, and any errors.
type Turn struct {
	Deltas      []string     `json:"deltas,omitempty"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
	BackedOff   bool         `json:"backed_off,omitempty"`
	MaxFailures bool         `json:"max_failures,omitempty"`
}

// Client is one connection to one lazymesh session's control socket.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	write  sync.Mutex
	read   sync.Mutex // one Say/Query at a time owns the inbound stream
	info   SessionInfo
	closed bool
}

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

// Close closes the connection. Idempotent.
func (c *Client) Close() error {
	c.read.Lock()
	defer c.read.Unlock()
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
			Type   string `json:"type"`
			Text   string `json:"text"`
			Tool   string `json:"tool"`
			Args   string `json:"args"`
			Result string `json:"result"`
			Error  string `json:"error"`
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

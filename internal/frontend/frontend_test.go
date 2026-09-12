package frontend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// startTestServer starts a Server on a temp socket with the given input
// and event channels, wired to the given query handler.
func startTestServer(t *testing.T, input chan string, events chan agent.Event, query func(ctx context.Context, what string) (any, error)) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := Start(Options{
		Path:      path,
		Input:     input,
		Events:    events,
		Query:     query,
		SessionID: "test-session",
		Model:     "testmodel/1",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// controller is a connected control client: the raw conn for writes and
// a buffered reader for lines.
type controller struct {
	conn net.Conn
	r    *bufio.Reader
}

// dial connects a controller to the socket.
func dial(t *testing.T, path string) *controller {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	return &controller{conn: conn, r: bufio.NewReader(conn)}
}

// send writes one control message line.
func (c *controller) send(t *testing.T, raw string) {
	t.Helper()
	if _, err := c.conn.Write([]byte(raw + "\n")); err != nil {
		t.Fatalf("write %q: %v", raw, err)
	}
}

// line reads one output line as a map.
func (c *controller) line(t *testing.T) map[string]any {
	t.Helper()
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return m
}

// TestSocketPermissions pins the security posture: the socket file is
// 0600, so only this user's processes can connect — the isolation a TCP
// listener cannot give.
func TestSocketPermissions(t *testing.T) {
	_, path := startTestServer(t, make(chan string, 8), make(chan agent.Event, 8), nil)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestHandshakeAndInput proves the round trip that makes the socket a
// control plane: on connect the controller learns the session, and an
// input line lands on the driver's input channel.
func TestHandshakeAndInput(t *testing.T) {
	input := make(chan string, 8)
	_, path := startTestServer(t, input, make(chan agent.Event, 8), nil)
	c := dial(t, path)

	hello := c.line(t)
	if hello["type"] != "session" || hello["session_id"] != "test-session" || hello["model"] != "testmodel/1" {
		t.Fatalf("handshake = %v", hello)
	}

	c.send(t, `{"type":"input","session_id":"test-session","text":"hello mesh"}`)

	select {
	case got := <-input:
		if got != "hello mesh" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input never reached the driver channel")
	}
}

// TestEventsReachControllers proves the output stream: an event fanned
// into the events channel arrives on the socket as its wire shape, and
// EventListening travels as turn_complete — the settle point.
func TestEventsReachControllers(t *testing.T) {
	events := make(chan agent.Event, 8)
	_, path := startTestServer(t, make(chan string, 8), events, nil)
	c := dial(t, path)
	c.line(t) // handshake

	events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "hi there"}
	line := c.line(t)
	if line["type"] != "assistant" || line["text"] != "hi there" {
		t.Fatalf("event line = %v", line)
	}

	events <- agent.Event{Kind: agent.EventListening}
	line = c.line(t)
	if line["type"] != "turn_complete" {
		t.Fatalf("listening line = %v", line)
	}
}

// TestQueryRoundTrip proves the query control message: the query handler
// is invoked and its data returned as query_result.
func TestQueryRoundTrip(t *testing.T) {
	_, path := startTestServer(t, make(chan string, 8), make(chan agent.Event, 8),
		func(ctx context.Context, what string) (any, error) {
			if what != "status" {
				return nil, fmt.Errorf("test query only knows status")
			}
			return map[string]any{"message_count": 3}, nil
		})
	c := dial(t, path)
	c.line(t) // handshake

	c.send(t, `{"type":"query","what":"status"}`)

	line := c.line(t)
	if line["type"] != "query_result" || line["what"] != "status" {
		t.Fatalf("query line = %v", line)
	}
	data, ok := line["data"].(map[string]any)
	if !ok || data["message_count"] != float64(3) {
		t.Fatalf("query data = %v", line["data"])
	}
}

// TestShutdownClosesShutdownChannel proves the shutdown control message
// is the headless run loop's exit signal.
func TestShutdownClosesShutdownChannel(t *testing.T) {
	s, path := startTestServer(t, make(chan string, 8), make(chan agent.Event, 8), nil)
	c := dial(t, path)
	c.line(t) // handshake

	c.send(t, `{"type":"shutdown"}`)

	select {
	case <-s.Shutdown():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not close the Shutdown channel")
	}
}

// TestDeltaWireShape proves streaming deltas reach controllers as delta
// lines, in the same stream as the assistant message that completes them.
func TestDeltaWireShape(t *testing.T) {
	events := make(chan agent.Event, 8)
	_, path := startTestServer(t, make(chan string, 8), events, nil)
	c := dial(t, path)
	c.line(t) // handshake

	events <- agent.Event{Kind: agent.EventAssistantDelta, Text: "par"}
	line := c.line(t)
	if line["type"] != "delta" || line["text"] != "par" {
		t.Fatalf("delta line = %v", line)
	}
}

// TestInterruptControlMessageCallsInterrupt proves the interrupt control
// message reaches the caller's interrupt function — the socket's half of
// the Phase 3 chain (the sessionhost test covers the other half).
func TestInterruptControlMessageCallsInterrupt(t *testing.T) {
	called := make(chan struct{}, 1)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := Start(Options{
		Path:      path,
		Input:     make(chan string, 8),
		Events:    make(chan agent.Event, 8),
		Interrupt: func() { called <- struct{}{} },
		SessionID: "test-session",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c := dial(t, path)
	c.line(t) // handshake
	c.send(t, `{"type":"interrupt"}`)

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt function was never called")
	}
}

// TestCloseRemovesSocketFile proves Close leaves no socket file behind.
func TestCloseRemovesSocketFile(t *testing.T) {
	s, path := startTestServer(t, make(chan string, 8), make(chan agent.Event, 8), nil)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after Close (stat err = %v)", err)
	}
}

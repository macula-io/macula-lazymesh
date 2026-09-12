package sdk

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// startServerForSDK boots a real control-plane server on a temp socket
// with channels the test can push events into, and a query handler that
// echoes status. It returns the socket path.
func startServerForSDK(t *testing.T) (string, chan string, chan agent.Event) {
	t.Helper()
	input := make(chan string, 8)
	events := make(chan agent.Event, 8)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path:   path,
		Input:  input,
		Events: events,
		Query: func(ctx context.Context, what string) (any, error) {
			return map[string]any{"message_count": 3, "what": what}, nil
		},
		Interrupt: func() {},
		SessionID: "sdk-test",
		Model:     "deepseek/test",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return path, input, events
}

// TestClientSayCollectsAFullTurn drives a complete turn over a real
// socket: deltas, assistant message, tool traffic, and the settle point
// all land in the returned Turn.
func TestClientSayCollectsAFullTurn(t *testing.T) {
	path, input, events := startServerForSDK(t)

	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if c.Session().SessionID != "sdk-test" || c.Session().Model != "deepseek/test" {
		t.Fatalf("handshake = %+v", c.Session())
	}

	go func() {
		events <- agent.Event{Kind: agent.EventAssistantDelta, Text: "hel"}
		events <- agent.Event{Kind: agent.EventAssistantDelta, Text: "lo"}
		events <- agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say", Text: `{"text":"x"}`}
		events <- agent.Event{Kind: agent.EventToolResult, ToolName: "mesh_say", Text: "ok"}
		events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "hello"}
		events <- agent.Event{Kind: agent.EventListening}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Say(ctx, "hi there")
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	if turn.Text != "hello" || len(turn.Deltas) != 2 || turn.Deltas[0] != "hel" || turn.Deltas[1] != "lo" {
		t.Fatalf("turn = %+v", turn)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Tool != "mesh_say" || turn.ToolCalls[0].Args != `{"text":"x"}` {
		t.Fatalf("tool calls = %+v", turn.ToolCalls)
	}
	if len(turn.ToolResults) != 1 || turn.ToolResults[0].Result != "ok" {
		t.Fatalf("tool results = %+v", turn.ToolResults)
	}

	// The input reached the driver channel — the other half of the round
	// trip.
	select {
	case got := <-input:
		if got != "hi there" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input never arrived")
	}
}

// TestClientQueryRoundTrips pins the query path: the reply's data comes
// back raw and decodable.
func TestClientQueryRoundTrips(t *testing.T) {
	path, _, _ := startServerForSDK(t)
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := c.Query(ctx, "status")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if m["message_count"] != float64(3) {
		t.Fatalf("query data = %v", m)
	}
}

// TestClientInterruptAndShutdown pins the two control messages: interrupt
// reaches the server's interrupt func, shutdown reaches the run loop's
// exit channel.
func TestClientInterruptAndShutdown(t *testing.T) {
	interrupted := make(chan struct{}, 1)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path:      path,
		Input:     make(chan string, 8),
		Events:    make(chan agent.Event, 8),
		Interrupt: func() { interrupted <- struct{}{} },
		SessionID: "sdk-test",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer s.Close()

	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if err := c.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	select {
	case <-interrupted:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt func never called")
	}

	if err := c.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-s.Shutdown():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown channel never closed")
	}
}

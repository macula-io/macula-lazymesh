package sdk

import (
	"context"
	"encoding/json"
	"errors"
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

// TestClientSayCollectsPendingRings: a ring surfaced during a turn lands
// in the Turn, so a controller driving with Say still sees who rang.
func TestClientSayCollectsPendingRings(t *testing.T) {
	path, _, events := startServerForSDK(t)
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	go func() {
		events <- agent.Event{Kind: agent.EventRingPending, ID: "ring-7", From: "Nova", Text: "come help"}
		events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "ok"}
		events <- agent.Event{Kind: agent.EventListening}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := c.Say(ctx, "hi")
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	want := PendingRing{RingID: "ring-7", From: "Nova", Purpose: "come help"}
	if len(turn.PendingRings) != 1 || turn.PendingRings[0] != want {
		t.Fatalf("pending rings = %+v, want [%+v]", turn.PendingRings, want)
	}
}

// TestClientHookPreToolAnswersDuringATurn: a controller that hooks
// pre_tool decides each call from inside Say, and its decision reaches
// the session's gate.
func TestClientHookPreToolAnswersDuringATurn(t *testing.T) {
	hook := frontend.NewPreToolHook(false)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	events := make(chan agent.Event, 8)
	s, err := frontend.Start(frontend.Options{
		Path: path, Input: make(chan string, 8), Events: events,
		SessionID: "sdk-test", Model: "deepseek/test", PreToolHook: hook,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var asked []string
	if err := c.HookPreTool(ctx, func(tool, args string) (bool, string) {
		asked = append(asked, tool+" "+args)
		return false, "not from this controller"
	}); err != nil {
		t.Fatalf("hook: %v", err)
	}

	decided := make(chan string, 1)
	go func() {
		allow, reason, err := hook.Decide(context.Background(), "mesh_say", `{"text":"x"}`)
		if err != nil || allow {
			decided <- "unexpected"
		} else {
			decided <- reason
		}
		events <- agent.Event{Kind: agent.EventListening}
	}()
	if _, err := c.Say(ctx, "go"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if got := <-decided; got != "not from this controller" {
		t.Fatalf("gate decided %q", got)
	}
	if len(asked) != 1 || asked[0] != `mesh_say {"text":"x"}` {
		t.Fatalf("handler saw %v", asked)
	}
	if err := c.Unhook(ctx); err != nil {
		t.Fatalf("unhook: %v", err)
	}
}

// TestClientServeHookAnswersOutsideSay: a policy process that never calls
// Say still decides every call, as a headless agent's mesh-triggered
// turns need (Fable on G8).
func TestClientServeHookAnswersOutsideSay(t *testing.T) {
	hook := frontend.NewPreToolHook(true)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path: path, Input: make(chan string, 8), Events: make(chan agent.Event, 8),
		SessionID: "sdk-test", Model: "deepseek/test", PreToolHook: hook,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.HookPreTool(ctx, func(tool, args string) (bool, string) { return tool == "mesh_say", "only mesh_say" }); err != nil {
		t.Fatalf("hook: %v", err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- c.ServeHook(serveCtx) }()

	if allow, _, err := hook.Decide(context.Background(), "mesh_say", "{}"); !allow || err != nil {
		t.Fatalf("mesh_say: allow=%v err=%v", allow, err)
	}
	if allow, reason, _ := hook.Decide(context.Background(), "mesh_join_room", "{}"); allow || reason != "only mesh_say" {
		t.Fatalf("mesh_join_room: allow=%v reason=%q", allow, reason)
	}
	stop()
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeHook ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHook did not stop on ctx cancel")
	}
}

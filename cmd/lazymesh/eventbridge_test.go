package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// #98: a burst of deltas the TUI and the socket cannot keep up with is
// followed by an approval request. Both consumers still get the approval,
// after all of the delta text that came before it.
func TestEventBridge_ApprovalSurvivesADeltaFlood(t *testing.T) {
	tuiEvents := make(chan agent.Event, 4)
	frontendEvents := make(chan agent.Event, 4)
	node, bridge := spawnBridge(t, tuiEvents, frontendEvents)

	var want strings.Builder
	for i := 0; i < 300; i++ {
		chunk := fmt.Sprintf("[%d]", i)
		want.WriteString(chunk)
		if err := node.Send(bridge, sessionhost.Event{Event: agent.Event{Kind: agent.EventAssistantDelta, Text: chunk}}); err != nil {
			t.Fatalf("send delta: %v", err)
		}
	}
	if err := node.Send(bridge, sessionhost.Event{Event: agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "risky"}}); err != nil {
		t.Fatalf("send approval: %v", err)
	}
	// Let the bridge work through its mailbox while nobody reads, as a
	// busy TUI would.
	time.Sleep(200 * time.Millisecond)

	results := make(chan string, 2)
	for name, ch := range map[string]chan agent.Event{"tui": tuiEvents, "socket": frontendEvents} {
		go func(name string, ch chan agent.Event) {
			var got strings.Builder
			timeout := time.After(5 * time.Second)
			for {
				select {
				case ev := <-ch:
					if ev.Kind == agent.EventApprovalRequested {
						if got.String() != want.String() {
							results <- fmt.Sprintf("%s: delta text before the approval differs:\n got %q\nwant %q", name, got.String(), want.String())
							return
						}
						results <- ""
						return
					}
					got.WriteString(ev.Text)
				case <-timeout:
					results <- name + ": the approval request never arrived after a flood of deltas"
					return
				}
			}
		}(name, ch)
	}
	for i := 0; i < 2; i++ {
		if msg := <-results; msg != "" {
			t.Error(msg)
		}
	}
}

// spawnBridge starts an event bridge delivering to the two channels, and
// stops it when the test ends.
func spawnBridge(t *testing.T, tuiEvents, frontendEvents chan agent.Event) (gen.Node, gen.PID) {
	t.Helper()
	node, err := sessionhost.Node(io.Discard)
	if err != nil {
		t.Fatalf("boot node: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bridge, err := node.Spawn(eventBridgeFactory, gen.ProcessOptions{},
		ctx, agent.NewOutlet(ctx.Done(), tuiEvents), agent.NewOutlet(ctx.Done(), frontendEvents),
		log.New(io.Discard, "", 0), (*roomwaiter.Manager)(nil), counters.New())
	if err != nil {
		t.Fatalf("spawn bridge: %v", err)
	}
	t.Cleanup(func() { node.Kill(bridge) })
	return node, bridge
}

// #98 r=2: a TUI that is not reading (the operator is in $EDITOR, and
// bubbletea's event loop is stopped) does not hold back the socket: a
// controller still gets every event and the approval.
func TestEventBridge_AnUnreadTUIDoesNotHoldBackTheSocket(t *testing.T) {
	tuiEvents := make(chan agent.Event, 2) // never read
	frontendEvents := make(chan agent.Event, 2)
	node, bridge := spawnBridge(t, tuiEvents, frontendEvents)

	for i := 0; i < 3; i++ {
		_ = node.Send(bridge, sessionhost.Event{Event: agent.Event{Kind: agent.EventToolResult, ToolName: "read_file", Text: fmt.Sprintf("result %d", i)}})
	}
	_ = node.Send(bridge, sessionhost.Event{Event: agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "risky"}})

	timeout := time.After(2 * time.Second)
	for got := 0; ; {
		select {
		case ev := <-frontendEvents:
			got++
			if ev.Kind == agent.EventApprovalRequested {
				if got != 4 {
					t.Fatalf("the approval arrived as event %d, want 4", got)
				}
				return
			}
		case <-timeout:
			t.Fatalf("with the TUI not reading, the socket got %d events and never the approval", got)
		}
	}
}

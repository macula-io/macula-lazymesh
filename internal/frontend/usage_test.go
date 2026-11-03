package frontend

import (
	"net"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// The usage report crosses the control socket whole (#78 gap 4), so an
// attached TUI shows the same context figure as the session's own.
func TestUsageEventSurvivesTheRoundTrip(t *testing.T) {
	in := agent.Event{Kind: agent.EventUsageReported, Usage: agent.UsageReport{ContextTokens: 12_345, ContextWindow: 200_000, SessionTokens: 48_100}}
	line, err := marshalEvent(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, ok := parseEvent(line)
	if !ok {
		t.Fatalf("parseEvent rejected %s", line)
	}
	if got.Kind != in.Kind || got.Usage != in.Usage {
		t.Fatalf("round trip lost data:\n  encoded %s\n  got     %+v\n  want    %+v", line, got, in)
	}
}

// A session and a controller need not be the same release (#78 gap 4):
// the controller skips an event type it does not know and keeps reading.
// That is what lets an attach from before "usage" existed sit on a session
// that sends it -- and what will let this one sit on a session newer than
// itself. The line here is from such a newer session.
func TestControllerSkipsAnEventTypeItDoesNotKnow(t *testing.T) {
	path := tempSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(`{"type":"session","session_id":"s","model":"m"}` + "\n"))
		_, _ = conn.Write([]byte(`{"type":"something_newer","figure":1}` + "\n"))
		_, _ = conn.Write([]byte(`{"type":"assistant","text":"still here"}` + "\n"))
		time.Sleep(time.Second)
	}()

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	select {
	case ev := <-c.Events():
		if ev.Kind != agent.EventAssistantMessage || ev.Text != "still here" {
			t.Fatalf("first event = %+v, want the assistant message after the unknown line", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("controller stopped at the unknown event type")
	}
}

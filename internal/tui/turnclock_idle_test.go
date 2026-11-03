package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// #91 review (Pluto's probe, kept as the regression test). An inbox read failure reported by the roster gate while the agent is
// parked (cmd/lazymesh reportGateFailures) is not a turn: no working clock,
// and the next real turn's length must not include the idle time.
func TestTurnClock_IdleGateErrorIsNotATurn(t *testing.T) {
	m, now, _ := turnClockModel(t)
	m = feed(t, m, agent.Event{Kind: agent.EventError, ToolName: "rostergate", Err: errors.New("room x: crew messages could not be read")})
	*now = now.Add(5 * time.Minute)
	if s := summary(m); strings.Contains(s, "working") {
		t.Fatalf("an idle gate error shows a running turn: %q", s)
	}
	m = submit(t, m, "hello")
	*now = now.Add(2 * time.Second)
	m = feed(t, m, agent.Event{Kind: agent.EventAssistantMessage, Text: "hi"}, agent.Event{Kind: agent.EventListening})
	if got := chatJoined(m); !strings.Contains(got, "· 2.0s") {
		t.Fatalf("the turn's length includes the idle time:\n%s", got)
	}
}

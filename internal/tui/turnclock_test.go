package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// turnClockModel is a model with a clock the test moves by hand, the agent
// model named, and an interrupt channel to observe.
func turnClockModel(t *testing.T) (Model, *time.Time, chan struct{}) {
	t.Helper()
	interruptCh := make(chan struct{}, 4)
	m := New(nil, Options{UserInputCh: make(chan string, 8), InterruptCh: interruptCh, AgentModel: "deepseek/deepseek-v4-flash", StatusBarPosition: "bottom"})
	m.width, m.height = 120, 30
	m.agentEvents = make(chan agent.Event)
	now := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, &now, interruptCh
}

func feed(t *testing.T, m Model, events ...agent.Event) Model {
	t.Helper()
	for _, ev := range events {
		updated, _ := m.Update(agentEventMsg(ev))
		m = updated.(Model)
	}
	return m
}

func submit(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeText(t, m, text)
	updated, _ := m.Update(typeKey(tea.KeyEnter))
	return updated.(Model)
}

func chatJoined(m Model) string {
	return sgrStripper.ReplaceAllString(strings.Join(m.chatContentLines(), "\n"), "")
}

func summary(m Model) string {
	return sgrStripper.ReplaceAllString(m.renderSummaryLine(), "")
}

// The check #78 gap 2 names: after a submit with no event yet, the status
// block says a turn is working and for how long; once the turn is over,
// the chat holds a line with the model and the duration.
func TestTurnClock_WorkingThenModelAndDuration(t *testing.T) {
	m, now, _ := turnClockModel(t)
	m = submit(t, m, "run the tests")

	if s := summary(m); !strings.Contains(s, "working 0s") || !strings.Contains(s, "esc interrupt") {
		t.Fatalf("no working marker right after submit: %q", s)
	}
	*now = now.Add(12 * time.Second)
	if s := summary(m); !strings.Contains(s, "working 12s") {
		t.Fatalf("elapsed time does not advance: %q", s)
	}

	*now = now.Add(300 * time.Millisecond)
	m = feed(t, m,
		agent.Event{Kind: agent.EventAssistantDelta, Text: "all "},
		agent.Event{Kind: agent.EventAssistantMessage, Text: "all green"},
		agent.Event{Kind: agent.EventListening},
	)
	if s := summary(m); strings.Contains(s, "working") {
		t.Fatalf("still working after the turn ended: %q", s)
	}
	if got := chatJoined(m); !strings.Contains(got, "deepseek/deepseek-v4-flash · 12.3s") {
		t.Fatalf("no model · duration line after the turn:\n%s", got)
	}
}

// A turn a room message started has no submit behind it; it is still a
// turn the operator sees running, and esc still stops it.
func TestTurnClock_TurnStartedByTheAgentCountsToo(t *testing.T) {
	m, now, interruptCh := turnClockModel(t)
	m = feed(t, m, agent.Event{Kind: agent.EventAssistantDelta, Text: "a peer asked"})
	*now = now.Add(3 * time.Second)
	if s := summary(m); !strings.Contains(s, "working 3s") {
		t.Fatalf("a turn started by agent activity shows no working marker: %q", s)
	}

	updated, _ := m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	select {
	case <-interruptCh:
	default:
		t.Fatal("esc did not interrupt a turn the agent started")
	}
	m = feed(t, m, agent.Event{Kind: agent.EventListening})
	if got := chatJoined(m); !strings.Contains(got, "· 3.0s · interrupted") {
		t.Fatalf("an interrupted turn does not say so:\n%s", got)
	}
}

// A turn that fails ends at the backoff, and says it failed; the listening
// beat after the backoff does not end a second, empty turn.
func TestTurnClock_FailedTurn(t *testing.T) {
	m, now, _ := turnClockModel(t)
	m = submit(t, m, "go")
	*now = now.Add(1500 * time.Millisecond)
	m = feed(t, m,
		agent.Event{Kind: agent.EventError, Err: fmt.Errorf("provider down")},
		agent.Event{Kind: agent.EventBackoff},
		agent.Event{Kind: agent.EventListening},
	)
	got := chatJoined(m)
	if !strings.Contains(got, "· 1.5s · failed") {
		t.Fatalf("a failed turn does not say so:\n%s", got)
	}
	if strings.Count(got, "deepseek/deepseek-v4-flash ·") != 1 {
		t.Fatalf("one turn, more than one end line:\n%s", got)
	}
	if s := summary(m); strings.Contains(s, "working") {
		t.Fatalf("still working after a failed turn: %q", s)
	}
}

// The spinner and clock move on their own tick, which runs only while a
// turn does: an idle TUI schedules nothing.
func TestTurnClock_TickRunsOnlyWhileATurnDoes(t *testing.T) {
	m, now, _ := turnClockModel(t)
	updated, cmd := m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	_ = cmd
	m = submit(t, m, "go")
	if !m.turnTicking {
		t.Fatal("no tick scheduled for a running turn")
	}
	first := summary(m)
	*now = now.Add(turnTickInterval)
	updated, cmd = m.Update(turnTickMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the tick did not re-arm while the turn runs")
	}
	if summary(m) == first {
		t.Fatalf("the spinner did not move: %q", first)
	}

	m = feed(t, m, agent.Event{Kind: agent.EventListening})
	updated, cmd = m.Update(turnTickMsg{})
	m = updated.(Model)
	if cmd != nil || m.turnTicking {
		t.Fatal("the tick re-armed after the turn ended")
	}
}

func TestTurnClock_Durations(t *testing.T) {
	for d, want := range map[time.Duration]string{
		300 * time.Millisecond:   "0.3s",
		12300 * time.Millisecond: "12.3s",
		65 * time.Second:         "1m05s",
		3725 * time.Second:       "1h02m",
	} {
		if got := turnDuration(d); got != want {
			t.Fatalf("turnDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

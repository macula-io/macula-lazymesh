package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// turnTickMsg moves the working spinner and clock. It is scheduled only
// while a turn runs, so an idle TUI wakes for nothing (#78 gap 2).
type turnTickMsg struct{}

// turnTickInterval is the spinner's frame time: fast enough to read as
// motion, slow enough that a long turn costs a handful of redraws a second.
const turnTickInterval = 150 * time.Millisecond

var turnWorkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C")).Bold(true)

var turnSpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func turnTick() tea.Cmd {
	return tea.Tick(turnTickInterval, func(time.Time) tea.Msg { return turnTickMsg{} })
}

// beginTurn starts the turn clock unless a turn is already running. A turn
// begins at a submit, or at the agent's first activity when nothing was
// submitted: a room message starts turns too, and the operator sees those
// running the same way.
func (m Model) beginTurn() (Model, tea.Cmd) {
	if !m.turnStartedAt.IsZero() {
		return m, nil
	}
	m.turnStartedAt = m.clock()
	m.turnInterrupted = false
	if m.turnTicking {
		return m, nil
	}
	m.turnTicking = true
	return m, turnTick()
}

// endTurn stops the turn clock and leaves one line in the chat saying which
// model ran the turn and how long it took, and that it was interrupted or
// failed when it was. Nothing happens when no turn is running, so the
// listening beat that follows a backoff does not end a second turn.
func (m Model) endTurn(failed bool) Model {
	if m.turnStartedAt.IsZero() {
		return m
	}
	text := m.agentModel
	if text == "" {
		text = "turn"
	}
	text += " · " + turnDuration(m.clock().Sub(m.turnStartedAt))
	switch {
	case failed:
		text += " · failed"
	case m.turnInterrupted:
		text += " · interrupted"
	}
	m.turnStartedAt = time.Time{}
	m.turnInterrupted = false
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: m.clock(), text: termTextOf(text)})
	m.syncViewport()
	return m
}

// trackTurn moves the turn clock for one agent event: the listening beat
// ends a turn, a backoff or the final failure ends it as failed, and work
// that only happens inside a turn begins one if none is running. Nothing
// else starts a turn: an error can arrive while the agent is parked (the
// roster gate reports an unreadable inbox then), and a ring is the mesh
// knocking -- counting either would run a clock with no turn behind it
// (#91 review).
func (m Model) trackTurn(ev agent.Event) (Model, tea.Cmd) {
	switch ev.Kind {
	case agent.EventListening:
		return m.endTurn(false), nil
	case agent.EventBackoff, agent.EventMaxFailuresReached:
		return m.endTurn(true), nil
	case agent.EventAssistantDelta, agent.EventAssistantMessage, agent.EventToolCall,
		agent.EventToolResult, agent.EventApprovalRequested:
		return m.beginTurn()
	}
	return m, nil
}

// handleTurnTick re-arms the tick while a turn runs and lets it lapse
// otherwise.
func (m Model) handleTurnTick() (Model, tea.Cmd) {
	if m.turnStartedAt.IsZero() {
		m.turnTicking = false
		return m, nil
	}
	return m, turnTick()
}

// turnSegment is the summary line's working marker, empty while idle:
// a spinner, the elapsed time, and the key that stops the turn.
func (m Model) turnSegment() string {
	if m.turnStartedAt.IsZero() {
		return ""
	}
	elapsed := m.clock().Sub(m.turnStartedAt)
	frame := turnSpinnerFrames[int(elapsed/turnTickInterval)%len(turnSpinnerFrames)]
	return fmt.Sprintf("%c working %s · esc interrupt", frame, turnElapsed(elapsed))
}

// turnElapsed is a running turn's age in whole seconds: tenths would flicker.
func turnElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return turnDuration(d)
}

// turnDuration is a finished turn's length: tenths of a second under a
// minute, then minutes and seconds, then hours and minutes.
func turnDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

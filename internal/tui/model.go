// Package tui is lazymesh's live view: three panels (rooms, pending rings,
// agent presence) fed entirely through macula-mcp's own read tools over
// the same MCP connection the agent loop uses.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// refreshInterval is how often the panels re-poll macula-mcp. These are
// documented as instant local reads, so this can be short without cost.
const refreshInterval = 2 * time.Second

type refreshMsg struct {
	state meshState
	err   error
}

type tickMsg time.Time

// Model is the bubbletea model for lazymesh's TUI.
type Model struct {
	mcp *mcpclient.Client

	state    meshState
	lastErr  error
	statusMu string // most recent status line, e.g. "agent joined <room>"

	width  int
	height int
}

// New builds a Model that reads through the given already-connected
// mcpclient.
func New(client *mcpclient.Client) Model {
	return Model{mcp: client}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) refreshCmd() tea.Cmd {
	client := m.mcp
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state, err := fetchMeshState(ctx, client)
		return refreshMsg{state: state, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tick())

	case refreshMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.state = msg.state
		}
		return m, nil
	}
	return m, nil
}

var (
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("lazymesh") + dimStyle.Render("  (q to quit)") + "\n\n")

	if m.lastErr != nil {
		b.WriteString(errStyle.Render(fmt.Sprintf("last refresh error: %s", m.lastErr)) + "\n\n")
	}

	b.WriteString(panelStyle.Render(m.renderRooms()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPendingRings()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPresence()) + "\n")

	return b.String()
}

func (m Model) renderRooms() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Rooms (%d joined)", len(m.state.joined))) + "\n")
	if len(m.state.joined) == 0 {
		b.WriteString(dimStyle.Render("no rooms joined yet") + "\n")
	}
	for _, r := range m.state.joined {
		b.WriteString(fmt.Sprintf("%s  %s  %d participants, %d messages\n",
			shortTopic(r.RoomTopic), dimStyle.Render("opened by "+shortID(r.OpenedBy)),
			len(r.ParticipantsSeen), r.MessagesReceived))
		for _, msg := range lastN(m.state.recent[r.RoomTopic], 3) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("    %s: %s\n", shortID(msg.From), truncate(msg.Text, 80))))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderPendingRings() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Pending rings (%d)", len(m.state.pending))) + "\n")
	if len(m.state.pending) == 0 {
		b.WriteString(dimStyle.Render("none") + "\n")
	}
	for _, r := range m.state.pending {
		b.WriteString(fmt.Sprintf("%s from %s: %s\n", r.Direction, shortID(r.Peer), truncate(r.Purpose, 80)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderPresence() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Presence (%d agents)", len(m.state.agents))) + "\n")
	for _, a := range m.state.agents {
		self := ""
		if a.IsSelf {
			self = dimStyle.Render(" (you)")
		}
		name := a.OperatorName
		if name == "" {
			name = shortID(a.NodeID)
		}
		b.WriteString(fmt.Sprintf("%s%s  %s  last seen %ds ago\n", name, self, dimStyle.Render(a.ConnectedVia), a.SecondsSinceSeen))
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func shortTopic(topic string) string {
	const prefix = "agents.room."
	if strings.HasPrefix(topic, prefix) {
		return shortID(strings.TrimPrefix(topic, prefix))
	}
	return topic
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func lastN(msgs []roomMessage, n int) []roomMessage {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

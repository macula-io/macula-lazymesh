// Package tui is lazymesh's interactive surface: a persistent one-line
// mesh-status strip (expandable into the full rooms/rings/presence view),
// a chat pane showing the agent's conversation, vim-style modal input for
// composing messages to the agent, and terminal-bell audio cues for
// activity that matters even when not looking at the screen.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// refreshInterval is how often the mesh-state panels re-poll macula-mcp.
// These are documented as instant local reads, so this can be short
// without cost.
const refreshInterval = 2 * time.Second

type refreshMsg struct {
	state meshState
	err   error
}

type tickMsg time.Time
type agentEventMsg agent.Event

// Mode is the TUI's modal-input state: normal mode navigates/commands,
// insert mode composes a message to the agent. Deliberately the real vim
// model (not a few remapped keys) -- see the plan doc's own reasoning: text
// entry and navigation compete for the same keys once both exist, and this
// is the actual solution to that, not a workaround.
type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert
)

// Model is the bubbletea model for lazymesh's TUI.
type Model struct {
	mcp *mcpclient.Client

	agentEvents <-chan agent.Event // nil when no --room agent is running
	userInputCh chan<- string      // where a submitted message is sent for runAgent to pick up

	state   meshState
	lastErr error

	mode              Mode
	meshExpanded      bool
	detailsExpanded   bool // global expand/collapse for tool-call detail in chat
	muted             bool
	statusBarPosition string // "top" or "bottom"

	chatEntries  []chatEntry
	chatViewport viewport.Model
	input        textinput.Model

	width  int
	height int
}

// New builds a Model that reads mesh state through client, renders
// agentEvents into the chat pane as they arrive (nil if no agent loop is
// running), and sends composed messages on userInputCh (nil has the same
// effect -- composing is still possible, it just has nowhere to go).
func New(client *mcpclient.Client, agentEvents <-chan agent.Event, userInputCh chan<- string, statusBarPosition string) Model {
	ti := textinput.New()
	ti.Placeholder = "message the agent..."
	ti.CharLimit = 2000
	ti.Prompt = "> "

	if statusBarPosition != "top" {
		statusBarPosition = "bottom"
	}

	return Model{
		mcp:               client,
		agentEvents:       agentEvents,
		userInputCh:       userInputCh,
		mode:              ModeNormal,
		statusBarPosition: statusBarPosition,
		input:             ti,
		chatViewport:      viewport.New(80, 20),
		// Bell defaults ON: Fable's finding #3 is specifically that a
		// wedged agent looks identical to a healthy one on screen: a cue
		// that's off by default would silently defeat its own purpose for
		// anyone who doesn't know to turn it on first.
		muted: false,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.refreshCmd(), tick(), textinput.Blink}
	if m.agentEvents != nil {
		cmds = append(cmds, waitForAgentEvent(m.agentEvents))
	}
	return tea.Batch(cmds...)
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

func waitForAgentEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return agentEventMsg(ev)
	}
}

func sendUserInput(ch chan<- string, text string) tea.Cmd {
	return func() tea.Msg {
		if ch != nil {
			ch <- text
		}
		return nil
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeComponents()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tick())

	case refreshMsg:
		return m.handleRefresh(msg)

	case agentEventMsg:
		return m.handleAgentEvent(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, DefaultKeyMap.ForceQuit) {
		return m, tea.Quit
	}

	if m.mode == ModeInsert {
		switch {
		case key.Matches(msg, DefaultKeyMap.Normal):
			m.input.Blur()
			m.mode = ModeNormal
			return m, nil
		case key.Matches(msg, DefaultKeyMap.Submit):
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.input.Blur()
			m.mode = ModeNormal
			if text == "" {
				return m, nil
			}
			m.chatEntries = append(m.chatEntries, youChatEntry(text))
			m.syncViewport()
			return m, sendUserInput(m.userInputCh, text)
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	// Normal mode.
	switch {
	case key.Matches(msg, DefaultKeyMap.Quit):
		return m, tea.Quit
	case key.Matches(msg, DefaultKeyMap.Insert):
		m.mode = ModeInsert
		return m, m.input.Focus()
	case key.Matches(msg, DefaultKeyMap.ToggleMesh):
		m.meshExpanded = !m.meshExpanded
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleQuiet):
		m.muted = !m.muted
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleDetails):
		m.detailsExpanded = !m.detailsExpanded
		m.syncViewport()
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Up):
		if !m.meshExpanded {
			m.chatViewport.LineUp(1)
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Down):
		if !m.meshExpanded {
			m.chatViewport.LineDown(1)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleRefresh(msg refreshMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.lastErr = msg.err
		return m, nil
	}
	prev := m.state
	m.lastErr = nil
	m.state = msg.state

	self := selfNodeID(m.state.agents)
	pattern := detectRoomMessageBell(prev, m.state, self)
	if ring := detectRingBell(prev, m.state); ring != bellNone {
		pattern = ring // a ring is rarer/more actionable than an ordinary message
	}
	return m, ringBell(pattern, m.muted)
}

func (m Model) handleAgentEvent(ev agentEventMsg) (Model, tea.Cmd) {
	pattern := bellNone
	switch ev.Kind {
	case agent.EventBackoff, agent.EventMaxFailuresReached:
		pattern = bellTriple
	}
	m.chatEntries = append(m.chatEntries, chatEntryFromAgentEvent(agent.Event(ev)))
	m.syncViewport()
	return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
}

func (m *Model) resizeComponents() {
	const reserved = 3 // status strip + input line + one blank line of slack
	h := m.height - reserved
	if h < 3 {
		h = 3
	}
	m.chatViewport.Width = m.width
	m.chatViewport.Height = h
	if m.width > 6 {
		m.input.Width = m.width - 4
	}
}

func (m *Model) syncViewport() {
	lines := make([]string, 0, len(m.chatEntries))
	for _, e := range m.chatEntries {
		lines = append(lines, e.render(m.detailsExpanded))
	}
	m.chatViewport.SetContent(strings.Join(lines, "\n"))
	m.chatViewport.GotoBottom()
}

var (
	panelStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusStripStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
)

func (m Model) View() string {
	status := m.renderStatusStrip()
	var body string
	if m.meshExpanded {
		body = m.renderExpandedMesh()
	} else {
		body = m.chatViewport.View()
	}
	input := m.renderInputLine()

	if m.statusBarPosition == "top" {
		return strings.Join([]string{status, body, input}, "\n")
	}
	return strings.Join([]string{body, input, status}, "\n")
}

func (m Model) renderStatusStrip() string {
	line := fmt.Sprintf("%d rooms · %d pending rings · %d agents seen",
		len(m.state.joined), len(m.state.pending), len(m.state.agents))
	if m.muted {
		line += "  [muted]"
	}

	hint := "m: mesh view  i: compose  e: expand  b: mute  q: quit"
	if m.mode == ModeInsert {
		hint = "esc: normal mode  enter: send"
	}

	out := statusStripStyle.Render(line) + "  " + dimStyle.Render(hint)
	if m.lastErr != nil {
		out += "  " + errStyle.Render(fmt.Sprintf("(refresh error: %s)", m.lastErr))
	}
	return out
}

func (m Model) renderInputLine() string {
	if m.mode == ModeInsert {
		return m.input.View()
	}
	return dimStyle.Render("-- normal mode -- press i to compose a message --")
}

func (m Model) renderExpandedMesh() string {
	var b strings.Builder
	b.WriteString(panelStyle.Render(m.renderRooms()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPendingRings()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPresence()))
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

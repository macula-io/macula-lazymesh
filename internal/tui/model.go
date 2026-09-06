// Package tui is lazymesh's interactive surface: a persistent one-line
// mesh-status strip (expandable into the full rooms/rings/presence view),
// a chat pane showing the agent's conversation, vim-style modal input for
// composing messages to the agent, and terminal-bell audio cues for
// activity that matters even when not looking at the screen.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/contactpolicy"
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
// insert mode composes a message to the agent, ring-popup mode is a
// blocking phone-call-style prompt for one incoming ring. Deliberately
// the real vim model (not a few remapped keys) -- see the plan doc's own
// reasoning: text entry and navigation compete for the same keys once
// both exist, and this is the actual solution to that, not a workaround.
type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert
	ModeRingPopup
)

// Options configures a new Model. Zero values are all valid (no agent
// running, no ring auto-accept, status strip at the bottom).
type Options struct {
	AgentEvents <-chan agent.Event // nil when no --room agent is running
	UserInputCh chan<- string      // where a submitted message is sent for runAgent to pick up

	StatusBarPosition string // "top" or "bottom"

	// ContactPolicyFile and AutoAcceptKnown together drive the ring
	// pop-up's own layered policy (see config.RingPolicy's doc comment
	// for why this isn't a direct map onto macula-mcp's contact_policy):
	// AutoAcceptKnown peers found in ContactPolicyFile's own allowlist
	// (via internal/contactpolicy.IsTrusted) skip the pop-up and are
	// accepted immediately; everyone else still gets it.
	ContactPolicyFile string
	AutoAcceptKnown   bool

	// AgentModel is shown in the status strip (e.g. "deepseek/deepseek-v4-flash")
	// so an operator watching live can see which provider/model is actually
	// driving the agent, rather than having to go read config.yaml. Leave
	// empty when no --room agent is running -- there's nothing "actually
	// running" to report in that case, just a configured-but-idle default.
	AgentModel string
}

// Model is the bubbletea model for lazymesh's TUI.
type Model struct {
	mcp toolCaller

	agentEvents <-chan agent.Event
	userInputCh chan<- string

	contactPolicyFile string
	autoAcceptKnown   bool
	seenRingIDs       map[string]bool // rings already auto-accepted, answered via the pop-up, or dismissed -- never re-surfaced

	state   meshState
	lastErr error

	mode              Mode
	meshExpanded      bool
	detailsExpanded   bool // global expand/collapse for tool-call detail in chat
	muted             bool
	statusBarPosition string // "top" or "bottom"
	agentModel        string // see Options.AgentModel

	// showChatter controls where routine tool-call activity (mesh
	// operations) is shown. Off by default: it goes to lastChatter, a
	// single ambient line in the status block, keeping the conversation
	// pane to actual dialogue (you/agent/error/system). Toggling it on
	// (the 'v' key) restores the old behavior of every tool call/result
	// also landing as its own line in chatEntries -- for anyone who wants
	// the full blow-by-blow inline rather than the ambient summary.
	showChatter bool
	lastChatter string // most recent tool call/result, rendered collapsed-form; empty until the first one

	pendingRingPopup *pendingRing // the one ring currently shown, nil if none

	chatEntries  []chatEntry
	chatViewport viewport.Model
	input        textinput.Model

	width  int
	height int
}

// New builds a Model that reads mesh state through client, renders
// agentEvents into the chat pane as they arrive, and sends composed
// messages on userInputCh -- see Options' own doc comment for what each
// zero value means.
func New(client toolCaller, opts Options) Model {
	ti := textinput.New()
	ti.Placeholder = "message the agent..."
	ti.CharLimit = 2000
	ti.Prompt = "> "

	statusBarPosition := opts.StatusBarPosition
	if statusBarPosition != "top" {
		statusBarPosition = "bottom"
	}

	return Model{
		mcp:               client,
		agentEvents:       opts.AgentEvents,
		userInputCh:       opts.UserInputCh,
		contactPolicyFile: opts.ContactPolicyFile,
		autoAcceptKnown:   opts.AutoAcceptKnown,
		seenRingIDs:       make(map[string]bool),
		mode:              ModeNormal,
		statusBarPosition: statusBarPosition,
		agentModel:        opts.AgentModel,
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

// editorFinishedMsg reports the outcome of an $EDITOR round-trip started
// by openEditorCmd. path is always the temp file's name (even on error) so
// handleEditorFinished can clean it up unconditionally.
type editorFinishedMsg struct {
	path string
	err  error
}

// openEditorCmd is issue #2's "editor-based composition": write the
// current draft to a temp file, suspend the TUI (tea.ExecProcess) to run
// $EDITOR against it, and read the result back on return. Falls back to
// "vi" when $EDITOR is unset -- something must run, and vi is the one
// editor a POSIX system is guaranteed to have.
func openEditorCmd(current string) tea.Cmd {
	f, err := os.CreateTemp("", "lazymesh-compose-*.md")
	if err != nil {
		return func() tea.Msg { return editorFinishedMsg{err: err} }
	}
	path := f.Name()
	_, writeErr := f.WriteString(current)
	closeErr := f.Close()
	if writeErr != nil {
		return func() tea.Msg { return editorFinishedMsg{path: path, err: writeErr} }
	}
	if closeErr != nil {
		return func() tea.Msg { return editorFinishedMsg{path: path, err: closeErr} }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{path: path, err: err}
	})
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

	case ringAnsweredMsg:
		return m.handleRingAnswered(msg)

	case editorFinishedMsg:
		return m.handleEditorFinished(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, DefaultKeyMap.ForceQuit) {
		return m, tea.Quit
	}

	if m.mode == ModeRingPopup {
		return m.handleRingPopupKey(msg)
	}

	// Works from either Normal or Insert -- composing in $EDITOR is useful
	// as a way INTO a message (from Normal) just as much as a way to
	// finish one already started (from Insert), and always lands in
	// Insert with the edited text loaded either way.
	if key.Matches(msg, DefaultKeyMap.OpenEditor) {
		return m, openEditorCmd(m.input.Value())
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
	case key.Matches(msg, DefaultKeyMap.ToggleChatter):
		m.showChatter = !m.showChatter
		m.resizeComponents()
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

	cmds := []tea.Cmd{ringBell(pattern, m.muted)}
	cmds = append(cmds, m.processPendingRings()...)
	return m, tea.Batch(cmds...)
}

// processPendingRings is the layered ring-answering policy itself
// (config.RingPolicy's own doc comment explains why this lives here
// rather than as a direct macula-mcp contact_policy mapping): a peer
// already on ContactPolicyFile's own allowlist is auto-accepted
// immediately, no pop-up; anyone else gets the real pop-up, one at a
// time -- a ring already seenRingIDs (auto-accepted, answered, or
// dismissed) is never revisited. Mutates m directly since this is only
// ever called from within handleRefresh, which already holds its own
// value-receiver copy.
//
// Never force-switches OUT of ModeInsert: found live 2026-09-06 -- an
// incoming ring while the operator was mid-composition silently yanked
// them into ModeRingPopup, and the in-progress input just vanished from
// view (the pop-up's own render path doesn't touch or preserve m.input at
// all). A not-yet-shown ring simply stays in m.state.pending/not yet in
// seenRingIDs -- still visible in the status strip's own "pending rings"
// count the whole time -- and this same function picks it up on a later
// refresh tick once the operator leaves ModeInsert on their own. Trusted
// auto-accepts are unaffected either way: they were never interactive.
func (m *Model) processPendingRings() []tea.Cmd {
	var cmds []tea.Cmd
	for _, ring := range m.state.pending {
		if m.seenRingIDs[ring.RingID] {
			continue
		}
		if m.autoAcceptKnown && contactpolicy.IsTrusted(m.contactPolicyFile, ring.Peer) {
			m.seenRingIDs[ring.RingID] = true
			m.chatEntries = append(m.chatEntries, chatEntry{
				kind: chatSystem,
				at:   time.Now(),
				text: fmt.Sprintf("auto-accepted ring from %s (trusted)", displayName(ring.Peer, ring.PeerPetname)),
			})
			cmds = append(cmds, answerRingCmd(m.mcp, ring.RingID, answerAccept, false, ""))
			continue
		}
		if m.mode != ModeInsert && m.pendingRingPopup == nil {
			r := ring
			m.pendingRingPopup = &r
			m.mode = ModeRingPopup
		}
	}
	if len(cmds) > 0 {
		m.syncViewport()
	}
	return cmds
}

// isChatter reports whether ev is routine tool-call activity (mesh
// operations) rather than dialogue -- the "technical chatter" issue #2
// relocates out of the conversation pane by default. Assistant messages,
// errors, and system notices (backoff, max-failures) always stay in the
// chat pane regardless of showChatter -- they're not routine, an operator
// needs to see them there.
func isChatter(kind agent.EventKind) bool {
	return kind == agent.EventToolCall || kind == agent.EventToolResult
}

func (m Model) handleAgentEvent(ev agentEventMsg) (Model, tea.Cmd) {
	pattern := bellNone
	switch ev.Kind {
	case agent.EventBackoff, agent.EventMaxFailuresReached:
		pattern = bellTriple
	}

	entry := chatEntryFromAgentEvent(agent.Event(ev))
	if isChatter(ev.Kind) && !m.showChatter {
		m.lastChatter = entry.render(false)
		m.resizeComponents() // the chatter line may be appearing for the first time
	} else {
		m.chatEntries = append(m.chatEntries, entry)
		m.syncViewport()
	}
	return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
}

// resizeComponents fits the chat viewport to whatever's left after the
// status block, the input line, and one blank line of slack. The status
// block's own height isn't fixed any more (issue #2's auto-grow: a mode
// indicator line always, a chatter line once any tool activity has
// happened), so this has to be called again whenever statusLines' length
// can change -- not just on tea.WindowSizeMsg -- or the viewport would
// either overflow the terminal or leave a stale gap.
// handleEditorFinished loads the $EDITOR round-trip's result into the
// compose line and always lands in Insert -- whether openEditorCmd was
// triggered from Normal or Insert, the natural next step is reviewing/
// sending what was just written, not going back to navigation.
func (m Model) handleEditorFinished(msg editorFinishedMsg) (Model, tea.Cmd) {
	if msg.path != "" {
		defer os.Remove(msg.path)
	}
	if msg.err != nil {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatError, at: time.Now(), text: fmt.Sprintf("$EDITOR composition failed: %s", msg.err)})
		m.syncViewport()
		return m, nil
	}
	content, err := os.ReadFile(msg.path)
	if err != nil {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatError, at: time.Now(), text: fmt.Sprintf("could not read composed message: %s", err)})
		m.syncViewport()
		return m, nil
	}
	m.input.SetValue(strings.TrimRight(string(content), "\n"))
	m.input.CursorEnd()
	m.mode = ModeInsert
	return m, m.input.Focus()
}

func (m *Model) resizeComponents() {
	reserved := len(m.statusLines()) + 2 // input line + one blank line of slack
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

// Colors match the macula brand palette (see chat.go's own comment) --
// structural chrome (panel borders, titles, the status strip) uses the
// same brand blue as every macula-*-full-*.svg logo, not lipgloss's
// generic 256-color example palette (panelStyle/titleStyle/
// statusStripStyle were ANSI 62/212/212 -- an arbitrary purple and an
// arbitrary pink, no connection to this project's brand).
var (
	panelStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#38BDF8")).Padding(0, 1)
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusStripStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
)

func (m Model) View() string {
	status := m.renderStatusStrip()

	// The ring pop-up takes over the body/input area entirely while
	// active -- a real incoming call blocks until answered too -- but the
	// status strip stays visible either way, same ambient-awareness
	// principle as the mesh view's own expand/collapse.
	if m.mode == ModeRingPopup && m.pendingRingPopup != nil {
		popup := m.renderRingPopup()
		if m.statusBarPosition == "top" {
			return strings.Join([]string{status, popup}, "\n")
		}
		return strings.Join([]string{popup, status}, "\n")
	}

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

// statusLines is the status block's content, one entry per rendered line.
// resizeComponents' reserved-height math counts this slice directly, so
// any new line kind that can appear/disappear at runtime (the chatter
// line here) must go through this, not be appended ad hoc in
// renderStatusStrip -- otherwise the viewport height and what's actually
// on screen drift apart.
func (m Model) statusLines() []string {
	lines := []string{m.renderModeIndicator()}
	if chatter := m.renderChatterLine(); chatter != "" {
		lines = append(lines, chatter)
	}
	lines = append(lines, m.renderSummaryLine())
	return lines
}

// renderModeIndicator follows vim's own bottom-of-screen convention
// (`-- INSERT --` etc.) -- issue #2's "mode indicator" ask, made its own
// line rather than folded into the summary line so it's legible at a
// glance rather than buried mid-sentence.
func (m Model) renderModeIndicator() string {
	switch m.mode {
	case ModeInsert:
		return statusStripStyle.Render("-- INSERT --")
	case ModeRingPopup:
		return statusStripStyle.Render("-- RING --")
	default:
		return dimStyle.Render("-- NORMAL --")
	}
}

// renderChatterLine is the ambient stand-in for tool-call activity that
// isChatter routed out of the chat pane. Empty (rendered as no line at
// all, see statusLines) until the first one arrives, and suppressed
// entirely once showChatter is on -- that activity is already inline in
// the chat pane at that point, so this would just be the same line twice.
func (m Model) renderChatterLine() string {
	if m.showChatter || m.lastChatter == "" {
		return ""
	}
	// m.lastChatter is already a rendered chatEntry line (its own
	// time/tool styling) -- prefix only, don't re-wrap it in dimStyle,
	// which would fight the ANSI codes already embedded in it.
	return dimStyle.Render("last: ") + m.lastChatter
}

func (m Model) renderSummaryLine() string {
	line := fmt.Sprintf("%d rooms · %d pending rings · %d agents seen",
		len(m.state.joined), len(m.state.pending), len(m.state.agents))
	if m.agentModel != "" {
		line += " · " + m.agentModel
	}
	if m.muted {
		line += "  [muted]"
	}

	hint := "m: mesh view  i: compose  ctrl+e: $EDITOR  v: verbose  e: expand  b: mute  q: quit"
	if m.mode == ModeInsert {
		hint = "esc: normal mode  enter: send  ctrl+e: edit in $EDITOR"
	}

	out := statusStripStyle.Render(line) + "  " + dimStyle.Render(hint)
	if m.lastErr != nil {
		out += "  " + errStyle.Render(fmt.Sprintf("(refresh error: %s)", m.lastErr))
	}
	return out
}

func (m Model) renderStatusStrip() string {
	return strings.Join(m.statusLines(), "\n")
}

// renderInputLine always shows the compose line, in either mode (issue
// #4: "always visible", vim-modal input model unchanged) -- a draft
// started in Insert and left with Esc stays visible, just unfocused,
// rather than disappearing behind a static hint as before. What actually
// differs between modes is whether keystrokes route into it at all,
// handled entirely in handleKey; there's nothing mode-specific to do here.
func (m Model) renderInputLine() string {
	return m.input.View()
}

func (m Model) renderExpandedMesh() string {
	var b strings.Builder
	b.WriteString(panelStyle.Render(m.renderRooms()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPendingRings()) + "\n")
	b.WriteString(panelStyle.Render(m.renderPresence()))
	return b.String()
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

// displayName prefers petname (a deterministic, human-legible label
// macula-mcp derives from the node_id itself, never self-asserted) over
// the raw hex id -- "don't make a human read raw hex" applied to agent
// identity specifically. Requires macula-mcp >= 0.24.0; petname empty
// (an older server, or the field genuinely absent) falls back to the
// shortened id exactly as before.
func displayName(nodeID, petname string) string {
	if petname != "" {
		return petname
	}
	return shortID(nodeID)
}

// roomLabel prefers a room's purpose (set when the room was opened with
// one, e.g. via mesh_open_room's purpose arg) over its raw topic hex --
// the same principle as displayName, applied to rooms: purpose is a
// human-written sentence, the topic is a 32-hex-char identifier nobody
// can be expected to recognize on sight. Purpose is often absent for
// older/purposeless rooms, so this falls back to the shortened topic
// exactly as before.
func roomLabel(topic, purpose string) string {
	if purpose != "" {
		return purpose
	}
	return shortTopic(topic)
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

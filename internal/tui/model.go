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
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/contactpolicy"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
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
	// ModeRealmJoin: typing a realm name to join, in the `r` panel --
	// its own mode, not a repurposed ModeInsert, because `i` means
	// something different depending on which overlay is showing (compose
	// a message normally; type a realm name while the realm panel is
	// open) and the two draft inputs (realmJoinInput vs. the main
	// compose input) must never share state.
	ModeRealmJoin
	// ModeMeshServiceCall: typing raw JSON arguments to invoke the `s`
	// panel's currently-selected curated procedure directly -- see
	// plans/PLAN_DIRECT_MESH_SERVICE_CALLS.md for why this exists (a
	// non-AI door into the same meshservices.Source the agent's own tool
	// chain calls through, so an operator who already knows which
	// procedure they want never has to pay mesh_services_enabled's
	// fixed-prefix token cost just to have the AI decide to call it on
	// their behalf). Same reasoning as ModeRealmJoin for being its own
	// mode with its own draft input, not a repurposed ModeInsert.
	ModeMeshServiceCall
	// ModeErrorPopup: reading the last error in full, wrapped and
	// scrollable, with a copy key. Its own mode for the same reason as
	// the others -- "c" means copy only while it is open, and "esc"
	// closes it rather than leaving whatever mode was underneath.
	ModeErrorPopup
	// ModeApprovalPopup: answering a per-action approval prompt (G9) --
	// the tool call waits, nothing else proceeds until y/n lands.
	ModeApprovalPopup
)

// Options configures a new Model. Zero values are all valid (no agent
// running, no ring auto-accept, status strip at the bottom).
type Options struct {
	AgentEvents <-chan agent.Event // nil when no --room agent is running
	UserInputCh chan<- string      // where a submitted message is sent for runAgent to pick up

	// InterruptCh receives a signal on the `x` key (normal mode): the
	// caller cancels the in-flight turn's context. nil when unwired, in
	// which case `x` reports rather than sending.
	InterruptCh chan<- struct{}

	// ApprovalCh receives the operator's decision on an approval popup
	// (G9). nil when unwired, in which case the popup's y/n still
	// dismisses the prompt but the answer goes nowhere (the session's
	// own approval timeout then refuses the call).
	ApprovalCh chan<- ApprovalAnswer

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

	// MeshServices backs the `s` panel (renderMeshServicesOverlay) --
	// nil when cfg.MeshServicesEnabled is false, in which case the panel
	// still shows the curated catalog (it's static data, meshservices.
	// Curated, not gated by this field) but marks it inactive rather
	// than showing a blank screen. THE SAME Source instance runAgent's
	// own tool chain calls through, not a second one -- so what a human
	// sees here is exactly what the agent can actually reach, not an
	// independent, potentially-diverging query (found while scoping,
	// 2026-09-08: this Source used to be built and wrapped entirely
	// inside cmd/lazymesh's buildToolSource, invisible outside it).
	MeshServices *meshservices.Source

	// MaculaMCPVersion and RealmIdentityFile back the `r` panel's own
	// join affordance -- passed straight to internal/realmjoin.Join, never
	// through m.mcp/the MCP session at all (see that package's own doc
	// comment for why joining a realm must never be a tool call).
	// RealmIdentityFile MUST be the exact MACULA_MCP_IDENTITY the running
	// macula-mcp server (m.mcp) is using -- main.go gets this from
	// mcpclient.Client.IdentityFile(), not a second guess at it, or a
	// fresh join would mint a credential for a node_id nobody's actual
	// mesh presence uses.
	MaculaMCPVersion  string
	RealmIdentityFile string

	// LogPath is the append-mode agent log. The `l` key opens it in
	// $EDITOR rather than rendering it: the file already exists, and an
	// editor already has search, jumping, copy and highlighting that an
	// overlay would only reimplement worse. Empty when not wired, which
	// `l` reports rather than opening an editor on nothing.
	LogPath string
}

// Model is the bubbletea model for lazymesh's TUI.
type Model struct {
	mcp          toolCaller
	meshServices *meshservices.Source // nil when cfg.MeshServicesEnabled is false -- see Options.MeshServices

	agentEvents     <-chan agent.Event
	userInputCh     chan<- string
	interruptCh     chan<- struct{}
	approvalCh      chan<- ApprovalAnswer
	pendingApproval *pendingApproval

	contactPolicyFile string
	autoAcceptKnown   bool
	seenRingIDs       map[string]bool // rings already auto-accepted, answered via the pop-up, or dismissed -- never re-surfaced

	state   meshState
	lastErr error

	mode                 Mode
	meshExpanded         bool
	meshServicesExpanded bool // `s` -- see Options.MeshServices and renderMeshServicesOverlay
	meshServicesCursor   int  // selected row in the `s` panel's table -- clamped in handleKey's Up/Down cases, not here, since this field alone doesn't know the current entry count
	realmExpanded        bool // `r` -- see renderRealmsOverlay
	detailsExpanded      bool // global expand/collapse for tool-call detail in chat
	muted                bool
	statusBarPosition    string // "top" or "bottom"
	agentModel           string // see Options.AgentModel

	// showChatter controls whether routine tool-call activity (mesh
	// operations) reaches the conversation pane at all. Off by default:
	// isChatter events are silently dropped, keeping the pane to actual
	// dialogue (you/agent/error/system). Toggling it on (the 'v' key)
	// shows every tool call/result inline as its own line in chatEntries,
	// for anyone who wants the full blow-by-blow. Used to also drive an
	// ambient one-line status-block summary of the most recent one;
	// dropped live 2026-09-07 (little practical value, per Raf) --
	// suppressed now means genuinely not shown, not shown elsewhere.
	// EventListening (see lastListeningAt) is deliberately NOT gated by
	// this: it's a liveness heartbeat, not routine chatter, and stays
	// visible regardless.
	showChatter bool
	// lastListeningAt is #13/#15's liveness heartbeat: EventListening
	// fires once per agent-loop cycle even when nothing else is
	// happening, and an advancing timestamp is what makes a frozen
	// process distinguishable from a correctly-idle one (a frozen one
	// shows the same time forever). Rendered in the summary line, not a
	// standalone status line -- the standalone "last: <chatter>" line
	// this used to share a mechanism with was dropped live 2026-09-07
	// (little practical value, per Raf) but the heartbeat itself is a
	// different, still-load-bearing concern and survives that in
	// condensed form. Zero until the first one arrives.
	lastListeningAt time.Time

	pendingRingPopup *pendingRing // the one ring currently shown, nil if none

	// The error pop-up's own state, nil when it is closed. Separate from
	// lastErr: lastErr is the live condition, this is the reading of it,
	// so closing the pop-up does not pretend the error went away.
	errorPopup *errorPopup

	// Distinct errors seen this run, oldest first, bounded. A repeating
	// refresh failure is one entry with a count, not one entry per tick.
	errorHistory []errorRecord

	logPath string

	// A new error is waiting to be shown. Set when one first appears,
	// cleared when the pop-up actually opens -- which may be several
	// keystrokes later, if the operator was composing or reading an
	// overlay at the time.
	autoPopPending bool

	// The `r` panel's own join affordance -- realmjoin.Join is called
	// directly from here, never through m.mcp/the agent's own tool
	// chain (see internal/realmjoin's own doc comment). realmJoinInput
	// is a SEPARATE textinput.Model from the main compose `input` below
	// -- deliberately never shares a draft with it, even though both are
	// "type some text and press enter" -- one is a message to the
	// agent, the other is a security-sensitive realm name a human is
	// meant to type deliberately (see realm_name.ts's own doc comment
	// on macula-mcp's side for why that must stay a distinct action).
	maculaMCPVersion  string
	realmIdentityFile string
	realmJoinInput    textinput.Model
	realmJoinEvents   <-chan realmjoin.Event // nil when no join is in flight
	realmJoinLatest   *realmjoin.Event       // the most recent event for the in-flight (or just-finished) join, nil once dismissed

	// ModeMeshServiceCall's own draft input -- a SEPARATE textinput.Model
	// from input/realmJoinInput, same reasoning as realmJoinInput's own
	// doc comment: never share a draft across genuinely different actions
	// just because both are "type text and press enter". meshServiceCall
	// Procedure is captured when `i` starts the call (the cursor row at
	// that moment), not re-read from the cursor at Submit time -- the
	// panel's row order can't change mid-input (nothing re-fetches
	// Snapshot() while typing), but naming the field for what it freezes
	// makes that invariant obvious rather than assumed. InFlight blocks a
	// stray second `i`/Submit from firing a second concurrent call, same
	// guard shape as realmJoinLatest == nil for ModeRealmJoin.
	meshServiceCallInput     textinput.Model
	meshServiceCallProcedure string
	meshServiceCallInFlight  bool

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

	realmInput := textinput.New()
	realmInput.Placeholder = "io.macula"
	realmInput.CharLimit = 253 // realm_name.ts's own MAX_LENGTH -- reject client-side at the same bound, not just server-side
	realmInput.Prompt = "join realm: "

	serviceCallInput := textinput.New()
	serviceCallInput.Placeholder = `{} or {"key": "value"}`
	serviceCallInput.CharLimit = 4000
	serviceCallInput.Prompt = "args (json): "

	statusBarPosition := opts.StatusBarPosition
	if statusBarPosition != "top" {
		statusBarPosition = "bottom"
	}

	return Model{
		mcp:                  client,
		meshServices:         opts.MeshServices,
		maculaMCPVersion:     opts.MaculaMCPVersion,
		realmIdentityFile:    opts.RealmIdentityFile,
		agentEvents:          opts.AgentEvents,
		userInputCh:          opts.UserInputCh,
		interruptCh:          opts.InterruptCh,
		approvalCh:           opts.ApprovalCh,
		contactPolicyFile:    opts.ContactPolicyFile,
		autoAcceptKnown:      opts.AutoAcceptKnown,
		seenRingIDs:          make(map[string]bool),
		mode:                 ModeNormal,
		statusBarPosition:    statusBarPosition,
		agentModel:           opts.AgentModel,
		logPath:              opts.LogPath,
		input:                ti,
		realmJoinInput:       realmInput,
		meshServiceCallInput: serviceCallInput,
		chatViewport:         viewport.New(80, 20),
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

	return runEditorCmd(path, func(err error) tea.Msg {
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

// realmJoinEventMsg wraps one internal/realmjoin.Event for bubbletea's
// own message loop.
type realmJoinEventMsg realmjoin.Event

// waitForRealmJoinEvent blocks for the next event on ch -- same shape as
// waitForAgentEvent, re-armed by handleRealmJoinEvent below after every
// non-terminal ("session") event, so the whole join is driven by this
// one channel read at a time, never a poll.
func waitForRealmJoinEvent(ch <-chan realmjoin.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return realmJoinEventMsg(ev)
	}
}

// realmJoinFunc is internal/realmjoin.Join, called through a package var
// -- same override-for-tests convention realmjoin.newCommand itself
// uses -- so a test can substitute a fake without ever risking a real
// npx/macula-mcp-realm spawn (realmjoin's own test suite already covers
// the subprocess machinery itself; this package's tests only need to
// verify startRealmJoin's OWN wiring: right args in, right Model/Cmd
// out). Never reassigned outside a test.
var realmJoinFunc = realmjoin.Join

// startRealmJoin execs internal/realmjoin.Join directly -- NEVER a call
// through m.mcp -- and starts listening for its events. Sets
// m.realmJoinLatest to a synthetic "starting" event in this SAME Update
// cycle, before returning -- not left nil until the first real event
// arrives. Found live 2026-09-09 (Raf: "'join one' does...nothing after
// entering io.macula"): renderRealms falls back to the plain
// list/placeholder whenever m.realmJoinLatest is nil, so leaving it nil
// for however long npx takes to resolve and exec macula-mcp-realm (a
// cold fetch with no local npx cache is genuinely seconds, not
// instant) read as "typed a realm name, nothing happened" -- the
// previous version of this comment assumed the first real event would
// always arrive "fast enough that there's nothing useful to show in
// between," which is exactly the assumption that broke. Setting it
// immediately also closes a second, related gap: the `i`-key guard
// above (`m.realmExpanded && m.realmJoinLatest == nil`) that's meant to
// stop a stray `i` from starting a second join on top of one already in
// flight only actually held once the first real event arrived -- during
// this same gap it did nothing, so a second `i` press could fire a
// second concurrent join.
func (m Model) startRealmJoin(realmName string) (Model, tea.Cmd) {
	ctx := context.Background() // this join's own lifetime is independent of any single request/response cycle -- it runs until the session resolves or the process exits
	events, err := realmJoinFunc(ctx, m.maculaMCPVersion, m.realmIdentityFile, realmName)
	if err != nil {
		ev := realmjoin.Event{Kind: "spawn_error", Realm: realmName, Message: err.Error()}
		m.realmJoinLatest = &ev
		return m, nil
	}
	starting := realmjoin.Event{Kind: "starting", Realm: realmName}
	m.realmJoinLatest = &starting
	m.realmJoinEvents = events
	return m, waitForRealmJoinEvent(events)
}

// handleRealmJoinEvent updates the panel with the latest event and, for
// a non-terminal one, re-arms the listener for the next.
func (m Model) handleRealmJoinEvent(msg realmJoinEventMsg) (Model, tea.Cmd) {
	ev := realmjoin.Event(msg)
	m.realmJoinLatest = &ev
	if ev.Terminal() {
		m.realmJoinEvents = nil
		return m, nil
	}
	return m, waitForRealmJoinEvent(m.realmJoinEvents)
}

// meshServiceCallResultMsg carries CallToolRaw's own (result, err) pair
// back into bubbletea's message loop -- one request/response, not a
// stream, so this is refreshCmd's shape (single Cmd, single Msg), not
// realmJoinEventMsg's (a channel of events re-armed after each one).
type meshServiceCallResultMsg struct {
	procedure string
	result    string
	err       error
}

// meshServiceCallSource is the subset of *meshservices.Source this package
// actually calls -- same override-for-tests convention as realmJoinFunc,
// so a test can substitute a fake CallToolRaw without a real mesh_call
// RPC. *meshservices.Source itself satisfies this.
type meshServiceCallSource interface {
	CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error)
}

// startMeshServiceCall invokes CallToolRaw asynchronously (a real network
// RPC -- never block Update) and reports the result through
// meshServiceCallResultMsg. ctx is background, same reasoning as
// startRealmJoin's own: this call's lifetime is independent of any single
// keystroke, and CallToolRaw's own timeout (callTimeoutMS) is what
// actually bounds it, not this context.
func startMeshServiceCall(source meshServiceCallSource, procedure, argumentsJSON string) tea.Cmd {
	return func() tea.Msg {
		result, err := source.CallToolRaw(context.Background(), procedure, argumentsJSON)
		return meshServiceCallResultMsg{procedure: procedure, result: result, err: err}
	}
}

// handleMeshServiceCallResult appends the outcome as a chat entry --
// chatError for a failure (bad JSON, rate-limited, RPC error -- CallToolRaw
// itself distinguishes these in its own error text), chatToolResult
// otherwise, both prefixed "[direct]" so this transcript entry is never
// mistaken for something the AI decided to do (see chat.go's own doc
// comment on chatEntryKind for why that distinction matters).
func (m Model) handleMeshServiceCallResult(msg meshServiceCallResultMsg) (Model, tea.Cmd) {
	m.meshServiceCallInFlight = false
	m.chatEntries = append(m.chatEntries, directMeshServiceCallEntry(msg))
	m.syncViewport()
	return m, nil
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
		return m.maybeAutoPopError(), tea.Batch(m.refreshCmd(), tick())

	case refreshMsg:
		return m.handleRefresh(msg)

	case agentEventMsg:
		return m.handleAgentEvent(msg)

	case tea.MouseMsg:
		// Wheel scrolling for the chat pane (the program runs with
		// WithMouseCellMotion). Overlays own the screen while open, and
		// popup modes answer keys, not wheels -- so the chat viewport
		// only receives the mouse when it is actually the surface on
		// screen.
		if m.meshExpanded || m.realmExpanded || m.meshServicesExpanded {
			return m, nil
		}
		if m.mode != ModeNormal && m.mode != ModeInsert {
			return m, nil
		}
		var cmd tea.Cmd
		m.chatViewport, cmd = m.chatViewport.Update(msg)
		return m, cmd

	case ringAnsweredMsg:
		return m.handleRingAnswered(msg)

	case editorFinishedMsg:
		return m.handleEditorFinished(msg)

	case logViewedMsg:
		return m.handleLogViewed(msg)

	case realmJoinEventMsg:
		return m.handleRealmJoinEvent(msg)

	case meshServiceCallResultMsg:
		return m.handleMeshServiceCallResult(msg)
	}
	return m, nil
}

// handleKey applies the keypress, then shows any error that was held
// back while the operator was busy. The check lives here rather than in
// Update's dispatch so it holds for every caller, including tests
// driving keys directly.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	updated, cmd := m.applyKey(msg)
	next, ok := updated.(Model)
	if !ok {
		return updated, cmd
	}
	return next.maybeAutoPopError(), cmd
}

func (m Model) applyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, DefaultKeyMap.ForceQuit) {
		return m, tea.Quit
	}

	if m.mode == ModeRingPopup {
		return m.handleRingPopupKey(msg)
	}

	if m.mode == ModeApprovalPopup {
		return m.handleApprovalPopupKey(msg)
	}

	if m.mode == ModeErrorPopup {
		return m.handleErrorPopupKey(msg)
	}

	// Works from either Normal or Insert -- composing in $EDITOR is useful
	// as a way INTO a message (from Normal) just as much as a way to
	// finish one already started (from Insert), and always lands in
	// Insert with the edited text loaded either way.
	if key.Matches(msg, DefaultKeyMap.OpenEditor) {
		return m, openEditorCmd(m.input.Value())
	}

	if m.mode == ModeRealmJoin {
		switch {
		case key.Matches(msg, DefaultKeyMap.Normal):
			m.realmJoinInput.Blur()
			m.realmJoinInput.Reset() // unlike the main compose input, no draft is kept -- a realm name is a one-shot action, not something to resume composing
			m.mode = ModeNormal
			m.resizeComponents()
			return m, nil
		case key.Matches(msg, DefaultKeyMap.Submit):
			name := strings.TrimSpace(m.realmJoinInput.Value())
			m.realmJoinInput.Reset()
			m.realmJoinInput.Blur()
			m.mode = ModeNormal
			m.resizeComponents()
			if name == "" {
				return m, nil
			}
			return m.startRealmJoin(name)
		default:
			var cmd tea.Cmd
			m.realmJoinInput, cmd = m.realmJoinInput.Update(msg)
			return m, cmd
		}
	}

	if m.mode == ModeMeshServiceCall {
		switch {
		case key.Matches(msg, DefaultKeyMap.Normal):
			m.meshServiceCallInput.Blur()
			m.meshServiceCallInput.Reset() // one-shot action, no draft kept -- same reasoning as realmJoinInput
			m.meshServiceCallProcedure = ""
			m.mode = ModeNormal
			m.resizeComponents()
			return m, nil
		case key.Matches(msg, DefaultKeyMap.Submit):
			argsJSON := strings.TrimSpace(m.meshServiceCallInput.Value())
			procedure := m.meshServiceCallProcedure
			m.meshServiceCallInput.Reset()
			m.meshServiceCallInput.Blur()
			m.meshServiceCallProcedure = ""
			m.mode = ModeNormal
			m.resizeComponents()
			if procedure == "" || m.meshServices == nil {
				// Panel was disabled or the procedure vanished between `i`
				// and Submit (shouldn't happen -- nothing re-fetches
				// Snapshot() mid-input -- but never invoke with an empty
				// procedure name either way).
				return m, nil
			}
			m.meshServiceCallInFlight = true
			return m, startMeshServiceCall(m.meshServices, procedure, argsJSON)
		default:
			var cmd tea.Cmd
			m.meshServiceCallInput, cmd = m.meshServiceCallInput.Update(msg)
			return m, cmd
		}
	}

	if m.mode == ModeInsert {
		switch {
		case key.Matches(msg, DefaultKeyMap.Normal):
			m.input.Blur()
			m.mode = ModeNormal
			m.resizeComponents() // hint row goes from 1 line (Insert) to 2 (Normal)
			return m, nil
		case key.Matches(msg, DefaultKeyMap.Submit):
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.input.Blur()
			m.mode = ModeNormal
			m.resizeComponents() // hint row goes from 1 line (Insert) to 2 (Normal)
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
	case key.Matches(msg, DefaultKeyMap.Normal):
		// Esc's only meaning in Normal mode: dismiss a finished/errored
		// join's status (renderRealmJoinProgress) back to the plain
		// membership list, without leaving the `r` panel itself. A no-op
		// everywhere else -- Normal mode has nothing else for Esc to do.
		if m.realmExpanded && m.realmJoinLatest != nil {
			m.realmJoinLatest = nil
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Insert):
		// `i` means something different depending on which overlay is
		// showing: compose a message to the agent normally, type a
		// realm name while the `r` panel is open, or type arguments to
		// directly invoke the `s` panel's selected procedure -- but only
		// when there's a membership list/curated row to act on, not
		// while a previous join's QR/status is still showing (dismiss
		// that with Esc first, so a stray `i` can never fire a second
		// join on top of one still in flight), and not while a mesh
		// service call is already in flight (same reasoning).
		if m.realmExpanded && m.realmJoinLatest == nil {
			m.mode = ModeRealmJoin
			m.resizeComponents()
			return m, m.realmJoinInput.Focus()
		}
		if m.meshServicesExpanded && m.meshServices != nil && !m.meshServiceCallInFlight {
			entries, _ := m.meshServiceEntries()
			if m.meshServicesCursor < len(entries) {
				m.meshServiceCallProcedure = entries[m.meshServicesCursor].Procedure()
				m.mode = ModeMeshServiceCall
				m.resizeComponents()
				return m, m.meshServiceCallInput.Focus()
			}
		}
		m.mode = ModeInsert
		m.resizeComponents() // hint row goes from 2 lines (Normal) to 1 (Insert)
		return m, m.input.Focus()
	case key.Matches(msg, DefaultKeyMap.ToggleMesh):
		// Mutually exclusive with the other two overlays, not stacked --
		// more than one showing at once would halve (or worse) the chat
		// margin pin-to-top already keeps tight, for concerns (mesh
		// STATE vs. available SERVICES vs. realm MEMBERSHIP) that are
		// never all what an operator wants to see in the same glance.
		m.meshExpanded = !m.meshExpanded
		if m.meshExpanded {
			m.meshServicesExpanded = false
			m.realmExpanded = false
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleMeshServices):
		m.meshServicesExpanded = !m.meshServicesExpanded
		if m.meshServicesExpanded {
			m.meshExpanded = false
			m.realmExpanded = false
		} else {
			// Reopening starts at the top, not wherever the cursor was
			// left -- same reasoning as realmJoinLatest getting cleared
			// when the `r` panel closes (see ToggleRealm's own case):
			// stale position from last time isn't what "open the panel"
			// should mean.
			m.meshServicesCursor = 0
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleRealm):
		m.realmExpanded = !m.realmExpanded
		if m.realmExpanded {
			m.meshExpanded = false
			m.meshServicesExpanded = false
		} else {
			// Leaving the panel entirely also dismisses whatever join
			// status was showing -- reopening starts from the plain
			// membership list, not a stale QR/error from last time.
			m.realmJoinLatest = nil
		}
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
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Interrupt):
		// Phase 3: cancel the in-flight turn. A non-blocking send --
		// the caller's own interrupt handling must never stall the TUI.
		if m.interruptCh != nil {
			select {
			case m.interruptCh <- struct{}{}:
				m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: "interrupting the current turn..."})
				m.syncViewport()
			default:
			}
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleLogs):
		return m.openAgentLog()
	case key.Matches(msg, DefaultKeyMap.ShowError):
		// Opens on the newest and steps back from there. An error that
		// has already been dismissed is still reachable, which is the
		// point of keeping the list at all.
		return m.openErrorPopup(len(m.errorHistory) - 1), nil
	case key.Matches(msg, DefaultKeyMap.Up):
		if m.meshServicesExpanded {
			if m.meshServicesCursor > 0 {
				m.meshServicesCursor--
			}
		} else if !m.meshExpanded && !m.realmExpanded {
			m.chatViewport.LineUp(1)
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Down):
		if m.meshServicesExpanded {
			entries, _ := m.meshServiceEntries()
			if m.meshServicesCursor < len(entries)-1 {
				m.meshServicesCursor++
			}
		} else if !m.meshExpanded && !m.realmExpanded {
			m.chatViewport.LineDown(1)
		}
		return m, nil
	}

	// Page keys scroll the chat pane a viewport at a time when no
	// overlay is up -- the viewport's own key handling, forwarded rather
	// than re-implemented (same reason the mouse wheel is forwarded).
	if !m.meshExpanded && !m.realmExpanded && !m.meshServicesExpanded {
		switch msg.String() {
		case "pgup", "pgdown", "home", "end":
			var cmd tea.Cmd
			m.chatViewport, cmd = m.chatViewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) handleRefresh(msg refreshMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.lastErr = msg.err
		// Only a FIRST sighting asks for the screen. The refresh loop
		// runs every two seconds, so popping per occurrence would put
		// the program behind a box that reopens faster than it can be
		// dismissed.
		if m.recordError(msg.err, time.Now()) {
			m.autoPopPending = true
		}
		return m.maybeAutoPopError(), nil
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
			m.resizeComponents() // hint row goes from 2 lines (Normal) to 1 (Ring)
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
// needs to see them there. EventListening joins the chatter set for the
// same reason ToolCall/ToolResult are here: it fires every cycle
// (#13/#15's liveness signal), and would drown out real conversation if
// it went to the main pane instead of the status strip.
func isChatter(kind agent.EventKind) bool {
	return kind == agent.EventToolCall || kind == agent.EventToolResult || kind == agent.EventListening
}

func (m Model) handleAgentEvent(ev agentEventMsg) (Model, tea.Cmd) {
	pattern := bellNone
	switch ev.Kind {
	case agent.EventBackoff, agent.EventMaxFailuresReached:
		pattern = bellTriple
	}

	if ev.Kind == agent.EventListening {
		// Always tracked, regardless of showChatter -- see
		// lastListeningAt's own doc comment: this is a liveness
		// heartbeat, a different concern from "should routine tool-call
		// chatter clutter the chat pane" below.
		m.lastListeningAt = time.Now()
	}

	if ev.Kind == agent.EventApprovalRequested {
		return m.showApproval(ApprovalRequestEvent{Tool: ev.ToolName, ID: ev.ID, Args: ev.Text})
	}

	if isChatter(ev.Kind) && !m.showChatter {
		// Routine tool-call activity, suppressed by default (see
		// isChatter's own doc comment) -- genuinely dropped now, not
		// shown elsewhere either. 'v' still shows it inline in the chat
		// pane for anyone who wants the full detail.
		return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
	}

	// D3 streaming: deltas grow the in-progress assistant entry, and the
	// turn's completed message finishes it -- one chat entry per turn,
	// however many chunks it took. The completed text replaces the
	// accumulated deltas so a final message that differs from the sum of
	// its chunks (never the case here, but the contract is the message is
	// authoritative) wins.
	if ev.Kind == agent.EventAssistantDelta {
		if n := len(m.chatEntries); n > 0 && m.chatEntries[n-1].kind == chatAssistant && m.chatEntries[n-1].streaming {
			m.chatEntries[n-1].text += ev.Text
			m.syncViewport()
			return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
		}
	} else if ev.Kind == agent.EventAssistantMessage {
		if n := len(m.chatEntries); n > 0 && m.chatEntries[n-1].kind == chatAssistant && m.chatEntries[n-1].streaming {
			m.chatEntries[n-1].text = ev.Text
			m.chatEntries[n-1].streaming = false
			m.syncViewport()
			return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
		}
	}

	entry := chatEntryFromAgentEvent(agent.Event(ev))
	m.chatEntries = append(m.chatEntries, entry)
	m.syncViewport()
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
	m.resizeComponents() // hint row goes from 2 lines (Normal) to 1 (Insert) -- a no-op if already Insert
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
	// Follow mode: new content pins the bottom ONLY when the operator was
	// already reading the bottom. Someone scrolled up in the history must
	// keep their place while new entries (and, mid-turn, every streaming
	// delta) arrive below — SetContent preserves the offset on its own,
	// so only the already-at-bottom case re-anchors.
	atBottom := m.chatViewport.AtBottom()
	lines := make([]string, 0, len(m.chatEntries))
	for i := range m.chatEntries {
		lines = append(lines, m.chatEntries[i].render(m.detailsExpanded, m.width))
	}
	m.chatViewport.SetContent(strings.Join(lines, "\n"))
	if atBottom {
		m.chatViewport.GotoBottom()
	}
}

// Colors match the macula brand palette (see chat.go's own comment) --
// structural chrome (the status strip) uses the same brand blue as every
// macula-*-full-*.svg logo, not lipgloss's generic 256-color example
// palette (panelStyle/titleStyle/statusStripStyle were ANSI 62/212/212 --
// an arbitrary purple and an arbitrary pink, no connection to this
// project's brand).
//
// panelStyle/titleStyle specifically were also brand blue and bold until
// 2026-09-08: once the mesh view started overlaying the panels on top of
// the chat pane instead of replacing it (renderMeshOverlay, "transparency"
// per Raf), a solid bright-blue bordered box read as a popup taking over
// the screen rather than a light layer over the conversation still
// mostly visible around it -- lightened to a dim, muted border/title
// (matching dimStyle) to read as a lighter overlay instead, same
// proportions/sizing as before, just visually quieter.
var (
	panelStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	titleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusStripStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	// summaryStyle is statusStripStyle's normal-weight sibling, for the
	// rooms/pending-rings/agents-seen/model portion of the summary line
	// specifically -- found live 2026-09-07: bold there read as louder
	// than that information warrants, especially now that the instance's
	// own petname (renderSummaryLine, its own distinct bold+color via
	// agentBadgeStyle) needs to be the thing that visually stands out on
	// that line, not compete with it.
	summaryStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
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

	// The approval pop-up takes over the same area the ring pop-up does:
	// a waiting tool call blocks the turn, and the prompt is the one
	// thing the operator needs to see.
	if m.mode == ModeApprovalPopup && m.pendingApproval != nil {
		popup := strings.Join(m.renderApprovalPopup(m.width), "\n")
		if m.statusBarPosition == "top" {
			return strings.Join([]string{status, popup}, "\n")
		}
		return strings.Join([]string{popup, status}, "\n")
	}

	if m.mode == ModeErrorPopup && m.errorPopup != nil {
		popup := m.renderErrorPopup()
		if m.statusBarPosition == "top" {
			return strings.Join([]string{status, popup}, "\n")
		}
		return strings.Join([]string{popup, status}, "\n")
	}

	var body string
	switch {
	case m.meshExpanded:
		body = m.renderMeshOverlay()
	case m.meshServicesExpanded:
		body = m.renderMeshServicesOverlay()
	case m.realmExpanded:
		body = m.renderRealmsOverlay()
	default:
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
// any new line kind that can appear/disappear at runtime (the hint row's
// own 1-vs-2-line count, depending on mode) must go through this, not be
// appended ad hoc in renderStatusStrip -- otherwise the viewport height
// and what's actually on screen drift apart.
func (m Model) statusLines() []string {
	lines := m.renderHintLines()
	return append(lines, m.renderSummaryLine())
}

// renderModeIndicator follows vim's own bottom-of-screen convention
// (`-- INSERT --` etc.).
func (m Model) renderModeIndicator() string {
	switch m.mode {
	case ModeInsert:
		return statusStripStyle.Render("-- INSERT --")
	case ModeRingPopup:
		return statusStripStyle.Render("-- RING --")
	case ModeRealmJoin:
		return statusStripStyle.Render("-- REALM --")
	case ModeMeshServiceCall:
		return statusStripStyle.Render("-- MESH CALL --")
	default:
		return dimStyle.Render("-- NORMAL --")
	}
}

// renderHintLines is the shortcuts row, leading with the mode indicator.
// Briefly split across 2 lines (2026-09-07) after the combined line
// routinely ran well past a narrow terminal's width; reverted back to 1
// line 2026-09-08 (Raf's own call, after weighing it directly: the extra
// row of vertical space matters more day to day than the clipping risk
// on a narrow terminal). Also retires what used to be the mode
// indicator's own separate status line: merging it into this line
// removes a line from the block for the same information, rather than
// adding one. The ring pop-up already shows its own hints
// (renderRingPopup), so needs none here.
func (m Model) renderHintLines() []string {
	mode := m.renderModeIndicator()
	switch m.mode {
	case ModeRingPopup:
		return []string{mode}
	case ModeInsert:
		return []string{mode + "  " + dimStyle.Render("esc: normal mode  enter: send  ctrl+e: edit in $EDITOR")}
	case ModeRealmJoin:
		return []string{mode + "  " + dimStyle.Render("esc: cancel  enter: join")}
	case ModeMeshServiceCall:
		return []string{mode + "  " + dimStyle.Render("esc: cancel  enter: call "+m.meshServiceCallProcedure)}
	default:
		// `i` and `esc` both mean something different depending on which
		// overlay is showing -- see handleKey's own Insert and Normal
		// cases -- so the hint names the actual action, not a fixed label.
		insertHint := "i: compose"
		if m.realmExpanded {
			if m.realmJoinLatest != nil {
				insertHint = "esc: dismiss"
			} else {
				insertHint = "i: join a realm"
			}
		}
		if m.meshServicesExpanded && m.meshServices != nil {
			insertHint = "↑↓: select  i: call selected"
		}
		return []string{mode + "  " + dimStyle.Render("m: mesh view  s: mesh services  r: realms  "+insertHint+"  ctrl+e: $EDITOR  v: verbose  e: expand  b: mute  q: quit")}
	}
}

// renderSummaryLine is the mesh-state line: this instance's own identity
// (when known), then the rooms/pending-rings/agents-seen counts and the
// configured model. The identity segment is bold, in its own
// deterministic color (agentBadgeStyle -- the same per-agent color
// scheme the presence badge and room-message previews already use, see
// identity.go), so it's the one thing on this line that visually stands
// out; found live 2026-09-07 that bolding the whole line competed with
// that instead of setting it apart, so the rest of the line
// (rooms/rings/agents/model) is normal weight (summaryStyle).
func (m Model) renderSummaryLine() string {
	counts := fmt.Sprintf("%d rooms · %d pending rings · %d agents seen",
		len(m.state.joined), len(m.state.pending), len(m.state.agents))
	out := summaryStyle.Render(counts)

	if m.agentModel != "" {
		// Bold (statusStripStyle -- the weight this whole line shared
		// before 2026-09-07) rather than a new color: Raf's own choice
		// between the two, 2026-09-08. Distinct enough from the rest of
		// the line without picking a color that would visually read as
		// another per-agent identity (agentBadgeStyle's own deterministic
		// palette below is reserved for that).
		out += summaryStyle.Render(" · ") + statusStripStyle.Render(m.agentModel)
	}

	var tail string
	if m.muted {
		tail += "  [muted]"
	}
	if !m.lastListeningAt.IsZero() {
		tail += " · listening " + m.lastListeningAt.Format("15:04:05")
	}
	if tail != "" {
		out += summaryStyle.Render(tail)
	}

	// Leads with "who am I" when known -- the dual-instance identity bug
	// (two lazymesh instances silently sharing one mesh node_id, fixed in
	// 8622117) was only visible by comparing raw node_ids or Presence
	// panel rows across terminals; this makes distinctness confirmable at
	// a glance, in the one place that's always on screen.
	if self, ok := selfAgent(m.state.agents); ok {
		identity := identityKey(self.NodeID, self.Petname)
		name := agentBadgeStyle(identity).Render(displayName(self.NodeID, self.Petname))
		out = name + summaryStyle.Render(" · ") + out
	}
	if m.lastErr != nil {
		out += "  " + formatSummaryError(m.lastErr)
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

// padToBodyHeight fills content with trailing blank lines up to the same
// body height resizeComponents already targets for the chat viewport
// (m.height - len(statusLines()) - 2, the "-2" being the input line plus
// one line of slack) -- anchors a shorter block to the screen edge
// (issue #12) rather than leaving the status bar wherever the block's own
// natural height happened to end. Never truncates -- a block taller than
// the available body height is left as-is.
func (m Model) padToBodyHeight(content string) string {
	target := m.height - len(m.statusLines()) - 2
	if target < 1 {
		return content
	}
	lines := strings.Count(content, "\n") + 1
	if lines >= target {
		return content
	}
	return content + strings.Repeat("\n", target-lines)
}

// renderOverlay composites ANY panel content over the chat pane rather
// than replacing it outright: real conversation lines stay visible in a
// margin below the panel(s) ("transparency", per Raf 2026-09-08) instead
// of the panel eating the entire body area edge to edge. Terminals can't
// do true alpha blending, so this is the practical equivalent -- the
// actual chat text, not a blank or dimmed backdrop (dimming an
// already-styled multi-segment chat line correctly would need
// re-emitting its ANSI state, not just wrapping it -- tried live
// 2026-09-08, a naive Faint() wrap breaks at the line's own first inner
// reset code, undimming everything after it).
//
// Pinned to the top of the body area (Raf, 2026-09-08, once the panels'
// own styling was lightened enough that this stopped reading as a
// centered popup): previously centered vertically with a margin split
// above and below, which needed a "don't repeat the same short
// conversation's lines in both margins" special case entirely of its
// own. Pinning to the top removes that whole class of problem -- there's
// only one margin now, below the panel, showing the newest chat lines
// (the panel effectively "covers" everything older, the same way a card
// dropped onto a scrolled page would).
//
// Shared by renderMeshOverlay (`m`) and renderMeshServicesOverlay (`s`,
// 2026-09-08) -- they differ only in what panelContent is, not in how it
// sits over the chat pane; extracted rather than duplicated once a
// second overlay needed the identical layout.
func (m Model) renderOverlay(panelContent string) string {
	panel := strings.Split(panelContent, "\n")
	target := m.height - len(m.statusLines()) - 2 // same target padToBodyHeight/resizeComponents use
	if target < 1 || len(panel) >= target {
		// No room for a visible margin either way -- the panel alone
		// already fills (or exceeds) the available height. Falls back to
		// the old full-bleed behavior rather than truncating it further;
		// it has no scroll of its own.
		return m.padToBodyHeight(strings.Join(panel, "\n"))
	}

	chat := m.chatContentLines()
	margin := target - len(panel)

	lines := make([]string, 0, target)
	lines = append(lines, panel...)
	lines = append(lines, chatMarginLines(chat, len(chat)-margin, len(chat))...)
	return strings.Join(lines, "\n")
}

func (m Model) renderMeshOverlay() string {
	return m.renderOverlay(m.renderExpandedMesh())
}

// renderMeshServicesOverlay is the `s` panel's own overlay, same layout
// as renderMeshOverlay (`m`) but showing internal/meshservices' curated
// catalog instead of Rooms/Pending rings/Presence -- a deliberately
// separate toggle, not a 4th panel merged into that stack: mesh STATE
// (who's here, what rooms exist) and available SERVICES (what the agent
// can call on the mesh) are different questions, and the existing
// stack's own pin-to-top layout already has just enough margin left for
// the chat pane without a 4th panel competing for it.
func (m Model) renderMeshServicesOverlay() string {
	return m.renderOverlay(panelStyle.Render(m.renderMeshServices()))
}

// renderRealmsOverlay is the `r` panel's own overlay, same shape as `m`/
// `s` and mutually exclusive with both.
func (m Model) renderRealmsOverlay() string {
	return m.renderOverlay(panelStyle.Render(m.renderRealms()))
}

// chatContentLines is the chat pane's actual content, one entry per
// rendered line -- deliberately NOT chatViewport.View()'s output, which
// pads a short conversation with blank filler lines at the bottom
// (anchored-top rendering, always exactly chatViewport.Height lines
// regardless of how much real content there is). Using that padded
// output here made renderMeshOverlay's bottom margin -- meant to be the
// newest chat lines -- slice into that blank filler instead, found live
// 2026-09-08 rendering an actual conversation. Mirrors syncViewport's own
// construction exactly, so it's always consistent with what the normal
// (non-overlay) chat pane would show.
func (m Model) chatContentLines() []string {
	lines := make([]string, 0, len(m.chatEntries))
	for i := range m.chatEntries {
		lines = append(lines, m.chatEntries[i].render(m.detailsExpanded, m.width))
	}
	joined := strings.Join(lines, "\n")
	if joined == "" {
		return nil
	}
	return strings.Split(joined, "\n")
}

// chatMarginLines returns lines[max(from,0):min(to,len(lines))],
// blank-padded up to the requested (to-from) count when the chat pane
// itself doesn't have that many lines yet (a fresh or short
// conversation). from may be negative (the caller computing a "last N"
// window on a short slice) -- handled the same as an out-of-range clamp,
// not a special case.
func chatMarginLines(lines []string, from, to int) []string {
	want := to - from
	if want <= 0 {
		return nil
	}
	if from < 0 {
		from = 0
	}
	if to > len(lines) {
		to = len(lines)
	}
	out := make([]string, 0, want)
	if from < to {
		out = append(out, lines[from:to]...)
	}
	for len(out) < want {
		out = append(out, "")
	}
	return out
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

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

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/checkoutnotes"
	"github.com/macula-io/macula-lazymesh/internal/contactpolicy"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
	"github.com/macula-io/macula-lazymesh/internal/termkeys"
)

// refreshInterval is how often the mesh-state panels re-poll macula-mcp.
// These are documented as instant local reads, so this can be short
// without cost.
const refreshInterval = 2 * time.Second

type refreshMsg struct {
	state meshState
	err   error
	// serverVersion is the release macula-mcp reports NOW, read on every
	// refresh rather than once at start: a respawn is transparent (the
	// same Client keeps answering), and an unpinned macula-mcp floats to
	// npm's latest on every spawn, so the release behind a healthy-looking
	// session can change under it -- the segment has to follow, or it
	// keeps naming a release that is no longer running
	// (macula-lazymesh#61 review).
	serverVersion string
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
	// ModeCompose is the default and only "idle" state: the composer has
	// focus, enter sends, and every key that is not a pop-up's, a panel's or
	// the leader's is a letter. There is no normal/insert split any more
	// (macula-lazymesh#62): a bare key is something someone meant to type.
	ModeCompose Mode = iota
	ModeRingPopup
	// ModeRealmJoin: typing a realm name to join, in the `r` panel --
	// its own mode, not a repurposed ModeCompose, because `i` means
	// something different depending on which overlay is showing (compose
	// a message normally; type a realm name while the realm panel is
	// open) and the two draft inputs (realmJoinInput vs. the main
	// compose input) must never share state.
	ModeRealmJoin
	// ModeMeshServiceCall: typing raw JSON arguments to invoke the `s`
	// panel's currently-selected curated procedure directly. Why it exists:
	// a non-AI door into the same meshservices.Source the agent's own tool
	// chain calls through, so an operator who already knows which
	// procedure they want never has to pay mesh_services_enabled's
	// fixed-prefix token cost just to have the AI decide to call it on
	// their behalf. Same reasoning as ModeRealmJoin for being its own
	// mode with its own draft input, not a repurposed ModeCompose.
	ModeMeshServiceCall
	// ModeErrorPopup: reading the last error in full, wrapped and
	// scrollable, with a copy key. Its own mode for the same reason as
	// the others -- "c" means copy only while it is open, and "esc"
	// closes it rather than leaving whatever mode was underneath.
	ModeErrorPopup
	// ModeApprovalPopup: answering a per-action approval prompt (G9) --
	// the tool call waits, nothing else proceeds until y/n lands.
	ModeApprovalPopup
	// ModeMeshServicesPanel: the `s` panel has focus -- up/down move the
	// cursor and enter calls the selected procedure, so the composer cannot
	// take those keys while it is open. Esc closes it and gives focus back.
	ModeMeshServicesPanel
	// ModeNotesPanel: reading a checkout's note in full before trusting it
	// (macula-io/macula-lazymesh#44). Its own mode because t means trust
	// only here, the scroll keys move the note's content rather than the
	// chat, and esc closes the panel instead of leaving whatever was
	// underneath.
	ModeNotesPanel
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

	// Attached is true when this TUI reached the session over its control
	// socket (lazymesh attach) rather than running it. It is set by the
	// caller, not inferred from a nil client: a caller with no mesh client
	// and one with no session are different things, and only the second
	// should stop showing mesh counts.
	Attached bool

	// ApprovalResolvedCh reports approvals answered while this TUI is
	// open — including ones another frontend answered first. nil when
	// unwired (the in-process TUI is the only frontend that can answer).
	// An attached TUI needs it so a popup that lost the race closes
	// itself instead of sitting there looking like a decision still to be
	// made (macula-io/macula-lazymesh#40's threat model).
	ApprovalResolvedCh <-chan frontend.ApprovalResolved

	StatusBarPosition string // "top" or "bottom"

	// Leader is the resolved key that arms the command leader, from the
	// config file's leader_key (see config.Config.LeaderKey) through
	// LeaderBinding, which is what refuses an unusable choice -- a key the
	// message box uses, ctrl+c, and anything that is not ctrl and one
	// letter. Resolved by the caller so the TUI never holds a leader it
	// cannot arm; the zero value means the default, ctrl+x.
	Leader key.Binding

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

	// MaculaMCPReportedVersion is the release macula-mcp named in its own
	// MCP handshake -- mcpclient.Client.ServerVersion(), the version
	// actually running rather than the one npm or the config would have
	// suggested. The summary line shows it as `mcp <version> ●`, which is
	// how a person can tell lazymesh is using macula-mcp and which release
	// (macula-lazymesh#61). Leave it empty when there is no macula-mcp of
	// this process's own to report (an attached TUI): the line then
	// carries no mcp segment, which is true, rather than a fabricated one.
	// MaculaMCPVersion above says whether a release is PINNED (non-empty)
	// or floating (empty), and the segment says `(pinned)` for the former.
	MaculaMCPReportedVersion string

	// CheckoutNotes is what the working directory's AGENTS.md/CLAUDE.md
	// said at start, with each one's trust state; CheckoutRefused is every
	// such file that was not delivered and why. CheckoutDir is the working
	// directory the panel re-reads before recording trust, and
	// CheckoutTrustPath is the record the `t` key writes (beside the
	// operator's config.yaml, never in the checkout). Empty in the
	// mesh-only profile, which keeps reading those files as the operator's
	// own instructions.
	CheckoutNotes     []checkoutnotes.Note
	CheckoutRefused   []checkoutnotes.Refused
	CheckoutDir       string
	CheckoutTrustPath string

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
	// attachMode is true when this TUI reached the session over its
	// control socket instead of running it (macula-lazymesh#40). It has no
	// macula-mcp of its own, so the mesh strip says so rather than
	// reporting counts it cannot see.
	attachMode bool

	agentEvents        <-chan agent.Event
	userInputCh        chan<- string
	interruptCh        chan<- struct{}
	approvalCh         chan<- ApprovalAnswer
	approvalResolvedCh <-chan frontend.ApprovalResolved
	pendingApproval    *pendingApproval

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
	// `N` -- the checkout-notes panel (macula-io/macula-lazymesh#44), and
	// the notes as read at start. Deliberately never re-read while
	// running: the content the operator trusts must be the bytes the model
	// was actually given, not whatever the file says by the time they press
	// t.
	notesPanel        *notesPanel
	notesCursor       int
	checkoutNotes     []checkoutnotes.Note
	refusedNotes      []checkoutnotes.Refused
	checkoutDir       string
	checkoutTrustPath string
	detailsExpanded   bool // global expand/collapse for tool-call detail in chat
	muted             bool
	statusBarPosition string // "top" or "bottom"
	agentModel        string // see Options.AgentModel

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

	// now is the clock the pop-ups' arming guard reads (#59). nil means
	// time.Now; tests set it so a 600 ms guard costs no wall clock.
	now func() time.Time

	// leaderPending is true between the leader key and the key after it
	// (#62). While it is set the next key is a command or a cancel, never a
	// letter: what someone meant to type cannot become a command, and a
	// command cannot land in the draft. Which key arms it is configurable
	// (#63): leader holds the configured binding, and every place that names
	// the key reads it from there.
	leaderPending bool
	leader        key.Binding

	// lastKeyAt is when a key last reached the TUI. A pop-up opens only when
	// this is popupIdleDelay in the past, so one cannot take the screen in the
	// middle of a word (#62).
	lastKeyAt time.Time

	// deferredApproval is the one approval request that arrived while the
	// person was busy, held until they are free to see it (nil when none is).
	// One, not a queue: the session has at most one question outstanding
	// (sessionhost's approvalSlot), so a newer request means the held one is
	// dead and is replaced -- showing the dead one first would put a prompt on
	// screen for a call nobody is waiting on any more, and then swap it for
	// the live one under the person's eyes (macula-lazymesh#62 review r=2).
	deferredApproval *ApprovalRequestEvent

	// lastInputAt is when this TUI last sent a message to the agent.
	// lastListeningAt (below) is its other half: the agent emits a listening
	// beat when it parks between turns, so a send that no beat has followed is
	// a turn in flight, which is what esc interrupts.
	lastInputAt time.Time

	// approvalArm and ringArm are the two pop-ups' arming state (#59): the
	// answer keys do nothing until a pause has passed, so a key meant for a
	// message cannot answer a prompt that appeared under the cursor.
	approvalArm popupArm
	ringArm     popupArm
	// approvalReturnTo and ringReturnTo are the modes the two pop-ups took
	// focus from, one each: both can be open at once, and one shared slot
	// hands focus back to the wrong pop-up (see refocus).
	approvalReturnTo Mode
	ringReturnTo     Mode

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

	// mcpReportedVersion is the release macula-mcp said it was in its own
	// handshake (mcpclient.Client.ServerVersion), shown on the summary line
	// so a person can see macula-mcp is in use and which release. Empty in
	// an attached TUI, which has no macula-mcp of its own, and then the
	// line carries no mcp segment at all rather than a made-up one
	// (macula-lazymesh#61).
	mcpReportedVersion string
	// mcpConnected is whether the last refresh call reached macula-mcp:
	// true until one fails or the subprocess exits, which is exactly what
	// the summary line's green/red segment reports. It says nothing about
	// the mesh beyond that (an unreachable peer is not a disconnected
	// macula-mcp).
	mcpConnected bool

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
	sel          selectionState // shift+drag selection over the chat pane
	chatViewport viewport.Model
	input        PromptEditor

	width  int
	height int
}

// New builds a Model that reads mesh state through client, renders
// agentEvents into the chat pane as they arrive, and sends composed
// messages on userInputCh -- see Options' own doc comment for what each
// zero value means.
func New(client toolCaller, opts Options) Model {
	// The chatbox (D3, opencode's own look-and-feel): the PromptEditor
	// component owns the key semantics (enter = newline, alt+enter =
	// send) -- see prompteditor.go.
	//
	// Focused here, before Init: the composer is the default focus since
	// #62, and a blurred textarea ignores every key. Init re-focuses it,
	// which is also what starts the cursor's blink loop.
	ti := NewPromptEditor()
	ti.Focus()

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

	// The leader from config, already resolved and refused-if-unusable by
	// LeaderBinding (main.go does it before the TUI opens). Unset means the
	// default: an operator who never wrote leader_key gets ctrl+x.
	leader := opts.Leader
	if len(leader.Keys()) == 0 {
		leader = DefaultKeyMap.Leader
	}

	m := Model{
		mcp:                client,
		attachMode:         opts.Attached,
		meshServices:       opts.MeshServices,
		maculaMCPVersion:   opts.MaculaMCPVersion,
		mcpReportedVersion: opts.MaculaMCPReportedVersion,
		// The TUI is only built once macula-mcp is up (main.go spawns it
		// first, and a failed spawn never reaches here), so the first
		// refresh has something to reach. A refresh that then fails is
		// what turns the segment red.
		mcpConnected:         true,
		realmIdentityFile:    opts.RealmIdentityFile,
		agentEvents:          opts.AgentEvents,
		userInputCh:          opts.UserInputCh,
		interruptCh:          opts.InterruptCh,
		approvalCh:           opts.ApprovalCh,
		approvalResolvedCh:   opts.ApprovalResolvedCh,
		contactPolicyFile:    opts.ContactPolicyFile,
		autoAcceptKnown:      opts.AutoAcceptKnown,
		seenRingIDs:          make(map[string]bool),
		mode:                 ModeCompose,
		statusBarPosition:    statusBarPosition,
		leader:               leader,
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
		muted:             false,
		checkoutNotes:     append([]checkoutnotes.Note{}, opts.CheckoutNotes...),
		refusedNotes:      append([]checkoutnotes.Refused{}, opts.CheckoutRefused...),
		checkoutDir:       opts.CheckoutDir,
		checkoutTrustPath: opts.CheckoutTrustPath,
	}
	// One line at start naming the macula-mcp this session is running on
	// (macula-lazymesh#61): the summary segment is a glance, this is the
	// record in the conversation itself, in the same voice as the rest of
	// the chat's system lines. Left out for an attached TUI, which has no
	// macula-mcp of its own to have connected to.
	if m.mcpReportedVersion != "" {
		m.chatEntries = append(m.chatEntries, chatEntry{
			kind: chatSystem,
			at:   time.Now(),
			text: fmt.Sprintf("connected to macula-mcp %s over stdio", m.mcpReportedVersion),
		})
	}
	// A file the operator trusted that has changed since is announced
	// rather than quietly demoted: the next start delivers it as data, and
	// the operator should hear that from the TUI, not discover it by
	// wondering why the model ignored their notes.
	for _, n := range m.checkoutNotes {
		if !n.Changed {
			continue
		}
		m.chatEntries = append(m.chatEntries, chatEntry{
			kind: chatSystem,
			at:   time.Now(),
			text: fmt.Sprintf("%s changed since you trusted it: it reaches the model as data until you trust this version again (N to review).", n.Name),
		})
	}
	return m
}

func (m Model) Init() tea.Cmd {
	// Push the kitty keyboard protocol NOW: Init is the first thing that
	// runs after bubbletea enters the alternate screen, and kitty scopes
	// protocol pushes to the screen that is active when they arrive. A
	// push from before the TUI started would sit on the main screen's
	// stack and never affect this screen. Popped on the quit paths below,
	// while the alternate screen is still current.
	fmt.Fprint(os.Stdout, termkeys.Enable)
	cmds := []tea.Cmd{m.refreshCmd(), tick(), m.input.Focus()}
	if m.agentEvents != nil {
		cmds = append(cmds, waitForAgentEvent(m.agentEvents))
	}
	if m.approvalResolvedCh != nil {
		cmds = append(cmds, waitForApprovalResolved(m.approvalResolvedCh))
	}
	return tea.Batch(cmds...)
}

// quit pops the keyboard protocol from the alternate screen's stack --
// bubbletea leaves the screen only after this Update returns, so the pop
// still lands on the right screen -- and quits the program.
func (m Model) quit() tea.Cmd {
	fmt.Fprint(os.Stdout, termkeys.Disable)
	return tea.Quit
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) refreshCmd() tea.Cmd {
	client := m.mcp
	if client == nil {
		// An attached TUI (lazymesh attach) has no macula-mcp of its own:
		// the session it watches owns the mesh connection. This is not a
		// failure, so it is not reported as one — an error pop-up on every
		// attach would greet the operator with a wall they cannot act on,
		// and empty counts would read as an idle mesh rather than an
		// unshown one. renderSummaryLine says what is true instead.
		return func() tea.Msg { return refreshMsg{state: meshState{}} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state, err := fetchMeshState(ctx, client)
		// Read after the calls, so a respawn this refresh triggered (or
		// found) is already reflected: Client.ServerVersion reads the
		// session that is live now.
		var version string
		if reporter, ok := client.(versionReporter); ok {
			version = reporter.ServerVersion()
		}
		return refreshMsg{state: state, err: err, serverVersion: version}
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

// waitForApprovalResolved is waitForAgentEvent for the approval
// resolution stream an attached TUI reads from its control socket.
func waitForApprovalResolved(ch <-chan frontend.ApprovalResolved) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return approvalResolvedMsg(r)
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
		debugKey(msg)
		return m.handleKey(msg)

	case tickMsg:
		m, held := m.maybeShowDeferredApproval()
		return m.maybeAutoPopError(), tea.Batch(m.refreshCmd(), tick(), held)

	case refreshMsg:
		return m.handleRefresh(msg)

	case agentEventMsg:
		return m.handleAgentEvent(msg)

	case approvalResolvedMsg:
		return m.handleApprovalResolved(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

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
	m.lastKeyAt = m.clock()
	updated, cmd := m.applyKey(msg)
	next, ok := updated.(Model)
	if !ok {
		return updated, cmd
	}
	next, held := next.maybeShowDeferredApproval()
	return next.maybeAutoPopError(), tea.Batch(cmd, held)
}

func (m Model) applyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, DefaultKeyMap.ForceQuit) {
		return m, m.quit()
	}

	// A pop-up or a focused panel owns the keyboard while it is open. The
	// leader cannot be pending here: opening either one cancels it, so a key
	// pressed after ctrl+x can never answer a prompt.
	switch m.mode {
	case ModeRingPopup:
		return m.handleRingPopupKey(msg)
	case ModeApprovalPopup:
		return m.handleApprovalPopupKey(msg)
	case ModeErrorPopup:
		return m.handleErrorPopupKey(msg)
	case ModeNotesPanel:
		return m.handleNotesPanelKey(msg)
	case ModeMeshServicesPanel:
		return m.handleMeshServicesPanelKey(msg)
	case ModeRealmJoin:
		return m.handleRealmJoinKey(msg)
	case ModeMeshServiceCall:
		return m.handleMeshServiceCallKey(msg)
	}

	// The leader is armed: the next key is a command, whether or not it is
	// one, and it never reaches the draft.
	if m.leaderPending {
		return m.runLeader(msg)
	}

	if key.Matches(msg, m.leader) {
		m.leaderPending = true
		m.resizeComponents()
		return m, nil
	}

	if key.Matches(msg, DefaultKeyMap.OpenEditor) {
		return m, openEditorCmd(m.input.Value())
	}

	if key.Matches(msg, DefaultKeyMap.Normal) {
		return m.escape()
	}

	// Chat scrolling, on the keys the composer leaves free. The read-only
	// overlays cover the chat, so there is nothing to scroll while one shows.
	if !m.meshExpanded && !m.realmExpanded {
		switch {
		case key.Matches(msg, DefaultKeyMap.ScrollLineUp):
			m.chatViewport.LineUp(1)
			return m, nil
		case key.Matches(msg, DefaultKeyMap.ScrollLineDown):
			m.chatViewport.LineDown(1)
			return m, nil
		case key.Matches(msg, DefaultKeyMap.ScrollPageUp), key.Matches(msg, DefaultKeyMap.ScrollPageDown):
			var cmd tea.Cmd
			m.chatViewport, cmd = m.chatViewport.Update(msg)
			return m, cmd
		}
	}

	// The realms list shows while the composer keeps focus, so enter has to
	// mean both things: with a draft it sends, and with an empty draft it
	// opens the join input. A key meant for a message never starts a join.
	if key.Matches(msg, DefaultKeyMap.Submit) && m.realmExpanded && m.realmJoinLatest == nil &&
		strings.TrimSpace(m.input.Value()) == "" {
		m.mode = ModeRealmJoin
		m.resizeComponents()
		return m, m.realmJoinInput.Focus()
	}

	// Everything else is the composer's.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeComponents() // the chatbox may have grown a row
	if m.input.Submitted() {
		text := strings.TrimSpace(m.input.Value())
		m.input.Reset()
		if text == "" {
			return m, cmd
		}
		m.chatEntries = append(m.chatEntries, youChatEntry(text))
		m.lastInputAt = time.Now()
		m.syncViewport()
		return m, tea.Batch(cmd, sendUserInput(m.userInputCh, text))
	}
	return m, cmd
}

// escape is esc's whole meaning, in the order the design sets: close what is
// open, cancel a pending leader, or stop a running turn. When none of those is
// true it does nothing at all and the draft stays exactly as it was -- it is
// not a way out of a mode, because there is no mode to leave.
func (m Model) escape() (tea.Model, tea.Cmd) {
	switch {
	case m.notesPanel != nil:
		return m.handleNotesPanelKey(tea.KeyMsg{Type: tea.KeyEsc})
	case m.meshServicesExpanded:
		m.meshServicesExpanded = false
		m.meshServicesCursor = 0
		m.resizeComponents()
		focus := m.composerFocus()
		return m, focus
	case m.realmExpanded:
		if m.realmJoinLatest != nil {
			// A finished join's status first, the panel second.
			m.realmJoinLatest = nil
			return m, nil
		}
		m.realmExpanded = false
		return m, nil
	case m.meshExpanded:
		m.meshExpanded = false
		return m, nil
	case m.leaderPending:
		m.leaderPending = false
		m.resizeComponents()
		return m, nil
	case m.turnRunning():
		return m.interruptTurn()
	}
	return m, nil
}

// runLeader runs the command the key after ctrl+x names. A key that names no
// command cancels the leader: whatever it was, it was not meant for the
// draft, and it is not replayed later.
func (m Model) runLeader(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.leaderPending = false
	m.resizeComponents()

	if key.Matches(msg, DefaultKeyMap.Normal) {
		return m, nil
	}

	switch {
	case key.Matches(msg, DefaultKeyMap.Quit):
		return m, m.quit()
	case key.Matches(msg, DefaultKeyMap.ToggleMesh):
		// Mutually exclusive with the other two overlays, not stacked --
		// more than one showing at once would halve (or worse) the chat
		// margin pin-to-top already keeps tight, for concerns (mesh STATE
		// vs. available SERVICES vs. realm MEMBERSHIP) that are never all
		// what an operator wants to see in the same glance.
		m.meshExpanded = !m.meshExpanded
		if m.meshExpanded {
			m.meshServicesExpanded = false
			m.realmExpanded = false
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleMeshServices):
		return m.openMeshServicesPanel()
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
	case key.Matches(msg, DefaultKeyMap.ToggleNotes):
		m.openNotes()
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleLogs):
		return m.openAgentLog()
	case key.Matches(msg, DefaultKeyMap.ShowError):
		// Opens on the newest and steps back from there. An error that
		// has already been dismissed is still reachable, which is the
		// point of keeping the list at all.
		return m.openErrorPopup(len(m.errorHistory) - 1), nil
	case key.Matches(msg, DefaultKeyMap.ToggleDetails):
		m.detailsExpanded = !m.detailsExpanded
		m.syncViewport()
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleChatter):
		m.showChatter = !m.showChatter
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ToggleQuiet):
		m.muted = !m.muted
		return m, nil
	case key.Matches(msg, DefaultKeyMap.CopyChat):
		return m.copyLastAnswer()
	case key.Matches(msg, DefaultKeyMap.Interrupt):
		return m.interruptTurn()
	case key.Matches(msg, DefaultKeyMap.ScrollTop):
		if !m.meshExpanded && !m.realmExpanded {
			m.chatViewport.GotoTop()
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.ScrollBottom):
		if !m.meshExpanded && !m.realmExpanded {
			m.chatViewport.GotoBottom()
		}
		return m, nil
	case key.Matches(msg, DefaultKeyMap.LeaderHelp):
		m.explainLeader()
		return m, nil
	}
	return m, nil
}

// personIdle reports whether a pop-up may take the screen: nobody is typing
// into anything (the composer, a panel's own input, an armed leader), and no
// key has arrived for popupIdleDelay. A prompt appearing mid-word is exactly
// what #59 is about; this is the other half of that fix -- it does not appear
// mid-word at all, so the arming guard has less to catch.
//
// Neither a read-only overlay nor another pop-up counts as "busy". An overlay
// has no draft to protect and gives its screen back when the prompt closes,
// and keys that reach a pop-up are handled (and guarded) there, never typed
// into anything -- so a ring arriving while an approval waits is shown rather
// than queued behind a question nobody may answer.
func (m Model) personIdle() bool {
	switch m.mode {
	case ModeCompose, ModeRingPopup, ModeApprovalPopup, ModeErrorPopup:
	default:
		return false
	}
	if m.meshServicesExpanded || m.notesPanel != nil || m.leaderPending {
		return false
	}
	return m.lastKeyAt.IsZero() || m.clock().Sub(m.lastKeyAt) >= popupIdleDelay
}

// composerIdle is personIdle with the composer itself in front and nothing
// overlaid on it. An error may only pop over the composer: over an open
// pop-up it would replace a question the operator is already answering (the
// ring or approval underneath), and closing it would leave that pop-up with
// nothing showing it.
func (m Model) composerIdle() bool {
	return m.mode == ModeCompose && !m.meshExpanded && !m.realmExpanded && m.personIdle()
}

// approvalMayOpen adds the approval's own rule to personIdle: it never
// replaces a prompt that is already on screen. A second request would put
// different arguments under a person already reading the first, with a fresh
// guard, so a y meant for the call they read would approve the one they have
// not. The session has at most one question outstanding, so the held one is
// the newest and the older is dead (macula-lazymesh#62 review r=2).
func (m Model) approvalMayOpen() bool {
	return m.pendingApproval == nil && m.personIdle()
}

// composerFocus hands the keyboard back to the composer if it has not got it.
// It returns no command when the composer is still focused: focusing again
// would restart the cursor's blink loop for nothing.
//
// A pointer receiver on purpose: focusing mutates the textarea, and a value
// receiver would focus a copy and hand back a model that still ignores keys.
func (m *Model) composerFocus() tea.Cmd {
	if m.input.Focused() {
		return nil
	}
	return m.input.Focus()
}

// maybeShowDeferredApproval shows the approval request that arrived while the
// person was busy, once they are not. The tool call is waiting on the answer
// either way; showing it a moment later is the whole difference.
func (m Model) maybeShowDeferredApproval() (Model, tea.Cmd) {
	if m.deferredApproval == nil || !m.approvalMayOpen() {
		return m, nil
	}
	ev := *m.deferredApproval
	m.deferredApproval = nil
	return m.showApprovalNow(ev)
}

// turnRunning reports whether the agent is in the middle of a turn: this TUI
// sent a message and the agent has not emitted a listening beat since -- it
// emits one when it parks between turns (see lastListeningAt). Esc interrupts
// only while this holds, so a stray esc while the agent is idle does nothing.
func (m Model) turnRunning() bool {
	return !m.lastInputAt.IsZero() && m.lastInputAt.After(m.lastListeningAt)
}

// interruptTurn cancels the in-flight turn. A non-blocking send: the caller's
// own interrupt handling must never stall the TUI.
func (m Model) interruptTurn() (tea.Model, tea.Cmd) {
	if m.interruptCh != nil {
		select {
		case m.interruptCh <- struct{}{}:
			m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: "interrupting the current turn..."})
			m.syncViewport()
		default:
		}
	}
	return m, nil
}

// takeServerVersion follows the release macula-mcp is actually running, as
// every refresh re-reads it. The first report is simply taken -- the start
// line already said what the session began on. A CHANGE is a respawn that
// brought up a different release (an unpinned macula-mcp floats to npm's
// latest on every spawn), and it is announced in the chat: the segment alone
// would change under the operator's eyes mid-conversation with no word about
// what happened, and the rest of the conversation's answers and tool calls
// now came from a different build (macula-lazymesh#61 review).
func (m Model) takeServerVersion(v string) Model {
	if v == "" || v == m.mcpReportedVersion {
		return m
	}
	was := m.mcpReportedVersion
	m.mcpReportedVersion = v
	if was == "" {
		return m
	}
	m.chatEntries = append(m.chatEntries, chatEntry{
		kind: chatSystem,
		at:   time.Now(),
		text: fmt.Sprintf("macula-mcp is now %s (was %s)", v, was),
	})
	return m
}

// explainLeader writes the whole keymap into the chat, where it can be read,
// scrolled and copied like anything else on screen. It is the same list the
// hint row offers while the leader is pending, so there is nothing to learn
// twice.
func (m *Model) explainLeader() {
	var b strings.Builder
	b.WriteString(m.leaderLabel() + " then:")
	for _, c := range leaderHelp() {
		b.WriteString("  " + c.Key + " " + c.Action)
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: b.String()})
	m.syncViewport()
}

// handleRealmJoinKey types a realm name in the `r` panel. Esc cancels without
// starting anything, enter joins, and the composer is back either way with its
// draft untouched -- a realm name is a one-shot action, not something to resume
// composing.
func (m Model) handleRealmJoinKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, DefaultKeyMap.Normal):
		m.realmJoinInput.Blur()
		m.realmJoinInput.Reset()
		m.mode = ModeCompose
		m.resizeComponents()
		focus := m.composerFocus()
		return m, focus
	case key.Matches(msg, DefaultKeyMap.Submit):
		name := strings.TrimSpace(m.realmJoinInput.Value())
		m.realmJoinInput.Reset()
		m.realmJoinInput.Blur()
		m.mode = ModeCompose
		m.resizeComponents()
		if name == "" {
			focus := m.composerFocus()
			return m, focus
		}
		next, cmd := m.startRealmJoin(name)
		focus := next.composerFocus()
		return next, tea.Batch(cmd, focus)
	default:
		var cmd tea.Cmd
		m.realmJoinInput, cmd = m.realmJoinInput.Update(msg)
		return m, cmd
	}
}

// handleMeshServiceCallKey types the raw JSON arguments for the `s` panel's
// selected procedure -- the non-AI door into the same mesh the agent's own
// tools call through.
func (m Model) handleMeshServiceCallKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, DefaultKeyMap.Normal):
		m.meshServiceCallInput.Blur()
		m.meshServiceCallInput.Reset() // one-shot action, no draft kept
		m.meshServiceCallProcedure = ""
		m.mode = ModeCompose
		m.resizeComponents()
		// This one was opened from the focus-taking panel, which blurs the
		// composer, so focus has to come back here.
		focus := m.composerFocus()
		return m, focus
	case key.Matches(msg, DefaultKeyMap.Submit):
		argsJSON := strings.TrimSpace(m.meshServiceCallInput.Value())
		procedure := m.meshServiceCallProcedure
		m.meshServiceCallInput.Reset()
		m.meshServiceCallInput.Blur()
		m.meshServiceCallProcedure = ""
		m.mode = ModeCompose
		m.resizeComponents()
		if procedure == "" || m.meshServices == nil {
			// The panel was disabled or the procedure vanished between the
			// selection and Submit; never invoke with an empty procedure.
			focus := m.composerFocus()
			return m, focus
		}
		m.meshServiceCallInFlight = true
		focus := m.composerFocus()
		return m, tea.Batch(startMeshServiceCall(m.meshServices, procedure, argsJSON), focus)
	default:
		var cmd tea.Cmd
		m.meshServiceCallInput, cmd = m.meshServiceCallInput.Update(msg)
		return m, cmd
	}
}

// handleMeshServicesPanelKey drives the `s` panel, which has focus while it is
// open: the arrows move the selection and enter calls the selected procedure.
// esc closes it and puts the composer back where it was, draft intact.
func (m Model) handleMeshServicesPanelKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, DefaultKeyMap.Normal):
		m.meshServicesExpanded = false
		m.meshServicesCursor = 0
		m.mode = ModeCompose
		m.resizeComponents()
		focus := m.composerFocus()
		return m, focus
	case key.Matches(msg, DefaultKeyMap.Submit):
		return m.startMeshServiceCallForSelection()
	case msg.String() == "up" || msg.String() == "k":
		if m.meshServicesCursor > 0 {
			m.meshServicesCursor--
		}
	case msg.String() == "down" || msg.String() == "j":
		entries, _ := m.meshServiceEntries()
		if m.meshServicesCursor < len(entries)-1 {
			m.meshServicesCursor++
		}
	}
	return m, nil
}

// openMeshServicesPanel shows the `s` panel and gives it focus: enter there
// calls the selected procedure, so the composer must not be taking enter. It
// opens whenever there is a row to show -- the curated catalog is static, so
// that includes the mesh_services_enabled: false case, where the panel is the
// one place that says why nothing is live rather than the key doing nothing.
func (m Model) openMeshServicesPanel() (tea.Model, tea.Cmd) {
	entries, _ := m.meshServiceEntries()
	if len(entries) == 0 {
		// Nothing curated to show: the panel would be an empty box over the
		// chat, so it stays closed.
		m.meshServicesExpanded = false
		return m, nil
	}
	m.meshExpanded = false
	m.realmExpanded = false
	m.meshServicesExpanded = true
	m.meshServicesCursor = 0
	m.mode = ModeMeshServicesPanel
	m.input.Blur()
	m.resizeComponents()
	return m, nil
}

// startMeshServiceCallForSelection opens the call input for the row under the
// cursor, if there is one and no call is already in flight.
func (m Model) startMeshServiceCallForSelection() (Model, tea.Cmd) {
	if m.meshServices == nil || m.meshServiceCallInFlight {
		return m, nil
	}
	entries, _ := m.meshServiceEntries()
	if m.meshServicesCursor >= len(entries) {
		return m, nil
	}
	m.meshServiceCallProcedure = entries[m.meshServicesCursor].Procedure()
	m.mode = ModeMeshServiceCall
	m.meshServicesExpanded = false
	m.resizeComponents()
	return m, m.meshServiceCallInput.Focus()
}

func (m Model) handleRefresh(msg refreshMsg) (Model, tea.Cmd) {
	m = m.takeServerVersion(msg.serverVersion)
	if msg.err != nil {
		m.lastErr = msg.err
		// The refresh is the one call every two seconds that proves
		// macula-mcp is still there, so a failure is also what turns the
		// summary line's mcp segment red -- including when the subprocess
		// died, since a dead session is exactly what a refresh reports
		// (macula-lazymesh#61).
		m.mcpConnected = false
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
	m.mcpConnected = true

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
// Never takes the keyboard from someone typing: found live 2026-09-06 -- an
// incoming ring while the operator was mid-composition silently yanked them
// into ModeRingPopup, and the in-progress input just vanished from view. A
// ring that cannot be shown yet stays in m.state.pending and out of
// seenRingIDs -- still counted in the status strip the whole time -- and this
// same function picks it up on a later tick once the person is idle. Trusted
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
		if m.personIdle() && m.pendingRingPopup == nil {
			r := ring
			*m = m.rememberFocus(ModeRingPopup)
			m.pendingRingPopup = &r
			m.ringArm = popupArm{shownAt: m.clock()}
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
// it went to the main pane instead of the status strip. A coding tool's
// call and result are not chatter: in a coding session they are what the
// agent is doing, and the operator reads them in the chat (#78 gap 1).
func isChatter(ev agent.Event) bool {
	if ev.Kind == agent.EventToolCall || ev.Kind == agent.EventToolResult {
		return !isCodingTool(ev.ToolName)
	}
	return ev.Kind == agent.EventListening
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
		next, cmd := m.showApproval(ApprovalRequestEvent{Tool: ev.ToolName, ID: ev.ID, Args: ev.Text})
		// A held request is a tool call waiting on a person who may never see
		// it: say so with the same double bell a ring gets, and reopen the
		// listener -- every other branch does, and dropping it here would end
		// this TUI's agent events at the first approval.
		rearm := waitForAgentEvent(m.agentEvents)
		if next.deferredApproval != nil {
			return next, tea.Batch(cmd, ringBell(bellDouble, m.muted), rearm)
		}
		return next, tea.Batch(cmd, rearm)
	}

	if isChatter(agent.Event(ev)) && !m.showChatter {
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
	if open := m.openToolCall(ev.ToolName); open != nil && (ev.Kind == agent.EventToolResult || ev.Kind == agent.EventError) {
		// A call's result or error joins the call's entry: the agent
		// runs its calls one at a time, so the newest open call of the
		// same tool is the one this answers (#78 gap 1).
		open.outcome = &entry
		m.syncViewport()
		return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
	}
	m.chatEntries = append(m.chatEntries, entry)
	m.syncViewport()
	return m, tea.Batch(ringBell(pattern, m.muted), waitForAgentEvent(m.agentEvents))
}

// lastAssistantText is the finished (non-streaming) assistant entry's raw
// markdown source, newest first — the thing worth sharing verbatim,
// neutralised: pasted into a terminal, an escape sequence would act there
// (#81).
func lastAssistantText(entries []chatEntry) (string, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].kind == chatAssistant && !entries[i].streaming && entries[i].text != "" {
			return neutralise(entries[i].text), true
		}
	}
	return "", false
}

// copyLastAnswer copies the last agent answer to the system clipboard via
// OSC-52 tooling (atotto/clipboard) and reports the outcome in the chat —
// a copy that silently fails is worse than one that names its reason, and
// the clipboard helper is legitimately absent over a bare SSH session.
func (m Model) copyLastAnswer() (Model, tea.Cmd) {
	text, ok := lastAssistantText(m.chatEntries)
	if !ok {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: "nothing to copy yet: no agent answer so far"})
		m.syncViewport()
		return m, nil
	}
	if err := clipboard.WriteAll(text); err != nil {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatError, at: time.Now(), text: fmt.Sprintf("could not copy to clipboard: %v", err)})
		m.syncViewport()
		return m, nil
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: fmt.Sprintf("copied the last agent answer (%d bytes) to the clipboard", len(text))})
	m.syncViewport()
	return m, nil
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
	m.mode = ModeCompose
	m.resizeComponents() // hint row goes from 2 lines (Normal) to 1 (Insert) -- a no-op if already Insert
	return m, m.input.Focus()
}

func (m *Model) resizeComponents() {
	// The chatbox is a bordered textarea whose height grows with its
	// content: reserve its ACTUAL rendered height (measured, not
	// derived -- the border arithmetic differs between focused and
	// blurred styles) plus one blank line of slack, so the chat
	// viewport never sits under the box.
	reserved := len(m.statusLines()) + m.input.RenderedHeight() + 1
	h := m.height - reserved
	if h < 3 {
		h = 3
	}
	m.chatViewport.Width = m.width
	m.chatViewport.Height = h
	if m.width > 6 {
		m.input.SetWidth(m.width - 6)
	}
}

func (m *Model) syncViewport() {
	// Follow mode: new content pins the bottom ONLY when the operator was
	// already reading the bottom. Someone scrolled up in the history must
	// keep their place while new entries (and, mid-turn, every streaming
	// delta) arrive below — SetContent preserves the offset on its own,
	// so only the already-at-bottom case re-anchors.
	atBottom := m.chatViewport.AtBottom()
	lines := renderChatEntries(m.chatEntries, m.detailsExpanded, m.width)
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
	// The summary line's macula-mcp segment (macula-lazymesh#61): green
	// while the last refresh reached it, the line's own error red once one
	// failed -- the same 196 errStyle and chatErrorStyleTUI use, so "red"
	// means one thing on this screen.
	mcpUpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	mcpDownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
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

	// The checkout-notes panel takes the whole body: trust is given to the
	// note's content, so the content is what has to be readable, whole and
	// scrolled, before t can act on it.
	if m.mode == ModeNotesPanel && m.notesPanel != nil {
		popup := m.renderNotesPanel()
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

	var screen string
	if m.statusBarPosition == "top" {
		screen = strings.Join([]string{status, body, input}, "\n")
	} else {
		screen = strings.Join([]string{body, input, status}, "\n")
	}
	return m.overlaySelection(screen)
}

// statusLines is the status block's content, one entry per rendered line.
// resizeComponents' reserved-height math counts this slice directly, so
// any new line kind that can appear/disappear at runtime (the hint row's
// own 1-vs-2-line count, depending on mode) must go through this, not be
// appended ad hoc in renderStatusStrip -- otherwise the viewport height
// and what's actually on screen drift apart.
func (m Model) statusLines() []string {
	lines := m.renderHintLines()
	if line, ok := m.renderHeldApprovalLine(); ok {
		lines = append(lines, line)
	}
	return append(lines, m.renderSummaryLine())
}

// renderHeldApprovalLine says a tool call is waiting on an answer that is not
// on screen yet. Without it a request held behind a focused panel, an armed
// leader or someone typing for two minutes is invisible, and the call is then
// refused by its own timeout with nothing ever having asked (macula-lazymesh#62
// review r=2, probe P3). It is one line, present only while something is held,
// so the reserved-height math in resizeComponents follows it for free.
func (m Model) renderHeldApprovalLine() (string, bool) {
	if m.deferredApproval == nil {
		return "", false
	}
	return statusStripStyle.Render("-- APPROVAL WAITING -- ") +
		dimStyle.Render("stop typing (or close the panel, or cancel "+m.leaderLabel()+") to answer: "+m.deferredApproval.Tool), true
}

// renderHintLines is the shortcuts row. While the leader is pending it shows
// the leader and what can follow it -- the whole list, so the leader is
// discoverable without a manual; the rest of the time it shows the few keys
// that do something outside a pop-up. One line, always: the row's height is
// part of the reserved-height math (statusLines).
func (m Model) renderHintLines() []string {
	switch m.mode {
	case ModeRingPopup:
		return []string{statusStripStyle.Render("-- RING --")}
	case ModeApprovalPopup:
		return []string{statusStripStyle.Render("-- APPROVAL --")}
	case ModeErrorPopup:
		return []string{statusStripStyle.Render("-- ERRORS --")}
	case ModeNotesPanel:
		return []string{statusStripStyle.Render("-- NOTES --") + "  " + dimStyle.Render("tab: next  "+keyHint(DefaultKeyMap.Trust, "trust")+"  N/esc: close")}
	case ModeRealmJoin:
		return []string{statusStripStyle.Render("-- REALM --") + "  " + dimStyle.Render(keyHint(DefaultKeyMap.Submit, "join")+"  "+keyHint(DefaultKeyMap.Normal, "cancel"))}
	case ModeMeshServiceCall:
		return []string{statusStripStyle.Render("-- MESH CALL --") + "  " + dimStyle.Render(keyHint(DefaultKeyMap.Submit, "call "+m.meshServiceCallProcedure)+"  "+keyHint(DefaultKeyMap.Normal, "cancel"))}
	case ModeMeshServicesPanel:
		return []string{statusStripStyle.Render("-- MESH SERVICES --") + "  " + dimStyle.Render("↑↓: select  "+keyHint(DefaultKeyMap.Submit, "call")+"  "+keyHint(DefaultKeyMap.Normal, "close"))}
	}

	if m.leaderPending {
		return []string{m.renderLeaderHint()}
	}

	hint := keyHint(DefaultKeyMap.Submit, "send") + "  shift+enter: newline  " +
		keyHint(DefaultKeyMap.Normal, "close or interrupt") + "  shift+↑↓: scroll  " +
		keyHint(DefaultKeyMap.OpenEditor, "$EDITOR") + "  " + m.leaderLabel() + ": commands"
	if m.realmExpanded && m.realmJoinLatest == nil && strings.TrimSpace(m.input.Value()) == "" {
		hint = keyHint(DefaultKeyMap.Submit, "join a realm") + "  " + hint
	}
	return []string{dimStyle.Render(hint)}
}

// leaderLabel is the leader as the screen names it: the configured key, never
// a constant. The hint row, the list `?` writes into the chat, the held
// approval's own sentence and the error marker all read it from here, so they
// cannot drift apart from the key that actually arms the leader (#63).
func (m Model) leaderLabel() string {
	return m.leader.Help().Key
}

// renderLeaderHint is the leader's own list: what it is waiting for and
// everything it can do, so the next key is never a guess.
func (m Model) renderLeaderHint() string {
	var b strings.Builder
	b.WriteString(statusStripStyle.Render(m.leaderLabel() + " …"))
	for _, c := range leaderHelp() {
		b.WriteString(dimStyle.Render("  " + c.Key + ": " + c.Action))
	}
	return b.String()
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
	var out string

	// Which macula-mcp this is, and whether it is still answering: a person
	// could not tell lazymesh used macula-mcp at all before this
	// (macula-lazymesh#61). Green while the last refresh call reached it,
	// red once one fails or the subprocess exits; `(pinned)` when the
	// release running is the one that was pinned, so a floating build and a
	// pinned one do not look identical. No segment when there is no
	// macula-mcp of this process's own (an attached TUI).
	//
	// First on the line, ahead of the counts, because the line is cut to
	// the terminal's width and this is the part that must survive the cut:
	// at a narrow terminal a long petname and the counts can fill it, and
	// "disconnected" is exactly what someone needs to see then
	// (macula-lazymesh#61 review).
	if m.mcpReportedVersion != "" {
		if m.mcpConnected {
			out += mcpUpStyle.Render("mcp " + m.mcpReportedVersion + " ●")
		} else {
			out += mcpDownStyle.Render("mcp ✗ disconnected")
		}
		if m.mcpPinned() {
			out += summaryStyle.Render(" (pinned)")
		}
		out += summaryStyle.Render(" · ")
	}

	if m.attachMode {
		// Counts would be fabricated here: this TUI cannot see the mesh at
		// all, and zero would read as an idle mesh rather than an unshown
		// one.
		out += summaryStyle.Render("attached · mesh not shown here (the session owns it)")
	} else {
		counts := fmt.Sprintf("%d rooms · %d pending rings · %d agents seen",
			len(m.state.joined), len(m.state.pending), len(m.state.agents))
		out += summaryStyle.Render(counts)
	}

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
	if self, ok := selfAgent(m.state.agents); ok && !m.attachMode {
		identity := identityKey(self.NodeID, self.Petname)
		name := agentBadgeStyle(identity).Render(displayName(self.NodeID, self.Petname))
		out = name + summaryStyle.Render(" · ") + out
	}
	if m.lastErr != nil {
		out += "  " + formatSummaryError(m.leaderLabel(), m.lastErr)
	}
	// One row, whatever is on it. resizeComponents reserves exactly
	// len(statusLines()) rows for the status block, so a summary line that
	// wrapped would push the chat body off the bottom and drift the two
	// apart -- the same class the no-embedded-newline rule guards. Cut to
	// the terminal's own width, ANSI-aware, tail first: the tail is the
	// least load-bearing part (the model label, the listening heartbeat),
	// and a full error is a keypress away in the error popup. Only when a
	// width is known, which is not yet true before the first resize.
	if m.width > 0 && lipgloss.Width(out) > m.width {
		out = lipgloss.NewStyle().MaxWidth(m.width).Inline(true).Render(out)
	}
	return out
}

func (m Model) renderStatusStrip() string {
	return strings.Join(m.statusLines(), "\n")
}

// mcpPinned reports whether the release actually running is the one
// `macula_mcp_version` pinned -- judged by comparing the two, never by the
// pin alone: a pin npm did not honour, or a floating build that happens to
// equal it, would otherwise be labelled `(pinned)` on the strength of config,
// beside a version it does not describe (macula-lazymesh#61 review).
func (m Model) mcpPinned() bool {
	return m.maculaMCPVersion != "" && sameRelease(m.maculaMCPVersion, m.mcpReportedVersion)
}

// sameRelease compares two version strings, ignoring a leading `v` so a pin
// written `v0.51.1` matches a server reporting `0.51.1`.
func sameRelease(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
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
	// The body height, with the SAME reservation resizeComponents uses
	// (status block + the chatbox's measured rendered height + slack) --
	// the old "-2" assumed a one-line input and let a taller chatbox
	// push the overlay past the terminal's bottom row.
	target := m.height - len(m.statusLines()) - m.input.RenderedHeight() - 1
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
	lines := renderChatEntries(m.chatEntries, m.detailsExpanded, m.width)
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

// chatboxBlurredStyle and chatboxFocusedStyle give the compose box its
// opencode-style border: dim when idle, brand blue when focused — the
// same pair panelStyle/titleStyle use for the rest of the chrome.
var (
	chatboxBlurredStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	chatboxFocusedStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#38BDF8")).Padding(0, 1)
)

package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
)

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune{r}})
}

func typeKey(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: t})
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	userInputCh := make(chan string, 8)
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom"})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

// These are exactly the modal-state bugs a reviewer would expect to find
// (cf's own words when delegating this): "q" must never quit while
// composing a message, and Esc/Enter must actually change mode.

func TestNormalMode_QQuits(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(runeKey('q'))
	if cmd == nil {
		t.Fatalf("expected a quit command from 'q' in normal mode")
	}
}

func TestInsertMode_QDoesNotQuit_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i')) // enter insert mode
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("expected insert mode after 'i', got %v", m.mode)
	}

	// tea.Cmd values aren't comparable, so the real assertion that 'q'
	// didn't quit is behavioral: still in insert mode, and the character
	// landed in the input rather than being consumed as a command.
	updated, _ = m.Update(runeKey('q'))
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("'q' while composing must not leave insert mode, got %v", m.mode)
	}
	if m.input.Value() != "q" {
		t.Fatalf("expected 'q' to be typed into the input, got %q", m.input.Value())
	}
}

func TestInsertMode_EscReturnsToNormal(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('h'))
	m = updated.(Model)
	if m.input.Value() != "h" {
		t.Fatalf("expected 'h' typed, got %q", m.input.Value())
	}

	updated, _ = m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Esc, got %v", m.mode)
	}
}

func TestInsertMode_EnterSubmitsAndReturnsToNormal(t *testing.T) {
	userInputCh := make(chan string, 8)
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom"})
	m.width, m.height = 80, 24
	m.resizeComponents()

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "hello agent" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}

	updated, cmd := m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Enter, got %v", m.mode)
	}
	if m.input.Value() != "" {
		t.Fatalf("expected input to be cleared after submit, got %q", m.input.Value())
	}
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatYou || m.chatEntries[0].text != "hello agent" {
		t.Fatalf("expected one chatYou entry with the typed text, got %+v", m.chatEntries)
	}
	if cmd == nil {
		t.Fatalf("expected a command to deliver the message to userInputCh")
	}
	cmd() // execute the returned tea.Cmd synchronously

	select {
	case got := <-userInputCh:
		if got != "hello agent" {
			t.Fatalf("expected %q on userInputCh, got %q", "hello agent", got)
		}
	default:
		t.Fatalf("expected the submitted message to be sent on userInputCh")
	}
}

func TestInsertMode_SubmittingEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if len(m.chatEntries) != 0 {
		t.Fatalf("expected no chat entry for an empty submit, got %+v", m.chatEntries)
	}
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after submitting empty input, got %v", m.mode)
	}
}

func TestNormalMode_MTogglesMeshExpanded(t *testing.T) {
	m := newTestModel(t)
	if m.meshExpanded {
		t.Fatalf("expected mesh view collapsed by default")
	}
	updated, _ := m.Update(runeKey('m'))
	m = updated.(Model)
	if !m.meshExpanded {
		t.Fatalf("expected 'm' to expand the mesh view")
	}
	updated, _ = m.Update(runeKey('m'))
	m = updated.(Model)
	if m.meshExpanded {
		t.Fatalf("expected a second 'm' to collapse it again")
	}
}

func TestInsertMode_MDoesNotToggleMeshView_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(runeKey('m'))
	m = updated.(Model)
	if m.meshExpanded {
		t.Fatalf("'m' while composing must not toggle the mesh view")
	}
	if m.input.Value() != "m" {
		t.Fatalf("expected 'm' to be typed into the input, got %q", m.input.Value())
	}
}

func TestNormalMode_STogglesMeshServicesExpanded(t *testing.T) {
	m := newTestModel(t)
	if m.meshServicesExpanded {
		t.Fatalf("expected mesh services view collapsed by default")
	}
	updated, _ := m.Update(runeKey('s'))
	m = updated.(Model)
	if !m.meshServicesExpanded {
		t.Fatalf("expected 's' to expand the mesh services view")
	}
	updated, _ = m.Update(runeKey('s'))
	m = updated.(Model)
	if m.meshServicesExpanded {
		t.Fatalf("expected a second 's' to collapse it again")
	}
}

func TestInsertMode_SDoesNotToggleMeshServicesView_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(runeKey('s'))
	m = updated.(Model)
	if m.meshServicesExpanded {
		t.Fatalf("'s' while composing must not toggle the mesh services view")
	}
	if m.input.Value() != "s" {
		t.Fatalf("expected 's' to be typed into the input, got %q", m.input.Value())
	}
}

// `m` and `s` are two different overlays over the same body area
// (mesh STATE vs. available SERVICES) -- opening one must close the
// other, never both stacked at once (see renderOverlay's own doc on
// why: a second panel stacked on top would halve the already-tight
// chat margin the pin-to-top layout keeps).
func TestNormalMode_MeshViewAndMeshServicesViewAreMutuallyExclusive(t *testing.T) {
	m := newTestModel(t)

	updated, _ := m.Update(runeKey('m'))
	m = updated.(Model)
	if !m.meshExpanded || m.meshServicesExpanded {
		t.Fatalf("expected only meshExpanded after 'm', got meshExpanded=%v meshServicesExpanded=%v", m.meshExpanded, m.meshServicesExpanded)
	}

	updated, _ = m.Update(runeKey('s'))
	m = updated.(Model)
	if m.meshExpanded || !m.meshServicesExpanded {
		t.Fatalf("expected 's' to close the mesh view and open mesh services, got meshExpanded=%v meshServicesExpanded=%v", m.meshExpanded, m.meshServicesExpanded)
	}

	updated, _ = m.Update(runeKey('m'))
	m = updated.(Model)
	if !m.meshExpanded || m.meshServicesExpanded {
		t.Fatalf("expected 'm' to close mesh services and reopen the mesh view, got meshExpanded=%v meshServicesExpanded=%v", m.meshExpanded, m.meshServicesExpanded)
	}
}

func TestNormalMode_RTogglesRealmExpanded(t *testing.T) {
	m := newTestModel(t)
	if m.realmExpanded {
		t.Fatalf("expected realms view collapsed by default")
	}
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	if !m.realmExpanded {
		t.Fatalf("expected 'r' to expand the realms view")
	}
	updated, _ = m.Update(runeKey('r'))
	m = updated.(Model)
	if m.realmExpanded {
		t.Fatalf("expected a second 'r' to collapse it again")
	}
}

// `r` joins the same mutual-exclusion group as `m`/`s` -- see
// TestNormalMode_MeshViewAndMeshServicesViewAreMutuallyExclusive above
// for the pairwise version; this checks the third overlay closes both
// of the other two, and that opening either of the other two closes it.
func TestNormalMode_RealmsViewIsMutuallyExclusiveWithTheOtherTwoOverlays(t *testing.T) {
	m := newTestModel(t)

	updated, _ := m.Update(runeKey('m'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('r'))
	m = updated.(Model)
	if m.meshExpanded || !m.realmExpanded {
		t.Fatalf("expected 'r' to close the mesh view, got meshExpanded=%v realmExpanded=%v", m.meshExpanded, m.realmExpanded)
	}

	updated, _ = m.Update(runeKey('s'))
	m = updated.(Model)
	if m.realmExpanded || !m.meshServicesExpanded {
		t.Fatalf("expected 's' to close the realms view, got realmExpanded=%v meshServicesExpanded=%v", m.realmExpanded, m.meshServicesExpanded)
	}
}

func TestInsertMode_RDoesNotToggleRealmsView_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(runeKey('r'))
	m = updated.(Model)
	if m.realmExpanded {
		t.Fatalf("'r' while composing must not toggle the realms view")
	}
	if m.input.Value() != "r" {
		t.Fatalf("expected 'r' to be typed into the input, got %q", m.input.Value())
	}
}

// `i` means something different depending on which overlay is showing
// (handleKey's own Insert case) -- with the realms view open and no
// join result currently displayed, it must enter ModeRealmJoin, not the
// normal compose mode.
func TestNormalMode_IEntersRealmJoinModeWhenRealmsViewIsOpen(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)

	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)
	if m.mode != ModeRealmJoin {
		t.Fatalf("expected ModeRealmJoin, got %v", m.mode)
	}
	if !m.realmJoinInput.Focused() {
		t.Fatalf("expected the realm-join input to be focused")
	}
}

func TestModeRealmJoin_EscCancelsWithoutStartingAJoin(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)

	for _, r := range "io.macula" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, cmd := m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected Esc to return to Normal mode, got %v", m.mode)
	}
	if m.realmJoinInput.Value() != "" {
		t.Fatalf("expected the draft realm name to be discarded on cancel, got %q", m.realmJoinInput.Value())
	}
	if m.realmJoinEvents != nil {
		t.Fatalf("expected no join to have been started")
	}
	if cmd != nil {
		t.Fatalf("expected no command from cancelling")
	}
}

func TestModeRealmJoin_SubmittingEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)

	updated, cmd := m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected Enter on an empty realm name to return to Normal mode, got %v", m.mode)
	}
	if m.realmJoinEvents != nil || cmd != nil {
		t.Fatalf("expected no join to have been started for an empty name")
	}
}

// The actual wiring test: Enter with real text calls realmJoinFunc with
// exactly what was typed and the Model's own configured
// version/identity, and starts listening on whatever channel it
// returns -- substituting realmJoinFunc so this never risks a real
// npx/macula-mcp-realm spawn (internal/realmjoin's own test suite
// already covers that machinery).
func TestModeRealmJoin_SubmitCallsRealmJoinFuncWithTheTypedNameAndStartsListening(t *testing.T) {
	original := realmJoinFunc
	defer func() { realmJoinFunc = original }()

	var gotVersion, gotIdentity, gotRealm string
	ch := make(chan realmjoin.Event, 1)
	realmJoinFunc = func(ctx context.Context, version, identityFile, realmName string) (<-chan realmjoin.Event, error) {
		gotVersion, gotIdentity, gotRealm = version, identityFile, realmName
		return ch, nil
	}

	m := newTestModel(t)
	m.maculaMCPVersion = "0.27.0"
	m.realmIdentityFile = "/tmp/identity.seed"
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "io.macula" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, cmd := m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)

	if gotVersion != "0.27.0" || gotIdentity != "/tmp/identity.seed" || gotRealm != "io.macula" {
		t.Fatalf("expected realmJoinFunc called with (0.27.0, /tmp/identity.seed, io.macula), got (%q, %q, %q)", gotVersion, gotIdentity, gotRealm)
	}
	if m.mode != ModeNormal {
		t.Fatalf("expected Normal mode after submitting, got %v", m.mode)
	}
	if cmd == nil {
		t.Fatalf("expected a command to start listening for events")
	}
}

// The exact bug found live 2026-09-09 (Raf: "'join one' does...nothing
// after entering io.macula"): m.realmJoinLatest stayed nil from Submit
// until the first real event arrived on the channel, so for however
// long npx took to resolve, renderRealms fell back to the plain
// list/placeholder -- looked identical to nothing having happened.
// Fixed: a synthetic "starting" event is set in the SAME Update cycle as
// Submit, before waitForRealmJoinEvent's own command has even run once.
func TestModeRealmJoin_SubmitSetsRealmJoinLatestImmediately(t *testing.T) {
	original := realmJoinFunc
	defer func() { realmJoinFunc = original }()
	ch := make(chan realmjoin.Event, 1) // never fed -- this test is only about state BEFORE any event arrives
	realmJoinFunc = func(ctx context.Context, version, identityFile, realmName string) (<-chan realmjoin.Event, error) {
		return ch, nil
	}

	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "io.macula" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, _ = m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)

	if m.realmJoinLatest == nil {
		t.Fatalf("expected realmJoinLatest set immediately on submit, before any event arrives -- got nil")
	}
	if m.realmJoinLatest.Kind != "starting" || m.realmJoinLatest.Realm != "io.macula" {
		t.Fatalf("expected a synthetic starting event for io.macula, got %+v", m.realmJoinLatest)
	}
	if got := m.renderRealms(); strings.Contains(got, "no realms joined yet") {
		t.Fatalf("expected the panel to show the in-progress join, not fall back to the empty-list placeholder, got:\n%s", got)
	}
}

// The `i`-guard (handleKey: `i` only starts a new join when
// m.realmJoinLatest == nil) is only real protection if realmJoinLatest
// is set the INSTANT a join starts, not once the first event arrives --
// otherwise a stray second `i` during that gap fires a second concurrent
// join on top of the first. Directly exercises the race the fix above
// also closes.
func TestNormalMode_IWhileAJoinIsStartingDoesNotFireASecondJoin(t *testing.T) {
	original := realmJoinFunc
	defer func() { realmJoinFunc = original }()
	calls := 0
	ch := make(chan realmjoin.Event, 1)
	realmJoinFunc = func(ctx context.Context, version, identityFile, realmName string) (<-chan realmjoin.Event, error) {
		calls++
		return ch, nil
	}

	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "io.macula" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, _ = m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if calls != 1 {
		t.Fatalf("expected exactly one call after the first submit, got %d", calls)
	}

	// Before any event has arrived on ch, a stray `i` must be a no-op --
	// realmJoinLatest is already non-nil (the synthetic starting event).
	updated, _ = m.Update(runeKey('i'))
	m = updated.(Model)
	if m.mode == ModeRealmJoin {
		t.Fatalf("expected `i` to be a no-op while a join is already in flight, entered ModeRealmJoin instead")
	}
	if calls != 1 {
		t.Fatalf("expected still exactly one realmJoinFunc call, a second `i` started another: %d", calls)
	}
}

func TestHandleRealmJoinEvent_NonTerminalEventReArmsListening(t *testing.T) {
	m := newTestModel(t)
	ch := make(chan realmjoin.Event, 1)
	m.realmJoinEvents = ch

	updated, cmd := m.Update(realmJoinEventMsg{Kind: "session", Realm: "io.macula", JoinURL: "https://realm.macula.io/join/s1"})
	m = updated.(Model)
	if m.realmJoinLatest == nil || m.realmJoinLatest.Kind != "session" {
		t.Fatalf("expected realmJoinLatest set to the session event, got %+v", m.realmJoinLatest)
	}
	if m.realmJoinEvents == nil {
		t.Fatalf("expected the events channel to stay set (still listening) after a non-terminal event")
	}
	if cmd == nil {
		t.Fatalf("expected a command re-arming the listener")
	}
}

func TestHandleRealmJoinEvent_TerminalEventStopsListening(t *testing.T) {
	m := newTestModel(t)
	ch := make(chan realmjoin.Event, 1)
	m.realmJoinEvents = ch

	updated, cmd := m.Update(realmJoinEventMsg{Kind: "confirmed", Realm: "io.macula", Handle: "rgfaber"})
	m = updated.(Model)
	if m.realmJoinLatest == nil || m.realmJoinLatest.Kind != "confirmed" {
		t.Fatalf("expected realmJoinLatest set to the confirmed event, got %+v", m.realmJoinLatest)
	}
	if m.realmJoinEvents != nil {
		t.Fatalf("expected the events channel cleared (stopped listening) after a terminal event")
	}
	if cmd != nil {
		t.Fatalf("expected no further command after a terminal event")
	}
}

func TestNormalMode_EscDismissesAFinishedRealmJoinBackToTheList(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('r'))
	m = updated.(Model)
	ev := realmjoin.Event{Kind: "confirmed", Realm: "io.macula", Handle: "rgfaber"}
	m.realmJoinLatest = &ev

	updated, _ = m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.realmJoinLatest != nil {
		t.Fatalf("expected Esc to dismiss the finished join's status, got %+v", m.realmJoinLatest)
	}
	if !m.realmExpanded {
		t.Fatalf("expected Esc to only dismiss the join status, not close the realms panel itself")
	}
}

func TestForceQuit_WorksInEitherMode(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit in normal mode")
	}

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit even while composing")
	}
}

// Regression guard added alongside the ring pop-up: adding a third mode
// must not accidentally exempt it from the universal quit key.
func TestForceQuit_WorksDuringRingPopup(t *testing.T) {
	m := newTestModel(t)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit even while the ring pop-up is showing")
	}
}

func TestNormalMode_ToggleMute(t *testing.T) {
	m := newTestModel(t)
	if m.muted {
		t.Fatalf("expected bell enabled by default")
	}
	updated, _ := m.Update(runeKey('b'))
	m = updated.(Model)
	if !m.muted {
		t.Fatalf("expected 'b' to mute")
	}
}

func TestRenderStatusStrip_OmitsAgentModelWhenNoAgentRunning(t *testing.T) {
	m := newTestModel(t)
	got := m.renderStatusStrip()
	if strings.Contains(got, "deepseek") {
		t.Fatalf("expected no model label with no --room agent running, got %q", got)
	}
}

func TestRenderStatusStrip_ShowsAgentModelWhenSet(t *testing.T) {
	userInputCh := make(chan string, 8)
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom", AgentModel: "deepseek/deepseek-v4-flash"})
	m.width, m.height = 80, 24
	m.resizeComponents()
	got := m.renderStatusStrip()
	if !strings.Contains(got, "deepseek/deepseek-v4-flash") {
		t.Fatalf("expected the configured provider/model in the status strip, got %q", got)
	}
}

func TestHandleAgentEvent_AppendsChatEntryAndReArms(t *testing.T) {
	events := make(chan agent.Event, 1)
	m := newTestModel(t)
	m.agentEvents = events

	events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "hi from agent"}
	ev := <-events // simulate what waitForAgentEvent would have received
	updated, cmd := m.Update(agentEventMsg(ev))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatAssistant {
		t.Fatalf("expected one chatAssistant entry, got %+v", m.chatEntries)
	}
	if cmd == nil {
		t.Fatalf("expected handleAgentEvent to return a command (bell + re-arm listen)")
	}
}

// Issue #2: mode indicator, vim's own "-- MODE --" convention.
func TestRenderStatusStrip_ShowsModeIndicator(t *testing.T) {
	m := newTestModel(t)
	if !strings.Contains(m.renderStatusStrip(), "-- NORMAL --") {
		t.Fatalf("expected a NORMAL mode indicator, got %q", m.renderStatusStrip())
	}
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	if !strings.Contains(m.renderStatusStrip(), "-- INSERT --") {
		t.Fatalf("expected an INSERT mode indicator after 'i', got %q", m.renderStatusStrip())
	}
}

// Briefly split across 2 lines in Normal mode (2026-09-07, a narrow
// terminal clipped the combined line); reverted to 1 line 2026-09-08
// (Raf's own call: the vertical space matters more day to day than the
// clipping risk). Every mode gets exactly 1 hint line; the ring pop-up
// shows its own hints (renderRingPopup) so needs none here beyond the
// mode indicator itself.
func TestRenderHintLines_AlwaysOneLine(t *testing.T) {
	m := newTestModel(t)
	if got := len(m.renderHintLines()); got != 1 {
		t.Fatalf("expected 1 hint line in Normal mode, got %d: %v", got, m.renderHintLines())
	}

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	if got := len(m.renderHintLines()); got != 1 {
		t.Fatalf("expected 1 hint line in Insert mode, got %d: %v", got, m.renderHintLines())
	}

	m2 := newTestModel(t)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m2.pendingRingPopup = &ring
	m2.mode = ModeRingPopup
	if got := len(m2.renderHintLines()); got != 1 {
		t.Fatalf("expected 1 hint line during a ring pop-up, got %d: %v", got, m2.renderHintLines())
	}
}

// Every shortcut must still appear on the combined line.
func TestRenderHintLines_NormalModeStillListsEveryShortcut(t *testing.T) {
	m := newTestModel(t)
	combined := strings.Join(m.renderHintLines(), " ")
	for _, want := range []string{"m:", "s:", "r:", "i:", "ctrl+e:", "v:", "e:", "b:", "q:"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("expected shortcut %q somewhere in the hint lines, got %q", want, combined)
		}
	}
}

// Issue found live 2026-09-07: bolding the whole summary line competed
// with the instance's own petname for visual attention instead of
// setting it apart. The petname segment stays bold, in its own
// deterministic color (agentBadgeStyle, same scheme as the presence
// badge/room-message previews); the rest of the line (rooms/rings/
// agents/model) is normal weight, same blue as before.
func TestRenderSummaryLine_PetnameStandsOutFromNormalWeightRest(t *testing.T) {
	if summaryStyle.GetBold() {
		t.Fatalf("expected the rooms/rings/agents-seen segment to be normal weight, got bold")
	}
	identity := identityKey("deadbeefcafe", "swift-otter")
	badge := agentBadgeStyle(identity)
	if !badge.GetBold() {
		t.Fatalf("expected the petname segment to stay bold")
	}
	if badge.GetForeground() == summaryStyle.GetForeground() {
		t.Fatalf("expected the petname's color to be distinct from the rest of the summary line, both were %v", badge.GetForeground())
	}
}

// Raf's ask, 2026-09-08: give the provider/model segment its own
// treatment too, similar to petname's. His choice between a new color or
// bold: bold (statusStripStyle, the weight this whole line shared before
// 2026-09-07) -- same blue as the rest of the line, not a new color that
// would risk reading as another per-agent identity.
func TestRenderSummaryLine_ModelIsBoldUnlikeTheRestOfTheLine(t *testing.T) {
	if !statusStripStyle.GetBold() {
		t.Fatalf("expected statusStripStyle (used for the model segment) to be bold")
	}
	if statusStripStyle.GetForeground() != summaryStyle.GetForeground() {
		t.Fatalf("expected the model segment to share the rest of the line's color, just bolder -- got %v vs %v", statusStripStyle.GetForeground(), summaryStyle.GetForeground())
	}

	m := newTestModel(t)
	m.agentModel = "deepseek/deepseek-v4-flash"
	got := m.renderSummaryLine()
	want := statusStripStyle.Render("deepseek/deepseek-v4-flash")
	if !strings.Contains(got, want) {
		t.Fatalf("expected the model rendered bold in the summary line, got %q", got)
	}
}

// Issue #4: the compose line stays visible (and keeps its draft) after
// Esc back to Normal mode, instead of being replaced by a static hint.
func TestRenderInputLine_AlwaysVisibleWithRetainedDraft(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "draft" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, _ = m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Esc, got %v", m.mode)
	}
	if !strings.Contains(m.renderInputLine(), "draft") {
		t.Fatalf("expected the retained draft still visible in normal mode, got %q", m.renderInputLine())
	}
}

// Issue #2 (revised live 2026-09-07: the status-strip summary was
// dropped, little practical value per Raf -- see renderSummaryLine's own
// doc comment): routine tool-call activity is dropped from the
// conversation pane by default, and not shown anywhere else either.
func TestHandleAgentEvent_ToolCallDroppedByDefault(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_call", Text: "{}"}))
	m = updated.(Model)
	if len(m.chatEntries) != 0 {
		t.Fatalf("expected no chat entry for a tool call by default, got %+v", m.chatEntries)
	}
	if strings.Contains(m.renderStatusStrip(), "mesh_call") {
		t.Fatalf("expected the tool call to not appear anywhere in the status strip, got %q", m.renderStatusStrip())
	}
}

// The liveness heartbeat is a different concern from routine tool-call
// chatter (see lastListeningAt's own doc comment) and is never dropped:
// it's always tracked and always shown in the summary line, regardless
// of showChatter.
func TestHandleAgentEvent_ListeningAlwaysUpdatesHeartbeat(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventListening}))
	m = updated.(Model)
	if m.lastListeningAt.IsZero() {
		t.Fatalf("expected lastListeningAt to be set after an EventListening")
	}
	if !strings.Contains(m.renderStatusStrip(), "listening ") {
		t.Fatalf("expected the summary line to show the listening heartbeat, got %q", m.renderStatusStrip())
	}
	if len(m.chatEntries) != 0 {
		t.Fatalf("expected EventListening to stay out of the chat pane by default, got %+v", m.chatEntries)
	}
}

// Errors and system notices are never "chatter" -- they stay in the chat
// pane regardless of showChatter.
func TestHandleAgentEvent_ErrorsNeverRelocated(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventError, ToolName: "mesh_call", Err: fmt.Errorf("boom")}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatError {
		t.Fatalf("expected the error inline in chat, got %+v", m.chatEntries)
	}
}

func TestHandleAgentEvent_ToolCallInlineWhenVerbose(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	m.showChatter = true

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_call", Text: "{}"}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatToolCall {
		t.Fatalf("expected the tool call inline in chat when verbose, got %+v", m.chatEntries)
	}
}

func TestNormalMode_VTogglesChatter(t *testing.T) {
	m := newTestModel(t)
	if m.showChatter {
		t.Fatalf("expected chatter relocated by default")
	}
	updated, _ := m.Update(runeKey('v'))
	m = updated.(Model)
	if !m.showChatter {
		t.Fatalf("expected 'v' to enable verbose chatter")
	}
}

func TestInsertMode_VDoesNotToggleChatter_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('v'))
	m = updated.(Model)
	if m.showChatter {
		t.Fatalf("'v' while composing must not toggle chatter mode")
	}
	if m.input.Value() != "v" {
		t.Fatalf("expected 'v' to be typed into the input, got %q", m.input.Value())
	}
}

// Issue #2: editor-based composition, from either mode.
func TestOpenEditor_WorksFromNormalAndInsertMode(t *testing.T) {
	for _, start := range []Mode{ModeNormal, ModeInsert} {
		m := newTestModel(t)
		m.mode = start
		_, cmd := m.Update(typeKey(tea.KeyCtrlE))
		if cmd == nil {
			t.Fatalf("expected ctrl+e to return a command from mode %v", start)
		}
	}
}

func TestHandleEditorFinished_LoadsContentAndEntersInsert(t *testing.T) {
	m := newTestModel(t)
	f, err := os.CreateTemp("", "lazymesh-test-*.md")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("composed in $EDITOR"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	updated, cmd := m.Update(editorFinishedMsg{path: f.Name()})
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("expected insert mode after the editor round-trip, got %v", m.mode)
	}
	if m.input.Value() != "composed in $EDITOR" {
		t.Fatalf("expected the edited content loaded into input, got %q", m.input.Value())
	}
	if cmd == nil {
		t.Fatalf("expected a focus command")
	}
	if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
		t.Fatalf("expected the temp file to be cleaned up, stat err: %v", err)
	}
}

func TestHandleEditorFinished_ErrorSurfacesAsChatEntry(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(editorFinishedMsg{err: fmt.Errorf("boom")})
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatError {
		t.Fatalf("expected a chatError entry on editor failure, got %+v", m.chatEntries)
	}
}

// statusLines()'s own line count must always match what renderStatusStrip
// actually prints, in every mode -- resizeComponents' reserved-height
// math (len(m.statusLines()) + 2) depends on this exactly, and the hint
// row's line count now varies by mode (2 in Normal, 1 in Insert/Ring,
// found live 2026-09-07 when the shortcuts row was split across 2
// lines). Also guards the original 2026-09-06 finding at the unit level
// it actually belongs at now that the chatter status line is gone: no
// status line may contain a raw embedded newline (see
// collapseNewlines/truncateForChat in chat_test.go for that fix itself;
// this just confirms nothing here can reintroduce the symptom).
func TestStatusLines_LineCountMatchesActualRenderedRows(t *testing.T) {
	for _, mode := range []Mode{ModeNormal, ModeInsert, ModeRingPopup} {
		m := newTestModel(t)
		m.mode = mode
		if mode == ModeRingPopup {
			ring := pendingRing{RingID: "r1", Peer: "peer1"}
			m.pendingRingPopup = &ring
		}
		m.resizeComponents()

		for i, line := range m.statusLines() {
			if strings.Contains(line, "\n") {
				t.Fatalf("mode %v: statusLines()[%d] contains an embedded newline, breaking the one-element-one-row assumption: %q", mode, i, line)
			}
		}
		if got, want := len(strings.Split(m.renderStatusStrip(), "\n")), len(m.statusLines()); got != want {
			t.Fatalf("mode %v: renderStatusStrip rendered as %d rows, but resizeComponents would count %d via statusLines()", mode, got, want)
		}
	}
}

// Issue #12: the mesh-expanded view's body (renderExpandedMesh) had no
// height accounting at all, so the status block landed wherever the panel
// stack's natural height happened to end rather than anchored to the
// screen edge -- for a taller terminal than the panel content, that meant
// empty space below the status bar instead of the status bar actually
// reaching the last row. Measures the ACTUAL total line count and where
// the status block lands, not just that its content is present somewhere.
func TestView_MeshExpanded_StatusAnchorsToBottomEdge(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	m.resizeComponents()
	m.meshExpanded = true
	// A short panel stack (no rooms/rings/agents) is exactly the case
	// where natural-height stacking left empty space below the status
	// bar instead of the bar reaching the real bottom row.

	view := m.View()
	lines := strings.Split(view, "\n")

	wantTotal := m.height - 1 // same "-2 reserved, -1 slack" convention the chat view already uses
	if len(lines) != wantTotal {
		t.Fatalf("expected %d total rendered lines for a %d-tall terminal, got %d:\n%s", wantTotal, m.height, len(lines), view)
	}

	statusLineCount := len(m.statusLines())
	gotTail := strings.Join(lines[len(lines)-statusLineCount:], "\n")
	wantTail := m.renderStatusStrip()
	if gotTail != wantTail {
		t.Fatalf("expected the status block anchored to the last %d lines (bottom position), got:\n%q\nwant:\n%q", statusLineCount, gotTail, wantTail)
	}
}

func TestView_MeshExpanded_StatusAnchorsToTopEdge(t *testing.T) {
	userInputCh := make(chan string, 8)
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "top"})
	m.width, m.height = 100, 30
	m.resizeComponents()
	m.meshExpanded = true

	view := m.View()
	lines := strings.Split(view, "\n")

	statusLineCount := len(m.statusLines())
	gotHead := strings.Join(lines[:statusLineCount], "\n")
	wantHead := m.renderStatusStrip()
	if gotHead != wantHead {
		t.Fatalf("expected the status block anchored to the first %d lines (top position), got:\n%q\nwant:\n%q", statusLineCount, gotHead, wantHead)
	}
}

// Found live 2026-09-08 ("now it repeats"): a conversation short enough
// that top+bottom margins overlap the same lines showed the SAME chat
// line in both margins. Fixed by splitting into two disjoint halves
// (older to the top margin, newer to the bottom) instead of taking
// independent, possibly-overlapping windows into the same slice.
func TestRenderMeshOverlay_ShortConversationNeverRepeatsALineInBothMargins(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 24
	m.resizeComponents()
	m.meshExpanded = true
	m.chatEntries = []chatEntry{youChatEntry("only message")}
	m.syncViewport()

	view := m.View()
	if got := strings.Count(view, "only message"); got != 1 {
		t.Fatalf("expected \"only message\" to appear exactly once, got %d times:\n%s", got, view)
	}
}

// Raf's ask, 2026-09-08: mesh view used to fully replace the chat pane;
// now it overlays the panels on top with real conversation still visible
// in a margin above and below ("transparency"). Found live while
// verifying this: chatViewport.View() pads a short conversation with
// blank filler at the bottom (anchored-top rendering, always exactly
// chatViewport.Height lines) -- slicing THAT for the "newest lines"
// bottom margin grabbed blank filler instead of real content. Fixed via
// chatContentLines (the unpadded entries, mirroring syncViewport's own
// construction) instead of the padded viewport render.
// Pinned to the top of the body area (2026-09-08, Raf: once the panels'
// own styling was lightened, the previous centered layout no longer
// needed the visual balance a top+bottom margin split was providing) --
// the panels render first, with a single margin of the newest chat lines
// below them. Older lines (including anything from the covered middle
// stretch of a long conversation) simply aren't shown; there's no top
// margin at all anymore.
func TestRenderMeshOverlay_ShowsNewestChatBelowThePanels(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 24
	m.resizeComponents()
	m.meshExpanded = true
	for i := 0; i < 20; i++ {
		m.chatEntries = append(m.chatEntries, youChatEntry(fmt.Sprintf("message number %d", i)))
	}
	m.syncViewport()

	view := m.View()
	if !strings.Contains(view, "message number 19") {
		t.Fatalf("expected the newest chat line visible below the panels, got:\n%s", view)
	}
	if strings.Contains(view, "message number 0") {
		t.Fatalf("expected the oldest chat line to be covered (no top margin anymore), got:\n%s", view)
	}
	if strings.Contains(view, "message number 10") {
		t.Fatalf("expected a middle message to be covered by the panels, not visible, got:\n%s", view)
	}
	// The panels must actually be the first content lines of the body,
	// not preceded by any chat.
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[0], "╭") {
		t.Fatalf("expected the panel stack's own top border as the very first body line, got %q", lines[0])
	}
}

// A terminal too short for the panels to fit AND leave any visible
// margin falls back to the pre-overlay full-bleed behavor (the panels
// alone, anchored to the screen edge) rather than truncating them
// further -- they have no scroll of their own.
func TestRenderMeshOverlay_FallsBackToFullBleedWhenNoRoomForMargin(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 15
	m.resizeComponents()
	m.meshExpanded = true
	m.chatEntries = []chatEntry{youChatEntry("this must not appear")}
	m.syncViewport()

	view := m.View()
	if strings.Contains(view, "this must not appear") {
		t.Fatalf("expected no room for a chat margin at this height, got:\n%s", view)
	}
	// Not asserting an exact total line count here: the panels (12 lines,
	// empty state) exceed this height's target (10) before any margin is
	// even considered, so padToBodyHeight's own documented "never
	// truncate" behavior legitimately lets the total exceed m.height --
	// pre-existing since #12, not something this change affects.
}

// Raf's ask, tied directly to the dual-instance identity bug fixed in
// 8622117: once two lazymesh instances actually have distinct mesh
// identities, that distinctness should be confirmable at a glance in the
// one place that's always on screen, not just correct under the hood.
func TestRenderStatusStrip_ShowsOwnPetnameWhenKnown(t *testing.T) {
	m := newTestModel(t)
	m.state.agents = []agentPresence{{NodeID: "deadbeefcafe", IsSelf: true, Petname: "swift-otter"}}
	got := m.renderStatusStrip()
	if !strings.Contains(got, "swift-otter") {
		t.Fatalf("expected own petname in the status strip, got %q", got)
	}
}

func TestRenderStatusStrip_OmitsPetnameBeforeFirstRefresh(t *testing.T) {
	m := newTestModel(t)
	got := m.renderStatusStrip()
	if strings.Contains(got, "· ·") {
		t.Fatalf("expected no dangling separator when petname is unknown, got %q", got)
	}
}

// The actual scenario this was built for: two instances, two distinct
// petnames, both visible in what would be two separate terminals'
// status strips.
func TestRenderStatusStrip_DistinctInstancesShowDistinctPetnames(t *testing.T) {
	a := newTestModel(t)
	a.state.agents = []agentPresence{{NodeID: "aaaa", IsSelf: true, Petname: "swift-otter"}}
	b := newTestModel(t)
	b.state.agents = []agentPresence{{NodeID: "bbbb", IsSelf: true, Petname: "quiet-falcon"}}

	gotA, gotB := a.renderStatusStrip(), b.renderStatusStrip()
	if !strings.Contains(gotA, "swift-otter") || strings.Contains(gotA, "quiet-falcon") {
		t.Fatalf("instance A: expected only its own petname, got %q", gotA)
	}
	if !strings.Contains(gotB, "quiet-falcon") || strings.Contains(gotB, "swift-otter") {
		t.Fatalf("instance B: expected only its own petname, got %q", gotB)
	}
}

func TestHandleAgentEvent_BackoffQueuesTripleBell(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event) // never fires again, fine for this assertion

	// We can't directly observe ringBell's side effect without executing
	// it against a real stdout, but we CAN confirm the entry lands as a
	// system message -- the observable, testable half of this behavior.
	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventBackoff}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatSystem {
		t.Fatalf("expected a chatSystem entry for EventBackoff, got %+v", m.chatEntries)
	}
}

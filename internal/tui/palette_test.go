package tui

// #78 gap 6: commands were found by memory, never by name. The palette is
// the leader's own list, searchable, and it runs what it finds through the
// same code a key press does.

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// openPaletteByLeader opens the palette the way a person does: the
// configured leader, then the palette's key.
func openPaletteByLeader(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = pressLeader(t, m, paletteKeyPress(DefaultKeyMap.Palette))
	if m.mode != ModePalette {
		t.Fatalf("%s %s did not open the palette: mode %v", m.leaderLabel(), DefaultKeyMap.Palette.Help().Key, m.mode)
	}
	return m
}

// typeInto sends s one rune at a time through Update.
func typeInto(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			m = send(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		m = send(t, m, runeKey(r))
	}
	return m
}

// TestPalette_FindsCopyByName is the issue's check, on the leader path
// (ctrl+p is the message box's own key, so the palette is <leader> p), under
// a leader that is not the default so a key written out as a literal fails.
func TestPalette_FindsCopyByName(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	m = openPaletteByLeader(t, m)
	m = typeInto(t, m, "cop")

	view := m.View()
	if !strings.Contains(view, "copy last answer") {
		t.Fatalf("typing cop does not list \"copy last answer\":\n%s", view)
	}
	if want := m.leaderLabel() + " " + DefaultKeyMap.CopyChat.Help().Key; !strings.Contains(view, want) {
		t.Fatalf("the palette does not show the command's own key %q:\n%s", want, view)
	}
	if got := paletteMatches(m.palette.query); len(got) == 0 || got[0].Help().Desc != "copy last answer" {
		t.Fatalf("\"copy last answer\" is not the first match for cop: %v", got)
	}

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != ModeCompose {
		t.Fatalf("enter left the palette open: mode %v", m.mode)
	}
	if !lastChatSays(m, "nothing to copy") {
		t.Fatal("enter on \"copy last answer\" did not run the copy command")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("the search leaked into the draft: %q", got)
	}
}

// TestPalette_RunsEveryCommandLikeItsKey runs each leader command from the
// palette, found by its full name, and asserts what the key itself is
// asserted to do in TestLeaderReachesEveryCommand. A command that does
// something else from the palette than from its key fails here.
func TestPalette_RunsEveryCommandLikeItsKey(t *testing.T) {
	byKey := map[string]key.Binding{}
	for _, b := range leaderCommands() {
		byKey[b.Help().Key] = b
	}
	for _, c := range leaderCases(t) {
		if c.key == DefaultKeyMap.Palette.Help().Key {
			continue
		}
		t.Run(c.key, func(t *testing.T) {
			b, ok := byKey[c.key]
			if !ok {
				t.Fatalf("%s has a leader case but no binding in leaderCommands", c.key)
			}
			m := openPaletteByLeader(t, c.prepare(t))
			m = typeInto(t, m, b.Help().Desc)
			if got := paletteMatches(m.palette.query); len(got) == 0 || got[0].Help().Key != c.key {
				t.Fatalf("its full name %q does not put it first", b.Help().Desc)
			}
			updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if !c.ran(m, cmd) {
				t.Fatalf("%q from the palette did not do what %s does", b.Help().Desc, c.key)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("the search leaked into the draft: %q", got)
			}
		})
	}
}

// TestPalette_ListsEveryCommandTheLeaderRuns: a command added to runLeader
// but not to leaderCommands would run from its key and be missing from the
// palette, the hint row and the `?` list alike. Read off runLeader's own
// source, so the check cannot agree with a stale copy.
func TestPalette_ListsEveryCommandTheLeaderRuns(t *testing.T) {
	src, err := os.ReadFile("model.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (m Model) runLeader(")
	if start < 0 {
		t.Fatal("runLeader not found in model.go")
	}
	end := strings.Index(body[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatal("the end of runLeader not found")
	}
	body = body[start : start+1+end]

	listed := map[string]bool{}
	for _, b := range leaderCommands() {
		listed[b.Help().Key] = true
	}
	km := reflect.ValueOf(DefaultKeyMap)
	for _, f := range regexp.MustCompile(`DefaultKeyMap\.(\w+)`).FindAllStringSubmatch(body, -1) {
		if f[1] == "Normal" {
			continue // esc cancels the leader; it is not a command
		}
		b, ok := km.FieldByName(f[1]).Interface().(key.Binding)
		if !ok {
			t.Fatalf("DefaultKeyMap.%s is not a binding", f[1])
		}
		if !listed[b.Help().Key] {
			t.Errorf("runLeader runs DefaultKeyMap.%s (%s) but leaderCommands does not list it, so the palette cannot find it", f[1], b.Help().Desc)
		}
	}
	for _, b := range paletteMatches("") {
		if b.Help().Key == DefaultKeyMap.Palette.Help().Key {
			t.Error("the palette offers itself")
		}
	}
	if got, want := len(paletteMatches("")), len(leaderCommands())-1; got != want {
		t.Errorf("an empty search lists %d commands, want all %d but the palette", got, want)
	}
}

// TestPalette_NotReachableOverAnApproval is the #59 lesson: while a prompt
// holds the keyboard, the leader and the palette's key are keys typed into
// the prompt, never a way out of it.
func TestPalette_NotReachableOverAnApproval(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	m.width, m.height = 80, 24
	m = send(t, m, agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"rm -rf build"}`,
	}))
	clock.advance(popupIdleDelay)
	m, _ = m.maybeShowDeferredApproval()
	if m.mode != ModeApprovalPopup {
		t.Fatalf("test setup: the approval did not take the screen: mode %v", m.mode)
	}

	clock.advance(100 * time.Millisecond)
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	clock.advance(100 * time.Millisecond)
	m = send(t, m, paletteKeyPress(DefaultKeyMap.Palette))
	if m.mode == ModePalette {
		t.Fatal("the palette opened over a pending approval")
	}
	if m.mode != ModeApprovalPopup || m.pendingApproval == nil {
		t.Fatalf("the approval lost the keyboard: mode %v pending %v", m.mode, m.pendingApproval)
	}
	select {
	case a := <-approvalCh:
		t.Fatalf("the leader and the palette key answered the approval: %+v", a)
	default:
	}
}

// TestPalette_NotReachableOverAnError: the same for the error pop-up.
func TestPalette_NotReachableOverAnError(t *testing.T) {
	m := failingRefresh(t, newTestModel(t), realRefreshError)
	if m.mode != ModeErrorPopup {
		t.Fatalf("test setup: the error pop-up is not showing: mode %v", m.mode)
	}
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m = send(t, m, paletteKeyPress(DefaultKeyMap.Palette))
	if m.mode == ModePalette {
		t.Fatal("the palette opened over the error pop-up")
	}
}

// TestPalette_EscClosesWithoutRunning: esc leaves the palette and runs
// nothing, whatever was selected.
func TestPalette_EscClosesWithoutRunning(t *testing.T) {
	m := openPaletteByLeader(t, modelWithLeader(t, ""))
	m = typeInto(t, m, "mute")
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != ModeCompose {
		t.Fatalf("esc left the palette open: mode %v", m.mode)
	}
	if m.muted {
		t.Fatal("esc ran the selected command")
	}
}

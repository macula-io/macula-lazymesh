package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The genuine string a failing refresh puts in front of an operator,
// captured off a real macula-mcp over stdio rather than invented: the
// tool name appears three times and the part worth reading -- the code
// and the reason -- is at the very end, which is the end a terminal cuts
// off first. 143 characters before the status line's own prefix.
const realRefreshError = "mesh_list_realms: macula-mcp tool mesh_list_realms reported an error: " +
	"MCP error -32602: Tool mesh_list_realms not found"

func keyPress(s string) tea.KeyMsg {
	if s == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// failingRefresh puts an error through the real path -- the refresh loop
// reporting a failure -- rather than assigning lastErr by hand. A new
// error opens the pop-up on its own now, so this returns a model with it
// already showing.
func failingRefresh(t *testing.T, m Model, text string) Model {
	t.Helper()
	m, _ = m.handleRefresh(refreshMsg{err: errors.New(text)})
	return m
}

func TestShowError_OpensPopupWithTheWholeMessage(t *testing.T) {
	m := failingRefresh(t, newTestModel(t), realRefreshError)

	if m.mode != ModeErrorPopup {
		t.Fatalf("expected ModeErrorPopup, got %v", m.mode)
	}
	if m.errorPopup == nil {
		t.Fatal("expected an error pop-up to be open")
	}
	if m.errorPopup.text != realRefreshError {
		t.Fatalf("pop-up must hold the untouched error.\n got: %q\nwant: %q", m.errorPopup.text, realRefreshError)
	}
}

// The whole point of the dialog: the tail survives. A rendered pop-up that
// dropped "-32602: Tool mesh_list_realms not found" would reproduce the bug
// it exists to fix, just inside a border.
func TestRenderErrorPopup_ShowsTheReasonAtTheEnd(t *testing.T) {
	m := failingRefresh(t, newTestModel(t), realRefreshError)

	out := m.renderErrorPopup()
	// The viewport wraps, so the text is broken across lines; compare on
	// a whitespace-collapsed copy rather than looking for the raw string.
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"-32602", "not found"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("rendered pop-up lost %q -- the reason is the part worth reading\n%s", want, out)
		}
	}
}

// The pop-up exists because a line ran off the right edge, so the one
// thing it must never do is run off the right edge itself. Checks the
// real View at a narrow width, and logs it so the rendering can be read
// with `go test -run FitsWithin -v`.
func TestRenderErrorPopup_FitsWithinTheTerminalWidth(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := newTestModel(t)
		m.width, m.height = width, 24
		m.resizeComponents()
		m = failingRefresh(t, m, realRefreshError)
		if m.errorPopup == nil {
			t.Fatal("expected the pop-up open -- an empty render would pass this test without testing anything")
		}

		// Scoped to the pop-up, not the whole View: the shortcuts row is
		// deliberately one long line that clips on a narrow terminal
		// (Raf's own call, 2026-09-08), so asserting on View would fail
		// on a decision that has nothing to do with this box.
		popup := m.renderErrorPopup()
		for i, line := range strings.Split(popup, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: pop-up line %d is %d columns wide, it will wrap or clip:\n%s", width, i, w, line)
			}
		}
		t.Logf("rendered at %d columns:\n%s", width, popup)
	}
}

// A long error is the case the scroll keys exist for, and an operator who
// cannot see that there is more below has effectively lost the tail again.
func TestErrorPopup_LongErrorScrollsAndSaysSo(t *testing.T) {
	long := realRefreshError + " " + strings.Repeat("decode mesh_list_realms: invalid character 'x' looking for beginning of value at offset 8412. ", 12)

	m := failingRefresh(t, newTestModel(t), long)

	if !strings.Contains(m.renderErrorPopup(), "Scroll") {
		t.Fatal("a pop-up with more content than fits must offer the scroll keys")
	}

	before := m.errorPopup.viewport.YOffset
	updated, _ := m.handleKey(keyPress("j"))
	m = updated.(Model)
	if m.errorPopup.viewport.YOffset <= before {
		t.Fatalf("expected j to scroll down, offset stayed at %d", m.errorPopup.viewport.YOffset)
	}
}

func TestShowError_DoesNothingWithoutAnError(t *testing.T) {
	m := newTestModel(t)
	m.lastErr = nil

	updated, _ := m.handleKey(keyPress("E"))
	m = updated.(Model)

	if m.mode != ModeNormal {
		t.Fatalf("expected to stay in ModeNormal with no error, got %v", m.mode)
	}
	if m.errorPopup != nil {
		t.Fatal("expected no pop-up when there is no error to show")
	}
}

// Closing the reading of an error must not pretend the error stopped being
// true. Only a successful refresh clears lastErr.
func TestErrorPopup_EscClosesButKeepsLastErr(t *testing.T) {
	m := failingRefresh(t, newTestModel(t), realRefreshError)

	updated, _ := m.handleKey(keyPress("esc"))
	m = updated.(Model)

	if m.mode != ModeNormal {
		t.Fatalf("expected ModeNormal after esc, got %v", m.mode)
	}
	if m.errorPopup != nil {
		t.Fatal("expected the pop-up to be closed")
	}
	if m.lastErr == nil {
		t.Fatal("esc must not clear lastErr -- the error is still true after you read it")
	}
}

// The marker has to sit before the message. The message is what runs off
// the right edge, so a hint appended after it would be the first thing
// lost, on the very error it exists to rescue.
func TestSummaryError_MarkerComesBeforeTheMessage(t *testing.T) {
	line := formatSummaryError(errors.New(realRefreshError))
	markerAt := strings.Index(line, "[E]")
	msgAt := strings.Index(line, "mesh_list_realms")
	if markerAt < 0 {
		t.Fatalf("expected an [E] affordance in the status line, got %q", line)
	}
	if msgAt < 0 || markerAt > msgAt {
		t.Fatalf("[E] must precede the message so clipping cannot eat it, got %q", line)
	}
}

// A copy key that silently does nothing is worse than one that says it
// could not: clipboard.WriteAll needs a helper binary that is absent over
// a bare SSH session, and that is a normal way to run this TUI.
func TestErrorPopup_ReportsACopyFailure(t *testing.T) {
	e := errorPopup{text: realRefreshError, copied: copyFailed, copyErrMsg: "exec: \"xclip\": executable file not found in $PATH"}
	line := e.copyLine()
	if !strings.Contains(line, "could not copy") {
		t.Fatalf("a failed copy must say so, got %q", line)
	}
	if !strings.Contains(line, "xclip") {
		t.Fatalf("a failed copy must name the reason, got %q", line)
	}
	if quiet := (errorPopup{}).copyLine(); quiet != "" {
		t.Fatalf("expected no copy line before any copy is attempted, got %q", quiet)
	}
}

// Both cases fail on the old byte-slicing implementation, for the two
// distinct ways it went wrong. n=2 cuts "ré" between é's two bytes and
// yields a string that is not valid UTF-8 at all. n=12 happens to land on
// a boundary but still keeps only 11 characters, because it was counting
// bytes while every caller passes a character budget.
func TestTruncateForChat_NeverSplitsAMultiByteCharacter(t *testing.T) {
	const s = "réseau maillé déjà résolu"

	for _, n := range []int{2, 12} {
		got := truncateForChat(s, n)
		trimmed := strings.TrimSuffix(got, "...")

		if !utf8.ValidString(got) {
			t.Fatalf("n=%d: truncation produced invalid UTF-8: %q", n, got)
		}
		if count := utf8.RuneCountInString(trimmed); count != n {
			t.Fatalf("n=%d: expected %d characters kept, got %d (%q)", n, n, count, trimmed)
		}
		if !strings.HasPrefix(s, trimmed) {
			t.Fatalf("n=%d: truncated text must be a prefix of the original, got %q", n, trimmed)
		}
	}
}

func TestTruncateForChat_ShortStringIsUntouched(t *testing.T) {
	s := "déjà vu"
	if got := truncateForChat(s, 40); got != s {
		t.Fatalf("expected %q unchanged, got %q", s, got)
	}
}

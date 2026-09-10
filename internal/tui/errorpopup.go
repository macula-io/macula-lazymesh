package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The status line appends the whole error and never truncates it -- the
// terminal cuts it, because that line does not wrap and the error is
// appended last, so the tail is the first thing over the right edge. The
// tail is also the part worth reading: a mesh error arrives double-wrapped
// ("mesh_list_realms: macula-mcp tool mesh_list_realms reported an error:
// MCP error -32602: Tool mesh_list_realms not found"), naming the tool
// three times up front and carrying the actual reason at the end. So this
// pop-up exists to show the string the program already holds, wrapped and
// scrollable, rather than to fetch or reconstruct anything.
//
// Follows ringpopup.go's shape: its own Mode, its own key handler, its own
// render, no generic dialog framework.

// errorPopupWidthCap keeps the box readable on a wide terminal; narrower
// terminals get whatever is left after the chrome.
const errorPopupWidthCap = 76

// errorPopupChromeCols is what the box costs around its text: the thick
// border's own column on each side, plus Padding(1, 2)'s two columns on
// each side. The viewport is sized in CONTENT columns, so this has to come
// off the terminal width first -- leaving it out made the box render six
// columns wider than the terminal, which is the same running-off-the-edge
// failure this pop-up exists to fix.
const errorPopupChromeCols = 6

// errorPopupChromeRows is the vertical equivalent: border top and bottom,
// Padding(1, 2)'s row above and below, the title and its blank line, the
// blank line and hint row under the viewport, and the status strip the
// View keeps visible alongside the pop-up.
const errorPopupChromeRows = 12

// copyStatus is what the pop-up says about the last copy attempt. Copying
// can genuinely fail -- clipboard.WriteAll shells out to xclip/xsel/wl-copy
// on Linux and there is none of that over a bare SSH session -- and a copy
// key that silently does nothing is worse than one that says it could not.
type copyStatus int

const (
	copyNotTried copyStatus = iota
	copyDone
	copyFailed
)

// errorPopup is the state behind ModeErrorPopup. text is the untouched
// error string: never collapsed, never truncated. m.lastErr holds an
// error value, not a rendered line, so newlines in the original survive
// to here and the viewport shows them as-is.
type errorPopup struct {
	text       string
	index      int // which errorHistory entry is showing
	viewport   viewport.Model
	copied     copyStatus
	copyErrMsg string
}

func errorPopupWidth(termWidth int) int {
	w := termWidth - errorPopupChromeCols
	if w > errorPopupWidthCap {
		w = errorPopupWidthCap
	}
	if w < 8 {
		w = 8
	}
	return w
}

func errorPopupHeight(termHeight int) int {
	h := termHeight - errorPopupChromeRows
	if h > 16 {
		h = 16
	}
	if h < 3 {
		h = 3
	}
	return h
}

// newErrorPopup wraps text to the available width up front. lipgloss
// hard-breaks a run longer than the width, which matters here: the real
// case is one long line with no line breaks in it at all, so a viewport
// that only wrapped on existing newlines would still show a single row
// running off the edge.
func newErrorPopup(text string, termWidth, termHeight int) errorPopup {
	w := errorPopupWidth(termWidth)
	wrapped := lipgloss.NewStyle().Width(w).Render(text)

	// Size to the content, up to what the terminal allows. A fixed height
	// would frame a two-line error in a dozen blank rows, which reads as
	// something failing to load rather than as a short message.
	h := strings.Count(wrapped, "\n") + 1
	if max := errorPopupHeight(termHeight); h > max {
		h = max
	}
	if h < 1 {
		h = 1
	}

	vp := viewport.New(w, h)
	vp.SetContent(wrapped)
	return errorPopup{text: text, viewport: vp}
}

// handleErrorPopupKey: scroll, copy, or close. Esc closes without
// clearing m.lastErr -- the error is still true after you have read it,
// and the next successful refresh clears it on its own.
func (m Model) handleErrorPopupKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.errorPopup == nil {
		m.mode = ModeNormal
		m.resizeComponents()
		return m, nil
	}

	switch {
	case key.Matches(msg, DefaultKeyMap.OlderError):
		return m.openErrorPopup(m.errorPopup.index - 1), nil
	case key.Matches(msg, DefaultKeyMap.NewerError):
		return m.openErrorPopup(m.errorPopup.index + 1), nil
	case key.Matches(msg, DefaultKeyMap.Copy):
		if err := clipboard.WriteAll(m.errorPopup.text); err != nil {
			m.errorPopup.copied = copyFailed
			m.errorPopup.copyErrMsg = err.Error()
			return m, nil
		}
		m.errorPopup.copied = copyDone
		m.errorPopup.copyErrMsg = ""
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Up):
		m.errorPopup.viewport.LineUp(1)
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Down):
		m.errorPopup.viewport.LineDown(1)
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Normal): // Esc
		m.errorPopup = nil
		m.mode = ModeNormal
		m.resizeComponents()
		return m, nil
	}
	return m, nil
}

var (
	errorPopupBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("196")).
				Padding(1, 2)
	errorPopupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	errorPopupOkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// copyLine reports what the last copy attempt did, in the operator's own
// terms. A failure names the reason, since "no clipboard tool installed"
// and "not running under a display server" need different responses.
func (e errorPopup) copyLine() string {
	switch e.copied {
	case copyDone:
		return errorPopupOkStyle.Render("copied to clipboard")
	case copyFailed:
		return chatErrorStyleTUI.Render("could not copy: " + collapseNewlines(e.copyErrMsg))
	default:
		return ""
	}
}

// errorPopupHeading names which error out of how many, how often it has
// come back, and when it was FIRST seen. First rather than last on
// purpose: a repeating failure's earliest sighting is the one whose
// timestamp tells you when things actually started going wrong.
func (m Model) errorPopupHeading() string {
	e := m.errorPopup
	title := fmt.Sprintf("⚠ Error %d of %d", e.index+1, len(m.errorHistory))
	if e.index < 0 || e.index >= len(m.errorHistory) {
		return errorPopupTitleStyle.Render("⚠ Error")
	}
	rec := m.errorHistory[e.index]

	detail := rec.first.Format("15:04:05")
	if rec.count > 1 {
		detail = fmt.Sprintf("%s, seen %d times, first at %s",
			rec.last.Format("15:04:05"), rec.count, rec.first.Format("15:04:05"))
	}
	return errorPopupTitleStyle.Render(title) + popupHintStyle.Render("   "+detail)
}

func (m Model) renderErrorPopup() string {
	e := m.errorPopup
	if e == nil {
		return ""
	}
	parts := []string{
		m.errorPopupHeading(),
		"",
		e.viewport.View(),
		"",
	}
	if line := e.copyLine(); line != "" {
		parts = append(parts, line, "")
	}

	hint := "[c] Copy   [esc] Close"
	if len(m.errorHistory) > 1 {
		hint = "[p] Older   [n] Newer   " + hint
	}
	if e.viewport.TotalLineCount() > e.viewport.Height {
		hint = "[k/↑ j/↓] Scroll   " + hint
	}
	parts = append(parts, popupHintStyle.Render(hint))
	return errorPopupBorderStyle.Render(strings.Join(parts, "\n"))
}

// errorPopupMarker is the affordance shown next to the status line's
// error. It sits BEFORE the message deliberately: the message is what
// runs off the right edge, so a hint appended after it would be the first
// thing lost -- exactly on the error it is meant to rescue.
func errorPopupMarker() string {
	return errStyle.Render("[E]")
}

func formatSummaryError(err error) string {
	return fmt.Sprintf("%s %s", errorPopupMarker(), errStyle.Render(fmt.Sprintf("error: %s", err)))
}

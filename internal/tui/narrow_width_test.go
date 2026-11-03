package tui

// macula-io/macula-lazymesh#86: a line wider than the terminal is wrapped
// by the terminal itself, which makes the screen one row taller than the
// layout reserved and pushes the top row away. Every rendered line of
// View() must fit the width the model was told about, at any width a
// person might actually use.

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// linesWiderThan reports the rendered lines of view that do not fit width,
// with the line itself, so a failure names the row that overflowed.
func linesWiderThan(view string, width int) []string {
	var over []string
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > width {
			over = append(over, line)
		}
	}
	return over
}

// modelAt builds an idle model told it is width x height columns, the way
// the terminal does it.
func modelAt(t *testing.T, width, height int) Model {
	t.Helper()
	m := newTestModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(Model)
}

// TestViewFitsTheTerminal is the issue's check: the main screen, with a
// chat worth reading on it, fits every width tried. The hint row alone is
// 111 cells at width 60, so this fails on the narrow widths before the
// fix.
func TestViewFitsTheTerminal(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := modelAt(t, width, 20)
			m = fillChat(t, m, 12)
			m = deliverAnswer(t, m, "an answer that arrived below")
			if over := linesWiderThan(m.View(), width); len(over) > 0 {
				t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
			}
		})
	}
}

// TestViewFitsTheTerminalScrolledUp covers the "new below" marker, the
// other row the issue names: it is added above the status strip only while
// the reader is scrolled up, so it is not visible in the default state.
func TestViewFitsTheTerminalScrolledUp(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := modelAt(t, width, 20)
			m = fillChat(t, m, 30)
			m.chatViewport.GotoTop()
			m = deliverAnswer(t, m, "an answer that arrived below")
			if !strings.Contains(m.View(), "new below") {
				t.Fatal("the marker did not appear; the test would prove nothing")
			}
			if over := linesWiderThan(m.View(), width); len(over) > 0 {
				t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
			}
		})
	}
}

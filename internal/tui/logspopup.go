package tui

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/logging"
)

// logsShown is how many entries the overlay pulls per open or refresh.
// The buffer holds a thousand; a few hundred is more than anyone reads in
// one sitting and keeps the wrap work bounded on every tick.
const logsShown = 200

// logsComponentWidth fixes the subsystem column. Component is one of a
// small closed set (mcp, agent, realm, mesh, refresh, provider), so a
// fixed width lines the messages up instead of ragging them by whichever
// name happened to be longest on screen.
const logsComponentWidth = 8

// logsPopup is the l overlay. Same shape as errorPopup and the ring
// pop-up: its own Mode, its own key handler, its own render.
type logsPopup struct {
	viewport viewport.Model
	count    int // entries currently drawn
	held     int // entries the buffer is holding, for an honest header
}

// levelStyle colors a line by severity rather than by component, because
// severity is what an operator scans a log for. Warn and error take the
// same two colors the rest of the TUI already uses for those meanings.
func levelStyle(l slog.Level) lipgloss.Style {
	switch {
	case l >= slog.LevelError:
		return chatErrorStyleTUI
	case l >= slog.LevelWarn:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case l >= slog.LevelInfo:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	default:
		return dimStyle
	}
}

// renderLogLine lays one entry out as time, level, component, message.
// The message is the only part allowed to wrap, so the three fixed
// columns stay aligned down the page.
func renderLogLine(e logging.Entry, width int) string {
	prefix := fmt.Sprintf("%s %-5s %-*s ",
		e.Time.Format("15:04:05"),
		e.Level.String(),
		logsComponentWidth, truncateForChat(e.Component, logsComponentWidth),
	)
	indent := lipgloss.Width(prefix)
	msgWidth := width - indent
	if msgWidth < 8 {
		// Too narrow to indent under the prefix; let the whole line wrap
		// to the full width rather than squeezing the message to nothing.
		return levelStyle(e.Level).Render(lipgloss.NewStyle().Width(width).Render(prefix + collapseNewlines(e.Message)))
	}

	msg := lipgloss.NewStyle().Width(msgWidth).Render(collapseNewlines(e.Message))
	lines := strings.Split(msg, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, prefix+line)
			continue
		}
		out = append(out, strings.Repeat(" ", indent)+line)
	}
	return levelStyle(e.Level).Render(strings.Join(out, "\n"))
}

// logsEmptyState says why an overlay can be legitimately empty. The
// buffer starts fresh every launch, so a new window shows nothing --
// which reads as broken logging unless it explains itself, and names the
// file that does hold the history.
func (m Model) logsEmptyState() string {
	path := m.logPath
	if path == "" {
		path = "agent.log"
	}
	return dimStyle.Render("No log entries yet this run.\n\nThis view holds only the current run.\nThe full history across runs is in " + path + ".")
}

// newLogsPopup reads the buffer once and lays the entries out. A nil
// buffer is a normal state, not a failure: the wiring that fills it lands
// separately from this overlay, so an unwired build shows the empty state
// rather than panicking on a key press.
func (m Model) newLogsPopup() logsPopup {
	w := errorPopupWidth(m.width)
	h := errorPopupHeight(m.height)

	var entries []logging.Entry
	held := 0
	if m.logBuffer != nil {
		entries = m.logBuffer.RecentEntries(logsShown)
		held = m.logBuffer.Len()
	}

	body := m.logsEmptyState()
	if len(entries) > 0 {
		lines := make([]string, 0, len(entries))
		for _, e := range entries {
			lines = append(lines, renderLogLine(e, w))
		}
		body = strings.Join(lines, "\n")
	}

	// Size to the content up to what the terminal allows, same as the
	// error dialog: two log lines framed by a dozen blank rows reads as
	// something failing to load rather than as a quiet run.
	if lines := strings.Count(body, "\n") + 1; lines < h {
		h = lines
	}
	if h < 1 {
		h = 1
	}

	vp := viewport.New(w, h)
	vp.SetContent(body)
	return logsPopup{viewport: vp, count: len(entries), held: held}
}

func (m Model) handleLogsKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.logsPopup == nil {
		m.mode = ModeNormal
		m.resizeComponents()
		return m, nil
	}

	switch {
	case key.Matches(msg, DefaultKeyMap.Up):
		m.logsPopup.viewport.LineUp(1)
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Down):
		m.logsPopup.viewport.LineDown(1)
		return m, nil
	case key.Matches(msg, DefaultKeyMap.Normal), key.Matches(msg, DefaultKeyMap.ToggleLogs):
		m.logsPopup = nil
		m.mode = ModeNormal
		m.resizeComponents()
		return m, nil
	}
	return m, nil
}

var logsPopupBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.ThickBorder()).
	BorderForeground(lipgloss.Color("#38BDF8")).
	Padding(1, 2)

var logsPopupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))

// logsHeading counts what is DRAWN against what the buffer HOLDS, not
// against everything logged this run. Once the ring wraps, the older
// entries are gone from memory, so "of N logged" would be a lie while
// "of N held" stays true.
func (p logsPopup) heading() string {
	title := logsPopupTitleStyle.Render("▤ Logs")
	if p.count == 0 {
		return title
	}
	return title + popupHintStyle.Render(fmt.Sprintf("   showing %d of %d held", p.count, p.held))
}

func (m Model) renderLogsPopup() string {
	p := m.logsPopup
	if p == nil {
		return ""
	}
	hint := "[esc] Close"
	if p.viewport.TotalLineCount() > p.viewport.Height {
		hint = "[k/↑ j/↓] Scroll   " + hint
	}
	body := strings.Join([]string{
		p.heading(),
		"",
		p.viewport.View(),
		"",
		popupHintStyle.Render(hint),
	}, "\n")
	return logsPopupBorderStyle.Render(body)
}

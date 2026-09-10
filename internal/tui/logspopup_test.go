package tui

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/logging"
)

func testLogBuffer(t *testing.T, entries ...logging.Entry) *logging.Buffer {
	t.Helper()
	b := logging.NewBuffer(100)
	for _, e := range entries {
		b.Append(e)
	}
	return b
}

func logEntry(level slog.Level, component, message string) logging.Entry {
	return logging.Entry{
		Time:      time.Date(2026, 9, 10, 14, 3, 22, 0, time.UTC),
		Level:     level,
		Component: component,
		Message:   message,
	}
}

func modelWithLogs(t *testing.T, b *logging.Buffer) Model {
	t.Helper()
	m := New(nil, Options{
		UserInputCh:       make(chan string, 8),
		StatusBarPosition: "bottom",
		LogBuffer:         b,
		LogPath:           "~/.config/lazymesh/agent.log",
	})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

func TestLogsOverlay_OpensAndCloses(t *testing.T) {
	m := modelWithLogs(t, testLogBuffer(t, logEntry(slog.LevelInfo, "mesh", "joined room")))

	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)
	if m.mode != ModeLogs {
		t.Fatalf("expected ModeLogs after l, got %v", m.mode)
	}

	updated, _ = m.handleKey(keyPress("esc"))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected ModeNormal after esc, got %v", m.mode)
	}
}

func TestLogsOverlay_ShowsTimeLevelComponentAndMessage(t *testing.T) {
	m := modelWithLogs(t, testLogBuffer(t,
		logEntry(slog.LevelError, "mcp", "tools/call mesh_list_realms failed"),
	))
	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)

	out := m.renderLogsPopup()
	for _, want := range []string{"14:03:22", "ERROR", "mcp", "tools/call mesh_list_realms failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log line lost %q:\n%s", want, out)
		}
	}
}

// Venus's buffer hands entries back newest first and the overlay renders
// them in that order, so the most recent thing is at the top where it is
// read first rather than at the bottom of a scroll.
func TestLogsOverlay_NewestFirst(t *testing.T) {
	m := modelWithLogs(t, testLogBuffer(t,
		logEntry(slog.LevelInfo, "agent", "oldest line"),
		logEntry(slog.LevelInfo, "agent", "newest line"),
	))
	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)

	out := m.renderLogsPopup()
	newest := strings.Index(out, "newest line")
	oldest := strings.Index(out, "oldest line")
	if newest < 0 || oldest < 0 {
		t.Fatalf("expected both lines, got:\n%s", out)
	}
	if newest > oldest {
		t.Fatalf("newest entry should be above the oldest:\n%s", out)
	}
}

// The buffer starts empty every launch, so an operator opening a fresh
// window sees nothing. Without saying why, that reads as logging being
// broken rather than as a new session.
func TestLogsOverlay_EmptyStateExplainsItself(t *testing.T) {
	m := modelWithLogs(t, testLogBuffer(t))
	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)

	out := m.renderLogsPopup()
	if !strings.Contains(out, "this run") {
		t.Fatalf("empty state must say the buffer is per-run:\n%s", out)
	}
	if !strings.Contains(out, "agent.log") {
		t.Fatalf("empty state must name the file holding the full history:\n%s", out)
	}
}

// The wiring in main.go is Venus's half and lands separately, so the TUI
// must survive a nil buffer rather than panic on a key press.
func TestLogsOverlay_SurvivesAnUnwiredBuffer(t *testing.T) {
	m := modelWithLogs(t, nil)

	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)

	if m.mode != ModeLogs {
		t.Fatalf("l should still open the overlay with no buffer wired, got %v", m.mode)
	}
	if out := m.renderLogsPopup(); !strings.Contains(out, "this run") {
		t.Fatalf("expected the empty state with no buffer wired, got:\n%s", out)
	}
}

func TestLogsOverlay_FitsWithinTheTerminalWidth(t *testing.T) {
	long := "macula-mcp tools/call mesh_list_realms: MCP error -32602: Tool mesh_list_realms not found, and then some more text to push well past any terminal"
	for _, width := range []int{40, 80, 120} {
		m := modelWithLogs(t, testLogBuffer(t,
			logEntry(slog.LevelError, "mcp", long),
			logEntry(slog.LevelInfo, "refresh", "polled mesh state"),
		))
		m.width, m.height = width, 24
		m.resizeComponents()

		updated, _ := m.handleKey(keyPress("l"))
		m = updated.(Model)

		out := m.renderLogsPopup()
		if out == "" {
			t.Fatal("expected a rendered overlay, not an empty string")
		}
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: line %d is %d columns wide:\n%s", width, i, w, line)
			}
		}
		t.Logf("rendered at %d columns:\n%s", width, out)
	}
}

// The logs overlay is something the operator asked to look at, so it gets
// the same protection as composing and the other overlays.
func TestLogsOverlay_HoldsBackAnAutoPop(t *testing.T) {
	m := modelWithLogs(t, testLogBuffer(t, logEntry(slog.LevelInfo, "mesh", "joined room")))
	updated, _ := m.handleKey(keyPress("l"))
	m = updated.(Model)

	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	if m.mode != ModeLogs {
		t.Fatal("an error must not cover the logs overlay")
	}

	updated, _ = m.handleKey(keyPress("esc"))
	m = updated.(Model)
	if m.mode != ModeErrorPopup {
		t.Fatal("the held error should appear once the logs overlay is closed")
	}
}

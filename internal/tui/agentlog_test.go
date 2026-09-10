package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func modelWithLogPath(t *testing.T, path string) Model {
	t.Helper()
	m := New(nil, Options{
		UserInputCh:       make(chan string, 8),
		StatusBarPosition: "bottom",
		LogPath:           path,
	})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

func TestOpenAgentLog_LaunchesAnEditor(t *testing.T) {
	m := modelWithLogPath(t, filepath.Join(t.TempDir(), "agent.log"))

	updated, cmd := m.handleKey(keyPress("l"))
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("l should hand the log to $EDITOR, got no command")
	}
	if m.mode != ModeNormal {
		t.Fatalf("opening the log in an editor should not change the TUI's mode, got %v", m.mode)
	}
}

// Launching an editor on an empty filename opens a scratch buffer, which
// looks exactly like an empty log. Say it is not configured instead.
func TestOpenAgentLog_UnconfiguredPathIsReportedNotOpened(t *testing.T) {
	m := modelWithLogPath(t, "")

	updated, cmd := m.handleKey(keyPress("l"))
	m = updated.(Model)

	if cmd != nil {
		t.Fatal("with no log path there is nothing to open, so no editor should launch")
	}
	last := m.chatEntries[len(m.chatEntries)-1]
	if last.kind != chatError || !strings.Contains(last.text, "no agent log path") {
		t.Fatalf("expected the missing path to be reported, got %+v", last)
	}
}

// The hazard this file exists to avoid. ctrl+e's return handler deletes
// the file it was given and loads its contents into the compose box.
// Viewing the log must do neither: the log must still exist afterwards,
// untouched, and nothing may land in a message the operator did not write.
func TestLogViewed_NeverDeletesTheLogOrLoadsItIntoCompose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	const content = "2026-09-10 14:03:22 ERROR mcp tools/call mesh_list_realms failed\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := modelWithLogPath(t, path)

	m, _ = m.handleLogViewed(logViewedMsg{})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the agent log must survive being viewed, but reading it failed: %v", err)
	}
	if string(got) != content {
		t.Fatalf("the agent log must be untouched by viewing it, got %q", got)
	}
	if v := m.input.Value(); v != "" {
		t.Fatalf("viewing the log must not put anything in the compose box, got %q", v)
	}
	if m.mode != ModeNormal {
		t.Fatalf("viewing the log must not switch into insert mode, got %v", m.mode)
	}
}

// A clean exit is silent: the operator was just looking at the log. A
// failure gets a line, or l would appear to do nothing at all.
func TestLogViewed_ReportsAFailureAndStaysQuietOnSuccess(t *testing.T) {
	m := modelWithLogPath(t, "/home/example/.config/lazymesh/agent.log")
	before := len(m.chatEntries)

	m, _ = m.handleLogViewed(logViewedMsg{})
	if len(m.chatEntries) != before {
		t.Fatal("a successful editor exit should not add a chat line")
	}

	m, _ = m.handleLogViewed(logViewedMsg{err: errors.New("exec: \"nvim\": executable file not found in $PATH")})
	last := m.chatEntries[len(m.chatEntries)-1]
	if last.kind != chatError {
		t.Fatalf("expected an error entry, got %+v", last)
	}
	for _, want := range []string{"agent.log", "executable file not found"} {
		if !strings.Contains(last.text, want) {
			t.Fatalf("failure line should name %q so it can be acted on, got %q", want, last.text)
		}
	}
}

func TestEditorBinary_FallsBackToVi(t *testing.T) {
	t.Setenv("EDITOR", "")
	if got := editorBinary(); got != "vi" {
		t.Fatalf("expected vi with $EDITOR unset, got %q", got)
	}
	t.Setenv("EDITOR", "hx")
	if got := editorBinary(); got != "hx" {
		t.Fatalf("expected $EDITOR to be honoured, got %q", got)
	}
}

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The l key opens the agent log in $EDITOR instead of rendering it in a
// pane. The file already exists and an editor already has search,
// jumping, copy and highlighting; a viewport would reimplement all of
// that, worse, against a second copy of the same truth.
//
// It reuses ctrl+e's mechanism -- editorBinary plus tea.ExecProcess --
// but deliberately NOT openEditorCmd itself. That path writes a temp
// file, and on return handleEditorFinished DELETES the file it was given
// and loads its contents into the compose box. Pointed at the agent log
// that would erase the log and paste it into a message, so the two
// callers share the launching and nothing else.

// logViewedMsg reports that the editor exited. There is no path field on
// purpose: nothing here removes a file, and a message carrying one would
// invite a future handler to.
type logViewedMsg struct {
	err error
}

// editorBinary is the editor both callers launch. Falls back to vi when
// $EDITOR is unset, since something must run and vi is the one editor a
// POSIX system is guaranteed to have.
func editorBinary() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	return "vi"
}

// runEditorCmd suspends the TUI and runs the editor against path. done
// turns the exit into whichever message the caller wants, which is what
// keeps composing and log-viewing from sharing a return path.
func runEditorCmd(path string, done func(error) tea.Msg) tea.Cmd {
	c := exec.Command(editorBinary(), path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, done)
}

// openAgentLog hands the agent log to $EDITOR. An unwired path is
// reported rather than launching an editor on an empty filename, which
// would open a scratch buffer and look like the log was empty.
func (m Model) openAgentLog() (tea.Model, tea.Cmd) {
	if m.logPath == "" {
		m.chatEntries = append(m.chatEntries, chatEntry{
			kind: chatError,
			at:   time.Now(),
			text: "no agent log path is configured, so there is nothing to open",
		})
		m.syncViewport()
		return m, nil
	}
	return m, runEditorCmd(m.logPath, func(err error) tea.Msg {
		return logViewedMsg{err: err}
	})
}

// handleLogViewed runs when the editor exits. Success is silent: the
// operator has just been looking at the log and does not need to be told
// they were. A failure is worth a line, since otherwise l would appear to
// do nothing at all.
func (m Model) handleLogViewed(msg logViewedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	m.chatEntries = append(m.chatEntries, chatEntry{
		kind: chatError,
		at:   time.Now(),
		text: fmt.Sprintf("could not open %s in %s: %s", m.logPath, editorBinary(), msg.err),
	})
	m.syncViewport()
	return m, nil
}

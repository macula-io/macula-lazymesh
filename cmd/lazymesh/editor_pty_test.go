//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Running an editor from the TUI (ctrl+e for the compose box, l for the
// agent log) is the one moment lazymesh hands its terminal to another
// program, and the editor only works if lazymesh has genuinely let go of
// stdin first (macula-lazymesh#60).
//
// Both tests here run the real binary under a pty, because the defect they
// cover lives in the wiring between the terminal and the TUI: every unit
// test of the key handling passed while the editor received nothing.

// TestEditorChildReceivesWhatIsTypedAtIt is the direct measurement. The
// editor is a script that blocks on a single line from the terminal and
// writes it to a file, so "did the editor get what the operator typed" is
// a file content question, not an inference from the screen.
//
// On the unfixed tree the editor's read and a read the TUI had already
// parked on the same terminal raced for the keystrokes, and the parked one
// won: the file stayed empty and the operator's ":q" went to a
// suspended-but-still-reading TUI that discarded it.
func TestEditorChildReceivesWhatIsTypedAtIt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	// read(1) is a builtin reading the terminal in its restored (cooked)
	// mode, so it returns the line once enter is pressed -- the same wait
	// an editor's keyboard loop does, with an observable result.
	body := "#!/bin/sh\ncd " + dir + "\necho ready > ready\nread -r line\nprintf '%s' \"$line\" > typed.txt\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}

	term := startAttachedWithEditor(t, startFakeSession(t), script)
	term.mustWrite(t, []byte{0x05}) // ctrl+e
	waitForFile(t, filepath.Join(dir, "ready"), 30*time.Second, "ctrl+e never started an editor")

	term.mustWrite(t, []byte(":q\r"))

	typed := filepath.Join(dir, "typed.txt")
	waitForFile(t, typed, 10*time.Second, "the editor never received the line typed at it")
	got, err := os.ReadFile(typed)
	if err != nil {
		t.Fatalf("read what the editor received: %v", err)
	}
	if string(got) != ":q" {
		t.Fatalf("the editor received %q, want %q: something else is reading the terminal", got, ":q")
	}
}

// TestRealEditorQuitsWithColonQ is the behaviour the issue is about, on a
// real editor: ctrl+e opens it on the draft, ":q" typed at it closes it,
// and the TUI comes back. Skipped where no vi-family editor is installed.
func TestRealEditorQuitsWithColonQ(t *testing.T) {
	var editor string
	for _, candidate := range []string{"nvim", "vim", "vi"} {
		if path, err := exec.LookPath(candidate); err == nil {
			editor = path
			break
		}
	}
	if editor == "" {
		t.Skip("no nvim/vim/vi installed, so a real editor cannot be run here")
	}

	term := startAttachedWithEditor(t, startFakeSession(t), editor)
	term.waitFor(t, "-- NORMAL --", 30*time.Second)

	term.mustWrite(t, []byte{0x05}) // ctrl+e

	// The editor takes the terminal over: it enters its own alternate
	// screen, which is the second one to appear (the TUI entered the first
	// when it started).
	deadline := time.Now().Add(30 * time.Second)
	for strings.Count(term.screen(), "\x1b[?1049h") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("ctrl+e did not put an editor on the terminal within 30s\nscreen:\n%s", term.screen())
		}
		time.Sleep(50 * time.Millisecond)
	}

	term.mustWrite(t, []byte(":q\r"))

	// A compose round trip always lands back in insert mode, so the marker
	// says both that the editor took ":q" and that the TUI resumed.
	term.waitFor(t, "-- INSERT --", 30*time.Second)
}

// editorTerminal is the attached TUI plus the pid of the lazymesh process
// running it, which the /proc-based cases need.
type editorTerminal struct {
	*fakeTerminal
	pid int
}

// startAttachedWithEditor is startAttached with $EDITOR pointed at
// editorPath, for the cases that need a specific program on ctrl+e.
func startAttachedWithEditor(t *testing.T, socketPath, editorPath string) *editorTerminal {
	t.Helper()
	bin := buildLazymesh(t)

	cmd := exec.Command(bin, "attach", socketPath)
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color", "EDITOR="+editorPath)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("start attach under a pty: %v", err)
	}
	term := &editorTerminal{fakeTerminal: &fakeTerminal{master: master, exited: make(chan error, 1)}, pid: cmd.Process.Pid}
	t.Cleanup(func() {
		master.Close()
		select {
		case <-term.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	go func() { term.exited <- cmd.Wait() }()
	go term.drain()
	term.waitFor(t, painted, 30*time.Second)
	time.Sleep(200 * time.Millisecond)
	return term
}

// mustWrite sends bytes to the terminal, failing the test rather than
// continuing into a case that would then fail for the wrong reason.
func (f *fakeTerminal) mustWrite(t *testing.T, b []byte) {
	t.Helper()
	if _, err := f.write(b); err != nil {
		t.Fatalf("write %q to the terminal: %v", b, err)
	}
}

// waitForFile blocks until path exists (and is non-empty for the result
// files) or fails the test with what it was waiting for -- a silent wait
// would let the case fail later for the wrong reason.
func waitForFile(t *testing.T, path string, timeout time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s: %s", filepath.Base(path), timeout, why)
}

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestNoReadIsParkedOnTheTerminalWhileTheEditorRuns pins the mechanism
// behind macula-lazymesh#60 directly, without relying on who happens to win
// a race for the next keystroke: while the editor is on the terminal,
// lazymesh must not have a thread blocked reading that terminal.
//
// The defect was exactly that. bubbletea parks a read on stdin and cancels
// it before running something with the terminal handed over; with a reader
// that cancelreader does not recognize as a file, Cancel() cannot interrupt
// the blocked read, bubbletea gives up waiting after 500ms, and the editor
// starts with lazymesh's read still queued on the same terminal. From then
// on the two readers race for every key, and the parked one -- queued first
// -- wins: "the editor receives nothing" (measured; see
// TestEditorChildReceivesWhatIsTypedAtIt).
//
// Reading /proc/<pid>/task/<tid>/syscall is what makes this observable
// without a race: a thread blocked in read(2) on fd 0 says the syscall
// number and arguments even while it is parked, so the question becomes a
// file read rather than a timing judgement.
func TestNoReadIsParkedOnTheTerminalWhileTheEditorRuns(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	// An editor that occupies the terminal and reads nothing: every read on
	// the terminal during its run is lazymesh's.
	body := "#!/bin/sh\necho ready > " + filepath.Join(dir, "ready") + "\nsleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write editor script: %v", err)
	}

	term := startAttachedWithEditor(t, startFakeSession(t), script)
	term.mustWrite(t, []byte{0x05}) // ctrl+e
	waitForFile(t, filepath.Join(dir, "ready"), 30*time.Second,
		"the editor never started, so there is nothing to measure")
	// The editor has the terminal from here until it exits. Take several
	// samples rather than one: the read loop is torn down asynchronously,
	// and a single sample could miss a reader that is one keystroke away
	// from being parked again.
	for i := 0; i < 10; i++ {
		if tids := threadsBlockedReadingStdin(term.pid); len(tids) > 0 {
			t.Fatalf("while the editor held the terminal, thread(s) %v of lazymesh were blocked reading it: "+
				"a read parked before the editor started still competes for every keystroke", tids)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// threadsBlockedReadingStdin lists the ids of pid's threads that are
// currently blocked in a read of file descriptor 0.
func threadsBlockedReadingStdin(pid int) []string {
	tasks, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task")
	if err != nil {
		return nil
	}
	var blocked []string
	for _, task := range tasks {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + task.Name() + "/syscall")
		if err != nil {
			// A thread can exit between the listing and the read; that is
			// not a thread reading the terminal.
			continue
		}
		fields := strings.Fields(string(raw))
		if len(fields) < 2 || fields[0] == "running" {
			continue
		}
		number, err := strconv.Atoi(fields[0])
		if err != nil || number != unix.SYS_READ {
			continue
		}
		if fields[1] == "0x0" || fields[1] == "0" {
			blocked = append(blocked, task.Name())
		}
	}
	return blocked
}

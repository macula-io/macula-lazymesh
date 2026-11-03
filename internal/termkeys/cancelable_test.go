//go:build !windows

package termkeys

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
)

// The bug this pins (macula-lazymesh#60): bubbletea parks a read on stdin
// and must be able to cancel it before it hands the terminal to an editor
// (and to anything else it runs while suspended). cancelreader only builds
// a reader whose Cancel can interrupt a blocked read when the reader
// satisfies cancelreader.File -- ReadWriteCloser, Fd AND Name. Without
// Name it falls back to one whose Cancel() returns false and leaves the
// blocked read in place; that read then sits on the same terminal as the
// editor and wins keystrokes out of it.
func TestCancellingAReadBlockedOnTheTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pty: %v", err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })

	input := New(tty)
	cr, err := cancelreader.NewReader(input)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	t.Cleanup(func() { cr.Close() })

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 256)
		_, err := cr.Read(buf)
		done <- err
	}()

	// Let the read reach the terminal and block there.
	time.Sleep(100 * time.Millisecond)

	if !cr.Cancel() {
		t.Fatal("Cancel() returned false: cancelreader did not recognize the terminal input as a file, " +
			"so the blocked read cannot be interrupted and stays parked on the terminal")
	}
	select {
	case err := <-done:
		if !errors.Is(err, cancelreader.ErrCanceled) {
			t.Fatalf("the blocked read returned %v, want %v", err, cancelreader.ErrCanceled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel() returned true but the blocked read was not interrupted")
	}
}

// The other half of the same contract, and the reason the cancellation is
// not offered unconditionally: cancelreader registers the descriptor with
// epoll, which refuses descriptors that cannot be polled. A reader that is
// not a terminal must therefore keep cancelreader's fallback -- measured:
// with Name offered for every reader, `lazymesh attach < /dev/null` could
// not start at all ("add reader to epoll interest list").
func TestOnlyATerminalInputIsACancelReaderFile(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pty: %v", err)
	}
	defer func() { ptmx.Close(); tty.Close() }()

	if _, ok := New(tty).(cancelreader.File); !ok {
		t.Fatal("a terminal input is not a cancelreader.File, so a read parked on it cannot be cancelled")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { r.Close(); w.Close() }()

	if _, ok := New(r).(cancelreader.File); ok {
		t.Fatal("a non-terminal input claims to be a cancelreader.File: cancelreader would register a " +
			"descriptor with epoll that epoll may refuse, and the program then cannot start")
	}
}

//go:build !windows

package termkeys

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
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

// TestPasteArrivesWholeThroughTheCancelReader pins why reads are bounded
// to the caller's buffer, which is what makes the cancellation of the
// previous test usable at all.
//
// bubbletea's cancelable reader waits on the descriptor BEFORE every Read
// (cancelreader_linux.go), so bytes translated into a buffer of ours that
// the caller did not ask for have nothing to wake their delivery: the wait
// is already over, and its next wake-up is the next keystroke. A paste
// longer than the buffer would then arrive in pieces, one keystroke per
// piece. Reading no more than the caller can take leaves the remainder in
// the terminal, where the wait does see it.
func TestPasteArrivesWholeThroughTheCancelReader(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pty: %v", err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })
	// Raw mode: in the default cooked mode a read stops at a line boundary,
	// and this paste has no newline in it.
	if _, err := term.MakeRaw(tty.Fd()); err != nil {
		t.Fatalf("raw mode: %v", err)
	}

	cr, err := cancelreader.NewReader(New(tty))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	t.Cleanup(func() { cr.Close() })

	// Longer than bubbletea's 256-byte input buffer, and nothing is written
	// after it: every byte must come from what is already queued.
	paste := bytes.Repeat([]byte("x"), 300)
	if _, err := ptmx.Write(paste); err != nil {
		t.Fatalf("write the paste: %v", err)
	}

	arrived := make(chan []byte, 1)
	go func() {
		var out []byte
		buf := make([]byte, 256)
		for len(out) < len(paste) {
			n, err := cr.Read(buf)
			if err != nil {
				break
			}
			out = append(out, buf[:n]...)
		}
		arrived <- out
	}()

	select {
	case out := <-arrived:
		if len(out) != len(paste) {
			t.Fatalf("the paste arrived as %d of %d bytes", len(out), len(paste))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the paste did not arrive whole: bytes past the caller's buffer are waiting " +
			"behind the cancel reader's wait for the next keystroke")
	}
}

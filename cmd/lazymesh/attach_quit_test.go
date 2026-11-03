//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// TestAttachedTUIQuitsUnderAPty pins the quit paths of `lazymesh attach`
// (macula-io/macula-lazymesh#49). It runs the real binary in a real
// pseudo-terminal and presses one key per case, because the failure this
// replaces was invisible to every unit test: the Model's key handling is
// correct, and it was the wiring between a terminal's bytes and that
// handler that nobody had exercised.
//
// The terminal here answers what a terminal answers. That is not a
// cosmetic detail: the TUI's startup asks the terminal for its background
// colour (OSC 11) and for a cursor-position report (CSI 6n), and while
// those go unanswered a read is in flight on the same stdin the key
// arrives on — a mute pty swallows the keypress and the run looks like a
// TUI that will not quit. tmux and kitty answer instantly; this harness
// answers the same two queries in the same way, so what is measured is
// the TUI, not the fake terminal's silence.
//
// The three cases were chosen from the live run on 2026-11-03 in a tmux
// pane (a real terminal, not a piped pty): `q` and ctrl+c quit, and esc
// does not — correctly, since with nothing open esc has nothing to close
// and #49 keeps key routing out of scope. Since #62 the quit key is
// `ctrl+x q` (a bare `q` types a q), so that is what this case sends.
func TestAttachedTUIQuitsUnderAPty(t *testing.T) {
	for _, tc := range []struct {
		name string
		// key is the exact byte run the terminal sends for this key.
		key []byte
		// why explains what this case is protecting.
		why string
	}{
		{
			// 0x18 is ctrl+x, then the leader's q: every command moved
			// behind the leader in #62, and a bare q is now just a letter.
			name: "ctrl-x-q",
			key:  []byte{0x18, 'q'},
			why:  "the leader's quit key, and the one a user reaches for first",
		},
		{
			name: "ctrl-c",
			key:  []byte{0x03},
			why:  "the universal escape hatch: it must work in every mode",
		},
		{
			// With the kitty keyboard protocol pushed (termkeys.Enable, which
			// the TUI writes on start), kitty reports ctrl+c as CSI 99;5u
			// rather than as 0x03. That translation is the only thing between
			// a kitty user and an unquittable TUI, and no terminal that is not
			// kitty will ever produce these bytes for us.
			name: "ctrl-c-as-kitty-reports-it",
			key:  []byte("\x1b[99;5u"),
			why:  "under the kitty keyboard protocol ctrl+c arrives as CSI 99;5u and must still quit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketPath := startFakeSession(t)
			term := startAttached(t, socketPath)

			if _, err := term.write(tc.key); err != nil {
				t.Fatalf("send %q: %v", tc.key, err)
			}

			select {
			case err := <-term.exited:
				if err != nil {
					t.Fatalf("%s quit the TUI but the process failed: %v\nscreen:\n%s", tc.name, err, term.screen())
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("%s did not quit the attached TUI within 15s (%s)\nscreen:\n%s", tc.name, tc.why, term.screen())
			}

			// Detach, never shutdown: the session must be untouched and still
			// attachable. #40's whole promise is that closing a TUI is not a
			// way to stop the work.
			ctrl, err := frontend.Dial(socketPath)
			if err != nil {
				t.Fatalf("the session did not survive the TUI quitting: %v", err)
			}
			ctrl.Close()
		})
	}
}

// TestAttachedTUIExitsWhenSignalled pins the other half of #49: an attach
// whose stdin is NOT a terminal. There is no keyboard in that case, so
// ctrl+c never arrives as a keystroke -- the kernel turns it into SIGINT --
// and `timeout 5 lazymesh attach` (the observation that opened the issue)
// is exactly this run. A TUI that cannot be interrupted is a trap: the
// session is fine, but the terminal it was started from is gone until
// someone SIGKILLs it.
//
// It was a real hang, not a slow exit. bubbletea installs its own signal
// handler unless told not to, and that handler sends InterruptMsg/QuitMsg
// on an unbuffered channel with no ctx case. When attach's own ctx path
// won the race to end the event loop, bubbletea's handler was left blocked
// forever and Run's shutdown waited for it. Which sender won was a race, so
// the hang came and went: measured live, 3 of 8 SIGINTs and 1 of 8 SIGTERMs
// needed SIGKILL. That is why this case runs a batch instead of once -- a
// single attempt would have missed it more often than it caught it.
func TestAttachedTUIExitsWhenSignalled(t *testing.T) {
	const perSignal = 15
	for _, sig := range []struct {
		name string
		kind syscall.Signal
	}{
		{"sigint", syscall.SIGINT},
		{"sigterm", syscall.SIGTERM},
	} {
		t.Run(sig.name, func(t *testing.T) {
			socketPath := startFakeSession(t)
			bin := buildLazymesh(t)
			hung := 0
			for i := 0; i < perSignal; i++ {
				if !exitsOnSignal(t, bin, socketPath, sig.kind) {
					hung++
				}
			}
			if hung > 0 {
				t.Fatalf("%d of %d attaches ignored %s and had to be killed", hung, perSignal, sig.name)
			}
		})
	}
}

// exitsOnSignal starts `lazymesh attach` with no terminal at all, signals
// it once it is running, and reports whether it exited on its own.
func exitsOnSignal(t *testing.T, bin, socketPath string, sig syscall.Signal) bool {
	t.Helper()
	// Stdin left nil: os/exec gives the child /dev/null, which is the
	// non-terminal case this test is about. Output is discarded so nothing
	// ever tries to be a terminal and no query goes unanswered.
	cmd := exec.Command(bin, "attach", socketPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start attach: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Let the program reach its event loop; the signal must land on a
	// running TUI, not on one still starting up.
	time.Sleep(250 * time.Millisecond)
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal attach: %v", err)
	}

	select {
	case <-done:
		return true
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-done
		return false
	}
}

// fakeTerminal is `lazymesh attach` running under a pty, plus the
// screen-reading goroutine and the answers a terminal owes it.
type fakeTerminal struct {
	master *os.File
	exited chan error

	mu  sync.Mutex
	out bytes.Buffer
	// answered tracks how many of each startup query have been answered, so
	// an answer is written once per query (a second OSC reply to no question
	// would be read by the TUI as keys).
	oscAnswered, dsrAnswered int
}

// The replies a terminal gives: an OSC 11 background-colour report and a
// CSI 6n cursor-position report.
const (
	oscBackgroundReport = "\x1b]11;rgb:0000/0000/0000\x1b\\"
	dsrCursorReport     = "\x1b[1;1R"

	// painted is rendered only once the TUI is up: the strip attached mode
	// shows in place of the mesh counters.
	painted = "mesh not shown here"
)

// startAttached runs the CLI against socketPath under a fresh pty and
// returns once the TUI has painted, so a key is never sent into a
// terminal that is still blank.
func startAttached(t *testing.T, socketPath string) *fakeTerminal {
	t.Helper()
	bin := buildLazymesh(t)

	cmd := exec.Command(bin, "attach", socketPath)
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("start attach under a pty: %v", err)
	}
	term := &fakeTerminal{master: master, exited: make(chan error, 1)}
	t.Cleanup(func() { master.Close() })

	go func() { term.exited <- cmd.Wait() }()

	// Drain the screen continuously: an undrained pty fills and blocks the
	// TUI's writes, which would make every case fail for a reason that has
	// nothing to do with quitting.
	go term.drain()
	term.waitFor(t, painted, 30*time.Second)

	// The paint is followed by the terminal's own replies arriving; give the
	// query's reader the moment it needs to finish rather than racing it.
	time.Sleep(200 * time.Millisecond)
	return term
}

func (f *fakeTerminal) drain() {
	buf := make([]byte, 4096)
	for {
		n, err := f.master.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.out.Write(buf[:n])
			// Answer each startup query once, the way a terminal does:
			// otherwise the TUI's own read sits unanswered on the same stdin
			// the next keypress arrives on, and eats it.
			for f.oscAnswered < bytes.Count(f.out.Bytes(), []byte("\x1b]11;?")) {
				f.master.Write([]byte(oscBackgroundReport))
				f.oscAnswered++
			}
			for f.dsrAnswered < bytes.Count(f.out.Bytes(), []byte("\x1b[6n")) {
				f.master.Write([]byte(dsrCursorReport))
				f.dsrAnswered++
			}
			f.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// write sends bytes to the terminal, serialized against the query
// answers so a key is never split by one.
func (f *fakeTerminal) write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.master.Write(b)
}

// screen returns everything the TUI has drawn so far.
func (f *fakeTerminal) screen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out.String()
}

// waitFor blocks until want appears on the screen, and fails the test with
// what it was waiting for and what it saw if it never does. A wait that
// times out silently would let the case go on to fail for a reason that is
// not the one being measured -- a key sent into a blank terminal, say --
// and the report would name the wrong thing.
func (f *fakeTerminal) waitFor(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(f.screen(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waited %s for %q to appear on the attached TUI's screen and it never did\nscreen:\n%s",
		timeout, want, f.screen())
}

// startFakeSession serves one control socket the way a running session
// does, minus the agent: the attach client sees a genuine session that
// answers, so what this test measures is the TUI's own exit and nothing
// an absent peer could explain.
func startFakeSession(t *testing.T) string {
	t.Helper()
	return startFakeSessionAs(t, frontend.Options{})
}

// startFakeSessionAs is startFakeSession with options of the caller's own --
// the leader a session was configured with, for the attach cases (#63) -- and
// the identity a session always has.
func startFakeSessionAs(t *testing.T, opts frontend.Options) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "ctrl.sock")
	opts.Path = socketPath
	if opts.Input == nil {
		opts.Input = make(chan string, 4)
	}
	if opts.Events == nil {
		opts.Events = make(chan agent.Event, 4)
	}
	opts.SessionID = "attach-quit-test"
	if opts.Model == "" {
		opts.Model = "testmodel/1"
	}
	srv, err := frontend.Start(opts)
	if err != nil {
		t.Fatalf("start fake session: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return socketPath
}

// TestMain removes the binary this package's tests build, whatever the
// outcome. The build is shared through buildOnce, so it cannot be removed
// by the test that happened to trigger it -- a later test would find it
// gone -- and leaving it behind means every `go test` run drops another
// temporary directory beside the user's other temp files.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildPath != "" {
		os.RemoveAll(filepath.Dir(buildPath))
	}
	os.Exit(code)
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  string
)

// buildLazymesh compiles the CLI once per test run. The binary is the
// subject: what is pinned here is the behaviour of the process a user
// actually starts, which no in-process test can stand in for.
func buildLazymesh(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lazymesh-attach-quit-")
		if err != nil {
			buildErr = err.Error()
			return
		}
		out := filepath.Join(dir, "lazymesh")
		cmd := exec.Command("go", "build", "-o", out, ".")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = err.Error() + "\n" + stderr.String()
			return
		}
		buildPath = out
	})
	if buildErr != "" {
		t.Fatalf("build lazymesh: %s", buildErr)
	}
	return buildPath
}

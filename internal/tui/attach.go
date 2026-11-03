package tui

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/frontend"
	"github.com/macula-io/macula-lazymesh/internal/termkeys"
)

// RunAttached runs the ordinary single-session TUI against a session
// reached over its control socket (macula-io/macula-lazymesh#40).
//
// It is deliberately the same Model the in-process TUI uses, driven by
// the same three channels: the socket replaces the local bus, not the
// interface. That is what makes "a kitty tab or a tmux pane" enough —
// there is no second TUI to keep in step with the first.
//
// Closing it detaches. Nothing here sends a shutdown control message; the
// session keeps running and a later attach picks up its event stream
// again.
func RunAttached(ctx context.Context, ctrl *frontend.Controller, opts Options) error {
	bridge := newAttachBridge(ctrl)
	opts.AgentEvents = ctrl.Events()
	opts.UserInputCh = bridge.input
	opts.InterruptCh = bridge.interrupt
	opts.ApprovalCh = bridge.answer
	opts.ApprovalResolvedCh = bridge.resolved
	opts.AgentModel = ctrl.Session().Model

	m := New(nil, opts)
	program := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithInput(termkeys.New(os.Stdin)),
	)
	go func() {
		<-ctx.Done()
		program.Quit()
	}()
	_, err := program.Run()

	// Detach, never shutdown: the session outlives every attached TUI.
	ctrl.Close()
	return err
}

// attachBridge couples the TUI's four channels to one control-socket
// client. It is a separate type so the wiring is testable without a
// terminal: RunAttached cannot run a tea program under `go test`, but the
// part that can be wrong — which channel goes where, and whether a lost
// approval answer is swallowed or surfaced — can.
type attachBridge struct {
	input     chan string
	interrupt chan struct{}
	answer    chan ApprovalAnswer
	resolved  chan frontend.ApprovalResolved
}

// newAttachBridge starts the four goroutines that carry the TUI's
// decisions to the session. Each is its own goroutine so a blocked write
// to the socket can never stall the tea loop.
func newAttachBridge(ctrl *frontend.Controller) *attachBridge {
	b := &attachBridge{
		input:     make(chan string, 8),
		interrupt: make(chan struct{}, 4),
		answer:    make(chan ApprovalAnswer, 8),
		resolved:  make(chan frontend.ApprovalResolved, 16),
	}
	go func() {
		for text := range b.input {
			if err := ctrl.Say(text); err != nil {
				fmt.Fprintln(os.Stderr, "lazymesh attach:", err)
			}
		}
	}()
	go func() {
		for range b.interrupt {
			if err := ctrl.Interrupt(); err != nil {
				fmt.Fprintln(os.Stderr, "lazymesh attach:", err)
			}
		}
	}()
	go func() {
		for answer := range b.answer {
			// A refusal means another frontend answered first. It is
			// reported into the TUI's own stream rather than to stderr (the
			// alt screen would swallow it) and rather than dropped: the
			// operator pressed a key and is owed the reason it did not
			// count. The server's broadcast tells them who won; this tells
			// them their own answer lost, which the broadcast alone does
			// not distinguish from one they never sent.
			if err := ctrl.Approve(answer.ID, answer.Allow); err != nil {
				select {
				case b.resolved <- frontend.ApprovalResolved{ID: answer.ID, Note: err.Error()}:
				default:
				}
			}
		}
	}()
	go func() {
		defer close(b.resolved)
		for r := range ctrl.Approvals() {
			b.resolved <- r
		}
	}()
	return b
}

// Package logging is lazymesh's log stack: one slog handler writing
// structured, levelled, component-tagged records to agent.log.
//
// agent.log is the single place logs live. There is deliberately no
// in-memory copy: the `l` key opens the file in $EDITOR, which already
// gives search, scrolling, jumping and copy, so a second copy in memory
// would only be a second version of the truth that can drift from the
// file. What this package adds is that the file is worth opening.
//
// Two constraints shaped this and neither is negotiable:
//
// Nothing here may write to stdout or stderr. Bubbletea owns the
// terminal once the alt screen is up, and an interleaved log line
// corrupts the display. cmd/lazymesh/main.go already carried that rule
// for its own file logger before this package existed; this package
// keeps it. The only writer is the io.Writer main.go hands to New,
// which is the append-mode ~/.config/lazymesh/agent.log file.
//
// Every existing SetLogger(*log.Logger) call site keeps working
// unchanged. mcpclient.Client, meshservices.Source and
// ringwaiter.Manager all take a *log.Logger and none of their
// signatures move: StdFor bridges a component-tagged slog handler back
// into a *log.Logger via slog.NewLogLogger, so those Printf calls land
// in the file exactly like a native slog record, and get a correct
// component tag without their packages knowing slog exists. Nothing
// that logs today stops logging.
package logging

import (
	"io"
	"log"
	"log/slog"
)

// componentKey is the attr name For and StdFor stamp on every record, so
// every line in agent.log says which subsystem produced it.
const componentKey = "component"

// componentDefault tags records that arrive without a component of their
// own, which in practice means the package-level slog functions routed
// here by SetDefault.
const componentDefault = "lazymesh"

// Stack is the assembled logging stack main.go wires up once at start.
type Stack struct {
	handler slog.Handler
}

// New builds the stack over fileW, which is the append-mode agent.log.
// fileW must never be os.Stdout or os.Stderr while the TUI is running.
func New(fileW io.Writer, level slog.Level) *Stack {
	return &Stack{
		handler: slog.NewTextHandler(fileW, &slog.HandlerOptions{Level: level}),
	}
}

// For returns a slog.Logger tagged with component, for new code.
func (s *Stack) For(component string) *slog.Logger {
	return slog.New(s.handler).With(slog.String(componentKey, component))
}

// StdFor returns a *log.Logger tagged with component, for the existing
// SetLogger(*log.Logger) call sites in mcpclient, meshservices and
// ringwaiter. Their Printf output is captured exactly like a native
// slog record, at Info, without those packages changing at all.
func (s *Stack) StdFor(component string) *log.Logger {
	h := s.handler.WithAttrs([]slog.Attr{slog.String(componentKey, component)})
	return slog.NewLogLogger(h, slog.LevelInfo)
}

// SetDefault points slog's package-level functions (slog.Info,
// slog.Error, ...) at this stack.
//
// This is a safety net, not an invitation. slog.Default()'s own handler
// writes to STDERR, which corrupts the alt screen the moment anything
// calls a package-level slog function. Prefer For(component) and pass
// the logger explicitly; this exists so that a stray slog.Info in some
// future call site lands in agent.log instead of on top of the TUI.
func (s *Stack) SetDefault() {
	slog.SetDefault(s.For(componentDefault))
}

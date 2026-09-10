// Package logging is lazymesh's log capture: one slog stack that fans
// every record out to the on-disk agent log AND to a bounded in-memory
// ring the TUI's log overlay reads back.
//
// Two constraints shaped this and neither is negotiable:
//
// Nothing here may write to stdout or stderr. Bubbletea owns the
// terminal once the alt screen is up, and an interleaved log line
// corrupts the display. cmd/lazymesh/main.go already carried that rule
// for its own file logger before this package existed; this package
// keeps it. The only writer is whatever io.Writer main.go hands to New,
// which is the append-mode ~/.config/lazymesh/agent.log file.
//
// Every existing SetLogger(*log.Logger) call site keeps working
// unchanged. mcpclient.Client, meshservices.Source and
// ringwaiter.Manager all take a *log.Logger and none of their
// signatures move: StdFor bridges a component-tagged slog handler back
// into a *log.Logger via slog.NewLogLogger, so those Printf calls land
// in the file and the ring exactly like a native slog record, and get a
// correct Component without their packages knowing slog exists.
package logging

import (
	"context"
	"io"
	"log"
	"log/slog"
	"sync"
	"time"
)

// DefaultCapacity is how many entries the ring holds before the oldest
// is dropped. The overlay shows a screenful; this is generous enough to
// scroll back through a whole session's incidents without letting a
// chatty agent loop grow memory without bound.
const DefaultCapacity = 1000

// componentKey is the attr name StdFor/For stamp on every record and
// bufferHandler reads back out into Entry.Component.
const componentKey = "component"

// componentUnset is what Entry.Component holds if a record somehow
// arrives with no component attr. Entry.Component is documented to the
// TUI as always populated, so this exists to keep that promise true
// rather than to be used: every logger this package hands out is
// already component-tagged.
const componentUnset = "lazymesh"

// Entry is one captured record, in the shape the TUI renders it.
//
// Time, Level, Component and Message are populated on EVERY entry --
// the overlay draws them as columns and a blank column is worse than a
// missing one. Attrs is frequently empty and is not worth a column.
type Entry struct {
	Time      time.Time
	Level     slog.Level
	Component string
	Message   string
	Attrs     map[string]string
}

// Buffer is a bounded ring of the most recent entries, safe for
// concurrent use: the agent loop writes to it while the TUI reads.
type Buffer struct {
	mu   sync.Mutex
	buf  []Entry
	next int // index the next append writes to
	n    int // entries currently held, <= len(buf)
}

// NewBuffer returns a ring holding at most capacity entries. A capacity
// below 1 falls back to DefaultCapacity rather than producing a buffer
// that silently discards everything.
func NewBuffer(capacity int) *Buffer {
	if capacity < 1 {
		capacity = DefaultCapacity
	}
	return &Buffer{buf: make([]Entry, capacity)}
}

// Append adds e, dropping the oldest entry once the ring is full.
func (b *Buffer) Append(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf[b.next] = e
	b.next = (b.next + 1) % len(b.buf)
	if b.n < len(b.buf) {
		b.n++
	}
}

// RecentEntries returns up to n entries, NEWEST FIRST. It never blocks
// on I/O and never fails: an empty slice means nothing has been logged
// yet, which is the overlay's whole error path. n <= 0 returns every
// entry held. Safe to call from any goroutine.
func (b *Buffer) RecentEntries(n int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > b.n {
		n = b.n
	}
	out := make([]Entry, 0, n)
	// Walk backwards from the most recently written slot.
	for i := 0; i < n; i++ {
		idx := (b.next - 1 - i + len(b.buf)*2) % len(b.buf)
		out = append(out, b.buf[idx])
	}
	return out
}

// Len is how many entries the ring currently holds.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

// bufferHandler is the slog.Handler half that turns records into Entry
// values on a Buffer. It carries its own accumulated attrs so a logger
// built with With(...) still reports them.
type bufferHandler struct {
	buf   *Buffer
	level slog.Level
	attrs []slog.Attr
}

func (h *bufferHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &bufferHandler{buf: h.buf, level: h.level, attrs: merged}
}

// WithGroup returns the handler unchanged: nothing in lazymesh groups
// attrs, and silently flattening a group into the ring would misreport
// key names. If grouping is ever wanted, implement it rather than
// letting this quietly drop the group name.
func (h *bufferHandler) WithGroup(string) slog.Handler { return h }

func (h *bufferHandler) Handle(_ context.Context, r slog.Record) error {
	component := componentUnset
	attrs := map[string]string{}
	collect := func(a slog.Attr) {
		if a.Key == componentKey {
			component = a.Value.String()
			return
		}
		attrs[a.Key] = a.Value.String()
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		collect(a)
		return true
	})
	h.buf.Append(Entry{
		Time:      r.Time,
		Level:     r.Level,
		Component: component,
		Message:   r.Message,
		Attrs:     attrs,
	})
	return nil
}

// multiHandler fans one record out to every wrapped handler, so the
// file and the ring see identical records rather than two independently
// formatted views that can drift.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: next}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: next}
}

// Handle delivers to every handler even if an earlier one errors --
// a failing file write must not cost the overlay its copy of the entry.
func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Stack is the assembled logging stack main.go wires up once at start.
type Stack struct {
	// Buffer is what the TUI's log overlay reads via RecentEntries.
	Buffer *Buffer

	handler slog.Handler
}

// New builds the stack: records fan out to fileW (the append-mode
// agent.log) and to a bounded in-memory ring. fileW must never be
// os.Stdout or os.Stderr while the TUI is running.
func New(fileW io.Writer, level slog.Level, capacity int) *Stack {
	buf := NewBuffer(capacity)
	return &Stack{
		Buffer: buf,
		handler: &multiHandler{handlers: []slog.Handler{
			slog.NewTextHandler(fileW, &slog.HandlerOptions{Level: level}),
			&bufferHandler{buf: buf, level: level},
		}},
	}
}

// For returns a slog.Logger tagged with component, for new code.
func (s *Stack) For(component string) *slog.Logger {
	return slog.New(s.handler).With(slog.String(componentKey, component))
}

// SetDefault points slog's package-level functions (slog.Info,
// slog.Error, ...) at this stack.
//
// This is a safety net, not an invitation. slog.Default()'s own handler
// writes to STDERR, which corrupts the alt screen the moment anything
// calls a package-level slog function. Prefer For(component) and pass
// the logger explicitly; this exists so that a stray slog.Info in some
// future dependency or call site lands in the file and the ring instead
// of on top of the TUI.
func (s *Stack) SetDefault() {
	slog.SetDefault(s.For(componentUnset))
}

// StdFor returns a *log.Logger tagged with component, for the existing
// SetLogger(*log.Logger) call sites in mcpclient, meshservices and
// ringwaiter. Their Printf output is captured exactly like a native
// slog record, at Info, without those packages changing at all.
func (s *Stack) StdFor(component string) *log.Logger {
	h := s.handler.WithAttrs([]slog.Attr{slog.String(componentKey, component)})
	return slog.NewLogLogger(h, slog.LevelInfo)
}

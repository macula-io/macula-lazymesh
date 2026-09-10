package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// errWriter fails every write, standing in for a full disk or a closed
// agent.log.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRecentEntriesIsNewestFirst(t *testing.T) {
	b := NewBuffer(10)
	for _, m := range []string{"first", "second", "third"} {
		b.Append(Entry{Message: m})
	}
	got := b.RecentEntries(3)
	want := []string{"third", "second", "first"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Message != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i].Message, want[i])
		}
	}
}

func TestRingDropsOldestOnceFull(t *testing.T) {
	b := NewBuffer(3)
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		b.Append(Entry{Message: m})
	}
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (the ring must stay bounded)", b.Len())
	}
	got := b.RecentEntries(0)
	want := []string{"e", "d", "c"}
	for i := range want {
		if got[i].Message != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i].Message, want[i])
		}
	}
	for _, e := range got {
		if e.Message == "a" || e.Message == "b" {
			t.Errorf("%q should have been dropped", e.Message)
		}
	}
}

func TestRecentEntriesCapsAtN(t *testing.T) {
	b := NewBuffer(10)
	for i := 0; i < 8; i++ {
		b.Append(Entry{Message: "x"})
	}
	if got := len(b.RecentEntries(3)); got != 3 {
		t.Errorf("RecentEntries(3) returned %d entries, want 3", got)
	}
	// n larger than what is held returns only what is held, not padding.
	if got := len(b.RecentEntries(99)); got != 8 {
		t.Errorf("RecentEntries(99) returned %d entries, want 8", got)
	}
	// n <= 0 means everything held.
	if got := len(b.RecentEntries(0)); got != 8 {
		t.Errorf("RecentEntries(0) returned %d entries, want 8", got)
	}
}

// The overlay's whole error path is an empty slice, so this must not
// panic and must not return nil-with-length.
func TestEmptyBufferReturnsEmptyNotPanic(t *testing.T) {
	b := NewBuffer(5)
	got := b.RecentEntries(10)
	if len(got) != 0 {
		t.Errorf("fresh buffer returned %d entries, want 0", len(got))
	}
	if b.Len() != 0 {
		t.Errorf("fresh buffer Len = %d, want 0", b.Len())
	}
}

// Component is promised to the TUI as populated on EVERY entry -- a
// blank column is worse than no column.
func TestComponentAlwaysPopulated(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelDebug, 10)

	s.For("mcp").Info("via slog")
	s.StdFor("realm").Printf("via the stdlib bridge")
	s.SetDefault()
	slog.Info("via the package-level default")

	entries := s.Buffer.RecentEntries(0)
	if len(entries) != 3 {
		t.Fatalf("captured %d entries, want 3", len(entries))
	}
	for _, e := range entries {
		if strings.TrimSpace(e.Component) == "" {
			t.Errorf("entry %q has a blank Component", e.Message)
		}
	}

	byMessage := map[string]string{}
	for _, e := range entries {
		byMessage[e.Message] = e.Component
	}
	if got := byMessage["via slog"]; got != "mcp" {
		t.Errorf("slog entry Component = %q, want %q", got, "mcp")
	}
	if got := byMessage["via the stdlib bridge"]; got != "realm" {
		t.Errorf("bridged entry Component = %q, want %q", got, "realm")
	}
}

// The load-bearing promise: the three existing SetLogger(*log.Logger)
// call sites keep their signature and their output now reaches BOTH the
// file and the ring. If this regresses, an existing signal goes silent.
func TestStdLoggerBridgeReachesBothFileAndBuffer(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelInfo, 10)

	s.StdFor("mcp").Printf("respawn failed: %v", errors.New("boom"))

	if s.Buffer.Len() != 1 {
		t.Fatalf("buffer holds %d entries, want 1 -- the bridge did not reach the ring", s.Buffer.Len())
	}
	got := s.Buffer.RecentEntries(1)[0]
	if !strings.Contains(got.Message, "respawn failed: boom") {
		t.Errorf("buffered message = %q, want it to contain the Printf output", got.Message)
	}
	if !strings.Contains(file.String(), "respawn failed: boom") {
		t.Errorf("file got %q, want it to contain the Printf output", file.String())
	}
	if !strings.Contains(file.String(), "component=mcp") {
		t.Errorf("file entry is missing the component tag: %q", file.String())
	}
}

// A failing file write must not cost the overlay its copy of the entry.
func TestBufferStillCapturesWhenFileWriteFails(t *testing.T) {
	s := New(errWriter{}, slog.LevelInfo, 10)

	s.For("agent").Error("the file is broken but this must still be visible")

	if s.Buffer.Len() != 1 {
		t.Fatalf("buffer holds %d entries, want 1 -- a failing file write swallowed the entry", s.Buffer.Len())
	}
}

func TestLevelFilteringApplies(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelWarn, 10)

	l := s.For("agent")
	l.Debug("dropped")
	l.Info("dropped")
	l.Warn("kept")
	l.Error("kept")

	if s.Buffer.Len() != 2 {
		t.Fatalf("buffer holds %d entries, want 2 (Warn and Error only)", s.Buffer.Len())
	}
	for _, e := range s.Buffer.RecentEntries(0) {
		if e.Level < slog.LevelWarn {
			t.Errorf("entry %q at level %v should have been filtered out", e.Message, e.Level)
		}
	}
}

// A capacity below 1 must not produce a buffer that silently discards
// everything written to it.
func TestNonPositiveCapacityFallsBackToDefault(t *testing.T) {
	b := NewBuffer(0)
	b.Append(Entry{Message: "kept"})
	if b.Len() != 1 {
		t.Fatalf("Len = %d, want 1 -- a zero capacity must not discard writes", b.Len())
	}
}

// The agent loop writes while the TUI reads. Run with -race.
func TestConcurrentAppendAndRead(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelInfo, 100)
	l := s.For("agent")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.Info("write")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.Buffer.RecentEntries(20)
				_ = s.Buffer.Len()
			}
		}()
	}
	wg.Wait()

	if s.Buffer.Len() != 100 {
		t.Errorf("Len = %d, want the ring to be full at 100", s.Buffer.Len())
	}
}

// Attrs beyond component are carried, and component itself is lifted
// out into its own field rather than left in the map.
func TestAttrsCarriedAndComponentLifted(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelInfo, 10)

	s.For("mesh").Info("call failed", slog.String("procedure", "mesh_list_realms"))

	got := s.Buffer.RecentEntries(1)[0]
	if got.Component != "mesh" {
		t.Errorf("Component = %q, want %q", got.Component, "mesh")
	}
	if _, present := got.Attrs[componentKey]; present {
		t.Errorf("component should be lifted into its own field, not left in Attrs: %v", got.Attrs)
	}
	if got.Attrs["procedure"] != "mesh_list_realms" {
		t.Errorf("Attrs[procedure] = %q, want %q", got.Attrs["procedure"], "mesh_list_realms")
	}
}

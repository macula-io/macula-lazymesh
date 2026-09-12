package sessionstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/provider"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), Fingerprint(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func testMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "you are a test agent"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello", ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "mesh_say", Arguments: `{"text":"x"}`}}},
		{Role: provider.RoleTool, Content: "ok", ToolCallID: "call_1", Name: "mesh_say"},
	}
}

// TestAppendLoadRoundTrip proves the wire shape survives a full write/
// read cycle: roles, content, tool calls, tool ids, order.
func TestAppendLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("sess1", testMessages()); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.Load("sess1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(testMessages()) {
		t.Fatalf("loaded %d messages, want %d", len(got), len(testMessages()))
	}
	for i, want := range testMessages() {
		if got[i].Role != want.Role || got[i].Content != want.Content || got[i].ToolCallID != want.ToolCallID {
			t.Fatalf("message %d = %+v, want %+v", i, got[i], want)
		}
		if len(got[i].ToolCalls) != len(want.ToolCalls) {
			t.Fatalf("message %d tool calls = %+v", i, got[i].ToolCalls)
		}
		for j, tc := range want.ToolCalls {
			if got[i].ToolCalls[j] != tc {
				t.Fatalf("message %d tool call %d = %+v, want %+v", i, j, got[i].ToolCalls[j], tc)
			}
		}
	}
}

// TestLoadMissingSessionIsEmpty pins the fresh-start contract: an id with
// no log is an empty conversation, never an error.
func TestLoadMissingSessionIsEmpty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Load("never-existed")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty conversation, got %d messages", len(got))
	}
}

// TestAppendIsIncremental proves appends accumulate: two appends read
// back as one ordered log.
func TestAppendIsIncremental(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("sess2", testMessages()[:2]); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := s.Append("sess2", testMessages()[2:]); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	got, err := s.Load("sess2")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(testMessages()) {
		t.Fatalf("loaded %d messages, want %d", len(got), len(testMessages()))
	}
}

// TestLoadSkipsTornTrailingLine pins the crash-resume contract: a
// half-written final line is skipped, everything before it survives.
func TestLoadSkipsTornTrailingLine(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("sess3", testMessages()[:2]); err != nil {
		t.Fatalf("append: %v", err)
	}
	f, err := os.OpenFile(s.Path("sess3"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte(`{"role":"user","content":"torn`)); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	f.Close()

	got, err := s.Load("sess3")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d messages, want 2 (torn line skipped)", len(got))
	}
}

// TestLatestAndResolve pins the reference aliases: latest picks the
// newest log, Resolve turns refs into ids, and an explicit missing id
// errors rather than silently starting fresh.
func TestLatestAndResolve(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("older", testMessages()[:1]); err != nil {
		t.Fatalf("append older: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := s.Append("newer", testMessages()[:1]); err != nil {
		t.Fatalf("append newer: %v", err)
	}

	id, err := s.Latest()
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if id != "newer" {
		t.Fatalf("latest = %q, want %q", id, "newer")
	}

	if got, err := Resolve("latest", s, "default"); err != nil || got != "newer" {
		t.Fatalf("resolve latest = %q, %v", got, err)
	}
	if got, err := Resolve("last", s, "default"); err != nil || got != "newer" {
		t.Fatalf("resolve last = %q, %v", got, err)
	}
	if got, err := Resolve("", s, "default"); err != nil || got != "default" {
		t.Fatalf("resolve empty = %q, %v", got, err)
	}
	if _, err := Resolve("missing", s, "default"); err == nil {
		t.Fatal("expected an error resolving a missing explicit session")
	}
}

// TestStoreIsFingerprintScoped proves two fingerprints never share logs.
func TestStoreIsFingerprintScoped(t *testing.T) {
	base := t.TempDir()
	a, err := New(base, "aaaa")
	if err != nil {
		t.Fatalf("store a: %v", err)
	}
	b, err := New(base, "bbbb")
	if err != nil {
		t.Fatalf("store b: %v", err)
	}
	if filepath.Dir(a.Path("x")) == filepath.Dir(b.Path("x")) {
		t.Fatalf("stores share a directory: %s", filepath.Dir(a.Path("x")))
	}
	if err := a.Append("x", testMessages()[:1]); err != nil {
		t.Fatalf("append a: %v", err)
	}
	got, err := b.Load("x")
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("store b saw store a's session: %d messages", len(got))
	}
}

package sessionstore

import (
	"os"
	"path/filepath"
	"strings"
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
	conv, err := s.Load("sess1")
	got := conv.Messages
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
	conv, err := s.Load("never-existed")
	got := conv.Messages
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
	conv, err := s.Load("sess2")
	got := conv.Messages
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

	conv, err := s.Load("sess3")
	got := conv.Messages
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
	conv, err := b.Load("x")
	got := conv.Messages
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("store b saw store a's session: %d messages", len(got))
	}
}

// #99: a new log opens with its schema version, and a turn that ended is
// marked as ended, so a later load can tell a turn that finished from one
// a crash cut short.
func TestTurns_AnEndedTurnIsNotUnfinished(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("t1", testMessages()); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.EndTurn("t1"); err != nil {
		t.Fatalf("end turn: %v", err)
	}
	raw, err := os.ReadFile(s.Path("t1"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if first := strings.SplitN(string(raw), "\n", 2)[0]; first != `{"schema":2}` {
		t.Fatalf("first line = %s, want the schema version", first)
	}
	conv, err := s.Load("t1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Messages) != len(testMessages()) || len(conv.Unfinished) != 0 {
		t.Fatalf("loaded %d messages, %d unfinished; want %d, 0", len(conv.Messages), len(conv.Unfinished), len(testMessages()))
	}
}

// #99: the messages after the last ended turn are a turn a crash cut
// short.
func TestTurns_MessagesAfterTheLastEndedTurnAreUnfinished(t *testing.T) {
	s := newTestStore(t)
	msgs := testMessages()
	if err := s.Append("t2", msgs[:2]); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.EndTurn("t2"); err != nil {
		t.Fatalf("end turn: %v", err)
	}
	for _, m := range msgs[1:] { // one message at a time, as a turn runs
		if err := s.Append("t2", []provider.Message{m}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	conv, err := s.Load("t2")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Messages) != 5 || len(conv.Unfinished) != 3 {
		t.Fatalf("loaded %d messages, %d unfinished; want 5, 3", len(conv.Messages), len(conv.Unfinished))
	}
	if conv.Unfinished[1].ToolCalls[0].Name != "mesh_say" {
		t.Fatalf("unfinished = %+v", conv.Unfinished)
	}
}

// #99: a log written before the schema version (schema 1) has no turn
// markers, so all of it counts as finished; the first write upgrades it
// to schema 2 in place, its messages intact.
func TestTurns_ASchemaOneLogIsUpgradedOnTheFirstWrite(t *testing.T) {
	s := newTestStore(t)
	old := `{"role":"user","content":"hi"}` + "\n" + `{"role":"assistant","content":"hello"}` + "\n"
	if err := os.WriteFile(s.Path("old"), []byte(old), 0o600); err != nil {
		t.Fatalf("write schema-1 log: %v", err)
	}
	conv, err := s.Load("old")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Messages) != 2 || len(conv.Unfinished) != 0 {
		t.Fatalf("schema-1 log: %d messages, %d unfinished; want 2, 0", len(conv.Messages), len(conv.Unfinished))
	}
	if err := s.Append("old", []provider.Message{{Role: provider.RoleUser, Content: "again"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	conv, err = s.Load("old")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Messages) != 3 || len(conv.Unfinished) != 1 || conv.Unfinished[0].Content != "again" {
		t.Fatalf("upgraded log: %d messages, unfinished %+v; want 3, [again]", len(conv.Messages), conv.Unfinished)
	}
	raw, _ := os.ReadFile(s.Path("old"))
	if !strings.HasPrefix(string(raw), `{"schema":2}`) {
		t.Fatalf("the log was not upgraded: %s", raw)
	}
}

// #99: a log from a newer lazymesh is refused, not misread.
func TestTurns_ANewerSchemaIsRefused(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path("new"), []byte(`{"schema":3}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := s.Load("new"); err == nil {
		t.Fatal("a schema-3 log loaded without complaint")
	}
	if err := s.Append("new", testMessages()[:1]); err == nil {
		t.Fatal("a schema-3 log was appended to")
	}
}

// #99: a crash that tore the last line does not swallow the next
// message: the next append starts on a line of its own.
func TestTurns_AnAppendAfterATornLineIsKept(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append("torn", testMessages()[:1]); err != nil {
		t.Fatalf("append: %v", err)
	}
	f, err := os.OpenFile(s.Path("torn"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Write([]byte(`{"role":"user","content":"torn`))
	f.Close()
	if err := s.Append("torn", []provider.Message{{Role: provider.RoleUser, Content: "after"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	conv, err := s.Load("torn")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := len(conv.Messages); n != 2 || conv.Messages[1].Content != "after" {
		t.Fatalf("messages = %+v, want the first and \"after\"", conv.Messages)
	}
}

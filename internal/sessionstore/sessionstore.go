// Package sessionstore persists lazymesh conversations as JSONL append
// logs (D2): one JSON object per line, one line per message, written with
// O_APPEND so concurrent appenders never interleave and a crash can only
// lose the final write, never corrupt an earlier one. Loading tolerates a
// torn trailing line — the last thing a crash was writing — and skips it
// rather than refusing to resume.
//
// Schema 2 (#99): the first line is {"schema":2}, each message is written
// as it happens, and {"turn":"ended"} follows the last message of every
// turn that ended, failed or not. The messages after the last such marker
// are a turn a crash cut short. A schema-1 log (no header, no markers,
// written whole after each turn) loads as all finished and is upgraded in
// place by its first write.
//
// Layout, per the D2 decision:
//
//	$XDG_DATA_HOME (or ~/.local/share)/lazymesh/sessions/
//	    <workspace-fingerprint>/<session-id>.jsonl
//
// The fingerprint keys conversations to the directory lazymesh was
// started from (claw-code's own shape); "latest" and "last" resolve to
// the most recently modified file in the fingerprint directory.
package sessionstore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// Store is one fingerprint directory of session logs.
type Store struct {
	Dir string
}

// messageLine is one log line's wire shape — provider.Message plus the
// tool calls flattened the way an OpenAI-compatible backend would carry
// them.
type messageLine struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []toolCallLine `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Failed     bool           `json:"failed,omitempty"`
}

// schemaVersion is the log format this lazymesh writes and reads; a log
// with a higher one comes from a newer lazymesh and is refused.
const schemaVersion = 2

var (
	schemaHeader = []byte(`{"schema":2}`)
	turnEnded    = []byte(`{"turn":"ended"}`)
)

// lineKind tells the three kinds of log line apart: the schema header, a
// turn marker, and a message (which has a role). Schema is nil when the
// line has no schema field: a schema-1 log starts with a message.
type lineKind struct {
	Schema *int   `json:"schema"`
	Turn   string `json:"turn"`
	Role   string `json:"role"`
}

// Conversation is a loaded log: every message in order, and the trailing
// ones that belong to a turn that never ended (a crash cut it short).
// Unfinished is a suffix of Messages, empty when the last turn ended.
type Conversation struct {
	Messages   []provider.Message
	Unfinished []provider.Message
}

type toolCallLine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// DefaultDataDir resolves the sessions root: $XDG_DATA_HOME first, then
// ~/.local/share — the same convention every lazymesh data file follows.
func DefaultDataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("sessionstore: resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "lazymesh", "sessions"), nil
}

// Fingerprint keys one workspace's sessions: the first 16 hex chars of
// the sha256 of the working directory's absolute path. Same shape as
// claw-code's workspace fingerprint — conversations belong to where the
// operator was when they happened.
func Fingerprint(wd string) string {
	sum := sha256.Sum256([]byte(wd))
	return hex.EncodeToString(sum[:8])
}

// New returns a store rooted at dir/<fingerprint>, creating it if needed.
func New(dir, fingerprint string) (*Store, error) {
	d := filepath.Join(dir, fingerprint)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, fmt.Errorf("sessionstore: create %s: %w", d, err)
	}
	return &Store{Dir: d}, nil
}

// Path is the log file for one session id.
func (s *Store) Path(sessionID string) string {
	return filepath.Join(s.Dir, sessionID+".jsonl")
}

// Append writes msgs, one JSON line each, with O_APPEND: safe against
// interleaving across writers, and a crash mid-write loses at most the
// final line. No fsync — the durability bar here is "a crash resumes
// from the last completed state change", not "every byte survives power
// loss", and the file is recreated-trivial data anyway.
func (s *Store) Append(sessionID string, msgs []provider.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	lines := make([][]byte, 0, len(msgs))
	for _, m := range msgs {
		line, err := json.Marshal(toLine(m))
		if err != nil {
			return fmt.Errorf("sessionstore: marshal message: %w", err)
		}
		lines = append(lines, line)
	}
	return s.write(sessionID, lines)
}

// EndTurn marks the turn whose messages were appended last as ended:
// whatever follows belongs to the next turn. last, if any, are the turn's
// final messages, written with the marker in one write, so a crash cannot
// leave them in the log without it.
func (s *Store) EndTurn(sessionID string, last ...provider.Message) error {
	lines := make([][]byte, 0, len(last)+1)
	for _, m := range last {
		line, err := json.Marshal(toLine(m))
		if err != nil {
			return fmt.Errorf("sessionstore: marshal message: %w", err)
		}
		lines = append(lines, line)
	}
	return s.write(sessionID, append(lines, turnEnded))
}

// write appends lines to a schema-2 log, starting one (or upgrading a
// schema-1 one) first, and on a line of their own after a torn last line.
func (s *Store) write(sessionID string, lines [][]byte) error {
	path := s.Path(sessionID)
	if err := s.prepare(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sessionstore: open %s: %w", path, err)
	}
	defer f.Close()
	var buf []byte
	for _, line := range lines {
		buf = append(append(buf, line...), '\n')
	}
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("sessionstore: append to %s: %w", path, err)
	}
	return nil
}

// prepare makes path a schema-2 log ready for an append: a new or empty
// file gets the header, a schema-1 log is upgraded, a newer schema is
// refused, and a torn last line is ended so it cannot swallow the next.
// It reads only the first line and the last byte: it runs before every
// message a turn writes.
func (s *Store) prepare(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, append(append([]byte(nil), schemaHeader...), '\n'), 0o600)
	}
	if err != nil {
		return fmt.Errorf("sessionstore: open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("sessionstore: stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return os.WriteFile(path, append(append([]byte(nil), schemaHeader...), '\n'), 0o600)
	}
	first, _ := bufio.NewReader(f).ReadBytes('\n')
	var head lineKind
	_ = json.Unmarshal(first, &head)
	if head.Schema == nil {
		return upgrade(path)
	}
	if err := checkSchema(path, *head.Schema); err != nil {
		return err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return fmt.Errorf("sessionstore: read %s: %w", path, err)
	}
	if last[0] == '\n' {
		return nil
	}
	w, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sessionstore: open %s: %w", path, err)
	}
	defer w.Close()
	_, err = w.Write([]byte{'\n'})
	return err
}

// upgrade rewrites a schema-1 log as schema 2: the header, its messages,
// and a turn marker, since a schema-1 log only ever held finished turns.
// The new file replaces the old by rename, so a crash leaves one or the
// other, never half of each.
func upgrade(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sessionstore: read %s: %w", path, err)
	}
	var out []byte
	out = append(append(out, schemaHeader...), '\n')
	for rest := raw; len(rest) > 0; {
		var line []byte
		line, rest, _ = bytesLine(rest)
		var kind lineKind
		if json.Unmarshal(line, &kind) != nil || kind.Role == "" {
			continue // a torn line
		}
		out = append(append(out, line...), '\n')
	}
	out = append(append(out, turnEnded...), '\n')
	tmp := path + ".upgrade"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("sessionstore: upgrade %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("sessionstore: upgrade %s: %w", path, err)
	}
	return nil
}

// bytesLine splits raw at its first newline.
func bytesLine(raw []byte) (line, rest []byte, ok bool) {
	for i, b := range raw {
		if b == '\n' {
			return raw[:i], raw[i+1:], true
		}
	}
	return raw, nil, false
}

// Load reads the whole log back. A missing file is an empty conversation,
// not an error; a torn line (a crash's last half-written record) is
// skipped so resume never refuses to run. A log from a newer lazymesh is
// refused.
func (s *Store) Load(sessionID string) (Conversation, error) {
	f, err := os.Open(s.Path(sessionID))
	if os.IsNotExist(err) {
		return Conversation{}, nil
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("sessionstore: open %s: %w", s.Path(sessionID), err)
	}
	defer f.Close()
	return readLog(f, s.Path(sessionID))
}

// readLog reads a log line by line with no cap on a line's length: a
// message is stored whole, and a write_file's arguments can be larger than
// any fixed buffer. A log it cannot read is an error, never a shorter
// conversation (#99).
func readLog(r io.Reader, path string) (Conversation, error) {
	var msgs []provider.Message
	schema, ended := 1, 0
	reader := bufio.NewReader(r)
	for first := true; ; first = false {
		raw, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return Conversation{}, fmt.Errorf("sessionstore: read %s: %w", path, err)
		}
		var kind lineKind
		if len(raw) > 0 && json.Unmarshal(raw, &kind) == nil {
			switch {
			case first && kind.Schema != nil:
				if err := checkSchema(path, *kind.Schema); err != nil {
					return Conversation{}, err
				}
				schema = *kind.Schema
			case kind.Turn == "ended":
				ended = len(msgs)
			case kind.Role != "":
				var line messageLine
				if json.Unmarshal(raw, &line) == nil {
					msgs = append(msgs, fromLine(line))
				}
			}
		} // else a torn line
		if err == io.EOF {
			break
		}
	}
	conv := Conversation{Messages: msgs}
	if schema >= 2 && ended < len(msgs) {
		conv.Unfinished = msgs[ended:]
	}
	return conv, nil
}

// checkSchema refuses a header naming any schema but this lazymesh's: a
// higher one comes from a newer lazymesh, and zero or less from no
// lazymesh at all.
func checkSchema(path string, schema int) error {
	switch {
	case schema > schemaVersion:
		return fmt.Errorf("sessionstore: %s was written by a newer lazymesh (schema %d)", path, schema)
	case schema != schemaVersion:
		return fmt.Errorf("sessionstore: %s has an unknown schema %d", path, schema)
	}
	return nil
}

// Latest returns the id of the most recently modified session log, or
// ErrNoSessions when the directory holds none.
func (s *Store) Latest() (string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return "", fmt.Errorf("sessionstore: read %s: %w", s.Dir, err)
	}
	var logs []os.FileInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		logs = append(logs, info)
	}
	if len(logs) == 0 {
		return "", ErrNoSessions
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].ModTime().After(logs[j].ModTime()) })
	return logs[0].Name()[:len(logs[0].Name())-len(".jsonl")], nil
}

// ErrNoSessions reports that "latest" has nothing to point at.
var ErrNoSessions = fmt.Errorf("sessionstore: no sessions to resume")

// Resolve turns a resume reference into a session id: "latest"/"last"
// resolve to the newest log, anything else is an explicit id that must
// exist, "" yields defaultID.
func Resolve(ref string, s *Store, defaultID string) (string, error) {
	switch ref {
	case "":
		return defaultID, nil
	case "latest", "last":
		id, err := s.Latest()
		if err != nil {
			return "", err
		}
		return id, nil
	}
	if _, err := os.Stat(s.Path(ref)); err != nil {
		return "", fmt.Errorf("sessionstore: no session %q to resume", ref)
	}
	return ref, nil
}

func toLine(m provider.Message) messageLine {
	line := messageLine{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
		Failed:     m.Failed,
	}
	for _, tc := range m.ToolCalls {
		line.ToolCalls = append(line.ToolCalls, toolCallLine{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	}
	return line
}

func fromLine(line messageLine) provider.Message {
	m := provider.Message{
		Role:       provider.Role(line.Role),
		Content:    line.Content,
		ToolCallID: line.ToolCallID,
		Name:       line.Name,
		Failed:     line.Failed,
	}
	for _, tc := range line.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	}
	return m
}

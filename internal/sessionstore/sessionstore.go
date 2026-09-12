// Package sessionstore persists lazymesh conversations as JSONL append
// logs (D2): one JSON object per line, one line per message, written with
// O_APPEND so concurrent appenders never interleave and a crash can only
// lose the final write, never corrupt an earlier one. Loading tolerates a
// torn trailing line — the last thing a crash was writing — and skips it
// rather than refusing to resume.
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
	f, err := os.OpenFile(s.Path(sessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sessionstore: open %s: %w", s.Path(sessionID), err)
	}
	defer f.Close()
	for _, m := range msgs {
		line, err := json.Marshal(toLine(m))
		if err != nil {
			return fmt.Errorf("sessionstore: marshal message: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("sessionstore: append to %s: %w", s.Path(sessionID), err)
		}
	}
	return nil
}

// Load reads the whole log back into messages. A missing file is an
// empty conversation, not an error; a torn trailing line (a crash's last
// half-written record) is skipped so resume never refuses to run.
func (s *Store) Load(sessionID string) ([]provider.Message, error) {
	f, err := os.Open(s.Path(sessionID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessionstore: open %s: %w", s.Path(sessionID), err)
	}
	defer f.Close()

	var msgs []provider.Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line messageLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue // torn trailing line
		}
		msgs = append(msgs, fromLine(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sessionstore: read %s: %w", s.Path(sessionID), err)
	}
	return msgs, nil
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
	}
	for _, tc := range line.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	}
	return m
}

// Package prompthistory keeps the prompts an operator sent, so the TUI can
// give them back with the up key, across restarts (#78 gap 5).
//
// Only the prompt text is kept: one JSON string per line, the last Max of
// them, in history/<fingerprint>.jsonl beside the session store's sessions/
// directory and keyed by the same checkout fingerprint. The session log
// already holds every prompt of the conversation; this is the short list
// recall needs, readable without replaying a session.
//
// A prompt can hold a secret, so the file is the operator's alone: created
// 0600 by the open itself, inside a 0700 directory, through an os.Root so
// nothing outside the directory is reached, and refused outright when what
// sits at the path is not a plain file only its owner can read.
package prompthistory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Max is how many prompts recall offers.
const Max = 50

// compactAt is when Load rewrites the file down to Max: appends are cheap
// and safe across two instances, so the file is let grow a while first.
const compactAt = 4 * Max

// Path is the history file for one checkout: dataDir is the lazymesh data
// directory (the parent of the session store's sessions/), fingerprint the
// checkout's sessionstore.Fingerprint.
func Path(dataDir, fingerprint string) string {
	return filepath.Join(dataDir, "history", fingerprint+".jsonl")
}

// Append records one sent prompt.
func Append(path, prompt string) error {
	line, err := json.Marshal(prompt)
	if err != nil {
		return fmt.Errorf("prompthistory: encode: %w", err)
	}
	root, name, err := openDir(path, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkPrivate(root, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("prompthistory: open %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("prompthistory: write %s: %w", path, err)
	}
	return f.Close()
}

// Load returns the last Max prompts, oldest first. No file is no history.
// A line that does not parse is skipped.
func Load(path string) ([]string, error) {
	root, name, err := openDir(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkPrivate(root, name); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	raw, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("prompthistory: read %s: %w", path, err)
	}
	all := parse(raw)
	if len(all) <= Max {
		return all, nil
	}
	kept := all[len(all)-Max:]
	if len(all) > compactAt {
		if err := rewrite(root, name, kept); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

func parse(raw []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var p string
		if json.Unmarshal(sc.Bytes(), &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// rewrite replaces the file with kept: a fresh file made by an exclusive
// create, renamed over the old one.
func rewrite(root *os.Root, name string, kept []string) error {
	var buf bytes.Buffer
	for _, p := range kept {
		line, _ := json.Marshal(p)
		buf.Write(append(line, '\n'))
	}
	tmp := name + ".compact"
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("prompthistory: compact: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		_ = root.Remove(tmp)
		return fmt.Errorf("prompthistory: compact: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("prompthistory: compact: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("prompthistory: compact: %w", err)
	}
	return nil
}

// openDir opens the history directory as a root, making it 0700 first when
// create is set.
func openDir(path string, create bool) (*os.Root, string, error) {
	dir, name := filepath.Split(path)
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", fmt.Errorf("prompthistory: make %s: %w", dir, err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

// checkPrivate refuses anything at the path but a plain file no one else
// can read: a symlink would carry prompts wherever it points, and a file
// others can read would show them what is added.
func checkPrivate(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("prompthistory: %s is not a plain file; remove it to keep prompt history", name)
	}
	// Windows reports no unix modes (a plain file reads 0666), and its
	// access lists live under the user's profile directory instead.
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("prompthistory: %s can be read by others (mode %o); chmod 600 it or remove it", name, fi.Mode().Perm())
	}
	return nil
}

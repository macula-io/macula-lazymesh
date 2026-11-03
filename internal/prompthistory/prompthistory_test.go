package prompthistory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historyPath(t *testing.T) string {
	t.Helper()
	return Path(filepath.Join(t.TempDir(), "lazymesh"), "abc123")
}

// What was sent comes back, oldest first, after the process that sent it
// is gone (#78 gap 5): the file is the only thing the two share.
func TestRememberedPromptsComeBack(t *testing.T) {
	path := historyPath(t)
	for _, p := range []string{"a", "b\nsecond line"} {
		if err := Append(path, p); err != nil {
			t.Fatalf("Append(%q): %v", p, err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"a", "b\nsecond line"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Load = %q, want %q", got, want)
	}
}

// No file yet is no history, not an error: a first run has nothing to
// recall.
func TestNoFileIsNoHistory(t *testing.T) {
	got, err := Load(historyPath(t))
	if err != nil || len(got) != 0 {
		t.Fatalf("Load on a missing file = %q, %v; want nothing and no error", got, err)
	}
}

// Only the last Max prompts are offered, and the file does not grow
// without bound: once it holds far more than that, Load rewrites it.
func TestHistoryKeepsTheLastFifty(t *testing.T) {
	path := historyPath(t)
	for i := 0; i < 4*Max+1; i++ {
		if err := Append(path, fmt.Sprintf("p%d", i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != Max || got[0] != fmt.Sprintf("p%d", 3*Max+1) || got[Max-1] != fmt.Sprintf("p%d", 4*Max) {
		t.Fatalf("Load kept %d prompts, first %q last %q", len(got), got[0], got[len(got)-1])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := strings.Count(string(raw), "\n"); n != Max {
		t.Fatalf("after Load the file holds %d lines, want it compacted to %d", n, Max)
	}
}

// A prompt can hold a secret: the file is the operator's alone.
func TestHistoryIsPrivateToTheOperator(t *testing.T) {
	path := historyPath(t)
	if err := Append(path, "token=hunter2"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("history file mode = %o, want 600", mode)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Fatalf("history dir mode = %o, want 700", mode)
	}
}

// A line that does not parse (a crash mid-write, a hand edit) is skipped,
// never the reason the rest is lost.
func TestABrokenLineCostsOnlyItself(t *testing.T) {
	path := historyPath(t)
	if err := Append(path, "before"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString("{not json\n")
	f.Close()
	if err := Append(path, "after"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Join(got, "|") != "before|after" {
		t.Fatalf("Load = %q, want before and after", got)
	}
}

// Something other than a plain file at the history path is refused, never
// written through: a symlink there would carry prompts wherever it points.
func TestASymlinkAtTheHistoryPathIsRefused(t *testing.T) {
	path := historyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if err := Append(path, "secret"); err == nil {
		t.Fatal("Append wrote through a symlink at the history path")
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load read through a symlink at the history path")
	}
	if raw, _ := os.ReadFile(elsewhere); len(raw) != 0 {
		t.Fatalf("the symlink's target was written: %q", raw)
	}
}

// A history file others can read is refused rather than added to: the
// operator is told, and nothing more of theirs lands in it.
func TestAHistoryFileOthersCanReadIsRefused(t *testing.T) {
	path := historyPath(t)
	if err := Append(path, "first"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := Append(path, "second"); err == nil {
		t.Fatal("Append added to a history file others can read")
	}
}

package localtools

// Escape attempts from Saturnus's review of #26 (PR #50, round 1), each
// reproduced against the real Prepare()'d bubblewrap sandbox and each an
// arbitrary host write on that tip: host code following a dangling link
// the shell planted, the paging directory swapped for a link, and the
// checkout's .git pointer rewritten from inside.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/confinedshell"
)

func attackRig(t *testing.T, gitFile bool) (*Source, string, string, *confinedshell.Sandbox) {
	requireBwrap(t)
	root := t.TempDir()
	home, co, cache, outside := filepath.Join(root, "home"), filepath.Join(root, "co"), filepath.Join(root, "cache"), filepath.Join(root, "outside")
	for _, d := range []string{home, co, cache, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if gitFile {
		os.WriteFile(filepath.Join(co, ".git"), []byte("gitdir: /nonexistent/real\n"), 0o644)
	} else {
		os.MkdirAll(filepath.Join(co, ".git"), 0o755)
	}
	sb, err := confinedshell.Prepare(context.Background(), confinedshell.Options{Checkout: co, Home: home, CacheDir: cache})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: co, Shell: sb, OutputDir: sb.OutputDir()})
	if err != nil {
		t.Fatal(err)
	}
	return src, co, outside, sb
}

func call(t *testing.T, s *Source, name, args string) string {
	out, err := s.CallToolRaw(context.Background(), name, args)
	t.Logf("%s -> %.200s err=%v", name, out, err)
	return out
}

func TestAttackDanglingSymlinkWrite(t *testing.T) {
	s, _, outside, _ := attackRig(t, false)
	target := filepath.Join(outside, "planted.desktop")
	call(t, s, "shell_exec", `{"command":"ln -s `+target+` pwn"}`)
	call(t, s, "write_file", `{"path":"pwn","content":"[Desktop Entry]\nExec=evil\n"}`)
	if _, err := os.Stat(target); err == nil {
		t.Errorf("ESCAPE: write_file created %s outside the checkout", target)
	}
}

func TestAttackPagingDirSymlink(t *testing.T) {
	s, _, outside, sb := attackRig(t, false)
	out := sb.OutputDir()
	call(t, s, "shell_exec", `{"command":"rm -rf `+out+` && ln -s `+outside+` `+out+` && head -c 20000 /dev/zero | tr '\\0' A"}`)
	entries, _ := os.ReadDir(outside)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "shell-") {
			t.Errorf("ESCAPE: paged output written outside scratch: %s", filepath.Join(outside, e.Name()))
		}
	}
}

func TestAttackGitFilePointer(t *testing.T) {
	s, co, _, _ := attackRig(t, true)
	call(t, s, "shell_exec", `{"command":"mkdir -p .evil && printf '[core]\\n\\tfsmonitor = touch /tmp/x\\n' > .evil/config && printf 'gitdir: .evil\\n' > .git"}`)
	b, _ := os.ReadFile(filepath.Join(co, ".git"))
	if strings.Contains(string(b), ".evil") {
		t.Errorf("ESCAPE: the checkout's .git pointer was rewritten from inside: %q", b)
	}
}

func requireBwrap(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the confined shell is Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bubblewrap is required to prove confinement: install bwrap")
	}
}

// TestWriteFileNeverWritesIntoGit: a host write into .git, in any letter
// case, plants a hook the operator's git runs; it is refused.
func TestWriteFileNeverWritesIntoGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/hooks/pre-commit", ".GIT/config", "sub/.git/config", ".git"} {
		if _, err := src.CallToolRaw(context.Background(), "write_file", `{"path":"`+path+`","content":"x"}`); err == nil {
			t.Errorf("write_file wrote %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit")); err == nil {
		t.Error("a hook was planted")
	}
}

// TestWriteFileRefusesASymlinkInsideToo: write_file does not follow a
// symlink at the final component even when it points inside.
func TestWriteFileRefusesASymlinkInsideToo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Skip(err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.CallToolRaw(context.Background(), "write_file", `{"path":"alias.txt","content":"changed"}`); err == nil {
		t.Error("write_file followed a symlink")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "real.txt")); string(got) != "keep" {
		t.Errorf("the link target changed: %q", got)
	}
}

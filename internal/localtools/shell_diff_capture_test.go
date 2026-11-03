package localtools

// Review r=2 of #46 (PR 70), finding 2: a command could hide its change.
// The after-read ran under the shell's own context, so a timed-out command
// left it expired, git never answered, and the result carried no diff and
// no notice -- the one thing this half promises is that a change cannot be
// invisible. And a diff had no size cap: a large append went whole into
// the model's context.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellResult(t *testing.T, src *Source, command string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"command": command})
	out, err := src.CallToolRaw(context.Background(), "shell_exec", string(body))
	if err != nil {
		t.Fatalf("shell_exec %q: %v", command, err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("shell_exec result is not JSON: %v\n%.300s", err, out)
	}
	return res
}

// A command that outlives its timeout still changed the checkout: the
// operator has to be told, with the diff or with an explicit "could not
// read it".
func TestATimedOutCommandStillReportsItsChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init", "--quiet")
	mustGit(t, dir, "add", "f.txt")
	mustGit(t, dir, "-c", "user.email=test@example.invalid", "-c", "user.name=test", "commit", "--quiet", "-m", "start")
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 1 * time.Second, Shell: HostShell(dir)})
	if err != nil {
		t.Fatal(err)
	}
	res := shellResult(t, src, "printf 'x\\n' >> f.txt; sleep 3")

	diff, hasDiff := res["working_tree_diff"].(string)
	_, hasUnread := res["working_tree_diff_unread"].(string)
	if !hasDiff && !hasUnread {
		t.Fatalf("a timed-out command hid its change: %v", res)
	}
	if hasDiff && !strings.Contains(diff, "+x") {
		t.Fatalf("the diff reported does not carry the change: %q", diff)
	}
}

// A checkout whose git cannot be read after the command must say so
// rather than stay silent. Here the command destroys the repository.
func TestAnUnreadableCheckoutIsReported(t *testing.T) {
	dir := t.TempDir()
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 10 * time.Second, Shell: HostShell(dir)})
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init", "--quiet")
	res := shellResult(t, src, "rm -rf .git")
	if _, ok := res["working_tree_diff_unread"].(string); !ok {
		t.Fatalf("a checkout that can no longer be read was reported as nothing: %v", res)
	}
}

// A huge diff is capped, with a notice, instead of filling the model's
// context.
func TestAHugeDiffIsCapped(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "f.txt")
	mustGit(t, dir, "-c", "user.email=test@example.invalid", "-c", "user.name=test", "commit", "--quiet", "-m", "start")
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 30 * time.Second, Shell: HostShell(dir)})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"command": "head -c 2000000 /dev/zero | tr '\\0' 'a' >> f.txt"})
	out, err := src.CallToolRaw(context.Background(), "shell_exec", string(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 2*shellPageBytes {
		t.Fatalf("a 2 MB diff produced a %d byte result", len(out))
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	diff, _ := res["working_tree_diff"].(string)
	if !strings.Contains(diff, "cut") {
		t.Fatalf("a capped diff does not say it was cut: %.200s", diff)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

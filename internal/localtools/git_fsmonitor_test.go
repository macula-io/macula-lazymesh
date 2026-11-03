package localtools

// Review r=2 of #46 (PR 70), finding 1: the host's `git diff` ran a
// nested repository's core.fsmonitor. The sandbox keeps only the top-level
// .git read-only, so with a gitlink in the index a command could init
// sub/ and point its fsmonitor at anything, and the host git then ran it
// outside bwrap.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// gitlinkCheckout is a checkout whose index carries a gitlink for sub/, as
// a repository with a submodule does, with sub/ left uninitialised.
func gitlinkCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "--quiet")
	run("-c", "user.email=test@example.invalid", "-c", "user.name=test", "commit", "--quiet", "--allow-empty", "-m", "init")
	head := string(run("rev-parse", "HEAD"))
	run("update-index", "--add", "--cacheinfo", "160000,"+trimNewline(head)+",sub")
	run("-c", "user.email=test@example.invalid", "-c", "user.name=test", "commit", "--quiet", "-m", "sub")
	return dir
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// plantFsmonitor runs, as the sandboxed command would, the init and config
// that make the host's git run the marker command when it looks at sub/.
func plantFsmonitor(t *testing.T, src *Source, marker string) {
	t.Helper()
	command := fmt.Sprintf("mkdir -p sub && git init -q sub && git -C sub config core.fsmonitor 'touch %s; false' && echo x > sub/f", marker)
	body, _ := json.Marshal(map[string]string{"command": command})
	if _, err := src.CallToolRaw(context.Background(), "shell_exec", string(body)); err != nil {
		t.Logf("planting command returned %v (a timeout here is expected)", err)
	}
}

func TestTheDiffDoesNotRunANestedReposFsmonitor(t *testing.T) {
	dir := gitlinkCheckout(t)
	marker := filepath.Join(t.TempDir(), "PWNED_FSMON")
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 10 * time.Second, Shell: testShell(dir)})
	if err != nil {
		t.Fatal(err)
	}

	plantFsmonitor(t, src, marker)

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ESCAPE: the host git ran a nested repository's core.fsmonitor")
	}
}

// apply_patch's own diff takes the same path, so it had the same hole.
func TestAnAppliedPatchDoesNotRunANestedReposFsmonitor(t *testing.T) {
	dir := gitlinkCheckout(t)
	marker := filepath.Join(t.TempDir(), "PWNED_FSMON")
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 10 * time.Second, Shell: testShell(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plantFsmonitor(t, src, marker)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ESCAPE: the host git ran a nested repository's core.fsmonitor")
	}
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,2 @@\n one\n+two\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ESCAPE: apply_patch's diff ran a nested repository's core.fsmonitor")
	}
}

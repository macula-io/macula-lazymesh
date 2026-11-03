package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/tui"
)

// The second half of slice D (#46): the tests run through slice C's
// confined shell_exec, and what the operator sees afterwards is the
// checkout as it now stands. Red test 3 on the issue: a patch plus a test
// run ends with the diff the TUI shows equal to `git diff` of the
// checkout.

// verifyCheckout is a real git checkout, since the confined shell refuses
// to run anywhere else (#26) and the diff is git's own.
func verifyCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("-c", "user.email=test@example.invalid", "-c", "user.name=test", "commit", "--quiet", "-m", "start")
	return dir
}

func verifyCodingTools(t *testing.T, dir string) agent.ToolSource {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the coding profile's shell is Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bubblewrap is required to prove the confined run: install bwrap")
	}
	cfg := codingConfig(t)
	cfg.LocalTools.WorkingDir = dir
	cfg.ToolAllowlist = []string{"apply_patch", "shell_exec"}
	cfg.ToolAllowlistExtends = false
	shell := prepareCodingShell(context.Background(), cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if shell.sandbox == nil {
		t.Fatalf("no sandbox: %s", shell.reason)
	}
	tools, _, err := buildCodingToolSource(cfg, &fakeMesh{}, onlyRoom(codingRoom), shell, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The multi-source answers a call only for a tool it has been asked
	// to list.
	if _, err := tools.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	return tools
}

func callTool(t *testing.T, tools agent.ToolSource, name, args string) string {
	t.Helper()
	out, err := tools.CallToolRaw(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// checkoutDiff is `git diff` in the checkout, the thing the operator's
// screen must agree with.
func checkoutDiff(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "--no-pager", "diff", "--no-color")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	return string(out)
}

// TestTheDiffShownAfterATestRunIsTheCheckout: apply a patch, then run a
// command in the confined shell that changes the checkout — the diff on
// screen must be the checkout's, not the patch's snapshot. Red before the
// change: the shell result carries no diff, so the screen stops at the
// patch and disagrees with `git diff`.
func TestTheDiffShownAfterATestRunIsTheCheckout(t *testing.T) {
	dir := verifyCheckout(t)
	tools := verifyCodingTools(t, dir)

	patch := "--- a/file.txt\n+++ b/file.txt\n@@ -1,2 +1,3 @@\n one\n two\n+three\n"
	patched := callTool(t, tools, "apply_patch", `{"patch":`+jsonString(t, patch)+`}`)

	// The "test run": a command that changes the checkout. In a real
	// package this is `go test` or a formatter the test invokes; what
	// matters here is that it is the confined shell that changes the tree.
	ran := callTool(t, tools, "shell_exec", `{"command":"printf 'four\\n' >> file.txt"}`)

	shownPatch := tui.AutoDetail(agent.Event{Kind: agent.EventToolResult, ToolName: "apply_patch", Text: firstField(t, patched, "result")})
	shownRun := tui.AutoDetail(agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: ran})
	if shownRun == "" {
		t.Fatal("the TUI shows nothing for the command that changed the checkout; the operator sees a stale diff")
	}
	want := checkoutDiff(t, dir)
	if !strings.Contains(want, "+four") {
		t.Fatalf("the checkout did not change: %q", want)
	}
	if !strings.Contains(shownPatch, "+three") {
		t.Fatalf("the patch's own diff was not shown: %q", shownPatch)
	}
	if !strings.Contains(shownRun, "+three") || !strings.Contains(shownRun, "+four") {
		t.Fatalf("the diff shown after the run is not the checkout's:\n%s\nwant:\n%s", shownRun, want)
	}
}

// TestAQuietCommandAddsNoDiff: a command that changes nothing carries no
// diff, and its entry stays behind the details toggle — an ordinary test
// run must not grow a diff on every call.
func TestAQuietCommandAddsNoDiff(t *testing.T) {
	dir := verifyCheckout(t)
	tools := verifyCodingTools(t, dir)
	out := callTool(t, tools, "shell_exec", `{"command":"true"}`)
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	if _, ok := res["working_tree_diff"]; ok {
		t.Fatalf("a command that changed nothing carried a diff: %s", out)
	}
	if detail := tui.AutoDetail(agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: out}); detail != "" {
		t.Fatalf("a quiet command's entry is shown without the toggle: %q", detail)
	}
}

// firstField is the "result" a paged tool answers with, or the text as
// it came when the tool answered plainly.
func firstField(t *testing.T, out, field string) string {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return out
	}
	if v, ok := res[field].(string); ok {
		return v
	}
	return out
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

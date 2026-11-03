package gittools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin threat model invariants E1-E10 of
// macula-io/macula-lazymesh#45: the host-side git tool commits and pushes
// the checkout's own branch and cannot be argued into pushing anything
// else, whatever the arguments or the approval say.

// fakeRunner stands in for git and records every command, so a test can
// prove that no push process ran at all.
type fakeRunner struct {
	toplevel string
	branch   string // symbolic-ref --short HEAD
	remote   string // branch.<branch>.remote
	calls    [][]string
	fail     map[string]int // subcommand -> exit code
}

func (f *fakeRunner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	sub := args[len(hardening)]
	switch sub {
	case "rev-parse":
		return Result{Stdout: f.toplevel + "\n"}, nil
	case "symbolic-ref":
		if f.branch == "" {
			return Result{Stderr: "fatal: HEAD is detached", ExitCode: 128}, nil
		}
		return Result{Stdout: f.branch + "\n"}, nil
	case "config":
		return Result{Stdout: f.remote + "\n"}, nil
	case "remote":
		name := args[len(hardening)+2]
		if name != f.remote {
			return Result{Stderr: "error: no such remote", ExitCode: 2}, nil
		}
		return Result{Stdout: "https://example.invalid/repo.git\n"}, nil
	case "check-ref-format":
		name := args[len(args)-1]
		if strings.HasPrefix(name, "-") || strings.ContainsAny(name, " ~^:?*[\\") || strings.Contains(name, "..") {
			return Result{Stderr: "fatal: invalid branch name", ExitCode: 128}, nil
		}
		return Result{Stdout: name + "\n"}, nil
	}
	return Result{}, nil
}

// commands returns the recorded calls with the hardening prefix cut, so a
// test reads "push origin b" the way a person would.
func (f *fakeRunner) commands() [][]string {
	var out [][]string
	for _, c := range f.calls {
		out = append(out, c[len(hardening):])
	}
	return out
}

func (f *fakeRunner) ran(sub string) [][]string {
	var out [][]string
	for _, c := range f.commands() {
		if c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

func newFake(t *testing.T) (*Source, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	f := &fakeRunner{toplevel: dir, branch: "titan/45-work", remote: "origin"}
	s, err := New(Config{Checkout: dir, Runner: f})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func call(t *testing.T, s *Source, name, args string) (string, error) {
	t.Helper()
	return s.CallToolRaw(context.Background(), name, args)
}

// TestPushRefusesProtectedTargets is invariant E1/E2: on main, master or
// a refs/tags target, a push is refused before any git push runs.
func TestPushRefusesProtectedTargets(t *testing.T) {
	for _, branch := range []string{"main", "master", "refs/tags/v1", "tags/v1", "refs/heads/main", "HEAD"} {
		t.Run(branch, func(t *testing.T) {
			s, f := newFake(t)
			f.branch = branch
			out, err := call(t, s, ToolPush, `{}`)
			if err == nil {
				t.Fatalf("push on %s was not refused: %s", branch, out)
			}
			if pushes := f.ran("push"); len(pushes) != 0 {
				t.Fatalf("a git push process ran for %s: %v", branch, pushes)
			}
		})
	}
}

// TestPushRefusesFlagSpellings is invariant E3: there is no argument that
// spells a flag, a refspec, a force, a delete, a mirror, an --all or a
// --tags, so each attempt is refused and nothing runs.
func TestPushRefusesFlagSpellings(t *testing.T) {
	for _, args := range []string{
		`{"force":true}`,
		`{"force-with-lease":true}`,
		`{"flags":"--force"}`,
		`{"args":["--force"]}`,
		`{"refspec":"+refs/heads/x:refs/heads/x"}`,
		`{"refspec":"main"}`,
		`{"branch":"main"}`,
		`{"delete":true}`,
		`{"mirror":true}`,
		`{"all":true}`,
		`{"tags":true}`,
		`{"command":"push --force origin main"}`,
	} {
		t.Run(args, func(t *testing.T) {
			s, f := newFake(t)
			if out, err := call(t, s, ToolPush, args); err == nil {
				t.Fatalf("push with %s was not refused: %s", args, out)
			}
			if pushes := f.ran("push"); len(pushes) != 0 {
				t.Fatalf("a git push process ran for %s: %v", args, pushes)
			}
		})
	}
}

// TestPushRefusesOtherRemoteAndURL is invariant E4: the only remote a push
// can go to is the one the checkout's config named at start.
func TestPushRefusesOtherRemoteAndURL(t *testing.T) {
	for _, args := range []string{
		`{"remote":"upstream"}`,
		`{"remote":"https://attacker.invalid/x.git"}`,
		`{"remote":"git@attacker.invalid:x.git"}`,
		`{"remote":"origin","url":"https://attacker.invalid/x.git"}`,
	} {
		t.Run(args, func(t *testing.T) {
			s, f := newFake(t)
			if out, err := call(t, s, ToolPush, args); err == nil {
				t.Fatalf("push with %s was not refused: %s", args, out)
			}
			if pushes := f.ran("push"); len(pushes) != 0 {
				t.Fatalf("a git push process ran for %s: %v", args, pushes)
			}
		})
	}
	// Naming the configured remote explicitly is allowed and changes nothing.
	s, f := newFake(t)
	if _, err := call(t, s, ToolPush, `{"remote":"origin"}`); err != nil {
		t.Fatalf("naming the configured remote was refused: %v", err)
	}
	if pushes := f.ran("push"); len(pushes) != 1 || strings.Join(pushes[0], " ") != "push origin titan/45-work" {
		t.Fatalf("pushes = %v, want exactly one `push origin titan/45-work`", pushes)
	}
}

// TestPushRunsExactlyOneCommand is the E1-E4 happy path: one git push to
// the configured remote, the current branch, no refspec.
func TestPushRunsExactlyOneCommand(t *testing.T) {
	s, f := newFake(t)
	out, err := call(t, s, ToolPush, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	pushes := f.ran("push")
	if len(pushes) != 1 {
		t.Fatalf("pushes = %v, want exactly one", pushes)
	}
	if got := strings.Join(pushes[0], " "); got != "push origin titan/45-work" {
		t.Fatalf("push command = %q", got)
	}
	var res Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("push result is not a result object: %v (%s)", err, out)
	}
}

// TestPushRefusesDetachedHead: there is no branch to push, so nothing runs.
func TestPushRefusesDetachedHead(t *testing.T) {
	s, f := newFake(t)
	f.branch = ""
	if _, err := call(t, s, ToolPush, `{}`); err == nil {
		t.Fatal("a push from a detached HEAD was not refused")
	}
	if pushes := f.ran("push"); len(pushes) != 0 {
		t.Fatalf("a git push process ran: %v", pushes)
	}
}

// TestPushUsesTheRemoteReadAtStart is invariant E5: rewriting the
// checkout's config mid-session does not move the target.
func TestPushUsesTheRemoteReadAtStart(t *testing.T) {
	s, f := newFake(t)
	f.remote = "attacker" // the config now says something else
	if _, err := call(t, s, ToolPush, `{}`); err != nil {
		t.Fatal(err)
	}
	pushes := f.ran("push")
	if len(pushes) != 1 || strings.Join(pushes[0], " ") != "push origin titan/45-work" {
		t.Fatalf("pushes = %v, want the remote resolved at start (origin)", pushes)
	}
}

// TestBranchValidatesNames is invariant E8: a name is checked with git's
// own check-ref-format, and an option in disguise never reaches git.
func TestBranchValidatesNames(t *testing.T) {
	for _, name := range []string{"-x", "--force", "../main", "a b", "a..b", "a~1"} {
		s, f := newFake(t)
		if out, err := call(t, s, ToolBranch, fmt.Sprintf(`{"name":%q}`, name)); err == nil {
			t.Fatalf("branch name %q was accepted: %s", name, out)
		}
		if switched := f.ran("switch"); len(switched) != 0 {
			t.Fatalf("git switch ran for name %q: %v", name, switched)
		}
	}
	s, f := newFake(t)
	if _, err := call(t, s, ToolBranch, `{"name":"titan/45-new","create":true}`); err != nil {
		t.Fatal(err)
	}
	if got := f.ran("switch"); len(got) != 1 || strings.Join(got[0], " ") != "switch -c titan/45-new" {
		t.Fatalf("switch = %v", got)
	}
	s, f = newFake(t)
	if _, err := call(t, s, ToolBranch, `{"name":"existing"}`); err != nil {
		t.Fatal(err)
	}
	if got := f.ran("switch"); len(got) != 1 || strings.Join(got[0], " ") != "switch existing" {
		t.Fatalf("switch = %v", got)
	}
}

// TestCommitValidatesPaths is invariant E8 for commit paths.
func TestCommitValidatesPaths(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../outside", "-x", ":(glob)**", ".git/config"} {
		s, f := newFake(t)
		args, _ := json.Marshal(map[string]any{"message": "m", "paths": []string{path}})
		if out, err := call(t, s, ToolCommit, string(args)); err == nil {
			t.Fatalf("commit path %q was accepted: %s", path, out)
		}
		if commits := f.ran("commit"); len(commits) != 0 {
			t.Fatalf("git commit ran for path %q: %v", path, commits)
		}
	}
	s, f := newFake(t)
	if _, err := call(t, s, ToolCommit, `{"message":"slice E","paths":["internal/gittools/gittools.go","README.md"]}`); err != nil {
		t.Fatal(err)
	}
	commits := f.ran("commit")
	if len(commits) != 1 {
		t.Fatalf("commits = %v", commits)
	}
	if got := strings.Join(commits[0], " "); got != "commit -m slice E -- internal/gittools/gittools.go README.md" {
		t.Fatalf("commit = %q", got)
	}
}

// TestCommitRequiresAMessage.
func TestCommitRequiresAMessage(t *testing.T) {
	s, f := newFake(t)
	if _, err := call(t, s, ToolCommit, `{"message":""}`); err == nil {
		t.Fatal("a commit with no message was accepted")
	}
	if commits := f.ran("commit"); len(commits) != 0 {
		t.Fatalf("git commit ran: %v", commits)
	}
}

// TestEveryCommandDisablesHooksAndFsmonitor is invariant E6: no git
// invocation this tool makes can run a hook or an fsmonitor command the
// checkout planted, so the pair is on every single one.
func TestEveryCommandDisablesHooksAndFsmonitor(t *testing.T) {
	s, f := newFake(t)
	_, _ = call(t, s, ToolBranch, `{"name":"titan/45-new","create":true}`)
	_, _ = call(t, s, ToolCommit, `{"message":"m"}`)
	_, _ = call(t, s, ToolPush, `{}`)
	if len(f.calls) == 0 {
		t.Fatal("no git command was recorded")
	}
	want := strings.Join(hardening, " ")
	for _, c := range f.calls {
		if len(c) < len(hardening) || strings.Join(c[:len(hardening)], " ") != want {
			t.Fatalf("git command %v does not start with %q", c, want)
		}
	}
}

// TestPlantedHooksAndFsmonitorNeverRun is invariant E6 proved against real
// git: a hook and an fsmonitor command planted in the checkout leave no
// trace through branch, commit and push.
func TestPlantedHooksAndFsmonitorNeverRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "-q", remote)
	runGit(t, root, "init", "-q", checkout)
	runGit(t, checkout, "config", "user.email", "test@example.invalid")
	runGit(t, checkout, "config", "user.name", "test")
	runGit(t, checkout, "remote", "add", "origin", remote)
	writeFile(t, filepath.Join(checkout, "work.txt"), "work\n")
	runGit(t, checkout, "add", "work.txt")
	runGit(t, checkout, "commit", "-qm", "init")
	// Stage the second file before planting anything: `git add` itself
	// queries an fsmonitor, and this test is about what the tool runs.
	writeFile(t, filepath.Join(checkout, "second.txt"), "second\n")
	runGit(t, checkout, "add", "second.txt")

	marker := filepath.Join(root, "hook-ran")
	hooks := filepath.Join(checkout, ".git", "hooks")
	for _, name := range []string{"pre-commit", "pre-push", "post-checkout", "post-commit"} {
		writeFile(t, filepath.Join(hooks, name), "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
		if err := os.Chmod(filepath.Join(hooks, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fsmonitor := filepath.Join(root, "fsmonitor.sh")
	writeFile(t, fsmonitor, "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	if err := os.Chmod(fsmonitor, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "config", "core.fsmonitor", fsmonitor)

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.CallToolRaw(ctx, ToolBranch, `{"name":"titan/45-real","create":true}`); err != nil {
		t.Fatalf("branch: %v", err)
	}
	if out, err := s.CallToolRaw(ctx, ToolCommit, `{"message":"a real commit"}`); err != nil {
		t.Fatalf("commit: %v", err)
	} else if !strings.Contains(out, `"exit_code":0`) {
		t.Fatalf("commit failed: %s", out)
	}
	if out, err := s.CallToolRaw(ctx, ToolPush, `{}`); err != nil {
		t.Fatalf("push: %v", err)
	} else if !strings.Contains(out, `"exit_code":0`) {
		t.Fatalf("push failed: %s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a planted hook or fsmonitor command ran")
	}
	out := runGit(t, remote, "rev-parse", "--verify", "refs/heads/titan/45-real")
	if strings.TrimSpace(out) == "" {
		t.Fatal("the branch did not reach the configured remote")
	}
}

// TestNewRequiresTheCheckoutsOwnToplevel: a subdirectory of a checkout is
// refused, so "the checkout" can never mean half of one.
func TestNewRequiresTheCheckoutsOwnToplevel(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "repo")
	sub := filepath.Join(checkout, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Checkout: sub, Runner: &fakeRunner{toplevel: checkout, branch: "b", remote: "origin"}}); err == nil {
		t.Fatal("a directory inside the checkout was accepted as the checkout")
	}
	if _, err := New(Config{Checkout: checkout, Runner: &fakeRunner{toplevel: root, branch: "b", remote: "origin"}}); err == nil {
		t.Fatal("a checkout was accepted when git reports another toplevel")
	}
	s, err := New(Config{Checkout: checkout, Runner: &fakeRunner{toplevel: checkout, branch: "b", remote: "origin"}})
	if err != nil {
		t.Fatalf("a toplevel was refused: %v", err)
	}
	if s == nil {
		t.Fatal("no source")
	}
}

// TestNewRefusesAMissingRemote: the start fails loudly rather than
// offering a push that can only ever fail.
func TestNewRefusesAMissingRemote(t *testing.T) {
	dir := t.TempDir()
	_, err := New(Config{Checkout: dir, Runner: &fakeRunner{toplevel: dir, branch: "b", remote: ""}})
	if err == nil {
		t.Fatal("a checkout with no remote was accepted")
	}
}

// TestNewRefusesAURLAsARemote: a config that names a URL in
// branch.<branch>.remote is not a remote name and never becomes the push
// target.
func TestNewRefusesAURLAsARemote(t *testing.T) {
	dir := t.TempDir()
	for _, remote := range []string{"https://attacker.invalid/x.git", "git@attacker.invalid:x.git", "-x"} {
		if _, err := New(Config{Checkout: dir, Runner: &fakeRunner{toplevel: dir, branch: "b", remote: remote}}); err == nil {
			t.Fatalf("remote %q was accepted", remote)
		}
	}
}

// TestRefusalsAreRecorded is invariant E10: every refusal names the tool,
// the arguments and the reason.
func TestRefusalsAreRecorded(t *testing.T) {
	dir := t.TempDir()
	var logged []string
	f := &fakeRunner{toplevel: dir, branch: "main", remote: "origin"}
	s, err := New(Config{Checkout: dir, Runner: f, Logf: func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, s, ToolPush, `{"remote":"upstream"}`); err == nil {
		t.Fatal("the push was not refused")
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{"remote":"upstream"}`); err == nil {
		t.Fatal("the pre-ask check let the push through")
	}
	if len(logged) != 2 {
		t.Fatalf("logged %d refusals, want 2: %v", len(logged), logged)
	}
	for _, want := range []string{ToolPush, "upstream", "remote"} {
		if !strings.Contains(logged[0], want) {
			t.Errorf("the record lacks %q: %s", want, logged[0])
		}
	}
}

// TestRefuseBeforeAskingRunsNoPush: the structural refusal the session
// calls before opening an approval prompt settles everything it can
// without running a push, a commit or a switch.
func TestRefuseBeforeAskingRunsNoPush(t *testing.T) {
	s, f := newFake(t)
	if err := s.RefuseBeforeAsking(ToolPush, `{"remote":"https://attacker.invalid/x"}`); err == nil {
		t.Fatal("a push to a URL was not refused before asking")
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{"force":true}`); err == nil {
		t.Fatal("a force push was not refused before asking")
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{}`); err != nil {
		t.Fatalf("a push on a work branch was refused for no reason: %v", err)
	}
	if err := s.RefuseBeforeAsking(ToolCommit, `{"message":"m","paths":["../outside"]}`); err == nil {
		t.Fatal("an escaping commit path was not refused before asking")
	}
	if err := s.RefuseBeforeAsking("mes_say", `{}`); err != nil {
		t.Fatalf("a tool this source does not serve was refused: %v", err)
	}
	for _, sub := range []string{"push", "commit", "switch"} {
		if got := f.ran(sub); len(got) != 0 {
			t.Fatalf("RefuseBeforeAsking ran git %s: %v", sub, got)
		}
	}
}

// TestRefuseBeforeAskingRefusesMainWithoutAsking: a main push is settled
// before the operator is asked, so the question is never put to them.
func TestRefuseBeforeAskingRefusesMainWithoutAsking(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRunner{toplevel: dir, branch: "main", remote: "origin"}
	s, err := New(Config{Checkout: dir, Runner: f})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{}`); err == nil {
		t.Fatal("a push on main was not refused before asking")
	}
	if got := f.ran("push"); len(got) != 0 {
		t.Fatalf("a git push ran: %v", got)
	}
}

// TestListToolsAdvertisesTheThreeOperations.
func TestListToolsAdvertisesTheThreeOperations(t *testing.T) {
	s, _ := newFake(t)
	tools, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name] = true
	}
	for _, want := range ToolNames() {
		if !got[want] {
			t.Errorf("%s is not advertised", want)
		}
	}
	if len(tools) != len(ToolNames()) {
		t.Errorf("advertised %d tools, want %d", len(tools), len(ToolNames()))
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

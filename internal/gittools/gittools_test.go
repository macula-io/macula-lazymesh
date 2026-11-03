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
	existing string // the remote this fake's repo actually has
	pushURL  string // remote get-url --push
	gitdir   string // rev-parse --absolute-git-dir
	dirty    string // a local config line the scan must find
	calls    [][]string
}

func (f *fakeRunner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	sub := args[len(hardening)]
	rest := args[len(hardening)+1:]
	switch sub {
	case "rev-parse":
		for _, a := range rest {
			if a == "--absolute-git-dir" {
				return Result{Stdout: f.gitdir + "\n"}, nil
			}
		}
		return Result{Stdout: f.toplevel + "\n"}, nil
	case "symbolic-ref":
		if f.branch == "" {
			return Result{Stderr: "fatal: HEAD is detached", ExitCode: 128}, nil
		}
		return Result{Stdout: f.branch + "\n"}, nil
	case "config":
		if len(rest) >= 1 && rest[0] == "--local" {
			if f.dirty != "" {
				return Result{Stdout: f.dirty + "\n"}, nil
			}
			return Result{ExitCode: 1}, nil
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "branch.") {
				if f.remote == "" {
					return Result{ExitCode: 1}, nil
				}
				return Result{Stdout: f.remote + "\n"}, nil
			}
			if strings.HasPrefix(a, "remote.") {
				return Result{ExitCode: 1}, nil // no remote.<name>.push/.mirror
			}
		}
		return Result{Stdout: f.remote + "\n"}, nil
	case "remote":
		name := rest[len(rest)-1]
		if name != f.existing {
			return Result{Stderr: "error: no such remote", ExitCode: 2}, nil
		}
		if f.pushURL != "" {
			return Result{Stdout: f.pushURL + "\n"}, nil
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

// fakeRepoDir is a directory the fake runner can call a checkout's
// toplevel: the .git it records must exist for the identity check.
func fakeRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newFake(t *testing.T) (*Source, *fakeRunner) {
	t.Helper()
	dir := fakeRepoDir(t)
	f := &fakeRunner{toplevel: dir, branch: "titan/45-work", remote: "origin", existing: "origin", gitdir: filepath.Join(dir, ".git")}
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
	if pushes := f.ran("push"); len(pushes) != 1 || strings.Join(pushes[0], " ") != "push --no-follow-tags origin refs/heads/titan/45-work:refs/heads/titan/45-work" {
		t.Fatalf("pushes = %v, want exactly one fully qualified push of the branch", pushes)
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
	if got := strings.Join(pushes[0], " "); got != "push --no-follow-tags origin refs/heads/titan/45-work:refs/heads/titan/45-work" {
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
	if len(pushes) != 1 || strings.Join(pushes[0], " ") != "push --no-follow-tags origin refs/heads/titan/45-work:refs/heads/titan/45-work" {
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
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Checkout: sub, Runner: &fakeRunner{toplevel: checkout, branch: "b", remote: "origin", existing: "origin", gitdir: filepath.Join(checkout, ".git")}}); err == nil {
		t.Fatal("a directory inside the checkout was accepted as the checkout")
	}
	if _, err := New(Config{Checkout: checkout, Runner: &fakeRunner{toplevel: root, branch: "b", remote: "origin", existing: "origin", gitdir: filepath.Join(checkout, ".git")}}); err == nil {
		t.Fatal("a checkout was accepted when git reports another toplevel")
	}
	s, err := New(Config{Checkout: checkout, Runner: &fakeRunner{toplevel: checkout, branch: "b", remote: "origin", existing: "origin", gitdir: filepath.Join(checkout, ".git")}})
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
	dir := fakeRepoDir(t)
	_, err := New(Config{Checkout: dir, Runner: &fakeRunner{toplevel: dir, branch: "b", remote: "", gitdir: filepath.Join(dir, ".git")}})
	if err == nil {
		t.Fatal("a checkout with no remote was accepted")
	}
}

// TestNewRefusesAURLAsARemote: a config that names a URL in
// branch.<branch>.remote is not a remote name and never becomes the push
// target.
func TestNewRefusesAURLAsARemote(t *testing.T) {
	dir := fakeRepoDir(t)
	for _, remote := range []string{"https://attacker.invalid/x.git", "git@attacker.invalid:x.git", "-x"} {
		if _, err := New(Config{Checkout: dir, Runner: &fakeRunner{toplevel: dir, branch: "b", remote: remote, existing: "origin", gitdir: filepath.Join(dir, ".git")}}); err == nil {
			t.Fatalf("remote %q was accepted", remote)
		}
	}
}

// TestRefusalsAreRecorded is invariant E10: every refusal names the tool,
// the arguments and the reason.
func TestRefusalsAreRecorded(t *testing.T) {
	dir := fakeRepoDir(t)
	var logged []string
	f := &fakeRunner{toplevel: dir, branch: "main", remote: "origin", existing: "origin", gitdir: filepath.Join(dir, ".git")}
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
	dir := fakeRepoDir(t)
	f := &fakeRunner{toplevel: dir, branch: "main", remote: "origin", existing: "origin", gitdir: filepath.Join(dir, ".git")}
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

// ---------------------------------------------------------------------
// Round 2: the review's required changes, each red first.
// ---------------------------------------------------------------------

// TestPushIgnoresRepoConfigThatRedirectsTheTarget is E11: the name
// "origin" is not the target -- the URL it resolves to is. A config that
// rewrites it after New, or that adds refs to the push, is refused.
func TestPushIgnoresRepoConfigThatRedirectsTheTarget(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	evil := filepath.Join(root, "evil.git")
	gitInitBare(t, evil)
	runGit(t, checkout, "remote", "add", "origin", evil)
	writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
	runGit(t, checkout, "add", "f.txt")
	runGit(t, checkout, "commit", "-qm", "init")
	runGit(t, checkout, "switch", "-c", "work")

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the push URL after New: the held one must still win, and the
	// mismatch must stop the push outright.
	other := filepath.Join(root, "other.git")
	gitInitBare(t, other)
	runGit(t, checkout, "config", "remote.origin.pushurl", other)
	if out, err := s.CallToolRaw(context.Background(), ToolPush, `{}`); err == nil {
		t.Fatalf("a rewritten push URL was pushed to: %s", out)
	} else if !strings.Contains(err.Error(), other) && !strings.Contains(err.Error(), evil) {
		t.Fatalf("the refusal does not name the URLs: %v", err)
	}
	if refs := gitOut(t, other, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("the push went to the rewritten URL: %s", refs)
	}
	if refs := gitOut(t, evil, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("the push ran anyway: %s", refs)
	}
}

// TestPushRefusesRepoConfigThatAddsRefs is E11 for push mappings and
// mirror: both change what a push sends, so both are refused outright.
func TestPushRefusesRepoConfigThatAddsRefs(t *testing.T) {
	for _, setting := range [][2]string{
		{"remote.origin.push", "refs/heads/*:refs/heads/*"},
		{"remote.origin.mirror", "true"},
	} {
		t.Run(setting[0], func(t *testing.T) {
			root := t.TempDir()
			checkout := filepath.Join(root, "checkout")
			gitInit(t, checkout)
			bare := filepath.Join(root, "remote.git")
			gitInitBare(t, bare)
			runGit(t, checkout, "remote", "add", "origin", bare)
			writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
			runGit(t, checkout, "add", "f.txt")
			runGit(t, checkout, "commit", "-qm", "init")
			runGit(t, checkout, "switch", "-c", "work")
			runGit(t, checkout, "config", setting[0], setting[1])

			s, err := New(Config{Checkout: checkout})
			if err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(setting[0])) {
					t.Fatalf("New refused %s without naming it: %v", setting[0], err)
				}
				return
			}
			if out, err := s.CallToolRaw(context.Background(), ToolPush, `{}`); err == nil {
				t.Fatalf("push ran with %s set: %s", setting[0], out)
			}
			if refs := gitOut(t, bare, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
				t.Fatalf("the push ran with %s set: %s", setting[0], refs)
			}
		})
	}
}

// TestPushFollowsNoTags is E11 for push.followTags: the flag wins over the
// config, so a tag in the checkout never travels with the branch.
func TestPushFollowsNoTags(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	bare := filepath.Join(root, "remote.git")
	gitInitBare(t, bare)
	runGit(t, checkout, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
	runGit(t, checkout, "add", "f.txt")
	runGit(t, checkout, "commit", "-qm", "init")
	runGit(t, checkout, "switch", "-c", "work")
	// An annotated tag: that is the one push.followTags carries along.
	runGit(t, checkout, "tag", "-a", "-m", "v2", "v2")
	runGit(t, checkout, "config", "push.followTags", "true")

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.CallToolRaw(context.Background(), ToolPush, `{}`); err != nil {
		t.Fatalf("push: %v", err)
	} else if !strings.Contains(out, `"exit_code":0`) {
		t.Fatalf("push failed: %s", out)
	}
	refs := gitOut(t, bare, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/heads/work") {
		t.Fatalf("the branch did not reach the remote: %q", refs)
	}
	if strings.Contains(refs, "refs/tags/") {
		t.Fatalf("a tag travelled with the branch: %q", refs)
	}
}

// TestPushSendsOnlyTheBranchRef is E11 for the argv: a fully qualified
// refspec and --no-follow-tags, so no config can widen what is sent.
func TestPushSendsOnlyTheBranchRef(t *testing.T) {
	s, f := newFake(t)
	if _, err := call(t, s, ToolPush, `{}`); err != nil {
		t.Fatal(err)
	}
	pushes := f.ran("push")
	if len(pushes) != 1 {
		t.Fatalf("pushes = %v", pushes)
	}
	got := strings.Join(pushes[0], " ")
	want := "push --no-follow-tags origin refs/heads/titan/45-work:refs/heads/titan/45-work"
	if got != want {
		t.Fatalf("push = %q, want %q", got, want)
	}
}

// TestApprovalTextNamesTheURLAndBranch: the operator approves what will
// actually run, not an empty argument object.
func TestApprovalTextNamesTheURLAndBranch(t *testing.T) {
	s, _ := newFake(t)
	text, err := s.ApprovalText(ToolPush, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"origin", "https://example.invalid/repo.git", "titan/45-work", "refs/heads/titan/45-work"} {
		if !strings.Contains(text, want) {
			t.Errorf("the approval text lacks %q: %s", want, text)
		}
	}
	if text, err := s.ApprovalText(ToolCommit, `{"message":"m"}`); err != nil || text != `{"message":"m"}` {
		t.Fatalf("a non-push call's approval text changed: (%q, %v)", text, err)
	}
}

// TestRepoConfigThatExecutesIsRefused is the widened E6: one case per key
// the review named that has no fixed value to pin, each refused before any
// git command that could run it.
func TestRepoConfigThatExecutesIsRefused(t *testing.T) {
	for _, setting := range [][2]string{
		{"gpg.program", "/bin/false"},
		{"filter.evil.clean", "/bin/false"},
		{"core.sshCommand", "/bin/false"},
		{"core.askPass", "/bin/false"},
		{"remote.origin.receivepack", "/bin/false"},
		{"remote.origin.uploadpack", "/bin/false"},
		{"include.path", "/etc/gitconfig.evil"},
		{"url.ssh://evil.invalid/.insteadOf", "https://example.invalid/"},
	} {
		t.Run(setting[0], func(t *testing.T) {
			root := t.TempDir()
			checkout := filepath.Join(root, "checkout")
			gitInit(t, checkout)
			bare := filepath.Join(root, "remote.git")
			gitInitBare(t, bare)
			runGit(t, checkout, "remote", "add", "origin", bare)
			writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
			runGit(t, checkout, "add", "f.txt")
			runGit(t, checkout, "commit", "-qm", "init")
			runGit(t, checkout, "switch", "-c", "work")
			runGit(t, checkout, "config", setting[0], setting[1])

			s, err := New(Config{Checkout: checkout})
			if err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(setting[0])) {
					t.Fatalf("New refused %s without naming it: %v", setting[0], err)
				}
				return
			}
			for _, tc := range []struct{ tool, args string }{
				{ToolBranch, `{"name":"another","create":true}`},
				{ToolCommit, `{"message":"m"}`},
				{ToolPush, `{}`},
			} {
				if out, err := s.CallToolRaw(context.Background(), tc.tool, tc.args); err == nil {
					t.Fatalf("%s ran with %s set: %s", tc.tool, setting[0], out)
				} else if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(setting[0])) {
					t.Fatalf("the refusal does not name %s: %v", setting[0], err)
				}
			}
		})
	}
}

// TestPinnedConfigCannotExecute is the other half for the keys that do have
// a fixed value: commit.gpgSign and credential.helper are set to something
// that would fail or run a program, and the pinned command line wins, which
// the commit succeeding proves (signing with no key would fail).
func TestPinnedConfigCannotExecute(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	bare := filepath.Join(root, "remote.git")
	gitInitBare(t, bare)
	runGit(t, checkout, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
	runGit(t, checkout, "add", "f.txt")
	runGit(t, checkout, "config", "commit.gpgSign", "true")
	runGit(t, checkout, "config", "credential.helper", "!/bin/false")
	runGit(t, checkout, "switch", "-c", "work")

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.CallToolRaw(context.Background(), ToolCommit, `{"message":"pinned"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"exit_code":0`) {
		t.Fatalf("the commit was blocked by commit.gpgSign: %s", out)
	}
	if out, err := s.CallToolRaw(context.Background(), ToolPush, `{}`); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"exit_code":0`) {
		t.Fatalf("the push failed under credential.helper: %s", out)
	}
}

// TestEveryCommandPinsTheConfigThatCouldExecute is the other half of the
// widened E6: even with a clean local config, the pins are on every argv.
func TestEveryCommandPinsTheConfigThatCouldExecute(t *testing.T) {
	s, f := newFake(t)
	_, _ = call(t, s, ToolBranch, `{"name":"titan/45-new","create":true}`)
	_, _ = call(t, s, ToolCommit, `{"message":"m"}`)
	_, _ = call(t, s, ToolPush, `{}`)
	want := []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "commit.gpgSign=false",
		"core.sshCommand=ssh", "credential.helper=", "core.askPass=", "protocol.ext.allow=never"}
	for _, c := range f.calls {
		joined := " " + strings.Join(c, " ") + " "
		for _, pin := range want {
			if !strings.Contains(joined, " "+pin+" ") {
				t.Fatalf("git command %v does not pin %s", c, pin)
			}
		}
	}
}

// TestGitDirSwapIsRefused is the review's .git swap case: the pointer is
// checked at New and again before every call, so swapping it mid-session
// stops the call with nothing run.
func TestGitDirSwapIsRefused(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	bare := filepath.Join(root, "remote.git")
	gitInitBare(t, bare)
	runGit(t, checkout, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
	runGit(t, checkout, "add", "f.txt")
	runGit(t, checkout, "commit", "-qm", "init")
	runGit(t, checkout, "switch", "-c", "work")

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	// Swap .git for a file naming an attacker-chosen git directory that is
	// self-consistent: it names this checkout back, so only what New
	// recorded about .git can stop it.
	evilGit := filepath.Join(root, "evil.git")
	gitInitBare(t, evilGit)
	writeFile(t, filepath.Join(evilGit, "gitdir"), filepath.Join(checkout, ".git")+"\n")
	if err := os.Rename(filepath.Join(checkout, ".git"), filepath.Join(root, "real.git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(checkout, ".git"), "gitdir: "+evilGit+"\n")
	if out, err := s.CallToolRaw(context.Background(), ToolCommit, `{"message":"m"}`); err == nil {
		t.Fatalf("a swapped .git was used: %s", out)
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{}`); err == nil {
		t.Fatal("the pre-ask check let a swapped .git through")
	}
}

// TestConfigAddedMidSessionIsRefused: the scan runs before every call, so a
// key written into .git/config after the tool started still stops the next
// call -- write_file reaches that file.
func TestConfigAddedMidSessionIsRefused(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	bare := filepath.Join(root, "remote.git")
	gitInitBare(t, bare)
	runGit(t, checkout, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(checkout, "f.txt"), "x\n")
	runGit(t, checkout, "add", "f.txt")
	runGit(t, checkout, "commit", "-qm", "init")
	runGit(t, checkout, "switch", "-c", "work")

	s, err := New(Config{Checkout: checkout})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "config", "filter.evil.clean", "/bin/false")
	if out, err := s.CallToolRaw(context.Background(), ToolCommit, `{"message":"m"}`); err == nil {
		t.Fatalf("a filter added after start was used: %s", out)
	}
	if err := s.RefuseBeforeAsking(ToolPush, `{}`); err == nil {
		t.Fatal("the pre-ask check let a filter added after start through")
	}
}

// TestGitDirSymlinkIsRefusedAtStart: a checkout whose .git is a symlink is
// never accepted, since every later check would be reading someone else's
// choice.
func TestGitDirSymlinkIsRefusedAtStart(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitInit(t, checkout)
	bare := filepath.Join(root, "remote.git")
	gitInitBare(t, bare)
	runGit(t, checkout, "remote", "add", "origin", bare)
	realGit := filepath.Join(root, "real.git")
	if err := os.Rename(filepath.Join(checkout, ".git"), realGit); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realGit, filepath.Join(checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Checkout: checkout}); err == nil {
		t.Fatal("a symlinked .git was accepted")
	}
}

// TestProtectedNamesAreCaseInsensitive: "Main" on a case-insensitive
// remote is main, so it is refused too, for both push and branch.
func TestProtectedNamesAreCaseInsensitive(t *testing.T) {
	for _, branch := range []string{"Main", "MASTER", "mAin", "refs/heads/Main"} {
		s, f := newFake(t)
		f.branch = branch
		if out, err := call(t, s, ToolPush, `{}`); err == nil {
			t.Fatalf("push on %q was not refused: %s", branch, out)
		}
		if pushes := f.ran("push"); len(pushes) != 0 {
			t.Fatalf("a push ran for %q: %v", branch, pushes)
		}
		if err := s.RefuseBeforeAsking(ToolPush, `{}`); err == nil {
			t.Fatalf("the pre-ask check let %q through", branch)
		}
	}
	for _, name := range []string{"main", "Main", "MASTER", "refs/heads/main"} {
		s, f := newFake(t)
		if out, err := call(t, s, ToolBranch, fmt.Sprintf(`{"name":%q,"create":true}`, name)); err == nil {
			t.Fatalf("branch %q was created or switched to: %s", name, out)
		}
		if got := f.ran("switch"); len(got) != 0 {
			t.Fatalf("git switch ran for %q: %v", name, got)
		}
	}
}

// TestGitEnvIsScrubbed: GIT_* from the operator's environment never reaches
// a command this tool runs.
func TestGitEnvIsScrubbed(t *testing.T) {
	t.Setenv("GIT_DIR", "/evil")
	t.Setenv("GIT_CONFIG_GLOBAL", "/evil/config")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, kv := range gitEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") && name != "GIT_TERMINAL_PROMPT" {
			t.Fatalf("gitEnv passes %s through", name)
		}
	}
	env := strings.Join(gitEnv(), " ")
	if !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("gitEnv does not disable the terminal prompt")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.invalid")
	runGit(t, dir, "config", "user.name", "test")
}

func gitInitBare(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--bare", "-q")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// Package gittools is the coding profile's host-side git tool
// (macula-io/macula-lazymesh#45, slice E of #38): the agent commits and
// pushes its own branch on the checkout's own remote, and cannot push to
// main, master or a tag, cannot force, and cannot reach a different
// remote -- not even when the operator's approval says allow.
//
// The design idea is that what cannot be expressed cannot be smuggled in.
// There are three operations, each with a fixed argv: no argument is ever
// passed through as a flag, no shell is involved, and what the operator
// approves is the resolved command (ApprovalText), not an empty argument
// object.
//
// Nothing the checkout's own config can change is trusted from config:
// the push URL is resolved once, at construction, and compared before
// every push; the argv carries --no-follow-tags and a fully qualified
// refspec; and every invocation pins on its command line the settings
// that would otherwise let a repository run a program
// (core.hooksPath=/dev/null, core.fsmonitor=false, commit.gpgSign=false,
// core.sshCommand=ssh, credential.helper=, core.askPass=,
// protocol.ext.allow=never). Local config that names a filter, a gpg
// program, a url rewrite, an ssh or askpass command, a remote
// receivepack/uploadpack or an include is refused outright, as is a .git
// that is a symlink or that changed after this tool started: a .git
// pointer swapped for an attacker's git directory hands host git a whole
// config, which is arbitrary execution by another route.
//
// This is a ToolSource. It refuses on its own arguments (so a caller that
// forgot to wire the pre-approval check is still safe), and it exposes
// RefuseBeforeAsking and ApprovalText so the session can settle a call
// before the operator is asked, and show them what it resolves to.
package gittools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

const (
	// ToolBranch creates or switches to a branch.
	ToolBranch = "git_branch"
	// ToolCommit commits staged changes, or exactly the named paths.
	ToolCommit = "git_commit"
	// ToolPush pushes the current branch to the checkout's own remote.
	ToolPush = "git_push"

	defaultTimeout = 2 * time.Minute
	maxCommitBytes = 8 << 10
	maxLoggedArgs  = 2 << 10
)

// ToolNames is every tool this source serves: the names an operator can
// put on an allowlist for it.
func ToolNames() []string {
	return []string{ToolBranch, ToolCommit, ToolPush}
}

// IsTool reports whether name is one of this source's tools.
func IsTool(name string) bool {
	return name == ToolBranch || name == ToolCommit || name == ToolPush
}

// hardening is the config every invocation carries. On the command line it
// outranks the repository's own config, so a checkout cannot re-enable its
// hooks, its fsmonitor command, commit signing with its own program, an
// ssh command, a credential helper, an askpass program or an ext: remote
// for a run this tool makes.
var hardening = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "commit.gpgSign=false",
	"-c", "core.sshCommand=ssh",
	"-c", "credential.helper=",
	"-c", "core.askPass=",
	"-c", "protocol.ext.allow=never",
}

// dangerousConfigPattern is every local config key that can run a program
// or move the push target on the commands this tool runs. One test per
// key keeps the list enforced by a failing check rather than by memory.
const dangerousConfigPattern = `^(filter|gpg|url)\.|\.(receivepack|uploadpack)$|^core\.(askpass|sshcommand)$|^include`

// declaredArgs is the whole argument vocabulary. Anything else -- a
// --force, a refspec, a URL, a flag of any spelling -- is not "ignored",
// it is refused, because a tool that ignores arguments eventually gets one
// it should have refused.
var declaredArgs = map[string]map[string]bool{
	ToolBranch: {"name": true, "create": true},
	ToolCommit: {"message": true, "paths": true},
	ToolPush:   {"remote": true},
}

// Logf records a refusal with its arguments and reason; log.Printf fits.
type Logf func(format string, args ...any)

// Result is one finished git process.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs one git command in dir. The tests substitute a recording
// fake, which is how "no git push process ran" is proved.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (Result, error)
}

// ExecRunner runs the real git binary with the operator's GIT_* variables
// removed, so the environment cannot supply a repository, a config file or
// a work tree this tool did not choose, and with prompting turned off so a
// missing credential fails instead of hanging on a terminal nobody is
// watching.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var res Result
	err := cmd.Run()
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("run git: %w", err)
}

// gitEnv is the environment a git command gets: everything the operator
// set except GIT_*, plus a terminal prompt that is off.
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

// Config configures a Source.
type Config struct {
	// Checkout is the repository the tool works in, and must be the
	// checkout's own toplevel.
	Checkout string
	// Runner replaces the real git binary; nil means ExecRunner.
	Runner Runner
	// Logf records refusals; nil means log.Printf.
	Logf Logf
	// Timeout bounds one git command; zero means two minutes.
	Timeout time.Duration
}

// Source implements agent.ToolSource with the three git operations.
type Source struct {
	checkout string
	remote   string
	pushURL  string
	gitDir   gitDirState
	runner   Runner
	logf     Logf
	timeout  time.Duration
}

// gitDirState is what the checkout's .git was when this tool started.
type gitDirState struct {
	// path is the resolved absolute git directory git itself reports.
	path string
	// info is the lstat of <checkout>/.git: compared with os.SameFile, so
	// a .git swapped for another directory is caught even when the name is
	// the same.
	info os.FileInfo
	// file is the contents of <checkout>/.git when it is a regular file
	// (a linked worktree's pointer); empty when it is a directory.
	file string
}

// New resolves the checkout, records what its .git is, and reads the two
// things that must never be re-read from config: the remote's name and the
// URL that name resolves to for a push. It fails loudly rather than
// offering a tool whose calls can only fail.
func New(cfg Config) (*Source, error) {
	if cfg.Checkout == "" {
		return nil, fmt.Errorf("gittools: local_tools.working_dir (the checkout) is required")
	}
	abs, err := filepath.Abs(cfg.Checkout)
	if err != nil {
		return nil, fmt.Errorf("gittools: resolve working_dir %s: %w", cfg.Checkout, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("gittools: resolve working_dir symlinks %s: %w", abs, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("gittools: stat working_dir %s: %w", canonical, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("gittools: working_dir %s is not a directory", canonical)
	}
	runner := cfg.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	s := &Source{checkout: canonical, runner: runner, logf: logf, timeout: timeout}

	ctx := context.Background()
	top, err := s.gitOK(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("gittools: %s is not a git checkout: %w", canonical, err)
	}
	toplevel, err := filepath.EvalSymlinks(strings.TrimSpace(top.Stdout))
	if err != nil {
		return nil, fmt.Errorf("gittools: resolve the checkout's toplevel: %w", err)
	}
	if toplevel != canonical {
		return nil, fmt.Errorf("gittools: working_dir %s is inside the checkout %s, not its toplevel", canonical, toplevel)
	}
	gitDir, err := s.readGitDir(ctx)
	if err != nil {
		return nil, err
	}
	s.gitDir = gitDir
	remote, pushURL, err := s.resolveRemote(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkRepoConfig(ctx); err != nil {
		return nil, err
	}
	if err := s.checkRemoteOverrides(ctx); err != nil {
		return nil, err
	}
	s.remote, s.pushURL = remote, pushURL
	return s, nil
}

// readGitDir records what the checkout's .git is and refuses one that is a
// symlink or that resolves to a git directory outside the checkout (unless
// it is the checkout's own linked-worktree gitdir, which names it back).
func (s *Source) readGitDir(ctx context.Context) (gitDirState, error) {
	dotGit := filepath.Join(s.checkout, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return gitDirState{}, fmt.Errorf("gittools: the checkout has no .git: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return gitDirState{}, fmt.Errorf("gittools: the checkout's .git is a symlink; this tool only works in a checkout whose .git it can vouch for")
	}
	st := gitDirState{info: info}
	switch {
	case info.IsDir():
	case info.Mode().IsRegular():
		content, err := os.ReadFile(dotGit)
		if err != nil {
			return gitDirState{}, fmt.Errorf("gittools: read the checkout's .git pointer: %w", err)
		}
		st.file = string(content)
	default:
		return gitDirState{}, fmt.Errorf("gittools: the checkout's .git is neither a directory nor a gitdir file")
	}
	res, err := s.gitOK(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return gitDirState{}, fmt.Errorf("gittools: resolve the checkout's git directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(res.Stdout))
	if err != nil {
		return gitDirState{}, fmt.Errorf("gittools: resolve the git directory %s: %w", strings.TrimSpace(res.Stdout), err)
	}
	st.path = resolved
	if err := s.checkGitDir(dotGit, st); err != nil {
		return gitDirState{}, err
	}
	return st, nil
}

// checkGitDir refuses a git directory outside the checkout, except the
// checkout's own linked-worktree gitdir, which must name it back.
func (s *Source) checkGitDir(dotGit string, st gitDirState) error {
	sep := string(filepath.Separator)
	if st.path == dotGit || strings.HasPrefix(st.path, dotGit+sep) {
		return nil
	}
	if st.file != "" && strings.TrimSpace(st.file) == "gitdir: "+st.path {
		back, err := os.ReadFile(filepath.Join(st.path, "gitdir"))
		if err == nil && strings.TrimSpace(string(back)) == dotGit {
			return nil
		}
	}
	return fmt.Errorf("gittools: the checkout's git directory %s is outside the checkout", st.path)
}

// verify re-checks, before every call, that the .git this tool recorded is
// still the .git git would use, and that the local config is still clean.
// A .git swapped for a file or a symlink is a whole attacker-chosen config.
func (s *Source) verify(ctx context.Context) error {
	current, err := s.readGitDir(ctx)
	if err != nil {
		return err
	}
	if current.path != s.gitDir.path || !os.SameFile(current.info, s.gitDir.info) || (current.file != "") != (s.gitDir.file != "") {
		return fmt.Errorf("gittools: the checkout's .git changed since this tool started (git directory %s, was %s); refusing to run", current.path, s.gitDir.path)
	}
	if current.file != s.gitDir.file {
		return fmt.Errorf("gittools: the checkout's .git pointer changed since this tool started; refusing to run")
	}
	return s.checkRepoConfig(ctx)
}

// checkRepoConfig refuses a local config that could run a program or move
// the push target on the commands this tool runs. The command-line pins
// cover the settings that have a fixed value; this covers the rest, and a
// config that appears mid-session is caught by verify.
func (s *Source) checkRepoConfig(ctx context.Context) error {
	res, err := s.git(ctx, "config", "--local", "--get-regexp", dangerousConfigPattern)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
	case 1:
		return nil
	default:
		return fmt.Errorf("gittools: read the checkout's local config: %s", firstLine(res.Stderr))
	}
	line := firstLine(res.Stdout)
	key := line
	if fields := strings.Fields(line); len(fields) > 0 {
		key = fields[0]
	}
	return fmt.Errorf("gittools: the checkout's local config sets %s, which can run a program or redirect a push; remove it before using this tool", key)
}

// checkRemoteOverrides refuses config that changes what a push sends or
// where it goes, beyond the URL this tool already holds.
func (s *Source) checkRemoteOverrides(ctx context.Context) error {
	if s.remote == "" {
		return nil
	}
	for _, key := range []string{"remote." + s.remote + ".push", "remote." + s.remote + ".mirror"} {
		res, err := s.git(ctx, "config", "--get", key)
		if err != nil {
			return err
		}
		switch res.ExitCode {
		case 0:
			if strings.TrimSpace(res.Stdout) != "" {
				return fmt.Errorf("gittools: the checkout's config sets %s, which changes what a push sends; remove it", key)
			}
		case 1:
		default:
			return fmt.Errorf("gittools: read %s: %s", key, firstLine(res.Stderr))
		}
	}
	return nil
}

// resolveRemote reads branch.<current>.remote, falling back to origin,
// confirms it names a remote this checkout has, and holds the URL that
// remote's push resolves to.
func (s *Source) resolveRemote(ctx context.Context) (string, string, error) {
	remote := "origin"
	if branch, err := s.currentBranch(ctx); err == nil {
		if res, err := s.gitOK(ctx, "config", "--get", "branch."+branch+".remote"); err == nil {
			if got := strings.TrimSpace(res.Stdout); got != "" {
				remote = got
			}
		}
	}
	if !validRemoteName(remote) {
		return "", "", fmt.Errorf("gittools: the checkout names %q as its remote, which is not a remote name (a URL is never accepted as a push target)", remote)
	}
	url, err := s.pushURLFor(ctx, remote)
	if err != nil {
		return "", "", err
	}
	return remote, url, nil
}

// pushURLFor is the URL a push to name resolves to right now: insteadOf
// and pushInsteadOf rewrites applied, which is exactly what must not
// change between New and the push.
func (s *Source) pushURLFor(ctx context.Context, name string) (string, error) {
	res, err := s.gitOK(ctx, "remote", "get-url", "--push", name)
	if err != nil {
		return "", fmt.Errorf("gittools: the checkout has no remote %q to push to: %w", name, err)
	}
	url := strings.TrimSpace(res.Stdout)
	if url == "" {
		return "", fmt.Errorf("gittools: the remote %q has no push URL", name)
	}
	return url, nil
}

// validRemoteName accepts a configured remote's name and nothing that
// could be a URL, an option or a refspec.
func validRemoteName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/':
		default:
			return false
		}
	}
	return true
}

// ListTools advertises the three operations.
func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{
		{
			Name: ToolBranch,
			Description: "Create or switch to a branch in the checkout. The name is validated with " +
				"git check-ref-format, and main and master are never switched to. Nothing is pushed by this call.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Branch name."},
					"create": map[string]any{"type": "boolean",
						"description": "Create the branch (git switch -c) rather than switch to an existing one."},
				},
				"required": []string{"name"},
			},
		},
		{
			Name: ToolCommit,
			Description: "Commit in the checkout with a message. With no paths it commits what is staged; " +
				"with paths it commits exactly those, which must be inside the checkout.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Commit message."},
					"paths": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional paths, relative to the checkout.",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name: ToolPush,
			Description: "Push the current branch to the checkout's own configured remote, as the ref " +
				"refs/heads/<branch>. It never pushes main, master or a tag, follows no tags, and takes no " +
				"refspec and no URL. The approval prompt shows the remote URL and the ref.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"remote": map[string]any{"type": "string",
						"description": "Optional; must be the checkout's own configured remote."},
				},
			},
		},
	}, nil
}

// RefuseBeforeAsking is the check the session runs outside AskSource, so a
// call this tool will refuse never becomes a question to the operator. It
// records the refusal here, because a refused-before-asking call never
// reaches CallToolRaw to be recorded there.
func (s *Source) RefuseBeforeAsking(tool, argumentsJSON string) error {
	if !IsTool(tool) {
		return nil
	}
	if err := s.validate(context.Background(), tool, argumentsJSON); err != nil {
		s.logf("[gittools] refused %s %s: %s", tool, capArgs(argumentsJSON), err)
		return err
	}
	return nil
}

// ApprovalText is what the operator is shown before a call runs: for a
// push, the remote, the URL it resolves to and the exact ref, so the
// question is about the command, not about an empty argument object. Any
// other call is shown as it was made.
func (s *Source) ApprovalText(tool, argumentsJSON string) (string, error) {
	if tool != ToolPush {
		return argumentsJSON, nil
	}
	args, err := decodeArgs(tool, argumentsJSON)
	if err != nil {
		return "", err
	}
	plan, err := s.pushPlan(context.Background(), args)
	if err != nil {
		s.logf("[gittools] refused %s %s: %s", tool, capArgs(argumentsJSON), err)
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"remote": s.remote,
		"url":    plan.url,
		"branch": plan.branch,
		"pushes": pushRefspec(plan.branch),
	})
	if err != nil {
		return "", fmt.Errorf("gittools: describe the push: %w", err)
	}
	return string(body), nil
}

// CallToolRaw validates first and runs second: a caller that never wired
// RefuseBeforeAsking is still protected by the same checks.
func (s *Source) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	result, err := s.call(ctx, name, argumentsJSON)
	if err != nil {
		s.logf("[gittools] refused %s %s: %s", name, capArgs(argumentsJSON), err)
	}
	return result, err
}

func (s *Source) call(ctx context.Context, name, argumentsJSON string) (string, error) {
	if err := s.validate(ctx, name, argumentsJSON); err != nil {
		return "", err
	}
	args, err := decodeArgs(name, argumentsJSON)
	if err != nil {
		return "", err
	}
	switch name {
	case ToolBranch:
		return s.branch(ctx, args)
	case ToolCommit:
		return s.commit(ctx, args)
	case ToolPush:
		return s.push(ctx, args)
	}
	return "", fmt.Errorf("gittools: unknown tool %q", name)
}

// validate is the one place a call is judged, shared by CallToolRaw and
// RefuseBeforeAsking so the two can never disagree.
func (s *Source) validate(ctx context.Context, name, argumentsJSON string) error {
	args, err := decodeArgs(name, argumentsJSON)
	if err != nil {
		return err
	}
	if !IsTool(name) {
		return fmt.Errorf("gittools: unknown tool %q", name)
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	switch name {
	case ToolBranch:
		_, err := s.branchPlan(ctx, args)
		return err
	case ToolCommit:
		_, err := s.commitPlan(args)
		return err
	case ToolPush:
		_, err := s.pushPlan(ctx, args)
		return err
	}
	return nil
}

// decodeArgs parses the arguments and refuses any key the tool does not
// declare.
func decodeArgs(name, argumentsJSON string) (map[string]any, error) {
	declared, ok := declaredArgs[name]
	if !ok {
		return nil, fmt.Errorf("gittools: unknown tool %q", name)
	}
	args := map[string]any{}
	if strings.TrimSpace(argumentsJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return nil, fmt.Errorf("%s: the arguments are not a JSON object: %w", name, err)
		}
	}
	for key := range args {
		if !declared[key] {
			return nil, fmt.Errorf("%s takes no argument %q: it accepts no flags, no refspec and no URL, and nothing is passed through to git", name, key)
		}
	}
	return args, nil
}

type branchPlan struct {
	name   string
	create bool
}

// branchPlan validates a branch name before anything runs: git's own
// check-ref-format decides what a branch name is, the name must come back
// unchanged (so @{-1}-style expansions and an option in disguise are
// refused rather than resolved), and main and master are never touched.
func (s *Source) branchPlan(ctx context.Context, args map[string]any) (branchPlan, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return branchPlan{}, fmt.Errorf("%s needs a branch \"name\"", ToolBranch)
	}
	if strings.HasPrefix(name, "-") {
		return branchPlan{}, fmt.Errorf("%q starts with a dash, so git could read it as an option; a branch name never does", name)
	}
	if strings.ContainsAny(name, " \t\r\n\x00") {
		return branchPlan{}, fmt.Errorf("%q contains whitespace; a branch name never does", name)
	}
	if protectedRef(name) {
		return branchPlan{}, fmt.Errorf("%q is main or master; this tool works on a branch, never on those", name)
	}
	res, err := s.gitOK(ctx, "check-ref-format", "--branch", name)
	if err != nil {
		return branchPlan{}, fmt.Errorf("%q is not a valid branch name: %s", name, firstLine(res.Stderr))
	}
	if got := strings.TrimSpace(res.Stdout); got != name {
		return branchPlan{}, fmt.Errorf("%q is not a plain branch name (git resolves it to %q)", name, got)
	}
	plan := branchPlan{name: name}
	if raw, ok := args["create"]; ok {
		create, ok := raw.(bool)
		if !ok {
			return branchPlan{}, fmt.Errorf("%s: \"create\" must be true or false", ToolBranch)
		}
		plan.create = create
	}
	return plan, nil
}

func (s *Source) branch(ctx context.Context, args map[string]any) (string, error) {
	plan, err := s.branchPlan(ctx, args)
	if err != nil {
		return "", err
	}
	if plan.create {
		return s.resultJSON(s.git(ctx, "switch", "-c", plan.name))
	}
	return s.resultJSON(s.git(ctx, "switch", plan.name))
}

type commitPlan struct {
	message string
	paths   []string
}

// commitPlan validates the message and every path before anything runs.
func (s *Source) commitPlan(args map[string]any) (commitPlan, error) {
	message, _ := args["message"].(string)
	if strings.TrimSpace(message) == "" {
		return commitPlan{}, fmt.Errorf("%s needs a non-empty \"message\"", ToolCommit)
	}
	if len(message) > maxCommitBytes {
		return commitPlan{}, fmt.Errorf("the commit message is %d bytes, over the %d-byte limit", len(message), maxCommitBytes)
	}
	plan := commitPlan{message: message}
	raw, ok := args["paths"]
	if !ok {
		return plan, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return commitPlan{}, fmt.Errorf("%s: \"paths\" must be a list of paths", ToolCommit)
	}
	for _, item := range list {
		path, ok := item.(string)
		if !ok {
			return commitPlan{}, fmt.Errorf("%s: every entry in \"paths\" must be a string", ToolCommit)
		}
		cleaned, err := s.checkPath(path)
		if err != nil {
			return commitPlan{}, err
		}
		plan.paths = append(plan.paths, cleaned)
	}
	return plan, nil
}

// checkPath refuses a path that could be an option, a pathspec magic, an
// escape from the checkout, or an edit of git's own machinery.
func (s *Source) checkPath(path string) (string, error) {
	switch {
	case path == "":
		return "", fmt.Errorf("%s: an empty path is not a path", ToolCommit)
	case strings.HasPrefix(path, "-"):
		return "", fmt.Errorf("%s: path %q starts with a dash, so git could read it as an option", ToolCommit, path)
	case filepath.IsAbs(path):
		return "", fmt.Errorf("%s: path %q is absolute; paths are relative to the checkout", ToolCommit, path)
	case strings.HasPrefix(path, ":"):
		return "", fmt.Errorf("%s: path %q uses a pathspec magic prefix, which is not a path", ToolCommit, path)
	case strings.ContainsRune(path, 0):
		return "", fmt.Errorf("%s: path %q contains a NUL byte", ToolCommit, path)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return "", fmt.Errorf("%s: path %q names the checkout itself; name the files to commit", ToolCommit, path)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: path %q escapes the checkout", ToolCommit, path)
	}
	sep := string(filepath.Separator)
	if cleaned == ".git" || strings.HasPrefix(cleaned, ".git"+sep) ||
		strings.Contains(cleaned, sep+".git"+sep) || strings.HasSuffix(cleaned, sep+".git") {
		return "", fmt.Errorf("%s: path %q is inside git's own directory", ToolCommit, path)
	}
	if !s.contained(filepath.Join(s.checkout, cleaned)) {
		return "", fmt.Errorf("%s: path %q resolves outside the checkout", ToolCommit, path)
	}
	return cleaned, nil
}

func (s *Source) commit(ctx context.Context, args map[string]any) (string, error) {
	plan, err := s.commitPlan(args)
	if err != nil {
		return "", err
	}
	argv := []string{"commit", "-m", plan.message}
	if len(plan.paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, plan.paths...)
	}
	return s.resultJSON(s.git(ctx, argv...))
}

type pushPlan struct {
	branch string
	url    string
}

// pushPlan is E1-E4 and E11: the only remote is the one whose URL was
// recorded at start, the only ref is the branch checked out now, the
// config cannot add refs or mirror, and a protected target is refused.
func (s *Source) pushPlan(ctx context.Context, args map[string]any) (pushPlan, error) {
	if raw, ok := args["remote"]; ok {
		remote, _ := raw.(string)
		if remote != s.remote {
			return pushPlan{}, fmt.Errorf("a push may only go to the checkout's own configured remote %q; %q is not it", s.remote, fmt.Sprint(raw))
		}
	}
	if err := s.checkRemoteOverrides(ctx); err != nil {
		return pushPlan{}, err
	}
	branch, err := s.currentBranch(ctx)
	if err != nil {
		return pushPlan{}, err
	}
	if protectedRef(branch) {
		return pushPlan{}, fmt.Errorf("this tool never pushes %q: main and master are the operator's to push", branch)
	}
	url, err := s.pushURLFor(ctx, s.remote)
	if err != nil {
		return pushPlan{}, err
	}
	if url != s.pushURL {
		return pushPlan{}, fmt.Errorf("the checkout's push URL for %q is now %s, not the %s it was when this tool started; refusing to push anywhere else", s.remote, url, s.pushURL)
	}
	return pushPlan{branch: branch, url: url}, nil
}

// protectedRef reports whether a ref names something a push or a branch
// operation must never touch, compared case-insensitively: "Main" on a
// case-insensitive remote is main.
func protectedRef(ref string) bool {
	target := strings.ToLower(strings.TrimSpace(ref))
	if strings.HasPrefix(target, "refs/tags/") || strings.HasPrefix(target, "tags/") {
		return true
	}
	switch strings.TrimPrefix(target, "refs/heads/") {
	case "main", "master", "head":
		return true
	}
	return false
}

// pushRefspec is the only ref a push ever sends: the branch, by its full
// name on both sides, so no config can turn it into a pattern or a tag.
func pushRefspec(branch string) string {
	return "refs/heads/" + branch + ":refs/heads/" + branch
}

func (s *Source) push(ctx context.Context, args map[string]any) (string, error) {
	plan, err := s.pushPlan(ctx, args)
	if err != nil {
		return "", err
	}
	return s.resultJSON(s.git(ctx, "push", "--no-follow-tags", s.remote, pushRefspec(plan.branch)))
}

// currentBranch reports the branch checked out now.
func (s *Source) currentBranch(ctx context.Context) (string, error) {
	res, err := s.gitOK(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("the checkout has no branch checked out (detached HEAD), so there is nothing to push: %s", firstLine(res.Stderr))
	}
	branch := strings.TrimSpace(res.Stdout)
	if branch == "" {
		return "", fmt.Errorf("the checkout has no branch checked out, so there is nothing to push")
	}
	return branch, nil
}

// git runs one command with the hardening pins in front of it.
func (s *Source) git(ctx context.Context, sub ...string) (Result, error) {
	argv := append(append([]string{}, hardening...), sub...)
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.runner.Run(ctx, s.checkout, argv...)
}

// gitOK is git for a command whose exit status is a decision, not a
// result: anything but zero becomes an error carrying git's own words.
func (s *Source) gitOK(ctx context.Context, sub ...string) (Result, error) {
	res, err := s.git(ctx, sub...)
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("git %s exited %d: %s", strings.Join(sub, " "), res.ExitCode, firstLine(res.Stderr))
	}
	return res, nil
}

// resultJSON renders a finished git process for the model, the same shape
// shell_exec uses.
func (s *Source) resultJSON(res Result, err error) (string, error) {
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"stdout":    res.Stdout,
		"stderr":    res.Stderr,
		"exit_code": res.ExitCode,
	})
	if err != nil {
		return "", fmt.Errorf("gittools: marshal result: %w", err)
	}
	return string(body), nil
}

// contained reports whether path is inside the checkout.
func (s *Source) contained(path string) bool {
	root := filepath.Clean(s.checkout)
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "(no output)"
	}
	return s
}

func capArgs(s string) string {
	if len(s) <= maxLoggedArgs {
		return s
	}
	return s[:maxLoggedArgs] + "..."
}

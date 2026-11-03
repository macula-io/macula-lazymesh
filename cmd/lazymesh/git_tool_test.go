package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
)

// These tests pin macula-io/macula-lazymesh#45 at the level the operator
// meets it: the git tool is offered only where it belongs, a push the tool
// will refuse never becomes an approval prompt, and an approved push of a
// work branch reaches the checkout's own remote and nothing else.

// gitCheckout builds a real repository with a bare remote and one commit,
// on a branch named main, and returns the checkout and the remote.
func gitCheckout(t *testing.T) (checkout, remote string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	checkout = filepath.Join(root, "checkout")
	remote = filepath.Join(root, "remote.git")
	gitRun(t, root, "init", "--bare", "-q", remote)
	gitRun(t, root, "init", "-q", checkout)
	gitRun(t, checkout, "config", "user.email", "test@example.invalid")
	gitRun(t, checkout, "config", "user.name", "test")
	gitRun(t, checkout, "remote", "add", "origin", remote)
	writeTestFile(t, filepath.Join(checkout, "README.md"), "work\n")
	gitRun(t, checkout, "add", "README.md")
	gitRun(t, checkout, "commit", "-qm", "init")
	gitRun(t, checkout, "branch", "-M", "main")
	return checkout, remote
}

func gitRun(t *testing.T, dir string, args ...string) string {
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitCodingConfig is codingConfig with the execution bit off, the checkout
// pointed at a real repository, and the git tools allowlisted.
func gitCodingConfig(t *testing.T, checkout string) config.Config {
	t.Helper()
	cfg := codingConfig(t)
	cfg.LocalTools = config.LocalTools{Enabled: false, WorkingDir: checkout}
	cfg.ToolAllowlist = append(append([]string{}, everyKnownTool...), "git_branch", "git_commit", "git_push")
	return cfg
}

// gitOnceProvider calls one git tool, then says it is done, and records
// every request it was sent.
type gitOnceProvider struct {
	mu    sync.Mutex
	tool  string
	args  string
	asked bool
	reqs  []provider.ChatRequest
}

func (g *gitOnceProvider) ContextWindow() int { return 1_000_000 }

func (g *gitOnceProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	first := !g.asked
	g.asked = true
	g.mu.Unlock()
	if !first {
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
	}
	call := provider.ToolCall{ID: "g1", Name: g.tool, Arguments: g.args}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}}}, nil
}

func (g *gitOnceProvider) sent() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	body, _ := json.Marshal(g.reqs)
	return string(body)
}

// startGitSession wires the coding profile the way run does, with the
// tool's own RefuseBeforeAsking in place, and returns what a driver needs.
func startGitSession(t *testing.T, cfg config.Config, p provider.Provider) (gen.Node, gen.PID, gen.PID, chan agent.Event) {
	t.Helper()
	policy, gitTool, err := buildCodingToolSource(cfg, &fakeMesh{}, onlyRoom(codingRoom), codingShell{reason: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ergoNode, err := sessionhost.Node(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	rootPid, err := sessionhost.StartRoot(ergoNode)
	if err != nil {
		t.Fatal(err)
	}
	args := codingSessionArgs(cfg, sessionhost.SessionArgs{
		Provider:     p,
		Toolchain:    policy,
		SystemPrompt: "coding test",
	}, gitTool)
	sessPid, err := sessionhost.StartSession(ergoNode, rootPid, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { sessionhost.StopSession(ergoNode, sessPid) })
	events := make(chan agent.Event, 256)
	if _, err := startEventBridge(ergoNode, ctx, sessPid, agent.NewOutlet(ctx.Done(), events), agent.NewOutlet(ctx.Done(), nil), log.New(io.Discard, "", 0), roomwaiter.New(nil, ""), func(agent.Event) {}, counters.New()); err != nil {
		t.Fatal(err)
	}
	return ergoNode, rootPid, sessPid, events
}

// TestGitToolAbsentOutsideTheCodingProfileAndTheAllowlist is the issue's
// fifth red test: no mesh profile, no allowlist entry, no git tool.
func TestGitToolAbsentOutsideTheCodingProfileAndTheAllowlist(t *testing.T) {
	checkout, _ := gitCheckout(t)

	coding := gitCodingConfig(t, checkout)
	if _, ok := gitToolConfig(coding); !ok {
		t.Fatal("the coding profile with an allowlist entry did not build the git tool")
	}

	notNamed := gitCodingConfig(t, checkout)
	notNamed.ToolAllowlist = everyKnownTool
	if _, ok := gitToolConfig(notNamed); ok {
		t.Fatal("the git tool was built although the allowlist does not name it")
	}
	policy, gitTool, err := buildCodingToolSource(notNamed, &fakeMesh{}, onlyRoom(codingRoom), codingShell{reason: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := toolchain.Chain(policy)
	if gitTool != nil {
		t.Fatal("a git source came back without an allowlist entry")
	}
	listed, err := tools.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed {
		if strings.HasPrefix(tool.Name, "git_") {
			t.Fatalf("%s is offered without an allowlist entry", tool.Name)
		}
	}

	mesh := gitCodingConfig(t, checkout)
	mesh.Profile = ""
	if _, ok := gitToolConfig(mesh); ok {
		t.Fatal("the mesh profile built the git tool")
	}
}

// TestGitPushOnMainNeverAsksAndNeverPushes: the refusal is settled before
// the approval prompt, so the operator is never asked about a push that
// cannot run, and nothing reaches the remote.
func TestGitPushOnMainNeverAsksAndNeverPushes(t *testing.T) {
	checkout, remote := gitCheckout(t) // on main
	p := &gitOnceProvider{tool: "git_push", args: `{}`}
	node, root, pid, events := startGitSession(t, gitCodingConfig(t, checkout), p)

	done := make(chan error, 1)
	go func() {
		reply, err := sessionhost.SayTurn(node, root, pid, "push it")
		if err == nil {
			err = reply.Err
		}
		done <- err
	}()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == agent.EventApprovalRequested {
				t.Fatalf("the operator was asked about a push the tool refuses: %+v", ev)
			}
		case <-done:
			if refs := gitRun(t, remote, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "main") {
				t.Fatalf("the refused push reached the remote: %s", refs)
			}
			if !strings.Contains(p.sent(), "main and master are the operator's to push") {
				t.Fatalf("the model never learned why the push was refused:\n%s", p.sent())
			}
			return
		case <-deadline:
			t.Fatal("the turn never finished")
		}
	}
}

// TestGitPushWithApprovalReachesTheConfiguredRemote: a work branch goes to
// the checkout's own remote, once, after the operator says yes.
func TestGitPushWithApprovalReachesTheConfiguredRemote(t *testing.T) {
	checkout, remote := gitCheckout(t)
	gitRun(t, checkout, "switch", "-c", "titan/45-test")
	p := &gitOnceProvider{tool: "git_push", args: `{}`}
	node, root, pid, events := startGitSession(t, gitCodingConfig(t, checkout), p)

	if refs := gitRun(t, remote, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("the remote already has refs: %s", refs)
	}

	done := make(chan error, 1)
	go func() {
		reply, err := sessionhost.SayTurn(node, root, pid, "push it")
		if err == nil {
			err = reply.Err
		}
		done <- err
	}()

	var approvalID string
	deadline := time.After(10 * time.Second)
	for approvalID == "" {
		select {
		case ev := <-events:
			if ev.Kind == agent.EventApprovalRequested && ev.ToolName == "git_push" {
				approvalID = ev.ID
				for _, want := range []string{remote, "refs/heads/titan/45-test", "origin"} {
					if !strings.Contains(ev.Text, want) {
						t.Fatalf("the approval prompt does not name %q: %s", want, ev.Text)
					}
				}
			}
		case err := <-done:
			t.Fatalf("the turn finished without asking (err %v); the push ran unasked", err)
		case <-deadline:
			t.Fatal("no approval request for git_push")
		}
	}
	if refs := gitRun(t, remote, "for-each-ref", "--format=%(refname)"); strings.TrimSpace(refs) != "" {
		t.Fatalf("the push ran before the approval was answered: %s", refs)
	}
	sessionhost.AnswerApproval(pid, approvalID, true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the approved push failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never finished after the approval")
	}
	refs := gitRun(t, remote, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "refs/heads/titan/45-test") {
		t.Fatalf("the approved push did not reach the configured remote: %q", refs)
	}
	if strings.Contains(refs, "main") {
		t.Fatalf("the remote holds something the test never pushed: %q", refs)
	}
}

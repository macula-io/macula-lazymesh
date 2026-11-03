package confinedshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The checks of the threat model on #26 (slice C of #38). Tests that
// need bubblewrap fail, not skip, on Linux without it: CI must prove
// confinement on its own runner.

func requireBwrap(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the confined shell is Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bubblewrap is required to prove confinement: install bwrap")
	}
}

// hostRunner is the unconfined shell lazymesh had before slice C: sh -c
// in the checkout, with the operator's rights and environment.
type hostRunner struct{ dir string }

func (h hostRunner) Run(ctx context.Context, command string) (Result, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = h.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

type layout struct {
	home, checkout, cache string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	l := layout{home: t.TempDir(), checkout: t.TempDir(), cache: t.TempDir()}
	if err := os.Mkdir(filepath.Join(l.checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l layout) prepare(t *testing.T) *Sandbox {
	t.Helper()
	sb, err := Prepare(context.Background(), Options{Checkout: l.checkout, Home: l.home, CacheDir: l.cache})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return sb
}

// TestSelfTestFailsTheUnconfinedShell: the shell lazymesh used before
// this slice fails the self-test, so it can never be offered as the
// coding profile's shell.
func TestSelfTestFailsTheUnconfinedShell(t *testing.T) {
	l := newLayout(t)
	scratch := t.TempDir()
	err := SelfTest(context.Background(), hostRunner{dir: l.checkout}, Probe{Checkout: l.checkout, Scratch: scratch, SecretDir: l.home})
	if err == nil {
		t.Fatal("the unconfined host shell passed the self-test")
	}
}

// TestPrepareRefusesWithoutBubblewrap: no bubblewrap, no shell.
func TestPrepareRefusesWithoutBubblewrap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	l := newLayout(t)
	_, err := Prepare(context.Background(), Options{Checkout: l.checkout, Home: l.home, CacheDir: l.cache, Bwrap: filepath.Join(t.TempDir(), "bwrap")})
	if err == nil {
		t.Fatal("Prepare built a shell without bubblewrap")
	}
}

// TestSelfTestCatchesALeakySandbox: a sandbox left with the network on,
// or with the home directory mounted, fails the self-test.
func TestSelfTestCatchesALeakySandbox(t *testing.T) {
	requireBwrap(t)
	l := newLayout(t)
	probe := func(sb *Sandbox) error {
		return SelfTest(context.Background(), sb, Probe{Checkout: sb.Checkout, Scratch: sb.Scratch, SecretDir: l.home})
	}
	if hostHasOnlyLoopback(t) {
		t.Log("host has only loopback; the network leak cannot be shown here")
	} else {
		netOn := l.prepare(t)
		netOn.shareNet = true
		if err := probe(netOn); err == nil || !strings.Contains(err.Error(), "network") {
			t.Errorf("a sandbox with the network on passed: %v", err)
		}
	}
	homeOn := l.prepare(t)
	homeOn.extraBinds = []string{l.home}
	if err := probe(homeOn); err == nil || !strings.Contains(err.Error(), "readable") {
		t.Errorf("a sandbox that mounts home passed: %v", err)
	}
}

func hostHasOnlyLoopback(t *testing.T) bool {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, _, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) != "lo" {
			return false
		}
	}
	return true
}

// TestApprovedCommandCannotReadAPlantedSecret is the slice's own check:
// an approved command in the real sandbox cannot read a secret outside
// the checkout, cannot see lazymesh's environment, cannot write .git, and
// can still work in the checkout.
func TestApprovedCommandCannotReadAPlantedSecret(t *testing.T) {
	requireBwrap(t)
	l := newLayout(t)
	sb := l.prepare(t)
	secret := filepath.Join(l.home, ".ssh-like", "id_key")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE-KEY-CANARY"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_API_KEY", "API-KEY-CANARY")
	ctx := context.Background()
	res, err := sb.Run(ctx, "cat "+quote(secret)+"; env; echo work > made.txt; touch .git/hooks-planted")
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"PRIVATE-KEY-CANARY", "API-KEY-CANARY"} {
		if strings.Contains(res.Stdout, canary) {
			t.Errorf("%s reached the command's output", canary)
		}
	}
	if got, err := os.ReadFile(filepath.Join(l.checkout, "made.txt")); err != nil || string(got) != "work\n" {
		t.Errorf("the checkout is not writable from inside: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(l.checkout, ".git", "hooks-planted")); err == nil {
		t.Error("a command wrote into .git")
	}
}

// TestFenceRefusesHomeAndPrivatePaths: no mount may be home, hold home,
// or overlap a private path; a project under home is fine.
func TestFenceRefusesHomeAndPrivatePaths(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".config", "lazymesh")
	f := newFence(home, []string{config, filepath.Join(home, "keys", "provider.key")})
	for _, bad := range []string{home, filepath.Dir(home), "/", filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg", "x"),
		filepath.Join(home, ".config"), config, filepath.Join(home, "keys"), "relative/path"} {
		if _, err := f.check(bad, "test path"); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if _, err := f.check(filepath.Join(home, "src", "project"), "test path"); err != nil {
		t.Errorf("a project under home was refused: %v", err)
	}
}

// TestPrepareRefusesHomeAsTheCheckout: lazymesh started in the home
// directory gets no shell, since the checkout is mounted read-write.
func TestPrepareRefusesHomeAsTheCheckout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	l := newLayout(t)
	if _, err := Prepare(context.Background(), Options{Checkout: l.home, Home: l.home, CacheDir: l.cache, Bwrap: "/bin/true"}); err == nil {
		t.Fatal("the home directory was accepted as the checkout")
	}
}

// TestArgsPinTheIsolationFlags: the flags no test without a terminal or
// a second process can observe are pinned in the argument list, among
// them --new-session (no controlling terminal to type into, C5) and
// --die-with-parent.
func TestArgsPinTheIsolationFlags(t *testing.T) {
	sb := &Sandbox{Bwrap: "bwrap", Checkout: "/work/repo", Scratch: "/cache/s"}
	args := strings.Join(sb.Args("true"), " ")
	for _, flag := range []string{"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-ipc",
		"--unshare-uts", "--unshare-net", "--clearenv", "--tmpfs /tmp", "--chdir /work/repo"} {
		if !strings.Contains(args, flag) {
			t.Errorf("bubblewrap is started without %s: %s", flag, args)
		}
	}
	if strings.Index(args, "--tmpfs /tmp") > strings.Index(args, "--bind /cache/s") {
		t.Error("/tmp is mounted after the scratch bind and would hide it")
	}
}

// TestTimeoutEndsTheCommand: the call's timeout ends a confined command
// and whatever it started, promptly.
func TestTimeoutEndsTheCommand(t *testing.T) {
	requireBwrap(t)
	sb := newLayout(t).prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, _ = sb.Run(ctx, "sleep 60 & sleep 60")
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("a timed-out command held the call for %s", took)
	}
}

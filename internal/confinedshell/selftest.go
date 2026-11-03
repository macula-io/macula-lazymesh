package confinedshell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Probe is what the self-test needs to know about the machine: the
// checkout and scratch directory the runner was built for, and a
// directory outside both in which to plant a secret the runner must not
// read (the operator's home, in production).
type Probe struct {
	Checkout  string
	Scratch   string
	Output    string
	SecretDir string
}

// canaryEnv is set in lazymesh's own environment for the length of the
// environment probe; a runner that passes it through leaks every key.
const canaryEnv = "LAZYMESH_SELFTEST_CANARY"

// SelfTest runs r through the probes of the threat model on #26 and
// returns the first one it fails: the command runs at all and can write
// its scratch directory; a secret outside the checkout is unreadable;
// only loopback exists; lazymesh's environment is absent; .git is not
// writable; there is no controlling terminal; nothing written to home
// reaches the real home. A Runner that passes may be offered as the
// shell; one that fails must not be.
func SelfTest(ctx context.Context, r Runner, p Probe) error {
	for _, probe := range []func(context.Context, Runner, Probe) error{
		probeRuns, probeSecret, probeNetwork, probeEnvironment, probeGitDir, probeOutputDir, probeTerminal, probeHomeWrite,
	} {
		if err := probe(ctx, r, p); err != nil {
			return fmt.Errorf("shell self-test: %w", err)
		}
	}
	return nil
}

func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// probeRuns comes first: every other probe expects a failure, and a
// sandbox that cannot start at all would pass them all.
func probeRuns(ctx context.Context, r Runner, p Probe) error {
	mark := filepath.Join(p.Scratch, ".selftest-"+token())
	defer os.Remove(mark)
	res, err := r.Run(ctx, "echo ok && touch "+quote(mark))
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		return fmt.Errorf("a plain command does not run (exit %d, err %v, stderr %q)", res.ExitCode, err, res.Stderr)
	}
	if _, err := os.Stat(mark); err != nil {
		return fmt.Errorf("the scratch directory is not writable from inside")
	}
	return nil
}

func probeSecret(ctx context.Context, r Runner, p Probe) error {
	dir, err := os.MkdirTemp(p.SecretDir, ".lazymesh-selftest-")
	if err != nil {
		return fmt.Errorf("plant the secret: %w", err)
	}
	defer os.RemoveAll(dir)
	secret := token()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return fmt.Errorf("plant the secret: %w", err)
	}
	res, err := r.Run(ctx, "cat "+quote(path))
	if err != nil {
		return err
	}
	if strings.Contains(res.Stdout, secret) || res.ExitCode == 0 {
		return fmt.Errorf("a file outside the checkout is readable (%s)", path)
	}
	return nil
}

func probeNetwork(ctx context.Context, r Runner, p Probe) error {
	res, err := r.Run(ctx, "cat /proc/net/dev")
	if err != nil || res.ExitCode != 0 {
		return fmt.Errorf("cannot list network interfaces (exit %d, err %v)", res.ExitCode, err)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if name = strings.TrimSpace(name); name != "lo" {
			return fmt.Errorf("the network is reachable: interface %q exists inside", name)
		}
	}
	return nil
}

func probeEnvironment(ctx context.Context, r Runner, p Probe) error {
	canary := token()
	if err := os.Setenv(canaryEnv, canary); err != nil {
		return err
	}
	defer os.Unsetenv(canaryEnv)
	res, err := r.Run(ctx, "env")
	if err != nil {
		return err
	}
	if strings.Contains(res.Stdout, canary) {
		return fmt.Errorf("lazymesh's environment reaches the command")
	}
	return nil
}

// probeGitDir writes into the checkout's .git in whichever form it has:
// a file inside the directory, or a new gitdir pointer over the file.
func probeGitDir(ctx context.Context, r Runner, p Probe) error {
	gitDir := filepath.Join(p.Checkout, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return fmt.Errorf("the checkout has no .git to protect: %w", err)
	}
	if !info.IsDir() {
		before, err := os.ReadFile(gitDir)
		if err != nil {
			return err
		}
		if _, err := r.Run(ctx, "printf 'gitdir: .lazymesh-selftest\\n' > "+quote(gitDir)); err != nil {
			return err
		}
		if after, _ := os.ReadFile(gitDir); string(after) != string(before) {
			_ = os.WriteFile(gitDir, before, info.Mode().Perm())
			return fmt.Errorf("the checkout's .git file is writable: a new gitdir pointer runs on the host")
		}
		return nil
	}
	mark := filepath.Join(gitDir, ".lazymesh-selftest-"+token())
	defer os.Remove(mark)
	if _, err := r.Run(ctx, "touch "+quote(mark)); err != nil {
		return err
	}
	if _, err := os.Stat(mark); err == nil {
		return fmt.Errorf("the checkout's .git is writable: a hook planted there runs on the host")
	}
	return nil
}

// probeOutputDir: the directory the host writes long output into must not
// be writable, or replaceable, from inside.
func probeOutputDir(ctx context.Context, r Runner, p Probe) error {
	if p.Output == "" {
		return fmt.Errorf("no output directory to protect")
	}
	mark := filepath.Join(p.Output, ".lazymesh-selftest-"+token())
	defer os.Remove(mark)
	if _, err := r.Run(ctx, "touch "+quote(mark)); err != nil {
		return err
	}
	if _, err := os.Stat(mark); err == nil {
		return fmt.Errorf("the output directory is writable from inside")
	}
	if _, err := r.Run(ctx, "rm -rf "+quote(p.Output)+" && ln -s / "+quote(p.Output)); err != nil {
		return err
	}
	if info, err := os.Lstat(p.Output); err != nil || !info.IsDir() {
		return fmt.Errorf("the output directory was removed or replaced from inside")
	}
	return nil
}

func probeTerminal(ctx context.Context, r Runner, p Probe) error {
	res, err := r.Run(ctx, ": </dev/tty")
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return fmt.Errorf("the command has a controlling terminal and could type into it")
	}
	return nil
}

func probeHomeWrite(ctx context.Context, r Runner, p Probe) error {
	mark := filepath.Join(p.SecretDir, ".lazymesh-selftest-write-"+token())
	defer os.Remove(mark)
	if _, err := r.Run(ctx, "touch "+quote(mark)); err != nil {
		return err
	}
	if _, err := os.Stat(mark); err == nil {
		return fmt.Errorf("a file written outside the checkout reached the real filesystem (%s)", mark)
	}
	return nil
}

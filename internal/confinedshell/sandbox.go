// Package confinedshell runs shell_exec inside bubblewrap, in either
// profile (#26, slice C of #38; one shell for both profiles in #94): an approved
// command sees the checkout and a scratch directory read-write, the
// toolchain read-only, and nothing else of the operator's home; it has
// no network, no controlling terminal, no new privileges, and none of
// lazymesh's environment. Prepare proves that on this machine with a
// self-test before the shell is offered, and returns an error instead of
// a weaker shell when it cannot.
//
// The operator still approves every call. Confinement is what holds when
// a person misreads a command.
package confinedshell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Result is one command's outcome.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs one shell command line. The self-test drives any Runner,
// so a harness that leaks can be shown to fail it.
type Runner interface {
	Run(ctx context.Context, command string) (Result, error)
}

// Sandbox is a bubblewrap invocation for one checkout.
type Sandbox struct {
	// Bwrap is the bubblewrap binary.
	Bwrap string
	// Checkout is mounted read-write and is the working directory.
	Checkout string
	// GitReadOnly is the checkout's .git, directory or file, and a
	// gitdir it points to inside the checkout, each mounted read-only
	// over the checkout: nothing inside can plant a hook, a config or a
	// new gitdir pointer that the host's git later runs.
	GitReadOnly []string
	// Scratch is mounted read-write: HOME and the build cache live here.
	Scratch string
	// Output is mounted read-only: the host writes long command output
	// here for the shell to page through, and the shell cannot swap the
	// directory for a link into the host's files.
	Output string
	// ReadOnly is every other path mounted, read-only, at its own path.
	ReadOnly []string
	// Env is the whole environment inside, as KEY=value.
	Env []string
	// shareNet and extraBinds exist only so tests can build a leaky
	// sandbox and watch the self-test catch it.
	shareNet   bool
	extraBinds []string
	outputRW   bool
}

// Args is the bubblewrap argument list for command.
func (s *Sandbox) Args(command string) []string {
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
	}
	if !s.shareNet {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--clearenv")
	for _, kv := range s.Env {
		key, value, _ := strings.Cut(kv, "=")
		args = append(args, "--setenv", key, value)
	}
	args = append(args, systemMounts()...)
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	ro := append([]string{}, s.ReadOnly...)
	sort.Strings(ro)
	for _, p := range ro {
		args = append(args, "--ro-bind", p, p)
	}
	for _, p := range s.extraBinds {
		args = append(args, "--ro-bind", p, p)
	}
	args = append(args,
		"--bind", s.Scratch, s.Scratch,
		"--bind", s.Checkout, s.Checkout)
	switch {
	case s.Output != "" && s.outputRW:
		args = append(args, "--bind", s.Output, s.Output)
	case s.Output != "":
		args = append(args, "--ro-bind", s.Output, s.Output)
	}
	for _, p := range s.GitReadOnly {
		args = append(args, "--ro-bind", p, p)
	}
	return append(args, "--chdir", s.Checkout, "--", "/bin/sh", "-c", command)
}

// Run runs command in the sandbox, with no stdin.
func (s *Sandbox) Run(ctx context.Context, command string) (Result, error) {
	cmd := exec.CommandContext(ctx, s.Bwrap, s.Args(command)...)
	// A command that leaves a child holding its output open must not
	// hold the call past its timeout.
	cmd.WaitDelay = 2 * time.Second
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

// systemDirs are the toolchain's own directories. Each is mounted
// read-only when it is a directory, or recreated as the same symlink on
// a merged-/usr system.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64"}

// etcFiles are the few /etc entries programs need to start and name
// users; nothing that holds a credential or a network setting.
var etcFiles = []string{
	"/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/ld.so.cache", "/etc/ld.so.conf",
	"/etc/ld.so.conf.d", "/etc/alternatives", "/etc/localtime", "/etc/ssl/certs", "/etc/ca-certificates",
}

func systemMounts() []string {
	var args []string
	for _, dir := range systemDirs {
		info, err := os.Lstat(dir)
		switch {
		case err != nil:
		case info.Mode()&os.ModeSymlink != 0:
			if target, err := os.Readlink(dir); err == nil {
				args = append(args, "--symlink", target, dir)
			}
		case info.IsDir():
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	for _, f := range etcFiles {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "--ro-bind", f, f)
		}
	}
	return args
}

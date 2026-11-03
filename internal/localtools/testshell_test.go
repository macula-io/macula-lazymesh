package localtools

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/macula-io/macula-lazymesh/internal/confinedshell"
)

// unconfinedTestShell is the shell these tests drive: a plain `sh -c` in dir,
// test code only.
//
// Production has none any more (#94): shell_exec always runs in a
// confinedshell.Sandbox, for both profiles, and a profile with no sandbox has
// no shell at all. The mechanics proven here -- paging a long result, catching
// a command's own diff, pinning git's fsmonitor -- are about what happens to a
// command's output, so a sandbox would only make them slower. Confinement
// itself is proven by confinedshell's own tests and by escape_attempts_test.go,
// which prepares a real sandbox.
type unconfinedTestShell struct{ dir string }

func testShell(dir string) confinedshell.Runner {
	return unconfinedTestShell{dir: dir}
}

func (h unconfinedTestShell) Run(ctx context.Context, command string) (confinedshell.Result, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = h.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	res := confinedshell.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			return res, runErr
		}
		res.ExitCode = exitErr.ExitCode()
	}
	return res, nil
}

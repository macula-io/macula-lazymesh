//go:build !windows

package realmjoin

import (
	"errors"
	"os/exec"
	"syscall"
)

// ownProcessGroup gives the join command its own process group and makes
// cancellation kill the group rather than the one process exec.CommandContext
// would kill.
//
// It matters because the command is a wrapper: npx starts the CLI, and the CLI
// can start more again. The direct child dying does not close stdout when a
// grandchild is the one holding it, so the NDJSON scan never reaches EOF,
// Join's channel never closes, and a cancellation looks like a hang that
// outlives the caller's timeout. Killing the group ends all of it.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// ESRCH: everything in the group is already gone, which is the outcome
		// this is after rather than a failure to report.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}

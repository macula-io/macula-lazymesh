//go:build windows

package realmjoin

import "os/exec"

// ownProcessGroup is the unix half of the cancellation contract, and Windows
// has no process group to join: killing a tree there wants a job object
// (CreateJobObject + AssignProcessToJobObject with KILL_ON_JOB_CLOSE), which
// this package does not create yet. So a cancellation on Windows behaves as
// exec.CommandContext always has -- it kills the direct child -- and a
// grandchild holding stdout keeps the stream open until it exits on its own.
// The read end is still closed when ctx is done, so Join's channel does close;
// what Windows does not get is the tree dying with it.
func ownProcessGroup(cmd *exec.Cmd) {}

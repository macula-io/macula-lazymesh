package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/confinedshell"
)

// One confined shell for both profiles (#94). The mesh profile used to get a
// plain `sh -c` with the operator's own rights; it now goes through
// confinedshell like the coding profile, and where the sandbox cannot be
// prepared there is no shell_exec at all.

// With no sandbox, the mesh profile refuses shell_exec and runs nothing. This
// is the whole point of the change: the "fails closed" property can no longer
// depend on which profile an operator happens to run.
func TestMeshProfileRefusesShellExecWithoutASandbox(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "the-command-ran")

	cfg := config.Default()
	cfg.LocalTools.Enabled = true
	cfg.LocalTools.WorkingDir = dir

	src, err := meshLocalTools(cfg, codingShell{reason: "no shell in the mesh profile: bubblewrap is not installed"})
	if err != nil {
		t.Fatalf("meshLocalTools: %v", err)
	}

	_, err = src.CallToolRaw(context.Background(), "shell_exec", `{"command":"touch `+marker+`"}`)
	if err == nil {
		t.Fatal("shell_exec ran although no sandbox was prepared: the mesh profile must have no unconfined shell")
	}
	if !strings.Contains(err.Error(), "bubblewrap is not installed") {
		t.Fatalf("the refusal does not name the reason there is no shell: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the command ran despite the refusal")
	}
}

// With a sandbox, the mesh profile's shell runs inside it: a command that
// reaches outside the checkout is refused by the sandbox, not merely asked not
// to. Skipped where bubblewrap is not installed, like the coding profile's own
// escape tests.
func TestMeshProfileShellRunsInsideTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed, so there is no sandbox to test")
	}
	root := t.TempDir()
	co, home, cache := filepath.Join(root, "co"), filepath.Join(root, "home"), filepath.Join(root, "cache")
	for _, d := range []string{co, home, cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sb, err := confinedshell.Prepare(context.Background(), confinedshell.Options{Checkout: co, Home: home, CacheDir: cache})
	if err != nil {
		t.Skipf("no sandbox on this machine: %v", err)
	}

	cfg := config.Default()
	cfg.LocalTools.Enabled = true
	cfg.LocalTools.WorkingDir = co

	src, err := meshLocalTools(cfg, codingShell{sandbox: sb})
	if err != nil {
		t.Fatalf("meshLocalTools: %v", err)
	}

	out, err := src.CallToolRaw(context.Background(), "shell_exec", `{"command":"cat /etc/hostname"}`)
	if err == nil {
		t.Fatalf("the mesh profile's shell read a path outside the checkout: %.200s", out)
	}
}

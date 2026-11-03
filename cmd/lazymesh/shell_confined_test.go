package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/confinedshell"
	"github.com/macula-io/macula-lazymesh/internal/localtools"
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
// reads a path outside the checkout fails there, not merely the approval
// being withheld. The checkout is a real repository (git init), since the
// sandbox mounts its .git read-only and refuses a working directory without
// one. A missing bubblewrap skips only off Linux; on Linux it is required,
// as for the coding profile's own escape tests.
func TestMeshProfileShellRunsInsideTheSandbox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the confined shell is Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bubblewrap is required to prove confinement: install bwrap")
	}
	root := t.TempDir()
	co, home, cache := filepath.Join(root, "co"), filepath.Join(root, "home"), filepath.Join(root, "cache")
	for _, d := range []string{co, home, cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", co).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", co, err, out)
	}
	sb, err := confinedshell.Prepare(context.Background(), confinedshell.Options{Checkout: co, Home: home, CacheDir: cache})
	if err != nil {
		t.Fatalf("prepare the sandbox: %v", err)
	}

	cfg := config.Default()
	cfg.LocalTools.Enabled = true
	cfg.LocalTools.WorkingDir = co

	src, err := meshLocalTools(cfg, codingShell{sandbox: sb})
	if err != nil {
		t.Fatalf("meshLocalTools: %v", err)
	}

	// The shell works at all: without this, a sandbox that refuses
	// everything would pass the escape assertion below.
	inside := runMeshShell(t, src, `{"command":"echo inside-the-sandbox"}`)
	if inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "inside-the-sandbox" {
		t.Fatalf("the mesh profile's shell did not run a plain command: exit %d, stdout %q, stderr %q", inside.ExitCode, inside.Stdout, inside.Stderr)
	}

	// And it is confined: a file the operator keeps outside the checkout
	// is not there. A failing command is a result, not a tool error
	// (shell_exec reports exit_code), so the assertion is on the result.
	secret := filepath.Join(root, "outside-the-checkout")
	if err := os.WriteFile(secret, []byte("operator-secret-"+tokenForTest()), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := runMeshShell(t, src, `{"command":"cat `+secret+`"}`)
	if outside.ExitCode == 0 {
		t.Fatalf("the mesh profile's shell read a file outside the checkout: %.200s", outside.Stdout)
	}
	if strings.Contains(outside.Stdout, "operator-secret-") {
		t.Fatalf("the mesh profile's shell printed a file outside the checkout: %q", outside.Stdout)
	}
	if strings.TrimSpace(outside.Stderr) == "" {
		t.Fatalf("the refusal is silent: exit %d, no stderr", outside.ExitCode)
	}
}

func tokenForTest() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// shellResult is the part of shell_exec's JSON answer these tests read.
type shellResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func runMeshShell(t *testing.T, src *localtools.Source, args string) shellResult {
	t.Helper()
	out, err := src.CallToolRaw(context.Background(), "shell_exec", args)
	if err != nil {
		t.Fatalf("shell_exec %s: %v", args, err)
	}
	var res shellResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("shell_exec %s returned %.200s: %v", args, out, err)
	}
	return res
}

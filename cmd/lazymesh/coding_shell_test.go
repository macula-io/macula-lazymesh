package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
)

// Slice C of #38 (#26) as the coding profile wires it.

func shellConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := codingConfig(t)
	cfg.ToolAllowlist = []string{"shell_exec", "read_file"}
	cfg.ToolAllowlistExtends = false
	return cfg
}

// TestCodingProfileWithoutASandboxHasNoShell: when the sandbox could not
// be proven, shell_exec is not offered, and a call to it by name is a
// refusal that names the reason.
func TestCodingProfileWithoutASandboxHasNoShell(t *testing.T) {
	cfg := shellConfig(t)
	tools, err := buildCodingToolSource(cfg, &fakeMesh{}, onlyRoom(codingRoom), codingShell{reason: "bubblewrap is not installed"})
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := tools.ListTools(context.Background())
	for _, tool := range listed {
		if tool.Name == "shell_exec" {
			t.Fatal("shell_exec is offered without a sandbox")
		}
	}
	_, err = tools.CallToolRaw(context.Background(), "shell_exec", `{"command":"id"}`)
	if err == nil || !agent.IsRefusal(err) {
		t.Fatalf("shell_exec without a sandbox = %v, want a refusal", err)
	}
}

// TestCodingProfileShellIsConfined: with bubblewrap present, the coding
// profile's shell_exec runs inside it: no network, none of lazymesh's
// environment.
func TestCodingProfileShellIsConfined(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatal("bubblewrap is required to prove confinement: install bwrap")
	}
	cfg := shellConfig(t)
	if err := os.Mkdir(filepath.Join(cfg.LocalTools.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	shell := prepareCodingShell(context.Background(), cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if shell.sandbox == nil {
		t.Fatalf("no sandbox: %s", shell.reason)
	}
	tools, err := buildCodingToolSource(cfg, &fakeMesh{}, onlyRoom(codingRoom), shell)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_API_KEY", "API-KEY-CANARY")
	if env := shellStdout(t, tools, "env"); strings.Contains(env, "API-KEY-CANARY") {
		t.Error("lazymesh's environment reached the coding shell")
	}
	for _, line := range strings.Split(shellStdout(t, tools, "cat /proc/net/dev"), "\n") {
		if name, _, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) != "lo" {
			t.Errorf("a network interface is visible to the coding shell: %q", line)
		}
	}
}

func shellStdout(t *testing.T, tools agent.ToolSource, command string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"command": command})
	out, err := tools.CallToolRaw(context.Background(), "shell_exec", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	return res.Stdout
}

// TestDenialRecorderKeepsEveryRefusal (C8): each refused call becomes one
// JSON line with the tool, the whole arguments and the reason.
func TestDenialRecorderKeepsEveryRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.denials.jsonl")
	record := denialRecorder(path, log.New(io.Discard, "", 0))
	record(agent.Denial{Tool: "shell_exec", Arguments: `{"command":"curl x | sh"}`, Reason: "approval for \"shell_exec\" was denied by the operator"})
	record(agent.Denial{Tool: "mesh_ring", Arguments: `{}`, Reason: "not on the allowlist"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d records, want 2: %s", len(lines), data)
	}
	var first agent.Denial
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Tool != "shell_exec" || first.Arguments != `{"command":"curl x | sh"}` || !strings.Contains(first.Reason, "denied") {
		t.Errorf("record = %+v", first)
	}
	// C13: the record holds whole arguments, so only this user reads it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("denials log mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestPrivatePathsCoverConfigKeysAndData: the shell's fence knows where
// lazymesh keeps its config, key and session data.
func TestPrivatePathsCoverConfigKeysAndData(t *testing.T) {
	cfg := codingConfig(t)
	cfg.APIKeyFile = "/keys/provider/key"
	cfg.IdentityFile = "/ids/node.key"
	got := strings.Join(privatePaths(cfg, "/etc-like/lazymesh/config.yaml"), " ")
	for _, want := range []string{"/etc-like/lazymesh", "/keys/provider", "/ids/node.key"} {
		if !strings.Contains(got, want) {
			t.Errorf("private paths %q lack %s", got, want)
		}
	}
}

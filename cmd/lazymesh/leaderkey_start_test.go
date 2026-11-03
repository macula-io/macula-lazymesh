//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A leader_key that would break typing or quitting stops the program before
// anything opens, naming the key and the reason (#63). The check is on the
// real binary with its real flag, because where it happens matters: after the
// TUI opens, the operator would be looking at a hint row naming a key that
// arms nothing.
func TestStartRefusesUnusableLeaderKey(t *testing.T) {
	bin := buildLazymesh(t)

	cases := []struct {
		name   string
		leader string
		want   string
	}{
		{"a key the message box uses", "ctrl+e", "message box"},
		{"the quit key", "ctrl+c", "quit"},
		{"not a ctrl key", "g", "ctrl"},
		{"ctrl and the wrong width", "ctrl+shift+x", "ctrl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte("leader_key: "+c.leader+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(bin, "--config", path)
			// HOME is redirected so nothing reads the operator's own config;
			// the TUI never opens, so no terminal is needed.
			cmd.Env = append(cmd.Environ(), "HOME="+dir)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("lazymesh started with leader_key %q, want it refused:\n%s", c.leader, out)
			}
			text := string(out)
			if !strings.Contains(text, "leader_key") {
				t.Fatalf("the refusal does not name the setting:\n%s", text)
			}
			if !strings.Contains(text, c.leader) {
				t.Fatalf("the refusal does not name the key %q:\n%s", c.leader, text)
			}
			if !strings.Contains(text, c.want) {
				t.Fatalf("the refusal does not say why (%q):\n%s", c.want, text)
			}
		})
	}
}

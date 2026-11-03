package confinedshell

import (
	"path/filepath"
	"testing"
)

// A private lazymesh data root fences everything beside sessions/, the
// prompt history included (#78 gap 5 review; Pluto's probe, with the root
// main.go now passes).
func TestThePrivateDataRootFencesThePromptHistory(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share", "lazymesh")
	f := newFence(home, []string{data})
	for _, p := range []string{filepath.Join(data, "sessions"), filepath.Join(data, "history"), filepath.Join(data, "history", "fp.jsonl")} {
		if _, err := f.check(p, "read-only path"); err == nil {
			t.Fatalf("%s is accepted as a sandbox mount", p)
		}
	}
}

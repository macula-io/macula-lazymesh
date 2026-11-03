package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/prompthistory"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
)

// The confined shell is fenced off from every lazymesh data directory, the
// prompt history beside sessions/ included (#78 gap 5 review): a
// read_only_paths entry or a checkout at or under it would otherwise mount
// every recent prompt into the sandbox, where model-run code can read it.
func TestPrivatePathsFenceThePromptHistory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dataDir, err := sessionstore.DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	history := filepath.Dir(prompthistory.Path(filepath.Dir(dataDir), "fp"))

	for _, p := range privatePaths(codingConfig(t), "") {
		if history == p || strings.HasPrefix(history, p+string(filepath.Separator)) {
			return
		}
	}
	t.Fatalf("no private path covers the prompt history %s: %q", history, privatePaths(codingConfig(t), ""))
}

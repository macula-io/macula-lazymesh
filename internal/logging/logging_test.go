package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// Every line in agent.log must say which subsystem produced it, whether
// it came from new slog code, the stdlib bridge, or the package-level
// default.
func TestEveryPathTagsItsComponent(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelDebug)

	s.For("mcp").Info("via slog")
	s.StdFor("realm").Printf("via the stdlib bridge")
	s.SetDefault()
	slog.Info("via the package-level default")

	lines := strings.Split(strings.TrimSpace(file.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("file has %d lines, want 3:\n%s", len(lines), file.String())
	}
	want := map[string]string{
		"via slog":                      "component=mcp",
		"via the stdlib bridge":         "component=realm",
		"via the package-level default": "component=lazymesh",
	}
	for msg, tag := range want {
		found := false
		for _, l := range lines {
			if strings.Contains(l, msg) {
				found = true
				if !strings.Contains(l, tag) {
					t.Errorf("line for %q is missing %s: %s", msg, tag, l)
				}
			}
		}
		if !found {
			t.Errorf("no line in the file for %q", msg)
		}
	}
}

// The load-bearing promise: the three existing SetLogger(*log.Logger)
// call sites keep their signature and their Printf output still reaches
// agent.log. If this regresses, a signal that exists today goes silent.
func TestStdLoggerBridgeReachesTheFile(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelInfo)

	s.StdFor("mcp").Printf("respawn failed: %v", errors.New("boom"))

	got := file.String()
	if !strings.Contains(got, "respawn failed: boom") {
		t.Errorf("file got %q, want it to contain the Printf output", got)
	}
	if !strings.Contains(got, "component=mcp") {
		t.Errorf("file entry is missing the component tag: %q", got)
	}
	if !strings.Contains(got, "level=INFO") {
		t.Errorf("bridged output should be written at INFO: %q", got)
	}
}

func TestLevelFilteringApplies(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelWarn)

	l := s.For("agent")
	l.Debug("dropped-debug")
	l.Info("dropped-info")
	l.Warn("kept-warn")
	l.Error("kept-error")

	got := file.String()
	for _, dropped := range []string{"dropped-debug", "dropped-info"} {
		if strings.Contains(got, dropped) {
			t.Errorf("%q should have been filtered out below Warn", dropped)
		}
	}
	for _, kept := range []string{"kept-warn", "kept-error"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q should have been written", kept)
		}
	}
}

// Structured attributes are the other thing this package adds over the
// old Printf-only logger.
func TestStructuredAttrsAreWritten(t *testing.T) {
	var file bytes.Buffer
	s := New(&file, slog.LevelInfo)

	s.For("mesh").Error("call failed", slog.String("procedure", "mesh_list_realms"))

	got := file.String()
	if !strings.Contains(got, "procedure=mesh_list_realms") {
		t.Errorf("structured attr missing from file line: %q", got)
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("level missing from file line: %q", got)
	}
}

// slog.Default()'s own handler writes to STDERR, which corrupts the alt
// screen. After SetDefault, a package-level call must land in the file.
func TestSetDefaultRoutesPackageLevelCallsToTheFile(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	var file bytes.Buffer
	s := New(&file, slog.LevelInfo)
	s.SetDefault()

	slog.Warn("stray call from somewhere else")

	if !strings.Contains(file.String(), "stray call from somewhere else") {
		t.Errorf("package-level slog call did not reach the file: %q", file.String())
	}
}

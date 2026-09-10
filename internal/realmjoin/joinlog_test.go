package realmjoin

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// nil is the default and means silence, matching mcpclient.Client,
// meshservices.Source and ringwaiter.Manager. Every existing test in
// this package runs with no logger set, so this must never panic.
func TestLogfIsSilentWithoutALogger(t *testing.T) {
	SetLogger(nil)
	t.Cleanup(func() { SetLogger(nil) })

	logf("realm join starting: %s", "io.macula")
}

// The reason a join failed must survive the TUI panel being dismissed,
// which only happens if logf actually reaches the logger main.go sets.
func TestLogfWritesThroughSetLogger(t *testing.T) {
	var buf bytes.Buffer
	SetLogger(log.New(&buf, "", 0))
	t.Cleanup(func() { SetLogger(nil) })

	logf("realm join %s for %s: %s", "error", "io.macula", "session expired")

	want := "realm join error for io.macula: session expired"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("logger got %q, want it to contain %q", buf.String(), want)
	}
}

// Clearing the logger must actually stop writes, or a test that sets one
// leaks it into every later test in the package.
func TestSetLoggerNilStopsWrites(t *testing.T) {
	var buf bytes.Buffer
	SetLogger(log.New(&buf, "", 0))
	SetLogger(nil)
	t.Cleanup(func() { SetLogger(nil) })

	logf("should not appear")

	if buf.Len() != 0 {
		t.Errorf("logger still received %q after SetLogger(nil)", buf.String())
	}
}

package realmjoin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEvent_Terminal(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"session", false},
		{"confirmed", true},
		{"already_joined", true},
		{"expired", true},
		{"timeout", true},
		{"error", true},
		{"spawn_error", true},
	}
	for _, c := range cases {
		if got := (Event{Kind: c.kind}).Terminal(); got != c.want {
			t.Errorf("Terminal() for kind %q = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestSpawnEnv_CarriesOnlyTheAllowlistPlusIdentity(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/x")
	t.Setenv("SOME_SECRET", "leak-me-not")

	env := spawnEnv("/tmp/identity.seed")

	got := map[string]bool{}
	for _, kv := range env {
		got[kv] = true
	}
	if !got["PATH=/usr/bin"] || !got["HOME=/home/x"] {
		t.Fatalf("expected PATH/HOME forwarded, got %v", env)
	}
	if !got["MACULA_MCP_IDENTITY=/tmp/identity.seed"] {
		t.Fatalf("expected MACULA_MCP_IDENTITY set to the passed identityFile, got %v", env)
	}
	for _, kv := range env {
		if kv == "SOME_SECRET=leak-me-not" {
			t.Fatalf("expected SOME_SECRET NOT forwarded (not on the allowlist), got %v", env)
		}
	}
}

func TestJoin_RejectsEmptyArgumentsWithoutSpawningAnything(t *testing.T) {
	ctx := context.Background()
	if _, err := Join(ctx, "", "id", "io.macula"); err == nil {
		t.Fatalf("expected an error for empty version")
	}
	if _, err := Join(ctx, "0.27.0", "", "io.macula"); err == nil {
		t.Fatalf("expected an error for empty identityFile -- joining under an unrelated identity must never happen silently")
	}
	if _, err := Join(ctx, "0.27.0", "id", ""); err == nil {
		t.Fatalf("expected an error for empty realmName")
	}
}

// fakeScript writes a shell script at dir/name that prints each of lines
// to stdout, one per line, then exits with exitCode. Used in place of
// the real npx/macula-mcp-realm invocation via newCommand -- this
// package's tests never spawn npx or touch the network, matching the
// discipline macula-mcp's own bin/realm.ts tests already use (fake
// fetch, never a live call).
func fakeScript(t *testing.T, lines []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-realm-cli.sh")
	script := "#!/bin/sh\n"
	for _, l := range lines {
		script += "echo '" + l + "'\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return path
}

func withFakeCommand(t *testing.T, scriptPath string) {
	t.Helper()
	original := newCommand
	newCommand = func(ctx context.Context, version, realmName string) *exec.Cmd {
		return exec.CommandContext(ctx, scriptPath)
	}
	t.Cleanup(func() { newCommand = original })
}

func drain(t *testing.T, ch <-chan Event, timeout time.Duration) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for the events channel to close, got so far: %+v", out)
		}
	}
}

func TestJoin_StreamsEachNDJSONLineAsAnEventInOrder(t *testing.T) {
	script := fakeScript(t, []string{
		`{"event":"session","realm":"io.macula","join_url":"https://realm.macula.io/join/s1","expires_at":"2026-09-08T20:00:00Z","qr_terminal":"QR"}`,
		`{"event":"confirmed","realm":"io.macula","org_identity":"mri:org:io.macula/rgfaber","handle":"rgfaber"}`,
	}, 0)
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "session" || got[0].JoinURL != "https://realm.macula.io/join/s1" {
		t.Fatalf("unexpected first event: %+v", got[0])
	}
	if got[1].Kind != "confirmed" || got[1].Handle != "rgfaber" {
		t.Fatalf("unexpected second event: %+v", got[1])
	}
	if !got[1].Terminal() {
		t.Fatalf("expected the confirmed event to be terminal")
	}
}

func TestJoin_UnparseableLineBecomesASpawnErrorEventInsteadOfSilentlyDropping(t *testing.T) {
	script := fakeScript(t, []string{`not json at all`}, 0)
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 1 || got[0].Kind != "spawn_error" {
		t.Fatalf("expected exactly one spawn_error event, got %+v", got)
	}
}

func TestJoin_ProcessFailingBeforeAnyEventReportsSpawnError(t *testing.T) {
	script := fakeScript(t, nil, 1) // exits 1, prints nothing
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 1 || got[0].Kind != "spawn_error" {
		t.Fatalf("expected exactly one spawn_error event for a silent non-zero exit, got %+v", got)
	}
}

// The realistic shape of a genuine CLI-reported failure (an invalid
// realm name, an expired session, a network error): bin/realm.ts emits
// a real "error"/"expired"/"timeout" event via its own emit() AND THEN
// exits non-zero (process.exitCode = 1 or 2). Without the sawEvent guard
// in Join's own waitErr handling, this would double-report: the real
// event already delivered, PLUS a spurious extra spawn_error tacked on
// after it just because the process also happened to exit non-zero.
func TestJoin_ARealCLIErrorEventFollowedByNonZeroExitIsNotDoubleReported(t *testing.T) {
	script := fakeScript(t, []string{`{"event":"error","realm":"attacker.controlled","message":"boom"}`}, 2)
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "attacker.controlled")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected exactly the one real error event, not a duplicated spawn_error tacked on for the non-zero exit, got %+v", got)
	}
	if got[0].Kind != "error" || got[0].Message != "boom" {
		t.Fatalf("expected the real CLI-reported error preserved as-is, got %+v", got[0])
	}
}

// Exit 127 is the shell's own "command not found" -- reproduced live
// against the real npx/macula-mcp-realm invocation (not assumed) by
// pinning a version older than 0.27.0, the release that added the
// macula-mcp-realm bin: npx finds and runs fine, its own child shell
// then can't find the -p-named bin inside that older package, and that
// shell is what actually exits 127, with nothing printed to stdout --
// exactly this fixture. A bare "exit status 127" told a real operator
// nothing about why; this is the one specific, common, self-diagnosable
// cause worth naming inline rather than leaving as an opaque Go error
// string.
func TestJoin_Exit127HintsAtAStaleVersionPin(t *testing.T) {
	script := fakeScript(t, nil, 127) // exits 127, prints nothing -- same shape npx's own missing-bin shell produces
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 1 || got[0].Kind != "spawn_error" {
		t.Fatalf("expected exactly one spawn_error event, got %+v", got)
	}
	if !strings.Contains(got[0].Message, "macula_mcp_version") {
		t.Fatalf("expected the exit-127 hint pointing at macula_mcp_version, got message: %q", got[0].Message)
	}
}

// A non-127 non-zero exit (network hiccup, some other real failure)
// must NOT get the version-pin hint tacked on -- it would be actively
// misleading for a cause this specific.
func TestJoin_NonZeroNon127ExitGetsNoVersionHint(t *testing.T) {
	script := fakeScript(t, nil, 1)
	withFakeCommand(t, script)

	events, err := Join(context.Background(), "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	got := drain(t, events, 5*time.Second)
	if len(got) != 1 || got[0].Kind != "spawn_error" {
		t.Fatalf("expected exactly one spawn_error event, got %+v", got)
	}
	if strings.Contains(got[0].Message, "macula_mcp_version") {
		t.Fatalf("expected no version-pin hint for a plain exit 1, got message: %q", got[0].Message)
	}
}

func TestJoin_ContextCancellationClosesTheChannelWithoutASpawnErrorEvent(t *testing.T) {
	// A script that sleeps well past when the test cancels ctx --
	// proves cancellation actually kills the subprocess rather than the
	// channel just closing while it keeps running orphaned.
	script := fakeScript(t, nil, 0)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("overwrite fake script: %v", err)
	}
	withFakeCommand(t, script)

	ctx, cancel := context.WithCancel(context.Background())
	events, err := Join(ctx, "0.27.0", "/tmp/identity.seed", "io.macula")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	cancel()
	got := drain(t, events, 5*time.Second)
	if len(got) != 0 {
		t.Fatalf("expected no events (a cancellation is not a reported failure), got %+v", got)
	}
}

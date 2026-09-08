// Package realmjoin execs `macula-mcp-realm join <name> --json` as a
// standalone subprocess, streaming its NDJSON events -- deliberately
// NEVER a call through mcpclient.Client's own persistent MCP session.
//
// This is the load-bearing half of macula-mcp's own R1 fix (see
// macula-io/macula-mcp's src/bin/realm.ts doc comment): a realm to join
// must only ever come from a human's own explicit action, never from
// inside a model's tool-calling loop, because mesh_join_realm was
// deliberately never given a realm parameter at all -- there is no
// server-side allowlist that could otherwise keep an agent from being
// steered into joining an attacker-chosen realm. The TUI's own `r` panel
// (internal/tui) is the human action; this package is how it reaches the
// CLI directly, with no MCP tool call anywhere in the path.
package realmjoin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Event mirrors macula-mcp's own bin/realm.ts SessionEvent shape --
// exactly the fields that shape actually emits, nothing invented on this
// side. Kind is one of "already_joined", "session", "confirmed",
// "expired", "timeout", "error" -- plus "spawn_error", added here for a
// failure this package's own caller needs to distinguish (the subprocess
// never started at all, or its stdout produced something that isn't a
// valid Event) that has no equivalent event on the CLI's own side, since
// the CLI can't report a failure to run itself.
type Event struct {
	Kind        string `json:"event"`
	Realm       string `json:"realm"`
	JoinURL     string `json:"join_url,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	QRTerminal  string `json:"qr_terminal,omitempty"`
	OrgIdentity string `json:"org_identity,omitempty"`
	Handle      string `json:"handle,omitempty"`
	Account     string `json:"account,omitempty"`
	JoinedAt    string `json:"joined_at,omitempty"`
	Tier        string `json:"tier,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Terminal reports whether ev ends the stream -- no further Event will
// ever follow it for the same Join call. Every kind except "session" is
// terminal: "session" is the interim "here's the link, still polling"
// event, the only one with more to come.
func (ev Event) Terminal() bool {
	return ev.Kind != "session" && ev.Kind != ""
}

// envAllowlist mirrors mcpclient's own -- see that package's identical
// var for why (PATH/HOME/TMPDIR only, npx's own needs, nothing else of
// this process's environment leaked to a subprocess just because it was
// spawned from it).
var envAllowlist = []string{"PATH", "HOME", "TMPDIR"}

// newCommand builds the subprocess to run -- a package var, not a
// hardcoded call inside Join, so a test can substitute a fake script in
// place of the real npx/macula-mcp-realm invocation (same override-a-var
// convention as mcpclient's own errorBackoff/respawnCooldown) without
// ever risking a real network call or a real join session against
// production. Never reassigned outside a test.
var newCommand = func(ctx context.Context, version, realmName string) *exec.Cmd {
	return exec.CommandContext(ctx, "npx", "-y", "-p", "@macula-io/mcp@"+version, "macula-mcp-realm", "join", realmName, "--json")
}

// Join execs `npx -y -p @macula-io/mcp@<version> macula-mcp-realm join
// <realmName> --json` and streams its NDJSON stdout as Events on the
// returned channel, closed once the subprocess exits (successfully or
// not) and every already-buffered line has been delivered. identityFile
// is passed through as MACULA_MCP_IDENTITY -- MUST be the exact same
// path the running macula-mcp server (mcpclient.Client) is using, or the
// credential this mints lands under a different node_id than the one
// this operator's agent is actually presenting on the mesh (see
// mcpclient.Client.IdentityFile's own doc comment for where that value
// comes from). ctx cancellation kills the subprocess; the channel is
// closed either way.
func Join(ctx context.Context, version, identityFile, realmName string) (<-chan Event, error) {
	if version == "" {
		return nil, fmt.Errorf("realmjoin: version must not be empty")
	}
	if identityFile == "" {
		return nil, fmt.Errorf("realmjoin: identityFile must not be empty -- joining under a fresh, unrelated identity would silently mint an identity nobody's agent presence actually uses")
	}
	if realmName == "" {
		return nil, fmt.Errorf("realmjoin: realmName must not be empty")
	}

	cmd := newCommand(ctx, version, realmName)
	cmd.Env = spawnEnv(identityFile)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("realmjoin: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("realmjoin: start macula-mcp-realm: %w", err)
	}

	events := make(chan Event, 4)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // qr_png_base64 lines are large -- default 64KiB scanner buffer truncates them
		sawEvent := false
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				events <- Event{Kind: "spawn_error", Realm: realmName, Message: fmt.Sprintf("unparseable line from macula-mcp-realm: %v", err)}
				continue
			}
			sawEvent = true
			events <- ev
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return // caller cancelled -- not a failure worth reporting as one
		}
		if waitErr != nil && !sawEvent {
			// Process failed before ever emitting a real event (e.g. npx
			// itself couldn't resolve/run) -- the only case this package
			// has anything useful to say beyond what the CLI's own
			// "error" event already reported (that one already flowed
			// through above, if it happened).
			events <- Event{Kind: "spawn_error", Realm: realmName, Message: waitErr.Error()}
		}
	}()
	return events, nil
}

// spawnEnv builds the subprocess environment -- pulled out as its own
// pure function so the actual variable pairing has a test independent of
// spawning a real process, same reasoning as mcpclient.spawnEnv.
func spawnEnv(identityFile string) []string {
	env := make([]string, 0, len(envAllowlist)+1)
	for _, name := range envAllowlist {
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}
	env = append(env, "MACULA_MCP_IDENTITY="+identityFile)
	return env
}

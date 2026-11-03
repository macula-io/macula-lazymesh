//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// The session is the source of truth for the leader (#63, Jupiter r=1). A
// session started with `--config other.yaml` holds its own leader_key; an
// attached TUI that read the attaching machine's ~/.config instead would arm a
// different key and name it, which is a keyboard that disagrees with the
// screen. Nothing to do with a config file on the attaching side: the leader
// comes over the control socket, from the session that will handle the keys.
func TestAttachedTUINamesTheSessionsLeader(t *testing.T) {
	socketPath := startFakeSessionAs(t, frontend.Options{Leader: "ctrl+g"})

	term := startAttached(t, socketPath)
	term.waitFor(t, "ctrl+g: commands", 30*time.Second)

	if strings.Contains(term.screen(), "ctrl+x: commands") {
		t.Fatalf("the attached TUI still names ctrl+x although the session's leader is ctrl+g:\n%s", term.screen())
	}
}

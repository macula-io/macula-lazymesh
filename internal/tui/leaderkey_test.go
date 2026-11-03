package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The leader key is configurable (#63). What matters is not the binding alone
// but everything that names it: the key that arms the leader, the hint row,
// the list written into the chat by `?`, and the error marker in the status
// strip. They move together, or the screen disagrees with the keyboard.

// keyMsgFor turns a binding's own key name ("ctrl+g") into the key a person
// presses, so a test drives the configured key rather than the ctrl+x
// constant.
func keyMsgFor(t *testing.T, name string) tea.KeyMsg {
	t.Helper()
	if !strings.HasPrefix(name, "ctrl+") || len(name) != len("ctrl+x") {
		t.Fatalf("%q is not ctrl and one letter", name)
	}
	c := name[len("ctrl+")]
	if c < 'a' || c > 'z' {
		t.Fatalf("%q is not ctrl and one letter", name)
	}
	return tea.KeyMsg{Type: tea.KeyType(int(tea.KeyCtrlA) + int(c-'a'))}
}

// modelWithLeader builds a model the way main does: the configured key is
// resolved by LeaderBinding first (which is what refuses an unusable one),
// then handed to New. Empty resolves to the default.
func modelWithLeader(t *testing.T, leader string) Model {
	t.Helper()
	lead, err := LeaderBinding(leader)
	if err != nil {
		t.Fatalf("leader_key %q was refused: %v", leader, err)
	}
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", Leader: lead})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

// pressLeader runs one command the way a person does with the configured
// leader: the leader key, then the key that names the command.
func pressLeader(t *testing.T, m Model, k tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	lead := m.leader.Keys()[0]
	updated, _ := m.handleKey(keyMsgFor(t, lead))
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("%s returned %T, not a Model", lead, updated)
	}
	if !next.leaderPending {
		t.Fatalf("%s did not arm the leader", lead)
	}
	updated, cmd := next.handleKey(k)
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("the leader command returned %T, not a Model", updated)
	}
	if next.leaderPending {
		t.Fatal("the leader is still pending after a command")
	}
	return next, cmd
}

// No setting means ctrl+x, and ctrl+x is then the only key that arms it.
func TestDefaultLeaderIsCtrlX(t *testing.T) {
	m := modelWithLeader(t, "")
	if got := m.leader.Keys()[0]; got != "ctrl+x" {
		t.Fatalf("the default leader is %q, want ctrl+x", got)
	}
	updated, _ := m.handleKey(ctrlX())
	m = updated.(Model)
	if !m.leaderPending {
		t.Fatal("ctrl+x did not arm the default leader")
	}
}

// A configured leader arms on its own key, and the default key is then an
// ordinary letter in the message.
func TestConfiguredLeaderReplacesCtrlX(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	if got := m.leader.Keys()[0]; got != "ctrl+g" {
		t.Fatalf("the leader is %q, want ctrl+g", got)
	}

	updated, _ := m.handleKey(ctrlX())
	m = updated.(Model)
	if m.leaderPending {
		t.Fatal("ctrl+x armed the leader although it is configured as ctrl+g")
	}

	updated, _ = m.handleKey(keyMsgFor(t, "ctrl+g"))
	m = updated.(Model)
	if !m.leaderPending {
		t.Fatal("ctrl+g did not arm the configured leader")
	}
}

// Every command behind the leader still runs, with the leader somewhere else.
// The scenario table builds its models with the default leader, so the
// configured one is resolved here through the same function New uses; that New
// itself wires it is asserted above.
func TestConfiguredLeaderDrivesEveryCommand(t *testing.T) {
	for _, c := range leaderCases(t) {
		t.Run(c.key, func(t *testing.T) {
			m := c.prepare(t)
			lead, err := LeaderBinding("ctrl+g")
			if err != nil {
				t.Fatalf("ctrl+g was refused: %v", err)
			}
			m.leader = lead

			m, cmd := pressLeader(t, m, keyPress(c.key))
			if !c.ran(m, cmd) {
				t.Fatalf("ctrl+g %s ran but nothing changed for it", c.key)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("ctrl+g %s leaked into the draft: %q", c.key, got)
			}
		})
	}
}

// The refused classes, each with the reason it is refused for: a key the
// message box already uses, the quit key, and anything that is not ctrl and
// one letter.
func TestLeaderKeyRefusals(t *testing.T) {
	composerKeys := []string{
		"ctrl+a", "ctrl+b", "ctrl+d", "ctrl+e", "ctrl+f", "ctrl+h", "ctrl+j",
		"ctrl+k", "ctrl+m", "ctrl+n", "ctrl+p", "ctrl+t", "ctrl+u", "ctrl+v",
		"ctrl+w",
	}
	for _, k := range composerKeys {
		t.Run(k, func(t *testing.T) {
			_, err := LeaderBinding(k)
			if err == nil {
				t.Fatalf("%s was accepted as the leader, but the message box uses it", k)
			}
			if !strings.Contains(err.Error(), "message box") {
				t.Fatalf("the refusal of %s does not say the message box uses it: %v", k, err)
			}
			if !strings.Contains(err.Error(), k) {
				t.Fatalf("the refusal does not name the key it refuses: %v", err)
			}
		})
	}

	t.Run("ctrl+c", func(t *testing.T) {
		_, err := LeaderBinding("ctrl+c")
		if err == nil {
			t.Fatal("ctrl+c was accepted as the leader, but it quits")
		}
		if !strings.Contains(err.Error(), "quit") {
			t.Fatalf("the refusal of ctrl+c does not say why: %v", err)
		}
	})

	// The terminal sends HT, and bubbletea names it "tab": a leader on
	// ctrl+i would be a key no keystroke can ever arm, with a hint row
	// naming it (Jupiter, r=1 on #63).
	t.Run("ctrl+i", func(t *testing.T) {
		_, err := LeaderBinding("ctrl+i")
		if err == nil {
			t.Fatal("ctrl+i was accepted as the leader, but the terminal sends it as tab")
		}
		if !strings.Contains(err.Error(), "tab") {
			t.Fatalf("the refusal of ctrl+i does not say the terminal sends it as tab: %v", err)
		}
	})

	for _, k := range []string{"g", "x", "alt+g", "ctrl+shift+x", "ctrl+1", "ctrl+G", ""} {
		if k == "" {
			continue // empty means "unset", which is the default, not a refusal
		}
		t.Run(k, func(t *testing.T) {
			_, err := LeaderBinding(k)
			if err == nil {
				t.Fatalf("%q was accepted as the leader, but it is not ctrl and one letter", k)
			}
			if !strings.Contains(err.Error(), "ctrl") {
				t.Fatalf("the refusal of %q does not say what the leader must be: %v", k, err)
			}
		})
	}

	t.Run("unset is the default", func(t *testing.T) {
		b, err := LeaderBinding("")
		if err != nil {
			t.Fatalf("an unset leader_key was refused: %v", err)
		}
		if got := b.Keys()[0]; got != "ctrl+x" {
			t.Fatalf("an unset leader_key resolves to %q, want ctrl+x", got)
		}
	})

	// Key names are what the terminal sends, and those are lowercase; a
	// refusal should say so rather than leave someone guessing why a
	// perfectly reasonable-looking ctrl+G is not accepted.
	t.Run("uppercase says lowercase", func(t *testing.T) {
		_, err := LeaderBinding("ctrl+G")
		if err == nil {
			t.Fatal("ctrl+G was accepted as the leader")
		}
		if !strings.Contains(err.Error(), "lowercase") {
			t.Fatalf("the refusal of ctrl+G does not say the name is lowercase: %v", err)
		}
	})
}

// The hint row names the configured leader, never a fixed ctrl+x.
func TestHintRowShowsConfiguredLeader(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	strip := m.renderStatusStrip()
	if !strings.Contains(strip, "ctrl+g") {
		t.Fatalf("the hint row does not name the configured leader:\n%s", strip)
	}
	if strings.Contains(strip, "ctrl+x") {
		t.Fatalf("the hint row still names ctrl+x with ctrl+g configured:\n%s", strip)
	}
}

// The list `?` writes into the chat opens with the configured leader.
func TestLeaderHelpNamesConfiguredLeader(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	m, _ = pressLeader(t, m, keyPress("?"))
	if len(m.chatEntries) == 0 {
		t.Fatal("the leader help wrote nothing into the chat")
	}
	last := m.chatEntries[len(m.chatEntries)-1].text
	if !strings.HasPrefix(last, "ctrl+g then:") {
		t.Fatalf("the leader help opens with %q, want it to name ctrl+g", last)
	}
}

// The error marker in the status strip is how a repeating failure is
// advertised; with the leader moved, it has to say so too.
func TestErrorMarkerNamesConfiguredLeader(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	m.lastErr = errors.New("mesh_rooms: connection refused")
	strip := m.renderStatusStrip()
	if !strings.Contains(strip, "[ctrl+g E]") {
		t.Fatalf("the error marker does not name the configured leader:\n%s", strip)
	}
	if strings.Contains(strip, "ctrl+x") {
		t.Fatalf("the error marker still names ctrl+x with ctrl+g configured:\n%s", strip)
	}
}

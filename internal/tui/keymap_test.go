package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
)

// The whole keymap behind the leader (#62). One row per command in keys.go's
// leaderHelp, each run the way a person runs it -- ctrl+x, then the key -- and
// each asserted on what it actually did, so a command that is listed but does
// nothing fails here rather than in someone's terminal.
type leaderCase struct {
	key     string
	prepare func(t *testing.T) Model
	ran     func(m Model, cmd tea.Cmd) bool
}

func leaderCases(t *testing.T) []leaderCase {
	t.Helper()

	withChat := func(t *testing.T) Model {
		m := newTestModel(t)
		m.width, m.height = 80, 12
		m.resizeComponents()
		for i := 0; i < 40; i++ {
			m.chatEntries = append(m.chatEntries, youChatEntry("line"))
		}
		m.syncViewport()
		return m
	}

	return []leaderCase{
		{"m", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.meshExpanded }},
		{"s", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.meshServicesExpanded && m.mode == ModeMeshServicesPanel }},
		{"r", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.realmExpanded }},
		{"N", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.mode == ModeNotesPanel && m.notesPanel != nil }},
		{"l", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool {
				// No log path configured in a test model: the command says so
				// rather than opening an editor on nothing.
				return lastChatSays(m, "no agent log path")
			}},
		{"E", func(t *testing.T) Model {
			m := newTestModel(t)
			m.errorHistory = []errorRecord{{text: "mesh_rooms: connection refused"}}
			return m
		}, func(m Model, _ tea.Cmd) bool { return m.mode == ModeErrorPopup && m.errorPopup != nil }},
		{"e", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.detailsExpanded }},
		{"v", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.showChatter }},
		{"b", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return m.muted }},
		{"y", func(t *testing.T) Model { return newTestModel(t) },
			func(m Model, _ tea.Cmd) bool { return lastChatSays(m, "nothing to copy") }},
		{"x", func(t *testing.T) Model {
			m := newTestModel(t)
			m.interruptCh = make(chan struct{}, 1)
			return m
		}, func(m Model, _ tea.Cmd) bool { return lastChatSays(m, "interrupting") }},
		{"g", func(t *testing.T) Model {
			m := withChat(t)
			m.chatViewport.GotoBottom()
			return m
		}, func(m Model, _ tea.Cmd) bool { return m.chatViewport.YOffset == 0 }},
		{"G", func(t *testing.T) Model {
			m := withChat(t)
			m.chatViewport.GotoTop()
			return m
		}, func(m Model, _ tea.Cmd) bool { return m.chatViewport.AtBottom() }},
		{"q", func(t *testing.T) Model { return newTestModel(t) },
			func(_ Model, cmd tea.Cmd) bool { return cmd != nil }},
		{"?", func(t *testing.T) Model { return newTestModel(t) },
			// That the list was written, not which key it opens with: the
			// leader is configurable (#63) and its own test asserts the
			// name, so hardcoding ctrl+x here would fail for a moved leader
			// without anything being wrong.
			func(m Model, _ tea.Cmd) bool { return lastChatSays(m, "then:") }},
	}
}

// lastChatSays reports whether the newest chat entry contains want.
func lastChatSays(m Model, want string) bool {
	if len(m.chatEntries) == 0 {
		return false
	}
	return strings.Contains(m.chatEntries[len(m.chatEntries)-1].text, want)
}

func TestLeaderReachesEveryCommand(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range leaderHelp() {
		listed[c.Key] = true
	}

	cases := leaderCases(t)
	if len(cases) != len(listed) {
		t.Fatalf("the leader help lists %d commands but %d are tested: the table and leaderHelp() have drifted", len(listed), len(cases))
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if !listed[c.key] {
				t.Fatalf("ctrl+x %s is not in leaderHelp(), so the hint row never offers it", c.key)
			}
			m := c.prepare(t)
			m, cmd := leader(t, m, keyPress(c.key))
			if !c.ran(m, cmd) {
				t.Fatalf("ctrl+x %s ran but nothing changed for it", c.key)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("ctrl+x %s leaked into the draft: %q", c.key, got)
			}
		})
	}
}

// A key after the leader is a command or a cancel, never a letter: what
// someone meant to type cannot become a command, and a command cannot land in
// the draft.
func TestLeaderCancelNeverReachesTheDraft(t *testing.T) {
	cases := []struct {
		name string
		then tea.KeyMsg
	}{
		{"esc", typeKey(tea.KeyEsc)},
		{"a key that is no command", runeKey('z')},
		{"the leader again", ctrlX()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t)
			updated, _ := m.Update(ctrlX())
			m = updated.(Model)
			if !m.leaderPending {
				t.Fatal("ctrl+x did not arm the leader")
			}

			updated, cmd := m.Update(c.then)
			m = updated.(Model)
			if m.leaderPending {
				t.Fatal("the leader is still armed after a cancel")
			}
			if m.input.Value() != "" {
				t.Fatalf("the cancel key reached the draft: %q", m.input.Value())
			}
			if cmd != nil {
				t.Fatal("a cancel ran a command")
			}
		})
	}
}

// esc's precedence, in the design's order: an open pop-up or panel first, then
// a pending leader, then a running turn, and otherwise nothing at all -- never
// a way out of a mode, because there is none.
func TestEscPrecedence(t *testing.T) {
	t.Run("an approval pop-up denies", func(t *testing.T) {
		approvalCh := make(chan ApprovalAnswer, 2)
		m := New(nil, Options{UserInputCh: make(chan string, 4), ApprovalCh: approvalCh})
		m.width, m.height = 80, 24
		updated, _ := m.Update(agentEventMsg(agent.Event{
			Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: "{}",
		}))
		m = updated.(Model)
		if m.pendingApproval == nil {
			t.Fatal("the approval pop-up did not open")
		}
		updated, _ = m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		if m.pendingApproval != nil || m.mode != ModeCompose {
			t.Fatalf("esc did not answer and close the pop-up: mode %v", m.mode)
		}
	})

	t.Run("a focused panel closes", func(t *testing.T) {
		m := newTestModel(t)
		m, _ = leader(t, m, keyPress("s"))
		if m.mode != ModeMeshServicesPanel {
			t.Fatalf("expected the services panel, got %v", m.mode)
		}
		updated, _ := m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		if m.mode != ModeCompose || m.meshServicesExpanded {
			t.Fatalf("esc did not close the panel: mode %v", m.mode)
		}
	})

	t.Run("a read-only overlay closes", func(t *testing.T) {
		m := newTestModel(t)
		m, _ = leader(t, m, keyPress("m"))
		updated, _ := m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		if m.meshExpanded {
			t.Fatal("esc did not close the mesh overlay")
		}
	})

	t.Run("a pending leader cancels before an overlay closes", func(t *testing.T) {
		m := newTestModel(t)
		m, _ = leader(t, m, keyPress("m"))
		updated, _ := m.Update(ctrlX())
		m = updated.(Model)
		updated, _ = m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		if m.leaderPending {
			t.Fatal("esc did not cancel the leader")
		}
		if !m.meshExpanded {
			t.Fatal("esc cancelled the leader and closed the overlay in one press")
		}
	})

	t.Run("a running turn is interrupted", func(t *testing.T) {
		interruptCh := make(chan struct{}, 1)
		m := New(nil, Options{UserInputCh: make(chan string, 4), InterruptCh: interruptCh})
		m.lastInputAt = time.Now() // sent, no listening beat since
		updated, _ := m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		select {
		case <-interruptCh:
		default:
			t.Fatal("esc did not interrupt the running turn")
		}
	})

	t.Run("with nothing open it does nothing", func(t *testing.T) {
		m := newTestModel(t)
		m = typeText(t, m, "still here")
		updated, cmd := m.Update(typeKey(tea.KeyEsc))
		m = updated.(Model)
		if cmd != nil {
			t.Fatal("esc with nothing open returned a command")
		}
		if m.mode != ModeCompose || !m.input.Focused() {
			t.Fatalf("esc with nothing open left the composer, got %v", m.mode)
		}
		if m.input.Value() != "still here" {
			t.Fatalf("esc with nothing open cost the draft: %q", m.input.Value())
		}
	})
}

// Every panel gives the keyboard and the draft back on esc (#62): the panel
// takes focus, esc closes it, and the next key is the next letter of the
// message that was being written.
func TestEveryPanelGivesFocusAndTheDraftBack(t *testing.T) {
	panels := []struct {
		name string
		// draft is what the composer holds when the panel opens. Empty for
		// the realms panel: enter there only opens the join input when there
		// is no draft to send, which is the rule that keeps a key meant for a
		// message from starting a join.
		draft string
		open  func(t *testing.T, m Model) Model
	}{
		{"mesh services", "half a thought", func(t *testing.T, m Model) Model {
			m, _ = leader(t, m, keyPress("s"))
			return m
		}},
		{"checkout notes", "half a thought", func(t *testing.T, m Model) Model {
			m, _ = leader(t, m, keyPress("N"))
			return m
		}},
		{"realm join", "", func(t *testing.T, m Model) Model {
			m, _ = leader(t, m, keyPress("r"))
			updated, _ := m.Update(typeKey(tea.KeyEnter))
			return updated.(Model)
		}},
		{"mesh service call", "half a thought", func(t *testing.T, m Model) Model {
			m.meshServices = meshservices.New(fakeMCPCaller{})
			m, _ = leader(t, m, keyPress("s"))
			updated, _ := m.Update(typeKey(tea.KeyEnter))
			return updated.(Model)
		}},
	}
	for _, p := range panels {
		t.Run(p.name, func(t *testing.T) {
			m := newTestModel(t)
			if p.draft != "" {
				m = typeText(t, m, p.draft)
			}
			m = p.open(t, m)

			updated, _ := m.Update(typeKey(tea.KeyEsc))
			m = updated.(Model)
			if m.mode != ModeCompose {
				t.Fatalf("esc did not give the composer its focus back: mode %v", m.mode)
			}
			if !m.input.Focused() {
				t.Fatalf("the composer is focused in the model but not in the textarea")
			}
			if m.input.Value() != p.draft {
				t.Fatalf("the panel cost the draft: %q", m.input.Value())
			}

			m = typeText(t, m, "!")
			if m.input.Value() != p.draft+"!" {
				t.Fatalf("the next key did not reach the draft: %q", m.input.Value())
			}
		})
	}
}

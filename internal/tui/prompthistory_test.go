package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/prompthistory"
)

// Prompt history (#78 gap 5): up in an empty composer gives back what was
// sent, and it is still there after a restart.

func promptHistoryModel(t *testing.T, path string) Model {
	t.Helper()
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", PromptHistoryPath: path})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

func pressKey(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	updated, _ := m.handleKey(k)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("handleKey returned %T, not a Model", updated)
	}
	return next
}

func typeDraft(t *testing.T, m Model, s string) Model {
	t.Helper()
	return pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func sendPrompt(t *testing.T, m Model, s string) Model {
	t.Helper()
	return pressKey(t, typeDraft(t, m, s), tea.KeyMsg{Type: tea.KeyEnter})
}

var (
	upKey   = tea.KeyMsg{Type: tea.KeyUp}
	downKey = tea.KeyMsg{Type: tea.KeyDown}
)

func TestUpInAnEmptyComposerRecallsTheLastPromptEvenAfterARestart(t *testing.T) {
	path := prompthistory.Path(filepath.Join(t.TempDir(), "lazymesh"), "fp")
	m := sendPrompt(t, promptHistoryModel(t, path), "a")
	if got := pressKey(t, m, upKey).input.Value(); got != "a" {
		t.Fatalf("up in the empty composer gave %q, want a", got)
	}

	restarted := promptHistoryModel(t, path)
	if got := pressKey(t, restarted, upKey).input.Value(); got != "a" {
		t.Fatalf("after a restart up gave %q, want a", got)
	}
}

func TestUpAndDownWalkThePromptsAndDownPastTheNewestEmptiesTheComposer(t *testing.T) {
	path := prompthistory.Path(filepath.Join(t.TempDir(), "lazymesh"), "fp")
	m := sendPrompt(t, sendPrompt(t, sendPrompt(t, promptHistoryModel(t, path), "one"), "two"), "three")

	for _, want := range []string{"three", "two", "one", "one"} {
		m = pressKey(t, m, upKey)
		if got := m.input.Value(); got != want {
			t.Fatalf("up gave %q, want %q", got, want)
		}
	}
	for _, want := range []string{"two", "three", ""} {
		m = pressKey(t, m, downKey)
		if got := m.input.Value(); got != want {
			t.Fatalf("down gave %q, want %q", got, want)
		}
	}
}

// A draft is the operator's: up never replaces text they typed, and once a
// recalled prompt is edited it is a draft like any other.
func TestUpNeverReplacesADraft(t *testing.T) {
	path := prompthistory.Path(filepath.Join(t.TempDir(), "lazymesh"), "fp")
	m := sendPrompt(t, promptHistoryModel(t, path), "a")

	m = pressKey(t, typeDraft(t, m, "draft"), upKey)
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("up replaced the draft with %q", got)
	}

	m = promptHistoryModel(t, path)
	m = typeDraft(t, pressKey(t, m, upKey), "!")
	m = pressKey(t, m, upKey)
	if got := m.input.Value(); got != "a!" {
		t.Fatalf("up after editing a recalled prompt gave %q, want the edit kept", got)
	}
}

// An unchanged recall that is sent is not stored twice in a row.
func TestSendingARecalledPromptAgainDoesNotRepeatIt(t *testing.T) {
	path := prompthistory.Path(filepath.Join(t.TempDir(), "lazymesh"), "fp")
	m := sendPrompt(t, sendPrompt(t, promptHistoryModel(t, path), "one"), "two")
	m = pressKey(t, pressKey(t, m, upKey), tea.KeyMsg{Type: tea.KeyEnter})

	got, err := prompthistory.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[1] != "two" {
		t.Fatalf("history = %q, want one and two once each", got)
	}
	m = pressKey(t, pressKey(t, m, upKey), upKey)
	if v := m.input.Value(); v != "one" {
		t.Fatalf("second up gave %q, want one", v)
	}
}

// No path (forget_prompts) is no history: nothing recalled, nothing written.
func TestNoHistoryPathRemembersNothing(t *testing.T) {
	m := sendPrompt(t, promptHistoryModel(t, ""), "a")
	if got := pressKey(t, m, upKey).input.Value(); got != "" {
		t.Fatalf("with history off up gave %q", got)
	}
}

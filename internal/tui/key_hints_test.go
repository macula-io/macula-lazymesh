package tui

// #74: on-screen key hints were written as literals, so a keymap change
// left them wrong. Raf hit the one that mattered: the realms panel said
// "i: join one" long after `i` stopped meaning anything, and pressing it
// typed an "i" into the message box. These two checks fail on that tip.

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
)

// boundKeys is every key the TUI actually binds, read off the keymap
// rather than restated here, so a check cannot agree with a stale copy.
func boundKeys() map[string]bool {
	out := map[string]bool{}
	v := reflect.ValueOf(DefaultKeyMap)
	for i := 0; i < v.NumField(); i++ {
		b, ok := v.Field(i).Interface().(key.Binding)
		if !ok {
			continue
		}
		for _, k := range b.Keys() {
			out[k] = true
		}
		if h := b.Help(); h.Key != "" {
			out[h.Key] = true
		}
	}
	return out
}

// TestRenderRealms_PlaceholderNamesTheRealJoinKey: what the empty realms
// panel tells the operator has to be the key that starts a join.
func TestRenderRealms_PlaceholderNamesTheRealJoinKey(t *testing.T) {
	m := newTestModel(t)
	got := m.renderRealms()

	want := DefaultKeyMap.Submit.Help().Key
	if want == "" {
		t.Fatal("the submit binding has no help key to name")
	}
	if !strings.Contains(got, want+": join one") {
		t.Fatalf("the empty realms panel does not name %q as the join key:\n%s", want, got)
	}
	if strings.Contains(got, "i: join") {
		t.Fatalf("the empty realms panel still says \"i: join\", and i types into the message box:\n%s", got)
	}
}

// TestPanelPlaceholdersNameOnlyBoundKeys is the class check: every key
// hint a panel prints is a key the keymap binds. A literal that outlives
// its binding fails here instead of in someone's terminal.
func TestPanelPlaceholdersNameOnlyBoundKeys(t *testing.T) {
	src, err := os.ReadFile("panels.go")
	if err != nil {
		t.Fatal(err)
	}
	placeholder := regexp.MustCompile(`panelPlaceholder\("([^"]*)"\)`)
	hint := regexp.MustCompile(`(?:^|\s|--) ?([A-Za-z][A-Za-z0-9+↑↓]*):\s`)
	bound := boundKeys()

	for _, match := range placeholder.FindAllStringSubmatch(string(src), -1) {
		for _, token := range hint.FindAllStringSubmatch(match[1], -1) {
			if !bound[token[1]] {
				t.Errorf("the placeholder %q names %q, which the keymap does not bind", match[1], token[1])
			}
		}
	}
}

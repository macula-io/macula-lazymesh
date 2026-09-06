// Package agent drives the tool-calling loop between an LLM provider and
// one or more tool sources (macula-mcp, and optionally a second,
// separately configurable source like internal/localtools): it converses,
// decides tool calls, executes them, feeds results back, and repeats until
// the model stops asking for tools.
package agent

import (
	"context"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// ToolSource is anything that can advertise tools and execute one by name.
// *mcpclient.Client and *localtools.Source both satisfy this structurally
// (mcpclient.Tool is a plain data struct, not an MCP-specific type, so a
// non-MCP source producing it is not a layering violation). The interface
// exists so tests can drive the loop against a fake tool source, and so
// MultiSource can compose several real ones without the loop itself
// knowing how many there are.
type ToolSource interface {
	ListTools(ctx context.Context) ([]mcpclient.Tool, error)
	CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error)
}

// EventKind classifies one Event emitted by a running Loop, so a consumer
// (the TUI, or a plain log) can render or record it without depending on
// the loop's internal control flow.
type EventKind int

const (
	EventAssistantMessage EventKind = iota
	EventToolCall
	EventToolResult
	EventError
	// EventBackoff and EventMaxFailuresReached are emitted by cmd/lazymesh's
	// runAgent (not by Loop itself -- the retry/backoff policy lives at
	// that level), not Say. They exist so a consumer like the TUI can
	// surface "the agent is silently stuck" as something other than a
	// visual-only cue -- this is directly Fable's finding #3 ("the TUI
	// still looks healthy" while the agent is wedged), given its own
	// signal instead of remaining indistinguishable from normal activity.
	EventBackoff
	EventMaxFailuresReached
)

// Event is one step the loop took, emitted as it happens so a caller (the
// TUI in particular) can render progress live instead of only seeing the
// final result.
type Event struct {
	Kind     EventKind
	Text     string // assistant content, or a tool's result text
	ToolName string // set for EventToolCall / EventToolResult
	Err      error  // set for EventError
}

// Loop is one running conversation against a provider, with some
// ToolSource's tools available to it -- a plain *mcpclient.Client, or a
// *MultiSource combining several.
type Loop struct {
	Provider provider.Provider
	Tools    ToolSource

	messages []provider.Message
	usage    provider.Usage
}

// Usage returns the cumulative token usage this Loop has consumed across
// every ChatCompletion call so far, for backends that report it (zero
// otherwise -- see provider.Usage's own doc comment). Added for the
// concurrency spike (macula-io/macula-lazymesh#14) to measure real token
// cost under the loop-owned-waiter design against today's baseline,
// rather than estimating it.
func (l *Loop) Usage() provider.Usage {
	return l.usage
}

// NewLoop starts a loop with the given system prompt as its first message.
func NewLoop(p provider.Provider, tools ToolSource, systemPrompt string) *Loop {
	l := &Loop{Provider: p, Tools: tools}
	if systemPrompt != "" {
		l.messages = append(l.messages, provider.Message{
			Role:    provider.RoleSystem,
			Content: systemPrompt,
		})
	}
	return l
}

// maxHistoryMessages bounds how much conversation Say ever sends to the
// provider. Without this, a long-running --room session's history grows
// forever -- cheap for a peer to force by just keeping a room active, and
// eventually the request itself starts failing outright once it exceeds
// the provider's own context limit (see trimHistory).
const maxHistoryMessages = 200

// Say adds a user message to the conversation and runs the loop until the
// model produces a plain assistant reply with no further tool calls,
// emitting an Event for every intermediate step along the way.
func (l *Loop) Say(ctx context.Context, userText string, events chan<- Event) error {
	l.messages = append(l.messages, provider.Message{
		Role:    provider.RoleUser,
		Content: userText,
	})
	l.trimHistory()

	tools, err := l.Tools.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	toolSpecs := make([]provider.ToolSpec, 0, len(tools))
	for _, t := range tools {
		toolSpecs = append(toolSpecs, provider.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// A tool-calling conversation can run several rounds: assistant asks
	// for tools, gets results, asks for more. Bounded so a misbehaving
	// model/tool pair can't loop forever unattended.
	const maxRounds = 25
	for round := 0; round < maxRounds; round++ {
		resp, err := l.Provider.ChatCompletion(ctx, provider.ChatRequest{
			Messages: l.messages,
			Tools:    toolSpecs,
		})
		if err != nil {
			emit(events, Event{Kind: EventError, Err: fmt.Errorf("chat completion: %w", err)})
			return err
		}
		l.messages = append(l.messages, resp.Message)
		l.usage.PromptTokens += resp.Usage.PromptTokens
		l.usage.CompletionTokens += resp.Usage.CompletionTokens
		l.usage.TotalTokens += resp.Usage.TotalTokens

		if resp.Message.Content != "" {
			emit(events, Event{Kind: EventAssistantMessage, Text: resp.Message.Content})
		}

		if len(resp.Message.ToolCalls) == 0 {
			return nil
		}

		for _, tc := range resp.Message.ToolCalls {
			emit(events, Event{Kind: EventToolCall, ToolName: tc.Name, Text: tc.Arguments})

			result, callErr := l.Tools.CallToolRaw(ctx, tc.Name, tc.Arguments)
			toolMsg := provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Name,
			}
			if callErr != nil {
				toolMsg.Content = fmt.Sprintf("error: %s", callErr)
				emit(events, Event{Kind: EventError, ToolName: tc.Name, Err: callErr})
			} else {
				toolMsg.Content = result
				emit(events, Event{Kind: EventToolResult, ToolName: tc.Name, Text: result})
			}
			l.messages = append(l.messages, toolMsg)
		}
	}
	return fmt.Errorf("agent loop: exceeded %d tool-calling rounds without a final reply", maxRounds)
}

// trimHistory drops the oldest complete "turns" (a user message and
// everything up to but not including the next user message) once
// l.messages exceeds maxHistoryMessages, keeping any leading system
// message intact. Cutting at user-message boundaries specifically is what
// keeps a tool_calls assistant message and its tool-result messages
// together -- splitting those would send a provider a tool result with no
// matching call, which most OpenAI-compatible APIs reject outright.
func (l *Loop) trimHistory() {
	if len(l.messages) <= maxHistoryMessages {
		return
	}
	systemOffset := 0
	if len(l.messages) > 0 && l.messages[0].Role == provider.RoleSystem {
		systemOffset = 1
	}
	rest := l.messages[systemOffset:]
	for systemOffset+len(rest) > maxHistoryMessages {
		cut := 1
		for cut < len(rest) && rest[cut].Role != provider.RoleUser {
			cut++
		}
		if cut >= len(rest) {
			break // nothing left we can safely cut at a turn boundary
		}
		rest = rest[cut:]
	}
	trimmed := make([]provider.Message, 0, systemOffset+len(rest))
	trimmed = append(trimmed, l.messages[:systemOffset]...)
	trimmed = append(trimmed, rest...)
	l.messages = trimmed
}

func emit(events chan<- Event, e Event) {
	if events == nil {
		return
	}
	events <- e
}

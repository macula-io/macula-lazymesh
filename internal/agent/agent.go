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
	// EventListening is emitted by cmd/lazymesh's runAgent once per cycle,
	// right before it waits for the next human message or room arrival
	// (macula-io/macula-lazymesh#13/#15). Loop-owned room-waiting means the
	// agent loop now parks silently in a Go select between events, with no
	// tool-call traffic of its own to show the TUI something is alive --
	// this is the replacement signal, a real acceptance criterion for #15,
	// not cosmetic polish: without it, a correctly-idle agent and a frozen
	// one look identical again, the exact regression #13 warned about.
	EventListening
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
// otherwise -- see provider.Usage's own doc comment).
func (l *Loop) Usage() provider.Usage {
	return l.usage
}

// MessageCount returns how many messages are currently in this Loop's own
// conversation history (including the leading system message, if any) --
// exists so a caller can measure trimHistory's real rotation rate under
// sustained load (macula-io/macula-lazymesh#14/#15's carried-over
// follow-up) instead of only estimating it from maxHistoryMessages and an
// assumed messages-per-cycle count.
func (l *Loop) MessageCount() int {
	return len(l.messages)
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

// maxHistoryBytes is trimHistory's second, independent cap (2026-09-07,
// the runaway-context incident): maxHistoryMessages alone caps message
// COUNT, not size, and a live instance reached over 1,048,576 tokens of
// history while sitting at nowhere near 200 messages -- a handful of
// mesh_read_inbox results (measured at ~75KB each against that same
// instance's real agent.log) is plenty to blow a real context window
// long before the count cap would ever trigger.
//
// 250,000 bytes is a conservative, deliberately provider-agnostic
// safety net, not a tuned-per-model budget: at a rough ~4 bytes/token
// (English text; tool-result JSON runs similar), that's roughly 62,500
// tokens, comfortably under even a modest ~64K-128K-token context after
// leaving headroom for the system prompt, the full tool-schema list, and
// the model's own reply -- while still leaving room for a real, useful
// conversation. A precise per-provider budget would need the model's
// actual context window plumbed through the Provider interface, which
// nothing here currently does; treated as a real option, not implemented
// here as its own separate piece of scope.
const maxHistoryBytes = 250_000

// maxToolResultBytes caps how much of a single tool result's raw content
// is kept in conversation history (2026-09-07, same incident) -- a
// second, independent safety net alongside maxHistoryBytes: that cap
// only removes OLD messages, so a single freshly-arrived oversized result
// (mesh_read_inbox's no-room_topic form can still return this large even
// after cmd/lazymesh's own prompt change to request a small limit on the
// idle-tick path -- other tools, or a caller-supplied room_topic/limit,
// can still produce a large result) would otherwise sit in the model's
// own most recent, active context at full size regardless of trimming.
// Deliberately only applied to what's STORED for the model, never to
// what's emitted as an Event/logged to agent.log -- full-fidelity tool
// output remains available for a human debugging live, which is exactly
// how this incident's own root cause was found.
const maxToolResultBytes = 20_000

// truncateForHistory shortens s to maxToolResultBytes if it's longer,
// appending a note so the model sees an honest, obviously-incomplete
// result rather than content that silently stops mid-structure with no
// explanation.
func truncateForHistory(s string) string {
	if len(s) <= maxToolResultBytes {
		return s
	}
	return fmt.Sprintf("%s\n... [truncated: %d of %d bytes shown -- call again with a narrower scope if you need the rest]",
		s[:maxToolResultBytes], maxToolResultBytes, len(s))
}

// ToolSpecsFrom maps mcpclient.Tool (macula-mcp's own shape) to
// provider.ToolSpec (what a ChatRequest actually sends) -- pulled out of
// Say so cmd/lazymesh's own startup budget check (R2, 2026-09-07) can
// build the exact same toolSpecs a real Say() call would, to measure the
// fixed prefix's real size before ever making a real request.
func ToolSpecsFrom(tools []mcpclient.Tool) []provider.ToolSpec {
	specs := make([]provider.ToolSpec, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, provider.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return specs
}

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
	toolSpecs := ToolSpecsFrom(tools)

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
		l.usage.PromptCacheHitTokens += resp.Usage.PromptCacheHitTokens
		l.usage.PromptCacheMissTokens += resp.Usage.PromptCacheMissTokens

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
				// Full result goes to the event (TUI/agent.log keep
				// full-fidelity output); only what's stored for the
				// model's own next turn is capped -- see
				// truncateForHistory's own doc comment.
				toolMsg.Content = truncateForHistory(result)
				emit(events, Event{Kind: EventToolResult, ToolName: tc.Name, Text: result})
			}
			l.messages = append(l.messages, toolMsg)
			// Trimmed after every tool result, not just once at Say's own
			// start (2026-09-07 fix): a single Say call can run up to
			// maxRounds rounds, each appending its own tool results --
			// without this, a pathological single turn could already
			// exceed the context window before the NEXT Say call ever
			// got a chance to trim anything.
			l.trimHistory()
		}
	}
	return fmt.Errorf("agent loop: exceeded %d tool-calling rounds without a final reply", maxRounds)
}

// trimHistory drops the oldest complete "turns" (a user message and
// everything up to but not including the next user message) while
// l.messages exceeds EITHER maxHistoryMessages OR maxHistoryBytes,
// keeping any leading system message intact. Cutting at user-message
// boundaries specifically is what keeps a tool_calls assistant message
// and its tool-result messages together -- splitting those would send a
// provider a tool result with no matching call, which most OpenAI-
// compatible APIs reject outright.
//
// Byte-aware trimming added 2026-09-07 (the runaway-context incident):
// the count-only cap alone let a real instance reach over a million
// tokens of history while sitting at nowhere near maxHistoryMessages,
// because a handful of its messages were tens of KB each. Both caps stay
// -- message count still matters on its own (many small messages cost
// real per-message overhead too), it's just no longer the ONLY thing
// that can trigger eviction.
func (l *Loop) trimHistory() {
	systemOffset := 0
	if len(l.messages) > 0 && l.messages[0].Role == provider.RoleSystem {
		systemOffset = 1
	}
	rest := l.messages[systemOffset:]
	for len(rest) > 0 && (systemOffset+len(rest) > maxHistoryMessages || messagesByteSize(rest) > maxHistoryBytes) {
		cut := 1
		for cut < len(rest) && rest[cut].Role != provider.RoleUser {
			cut++
		}
		if cut >= len(rest) {
			break // nothing left we can safely cut at a turn boundary
		}
		rest = rest[cut:]
	}
	if systemOffset+len(rest) == len(l.messages) {
		return // nothing was actually cut -- avoid the reallocation below
	}
	trimmed := make([]provider.Message, 0, systemOffset+len(rest))
	trimmed = append(trimmed, l.messages[:systemOffset]...)
	trimmed = append(trimmed, rest...)
	l.messages = trimmed
}

// messagesByteSize sums a rough content size across msgs -- Content plus
// each tool call's Name/Arguments, the parts that actually scale with
// what a tool returned or the model asked for (ID/ToolCallID/Role are all
// small, fixed-shape fields not worth counting). Not a real tokenizer:
// see maxHistoryBytes's own doc comment for why an approximate,
// provider-agnostic budget is the deliberate choice here.
func messagesByteSize(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Name) + len(tc.Arguments)
		}
	}
	return total
}

func emit(events chan<- Event, e Event) {
	if events == nil {
		return
	}
	events <- e
}

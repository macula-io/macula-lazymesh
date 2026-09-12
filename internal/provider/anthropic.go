package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AnthropicDefaultBaseURL is Anthropic's Messages API root.
const AnthropicDefaultBaseURL = "https://api.anthropic.com"

// AnthropicDefaultModel is the current Claude flagship. Verified against
// the live mesh roster on 2026-09-12 (a real running agent reports
// "claude-opus-5" as its model), not guessed from a docs page that may
// have moved on.
const AnthropicDefaultModel = "claude-opus-5"

// anthropicAPIVersion is the version header the Messages API requires.
const anthropicAPIVersion = "2023-06-01"

// anthropicMaxTokens is what every Messages call declares as its output
// budget — the API requires the field, and 8192 is a conservative bound
// for an agent turn that can keep calling for more. A per-turn budget
// derived from the context window is possible later; this constant is
// the honest fixed choice for now.
const anthropicMaxTokens = 8192

// Anthropic implements Provider and Streamer over Anthropic's Messages
// API (G14): the genuinely different wire shape that proves the Provider
// interface generalizes — top-level system, content-block messages,
// tool_use/tool_result blocks, and an SSE stream whose events do not
// resemble OpenAI's.
type Anthropic struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func NewAnthropic(baseURL, model, apiKey string) *Anthropic {
	if baseURL == "" {
		baseURL = AnthropicDefaultBaseURL
	}
	if model == "" {
		model = AnthropicDefaultModel
	}
	return &Anthropic{BaseURL: baseURL, Model: model, APIKey: apiKey, HTTP: http.DefaultClient}
}

// --- wire types ---

type antContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result blocks carry their call's id here
	Content   string          `json:"content,omitempty"`     // tool_result blocks carry their result text here
}

type antMessage struct {
	Role    string            `json:"role"`
	Content []antContentBlock `json:"content"`
}

type antToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type antMessagesRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    []antContentBlock `json:"system,omitempty"`
	Messages  []antMessage      `json:"messages"`
	Tools     []antToolSpec     `json:"tools,omitempty"`
	Stream    bool              `json:"stream,omitempty"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type antMessagesResponse struct {
	Content []antContentBlock `json:"content"`
	Usage   antUsage          `json:"usage"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// toAntMessages converts lazymesh's message list into Anthropic's shape:
// every system-role message becomes a top-level system block, assistant
// tool_calls become tool_use blocks, and the tool messages that follow
// an assistant message become ONE user message of tool_result blocks (the
// only shape the Messages API accepts for tool results).
func toAntMessages(msgs []Message) (system []antContentBlock, messages []antMessage) {
	for _, m := range msgs {
		if m.Role == RoleSystem {
			system = append(system, antContentBlock{Type: "text", Text: m.Content})
		}
	}
	var toolResults []antContentBlock
	flushToolResults := func() {
		if len(toolResults) == 0 {
			return
		}
		messages = append(messages, antMessage{Role: "user", Content: toolResults})
		toolResults = nil
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			flushToolResults()
			content := []antContentBlock{{Type: "text", Text: m.Content}}
			messages = append(messages, antMessage{Role: "user", Content: content})
		case RoleAssistant:
			content := make([]antContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				content = append(content, antContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, antContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: json.RawMessage(tc.Arguments)})
			}
			messages = append(messages, antMessage{Role: "assistant", Content: content})
		case RoleTool:
			toolResults = append(toolResults, antContentBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		}
	}
	flushToolResults()
	return system, messages
}

func toAntTools(specs []ToolSpec) []antToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]antToolSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, antToolSpec{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema})
	}
	return out
}

func fromAntResponse(resp antMessagesResponse) ChatResponse {
	out := Message{Role: RoleAssistant}
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			out.Content += block.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: string(block.Input)})
		}
	}
	return ChatResponse{
		Message: out,
		Usage: Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

func (a *Anthropic) newRequest(ctx context.Context, body antMessagesRequest) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal messages request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.BaseURL, "/")+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build messages request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	return req, nil
}

// ChatCompletion performs one Messages API call. Satisfies Provider.
func (a *Anthropic) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	system, messages := toAntMessages(req.Messages)
	httpReq, err := a.newRequest(ctx, antMessagesRequest{
		Model:     a.Model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  messages,
		Tools:     toAntTools(req.Tools),
	})
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call messages endpoint: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read messages response: %w", err)
	}
	var parsed antMessagesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode messages response (status %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return ChatResponse{}, fmt.Errorf("messages API error (status %d): %s: %s", resp.StatusCode, parsed.Error.Type, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("messages API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return fromAntResponse(parsed), nil
}

// ChatCompletionStream performs one streamed Messages API call. Satisfies
// Streamer: text deltas reach onDelta in order, tool_use input fragments
// accumulate per block index, and usage comes from message_start +
// message_delta.
func (a *Anthropic) ChatCompletionStream(ctx context.Context, req ChatRequest, onDelta func(chunk string) error) (ChatResponse, error) {
	system, messages := toAntMessages(req.Messages)
	httpReq, err := a.newRequest(ctx, antMessagesRequest{
		Model:     a.Model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  messages,
		Tools:     toAntTools(req.Tools),
		Stream:    true,
	})
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call streaming messages endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return ChatResponse{}, fmt.Errorf("streaming messages API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	type blockState struct {
		kind  string
		name  string
		id    string
		input strings.Builder
	}
	blocks := make(map[int]*blockState)
	var text strings.Builder
	var usage Usage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage antUsage `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode stream event %q: %w", data, err)
		}
		switch event.Type {
		case "message_start":
			usage.PromptTokens = event.Message.Usage.InputTokens
		case "content_block_start":
			blocks[event.Index] = &blockState{kind: event.ContentBlock.Type, id: event.ContentBlock.ID, name: event.ContentBlock.Name}
		case "content_block_delta":
			b := blocks[event.Index]
			if b == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				text.WriteString(event.Delta.Text)
				if err := onDelta(event.Delta.Text); err != nil {
					return ChatResponse{}, fmt.Errorf("stream delta consumer: %w", err)
				}
			case "input_json_delta":
				b.input.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			usage.CompletionTokens = event.Usage.OutputTokens
		case "message_stop":
			// terminal event
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read stream: %w", err)
	}

	out := Message{Role: RoleAssistant, Content: text.String()}
	for _, b := range blocks {
		if b.kind == "tool_use" {
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.id, Name: b.name, Arguments: b.input.String()})
		}
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return ChatResponse{Message: out, Usage: usage}, nil
}

// anthropicClaudeContextWindow is Claude's established context length for
// the current generation — 200K tokens across the Claude family.
const anthropicClaudeContextWindow = 200_000

// ContextWindow reports anthropicClaudeContextWindow.
func (a *Anthropic) ContextWindow() int {
	return anthropicClaudeContextWindow
}

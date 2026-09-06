package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DeepSeekDefaultBaseURL is DeepSeek's OpenAI-compatible API root.
const DeepSeekDefaultBaseURL = "https://api.deepseek.com"

// DeepSeekDefaultModel is DeepSeek's current cheapest GA model id.
//
// Verified 2026-09-06 against DeepSeek's own changelog
// (api-docs.deepseek.com/updates/): deepseek-chat and deepseek-reasoner
// were deprecated 2026-07-24; deepseek-v4-flash went GA-track public beta
// 2026-07-31, deepseek-v4-pro went GA 2026-08-13. flash is picked over pro
// here specifically for cost: Raf's own stated reason for defaulting to
// DeepSeek at all was "the only backend cheap enough to run an agent
// against continuously," and flash is the cheaper of the two current
// options, which fits an MVP meant to idle in a mesh room far more of the
// time than it's actually generating. This is a one-line config change,
// not a permanent bet -- see Provider interface in provider.go.
const DeepSeekDefaultModel = "deepseek-v4-flash"

// DeepSeek is the default Provider: DeepSeek's OpenAI-compatible chat
// completions API, called directly (no SDK -- the wire shape is small
// enough that a dependency would cost more than it saves).
type DeepSeek struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func NewDeepSeek(baseURL, model, apiKey string, httpClient *http.Client) *DeepSeek {
	if baseURL == "" {
		baseURL = DeepSeekDefaultBaseURL
	}
	if model == "" {
		model = DeepSeekDefaultModel
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &DeepSeek{BaseURL: baseURL, Model: model, APIKey: apiKey, HTTP: httpClient}
}

type dsFunctionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type dsToolSpec struct {
	Type     string         `json:"type"`
	Function dsFunctionSpec `json:"function"`
}

type dsFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type dsToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function dsFunctionCall `json:"function"`
}

type dsMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []dsToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type dsChatRequest struct {
	Model      string       `json:"model"`
	Messages   []dsMessage  `json:"messages"`
	Tools      []dsToolSpec `json:"tools,omitempty"`
	ToolChoice string       `json:"tool_choice,omitempty"`
}

type dsChoice struct {
	Message dsMessage `json:"message"`
}

type dsUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type dsChatResponse struct {
	Choices []dsChoice `json:"choices"`
	Usage   dsUsage    `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func toDSMessages(msgs []Message) []dsMessage {
	out := make([]dsMessage, 0, len(msgs))
	for _, m := range msgs {
		dm := dsMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			dm.ToolCalls = append(dm.ToolCalls, dsToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: dsFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out = append(out, dm)
	}
	return out
}

func toDSTools(specs []ToolSpec) []dsToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]dsToolSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, dsToolSpec{
			Type: "function",
			Function: dsFunctionSpec{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  s.InputSchema,
			},
		})
	}
	return out
}

func (d *DeepSeek) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := dsChatRequest{
		Model:      d.Model,
		Messages:   toDSMessages(req.Messages),
		Tools:      toDSTools(req.Tools),
		ToolChoice: "auto",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal deepseek request: %w", err)
	}

	url := strings.TrimRight(d.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build deepseek request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+d.APIKey)

	resp, err := d.HTTP.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call deepseek: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read deepseek response: %w", err)
	}

	var parsed dsChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode deepseek response (status %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return ChatResponse{}, fmt.Errorf("deepseek API error (status %d): %s", resp.StatusCode, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("deepseek API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	if len(parsed.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("deepseek response had no choices")
	}

	msg := parsed.Choices[0].Message
	out := Message{
		Role:    Role(msg.Role),
		Content: msg.Content,
	}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return ChatResponse{
		Message: out,
		Usage: Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
			TotalTokens:      parsed.Usage.TotalTokens,
		},
	}, nil
}

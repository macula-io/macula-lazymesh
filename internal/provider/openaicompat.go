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

// callOpenAICompatChatCompletions is the wire implementation DeepSeek and
// NVIDIA share: both speak the identical OpenAI-compatible chat-
// completions shape (model/messages/tools in, choices[0].message + usage
// out), differing only in base URL, model id, and API key -- exactly the
// "config-only variation on the same wire format" anthropic.go's own
// comment already names as the case that would NOT prove Provider
// actually generalizes (Anthropic's real Messages API does have a
// genuinely different shape, which is why that one stays a stub instead
// of going through this).
//
// Reuses deepseek.go's own ds*-prefixed request/response types rather
// than duplicating them under a new name: those types were never actually
// DeepSeek-specific in shape, just named for the first caller. Renaming
// them to something provider-neutral would have meant also touching
// deepseek_test.go's own references to dsChatRequest, which this change
// deliberately leaves untouched -- flagged here rather than silently
// reused, since a reader hitting an nvidia.go call into "dsChatRequest"
// deserves to know why the name doesn't match.
func callOpenAICompatChatCompletions(ctx context.Context, httpClient *http.Client, baseURL, model, apiKey string, req ChatRequest) (ChatResponse, error) {
	body := dsChatRequest{
		Model:      model,
		Messages:   toDSMessages(req.Messages),
		Tools:      toDSTools(req.Tools),
		ToolChoice: "auto",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat completion request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat completion request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read chat completion response: %w", err)
	}

	var parsed dsChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat completion response (status %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return ChatResponse{}, fmt.Errorf("chat completions API error (status %d): %s", resp.StatusCode, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("chat completions API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	if len(parsed.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat completion response had no choices")
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

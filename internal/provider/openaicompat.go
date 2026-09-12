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

// dsStreamOptions asks streaming backends for the final usage chunk
// (stream_options.include_usage): without it, most OpenAI-compatible
// backends only report usage in the last data chunk when explicitly told,
// and a streamed turn would otherwise never learn its token cost.
type dsStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// dsChatStreamRequest is dsChatRequest plus the streaming switches.
type dsChatStreamRequest struct {
	dsChatRequest
	Stream        bool            `json:"stream"`
	StreamOptions dsStreamOptions `json:"stream_options,omitempty"`
}

// dsStreamChoice is one choice in a streamed chunk: deltas carry content
// and tool-call fragments instead of complete messages.
type dsStreamChoice struct {
	Delta dsStreamDelta `json:"delta"`
}

type dsStreamDelta struct {
	Content   string       `json:"content"`
	ToolCalls []dsToolCall `json:"tool_calls,omitempty"`
}

// dsStreamChunk is one SSE data payload: content/tool-call deltas, and —
// on the final chunk — the usage object.
type dsStreamChunk struct {
	Choices []dsStreamChoice `json:"choices"`
	Usage   dsUsage          `json:"usage,omitempty"`
}

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
			PromptTokens:          parsed.Usage.PromptTokens,
			CompletionTokens:      parsed.Usage.CompletionTokens,
			TotalTokens:           parsed.Usage.TotalTokens,
			PromptCacheHitTokens:  parsed.Usage.PromptCacheHitTokens,
			PromptCacheMissTokens: parsed.Usage.PromptCacheMissTokens,
		},
	}, nil
}

// callOpenAICompatChatCompletionsStream is the streaming counterpart to
// callOpenAICompatChatCompletions, shared by the same backends. The wire
// is server-sent events: one `data: {json}` line per chunk, terminated by
// `data: [DONE]`. Content deltas go to onDelta in order; tool-call
// deltas arrive indexed with their arguments possibly split across
// chunks, so fragments are accumulated per index and assembled into the
// final message's ToolCalls. Usage is taken from the final chunk's usage
// object (zero when the backend didn't report one, same zero-as-fact
// posture as the non-streaming path).
func callOpenAICompatChatCompletionsStream(ctx context.Context, httpClient *http.Client, baseURL, model, apiKey string, req ChatRequest, onDelta func(chunk string) error) (ChatResponse, error) {
	body := dsChatStreamRequest{
		dsChatRequest: dsChatRequest{
			Model:      model,
			Messages:   toDSMessages(req.Messages),
			Tools:      toDSTools(req.Tools),
			ToolChoice: "auto",
		},
		Stream:        true,
		StreamOptions: dsStreamOptions{IncludeUsage: true},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal stream request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call streaming chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return ChatResponse{}, fmt.Errorf("streaming chat completions returned status %d (unreadable body: %v)", resp.StatusCode, readErr)
		}
		return ChatResponse{}, fmt.Errorf("streaming chat completions returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var content strings.Builder
	toolCalls := make(map[int]*ToolCall)
	var usage Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk dsStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return ChatResponse{}, fmt.Errorf("decode stream chunk %q: %w", data, err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if err := onDelta(choice.Delta.Content); err != nil {
					return ChatResponse{}, fmt.Errorf("stream delta consumer: %w", err)
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc, ok := toolCalls[tc.Index]
				if !ok {
					acc = &ToolCall{ID: tc.ID, Name: tc.Function.Name}
					toolCalls[tc.Index] = acc
				}
				acc.Arguments += tc.Function.Arguments
			}
		}
		if chunk.Usage != (dsUsage{}) {
			usage = Usage{
				PromptTokens:          chunk.Usage.PromptTokens,
				CompletionTokens:      chunk.Usage.CompletionTokens,
				TotalTokens:           chunk.Usage.TotalTokens,
				PromptCacheHitTokens:  chunk.Usage.PromptCacheHitTokens,
				PromptCacheMissTokens: chunk.Usage.PromptCacheMissTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read stream: %w", err)
	}

	msg := Message{Role: RoleAssistant, Content: content.String()}
	for _, acc := range toolCalls {
		msg.ToolCalls = append(msg.ToolCalls, *acc)
	}
	return ChatResponse{Message: msg, Usage: usage}, nil
}

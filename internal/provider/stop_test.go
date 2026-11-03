package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These are the checks macula-io/macula-lazymesh#77 exists for: a reply the
// backend cut short must say so, and a stream that stopped arriving must not
// be handed back as a finished turn. Before #77 both arrived as ordinary
// ChatResponses with no error and nothing to inspect.

func TestOpenAICompat_NonStream_ReportsOutputLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"the answer stops mid"},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":8,"total_tokens":12}}`))
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if resp.Stop.Reason != StopOutputLimit {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopOutputLimit)
	}
	if !resp.Stop.Cut() {
		t.Fatal("a reply the backend capped must be reported as cut")
	}
}

func TestOpenAICompat_NonStream_NormalStopIsNotACut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletion(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if resp.Stop.Reason != StopNormal || resp.Stop.Cut() {
		t.Fatalf("stop = %+v, want a normal stop that is not a cut", resp.Stop)
	}
}

// TestOpenAICompat_Stream_ReportsOutputLimit: the streaming path drops the
// same signal the non-streaming one used to -- this is the path Raf's
// agent.log took when his reply stopped mid-sentence.
func TestOpenAICompat_Stream_ReportsOutputLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"the answer stops"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":8,"total_tokens":12}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c + "\n\n")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletionStream(context.Background(), ChatRequest{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", err)
	}
	if resp.Stop.Reason != StopOutputLimit {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopOutputLimit)
	}
	if resp.Message.Content != "the answer stops" {
		t.Fatalf("content = %q, want the text kept whole", resp.Message.Content)
	}
}

// TestOpenAICompat_Stream_EndingWithoutTerminalEventIsACut: the connection
// simply stops. No [DONE], no finish_reason. This used to return the partial
// text and a nil error, which is indistinguishable from a finished answer.
func TestOpenAICompat_Stream_EndingWithoutTerminalEventIsACut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletionStream(context.Background(), ChatRequest{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", err)
	}
	if resp.Stop.Reason != StopStreamEndedEarly {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopStreamEndedEarly)
	}
	if resp.Message.Content != "half an ans" {
		t.Fatalf("content = %q, want the text kept whole", resp.Message.Content)
	}
}

// TestOpenAICompat_Stream_DoneWithoutFinishReasonIsNotACut: a well-formed end
// that names no condition is not evidence of a cut, and must not be flagged
// as one -- the existing streaming tests' streams look exactly like this.
func TestOpenAICompat_Stream_DoneWithoutFinishReasonIsNotACut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletionStream(context.Background(), ChatRequest{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", err)
	}
	if resp.Stop.Cut() {
		t.Fatalf("stop = %+v, a [DONE] end is not a cut", resp.Stop)
	}
}

func TestAnthropic_ChatCompletion_ReportsOutputLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"cut"}],"usage":{"input_tokens":3,"output_tokens":2},"stop_reason":"max_tokens"}`))
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletion(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Stop.Reason != StopOutputLimit {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopOutputLimit)
	}
}

func TestAnthropic_ChatCompletion_EndTurnIsNormal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletion(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Stop.Reason != StopNormal || resp.Stop.Cut() {
		t.Fatalf("stop = %+v, want a normal stop that is not a cut", resp.Stop)
	}
}

// TestAnthropic_Stream_EndingWithoutMessageStopIsACut: Anthropic's terminal
// event is message_stop; a stream that ends before it was truncated.
func TestAnthropic_Stream_EndingWithoutMessageStopIsACut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half an ans"}}`,
		}
		for _, e := range events {
			if _, err := w.Write([]byte(e + "\n\n")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletionStream(context.Background(), ChatRequest{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if resp.Stop.Reason != StopStreamEndedEarly {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopStreamEndedEarly)
	}
}

func TestAnthropic_Stream_ReportsOutputLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":2}}`,
			`data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			if _, err := w.Write([]byte(e + "\n\n")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletionStream(context.Background(), ChatRequest{}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if resp.Stop.Reason != StopOutputLimit {
		t.Fatalf("stop = %+v, want reason %q", resp.Stop, StopOutputLimit)
	}
}

func TestStopFromReason(t *testing.T) {
	cases := []struct {
		reason string
		want   Stop
	}{
		{"", Stop{Reason: StopUnspecified}},
		{"stop", Stop{Reason: StopNormal}},
		{"end_turn", Stop{Reason: StopNormal}},
		{"tool_use", Stop{Reason: StopNormal}},
		{"length", Stop{Reason: StopOutputLimit}},
		{"max_tokens", Stop{Reason: StopOutputLimit}},
		{"content_filter", Stop{Reason: StopOther, Detail: "content_filter"}},
	}
	for _, c := range cases {
		if got := stopFromReason(c.reason); got != c.want {
			t.Errorf("stopFromReason(%q) = %+v, want %+v", c.reason, got, c.want)
		}
	}
}

func TestStopString(t *testing.T) {
	cases := []struct {
		stop Stop
		want string
	}{
		{Stop{}, "unspecified"},
		{Stop{Reason: StopNormal}, "normal"},
		{Stop{Reason: StopOutputLimit}, "output_limit"},
		{Stop{Reason: StopOther, Detail: "content_filter"}, "other(content_filter)"},
	}
	for _, c := range cases {
		if got := c.stop.String(); got != c.want {
			t.Errorf("%+v.String() = %q, want %q", c.stop, got, c.want)
		}
	}
}

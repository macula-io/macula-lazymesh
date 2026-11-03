package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// fakeStreamer is a test Provider that streams fixed chunks and then
// reports the joined content as the completed message.
type fakeStreamer struct {
	chunks []string
}

func (f *fakeStreamer) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: strings.Join(f.chunks, "")}}, nil
}

func (f *fakeStreamer) ContextWindow() int { return 1000 }

func (f *fakeStreamer) ChatCompletionStream(ctx context.Context, req provider.ChatRequest, onDelta func(chunk string) error) (provider.ChatResponse, error) {
	for _, c := range f.chunks {
		if err := onDelta(c); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: strings.Join(f.chunks, "")}}, nil
}

type noTools struct{}

func (noTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (noTools) CallToolRaw(context.Context, string, string) (string, error) {
	return "ok", nil
}

// TestSayStreamsDeltasThenCompletes proves the streaming path's event
// contract: one delta per chunk, then the single completed assistant
// message — the shape every live renderer depends on.
func TestSayStreamsDeltasThenCompletes(t *testing.T) {
	p := &fakeStreamer{chunks: []string{"hel", "lo"}}
	loop := NewLoop(p, noTools{}, "")
	events := make(chan Event, 16)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var kinds []EventKind
	var deltaText, messageText string
	for ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == EventAssistantDelta {
			deltaText += ev.Text
		}
		if ev.Kind == EventAssistantMessage {
			messageText = ev.Text
		}
	}
	if len(kinds) != 3 || kinds[0] != EventAssistantDelta || kinds[1] != EventAssistantDelta || kinds[2] != EventAssistantMessage {
		t.Fatalf("event kinds = %v, want [delta delta message]", kinds)
	}
	if deltaText != "hello" || messageText != "hello" {
		t.Fatalf("delta text = %q, message text = %q", deltaText, messageText)
	}
}

// TestSayWithoutStreamerIsUnchanged proves the fallback contract: a plain
// Provider produces exactly one assistant message and no deltas, byte for
// byte the pre-streaming behavior.
func TestSayWithoutStreamerIsUnchanged(t *testing.T) {
	plain := plainProvider{reply: "hello"}
	loop := NewLoop(plain, noTools{}, "")
	events := make(chan Event, 16)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var kinds []EventKind
	for ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 1 || kinds[0] != EventAssistantMessage {
		t.Fatalf("event kinds = %v, want exactly one assistant message", kinds)
	}
}

type plainProvider struct {
	reply string
}

func (p plainProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: p.reply}}, nil
}

func (p plainProvider) ContextWindow() int { return 1000 }

// cutStreamer is a Streamer whose reply the backend cut short, the way a
// real one reports it (#77).
type cutStreamer struct {
	chunks []string
	stop   provider.Stop
}

func (f *cutStreamer) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: strings.Join(f.chunks, "")}, Stop: f.stop}, nil
}

func (f *cutStreamer) ContextWindow() int { return 1000 }

func (f *cutStreamer) ChatCompletionStream(ctx context.Context, req provider.ChatRequest, onDelta func(chunk string) error) (provider.ChatResponse, error) {
	for _, c := range f.chunks {
		if err := onDelta(c); err != nil {
			return provider.ChatResponse{}, err
		}
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: strings.Join(f.chunks, "")}, Stop: f.stop}, nil
}

// TestSayCarriesACutStopToTheEventAndTheLoop: the reason a reply ended must
// survive the loop in both places that report it -- the completed event the
// TUI renders from, and the loop's own status the usage line reads -- and
// the cut's text must arrive whole.
func TestSayCarriesACutStopToTheEventAndTheLoop(t *testing.T) {
	p := &cutStreamer{chunks: []string{"the answer ", "stops mid"}, stop: provider.Stop{Reason: provider.StopOutputLimit}}
	loop := NewLoop(p, noTools{}, "")
	events := make(chan Event, 16)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var gotText string
	var gotStop provider.Stop
	for ev := range events {
		if ev.Kind == EventAssistantMessage {
			gotText, gotStop = ev.Text, ev.Stop
		}
	}
	if gotText != "the answer stops mid" {
		t.Fatalf("message text = %q, want the cut text kept whole", gotText)
	}
	if gotStop.Reason != provider.StopOutputLimit {
		t.Fatalf("event stop = %+v, want %q", gotStop, provider.StopOutputLimit)
	}
	if got := loop.LastStopReason(); got.Reason != provider.StopOutputLimit {
		t.Fatalf("loop LastStopReason = %+v, want %q", got, provider.StopOutputLimit)
	}
}

// TestSayWithoutStreamerCarriesACutStop: the non-streaming fallback reports
// the same fact, so a plain Provider is not a second, silent code path.
func TestSayWithoutStreamerCarriesACutStop(t *testing.T) {
	loop := NewLoop(cutPlain{reply: "cut", stop: provider.Stop{Reason: provider.StopStreamEndedEarly}}, noTools{}, "")
	events := make(chan Event, 16)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var got provider.Stop
	for ev := range events {
		if ev.Kind == EventAssistantMessage {
			got = ev.Stop
		}
	}
	if got.Reason != provider.StopStreamEndedEarly {
		t.Fatalf("event stop = %+v, want %q", got, provider.StopStreamEndedEarly)
	}
	if got := loop.LastStopReason(); got.Reason != provider.StopStreamEndedEarly {
		t.Fatalf("loop LastStopReason = %+v, want %q", got, provider.StopStreamEndedEarly)
	}
}

type cutPlain struct {
	reply string
	stop  provider.Stop
}

func (p cutPlain) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: p.reply}, Stop: p.stop}, nil
}

func (p cutPlain) ContextWindow() int { return 1000 }

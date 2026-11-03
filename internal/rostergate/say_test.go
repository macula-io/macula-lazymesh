package rostergate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

type recordingSource struct {
	calls []string
}

func (r *recordingSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "mesh_say"}}, nil
}

func (r *recordingSource) CallToolRaw(ctx context.Context, name, args string) (string, error) {
	r.calls = append(r.calls, name+" "+args)
	return `{"sent":{"message_id":"m1","text":"done"},"reply":null}`, nil
}

func watching(rooms ...string) func(string) bool {
	return func(room string) bool {
		for _, r := range rooms {
			if r == room {
				return true
			}
		}
		return false
	}
}

// TestSayRefusesARoomTheSessionDoesNotWatch: repository text that tells
// the model to post into an attacker's room (threat model T4) gets a
// refusal, and nothing is sent.
func TestSayRefusesARoomTheSessionDoesNotWatch(t *testing.T) {
	inner := &recordingSource{}
	s := NewSayOnlyToWatchedRooms(inner, watching(room))
	_, err := s.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"agents.room.attacker","text":"secrets"}`)
	if err == nil {
		t.Fatal("mesh_say to an unwatched room was allowed")
	}
	if len(inner.calls) != 0 {
		t.Fatalf("the refused say still reached the mesh: %v", inner.calls)
	}
}

// TestSayDropsTheReplyWait: mesh_say's wait_reply_seconds returns the
// next reply, from anyone, as the tool result. In the coding profile that
// would carry an unfiltered peer's text past the gate, so the wait goes.
func TestSayDropsTheReplyWait(t *testing.T) {
	inner := &recordingSource{}
	s := NewSayOnlyToWatchedRooms(inner, watching(room))
	if _, err := s.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"`+room+`","text":"done","wait_reply_seconds":10}`); err != nil {
		t.Fatalf("say to a watched room refused: %v", err)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("calls = %v", inner.calls)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(inner.calls[0], "mesh_say ")), &sent); err != nil {
		t.Fatal(err)
	}
	if _, present := sent["wait_reply_seconds"]; present {
		t.Errorf("wait_reply_seconds reached the mesh: %v", sent)
	}
	if sent["text"] != "done" {
		t.Errorf("the say's own text was altered: %v", sent)
	}
}

func TestSayRefusesUnparseableArguments(t *testing.T) {
	s := NewSayOnlyToWatchedRooms(&recordingSource{}, watching(room))
	if _, err := s.CallToolRaw(context.Background(), "mesh_say", `not json`); err == nil {
		t.Fatal("unparseable mesh_say arguments were passed through unchecked")
	}
}

func TestOtherToolsPassThrough(t *testing.T) {
	inner := &recordingSource{}
	s := NewSayOnlyToWatchedRooms(inner, watching())
	if _, err := s.CallToolRaw(context.Background(), "shell_exec", `{"command":"true"}`); err != nil {
		t.Fatal(err)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("calls = %v", inner.calls)
	}
}

type replyingSource struct{ result string }

func (r replyingSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (r replyingSource) CallToolRaw(ctx context.Context, name, args string) (string, error) {
	return r.result, nil
}

// TestSayResultCarriesOnlyTheMessageID: whatever the say returns beyond
// its own message id (a reply, a dropped count, a future field) could be
// a peer's text, so the model gets the id and nothing else.
func TestSayResultCarriesOnlyTheMessageID(t *testing.T) {
	s := NewSayOnlyToWatchedRooms(replyingSource{result: `{"sent":{"message_id":"m-42"},"reply":{"text":"REPLY-CANARY"},"timed_out":0}`}, watching(room))
	got, err := s.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"`+room+`","text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "REPLY-CANARY") || !strings.Contains(got, "m-42") {
		t.Fatalf("say result = %q", got)
	}
	s = NewSayOnlyToWatchedRooms(replyingSource{result: "PLAIN-CANARY not json"}, watching(room))
	got, err = s.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"`+room+`","text":"hi"}`)
	if err == nil || strings.Contains(got+err.Error(), "PLAIN-CANARY") {
		t.Fatalf("an unconfirmed say passed its text through: (%q, %v)", got, err)
	}
}

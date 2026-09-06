package tui

import "testing"

func TestDisplayName_PrefersPetnameOverRawID(t *testing.T) {
	got := displayName("8d91b48aff75690d4a509fe2944ca70ceae4ecef4a2b9c2325b9c570193ff532", "gentle_crimson_otter")
	if got != "gentle_crimson_otter" {
		t.Fatalf("expected petname to win, got %q", got)
	}
}

func TestDisplayName_FallsBackToShortIDWhenPetnameEmpty(t *testing.T) {
	id := "8d91b48aff75690d4a509fe2944ca70ceae4ecef4a2b9c2325b9c570193ff532"
	got := displayName(id, "")
	if got != shortID(id) {
		t.Fatalf("expected fallback to shortID, got %q", got)
	}
}

func TestRoomLabel_PrefersPurposeOverRawTopic(t *testing.T) {
	got := roomLabel("agents.room.58022d60606c8cdb7d726ea42cf5a675", "Form a 3-agent team")
	if got != "Form a 3-agent team" {
		t.Fatalf("expected purpose to win, got %q", got)
	}
}

func TestRoomLabel_FallsBackToShortTopicWhenPurposeEmpty(t *testing.T) {
	topic := "agents.room.58022d60606c8cdb7d726ea42cf5a675"
	got := roomLabel(topic, "")
	if got != shortTopic(topic) {
		t.Fatalf("expected fallback to shortTopic, got %q", got)
	}
}

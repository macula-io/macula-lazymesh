package tui

import (
	"strings"
	"testing"
)

func TestIdentityKey_PrefersPetnameOverNodeID(t *testing.T) {
	if got := identityKey("deadbeef", "swift-otter"); got != "swift-otter" {
		t.Fatalf("expected petname preferred, got %q", got)
	}
	if got := identityKey("deadbeef", ""); got != "deadbeef" {
		t.Fatalf("expected node_id fallback when petname is empty, got %q", got)
	}
}

func TestAgentColor_DeterministicAndInPalette(t *testing.T) {
	for _, identity := range []string{"swift-otter", "brave-falcon", "deadbeef1234", "a"} {
		first := agentColor(identity)
		second := agentColor(identity)
		if first != second {
			t.Fatalf("expected agentColor(%q) to be deterministic, got %q then %q", identity, first, second)
		}
		found := false
		for _, hue := range agentPalette {
			if hue == first {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected agentColor(%q) = %q to be one of agentPalette's hues", identity, first)
		}
	}
}

// The two macula brand hues are already reserved for chatYouStyle/
// chatAssistantStyl -- a third-party agent must never be able to collide
// with either, or it would read as "you" or "the assistant" instead of
// its own identity.
func TestAgentPalette_ExcludesReservedBrandHues(t *testing.T) {
	for _, reserved := range []string{"#38BDF8", "#FB923C"} {
		for _, hue := range agentPalette {
			if hue == reserved {
				t.Fatalf("agentPalette must not contain the reserved brand hue %q", reserved)
			}
		}
	}
}

func TestAgentInitials(t *testing.T) {
	cases := []struct {
		identity string
		want     string
	}{
		{"brave-falcon", "BF"},
		{"swift_otter", "SO"},
		{"quiet falcon", "QF"},
		{"falcon", "FA"},
		{"a", "A"},
		{"", "??"},
		{"deadbeef1234", "DE"},
	}
	for _, c := range cases {
		if got := agentInitials(c.identity); got != c.want {
			t.Fatalf("agentInitials(%q) = %q, want %q", c.identity, got, c.want)
		}
	}
}

func TestAgentBadgeStyle_RendersInitialsInIdentityColor(t *testing.T) {
	identity := "swift-otter"
	rendered := agentBadgeStyle(identity).Render(agentInitials(identity))
	if !strings.Contains(rendered, "SO") {
		t.Fatalf("expected badge to contain the initials, got %q", rendered)
	}
}

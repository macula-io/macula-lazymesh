package meshservices

import (
	"strings"
	"testing"
)

func TestTryHexASCII_DecodesRealExample(t *testing.T) {
	// "ok" -> 6f6b, the actual case found live (hecate-llm's check_health).
	got, ok := tryHexASCII("6f6b")
	if !ok || got != "ok" {
		t.Fatalf("expected (\"ok\", true), got (%q, %v)", got, ok)
	}
}

func TestTryHexASCII_RejectsRealNodeIDLikeHex(t *testing.T) {
	// A real chunk_id observed live: decodes to non-printable bytes, must
	// NOT be treated as hex-encoded ASCII.
	_, ok := tryHexASCII("54eb1991420a2e4e")
	if ok {
		t.Fatalf("expected a real chunk-id-shaped hex string to be rejected")
	}
}

func TestTryHexASCII_RejectsOddLength(t *testing.T) {
	if _, ok := tryHexASCII("abc"); ok {
		t.Fatalf("expected odd-length hex to be rejected")
	}
}

func TestTryHexASCII_RejectsInvalidHex(t *testing.T) {
	if _, ok := tryHexASCII("zzzz"); ok {
		t.Fatalf("expected non-hex characters to be rejected")
	}
}

func TestTryHexASCII_RejectsEmpty(t *testing.T) {
	if _, ok := tryHexASCII(""); ok {
		t.Fatalf("expected empty string to be rejected")
	}
}

func TestDecodeHexASCII_WalksNestedStructures(t *testing.T) {
	raw := `{"status":"6f6b","meta":{"detail":"65636f6e6e"},"ids":["54eb1991420a2e4e"],"score":0.87}`
	got := decodeHexASCII(raw)

	if !strings.Contains(got, `"status":"ok"`) {
		t.Fatalf("expected top-level status to decode to ok, got %s", got)
	}
	if !strings.Contains(got, `"detail":"econn"`) {
		t.Fatalf("expected nested detail to decode, got %s", got)
	}
	if !strings.Contains(got, `"54eb1991420a2e4e"`) {
		t.Fatalf("expected the chunk-id-shaped array value to survive undecoded, got %s", got)
	}
}

func TestDecodeHexASCII_NonJSONPassesThroughUnchanged(t *testing.T) {
	raw := "not json at all, just plain text: 6f6b"
	if got := decodeHexASCII(raw); got != raw {
		t.Fatalf("expected non-JSON input to pass through unchanged, got %q", got)
	}
}

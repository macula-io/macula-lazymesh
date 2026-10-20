package mcpclient

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func initResult(version string) *mcp.InitializeResult {
	return &mcp.InitializeResult{ServerInfo: &mcp.Implementation{Name: "macula-mcp", Version: version}}
}

// Issue #17: on Node 22, npx resolved @macula-io/mcp to 0.16.0 and lazymesh ran
// it without a word. That version is refused, naming both versions and Node.
func TestCheckServerVersionRefusesTheSilentFallback(t *testing.T) {
	err := checkServerVersion(initResult("0.16.0"))
	if err == nil {
		t.Fatal("macula-mcp 0.16.0 was accepted")
	}
	for _, want := range []string{"0.16.0", MinMaculaMCPVersion, "node --version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

func TestCheckServerVersionAcceptsTheFloorAndAbove(t *testing.T) {
	for _, v := range []string{MinMaculaMCPVersion, "0.48.1", "0.49.0", "1.0.0", "0.49.0-rc.1"} {
		if err := checkServerVersion(initResult(v)); err != nil {
			t.Errorf("%s refused: %v", v, err)
		}
	}
}

// The floor is a wire-1.0 floor: every macula-mcp that was fine on the
// pre-1.0 fleet is now refused, including the 0.38.0 the floor used to name.
func TestCheckServerVersionRefusesAFlooredOutRelease(t *testing.T) {
	for _, v := range []string{"0.38.0", "0.39.0", "0.40.0-rc.1", "0.47.9"} {
		if err := checkServerVersion(initResult(v)); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestCheckServerVersionRefusesOlderOrUnknown(t *testing.T) {
	for _, v := range []string{"0.37.9", "0.9.99", "", "latest", "0.38"} {
		if err := checkServerVersion(initResult(v)); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	if err := checkServerVersion(nil); err == nil {
		t.Error("a handshake with no result was accepted")
	}
	if err := checkServerVersion(&mcp.InitializeResult{}); err == nil {
		t.Error("a handshake with no serverInfo was accepted")
	}
}

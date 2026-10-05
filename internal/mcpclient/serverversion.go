package mcpclient

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MinMaculaMCPVersion is the oldest macula-mcp that reaches the fleet: 0.38.0
// is the first on @macula-io/ts 0.26.0 (macula-go v0.20.0), which dials
// handshake v5, the wire every fleet station speaks.
const MinMaculaMCPVersion = "0.38.0"

// checkServerVersion refuses a macula-mcp older than MinMaculaMCPVersion, by
// the version it reports in its MCP handshake. npx resolves an old release
// without a word when Node is older than @macula-io/mcp's engines floor (on
// Node 22 it picks 0.16.0, which has no mesh_wait_ring and cannot reach the
// fleet), so the version actually spawned is what gets checked (issue #17).
func checkServerVersion(res *mcp.InitializeResult) error {
	if res == nil || res.ServerInfo == nil || res.ServerInfo.Version == "" {
		return fmt.Errorf("macula-mcp did not report its version; need %s or later", MinMaculaMCPVersion)
	}
	have := res.ServerInfo.Version
	if !atLeast(have, MinMaculaMCPVersion) {
		return fmt.Errorf("macula-mcp %s is older than %s, the oldest that reaches the fleet; "+
			"npx installs an old release when Node is older than @macula-io/mcp's engines floor "+
			"(node >= 24.18.1), so check `node --version`", have, MinMaculaMCPVersion)
	}
	return nil
}

// atLeast reports whether version have is at or above floor, comparing
// major.minor.patch numerically. A pre-release or build suffix is ignored;
// a version that does not parse is never at least anything.
func atLeast(have, floor string) bool {
	h, ok := parts(have)
	if !ok {
		return false
	}
	f, _ := parts(floor)
	for i := range h {
		if h[i] != f[i] {
			return h[i] > f[i]
		}
	}
	return true
}

func parts(v string) ([3]int, bool) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return out, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

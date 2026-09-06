// Package contactpolicy reads and writes the same JSON file shape
// macula-mcp's own policy.ts manages
// (~/.config/macula-mcp/contact_policy.json by default, or wherever
// mcpclient.SpawnOptions.ContactPolicyFile points lazymesh's own isolated
// copy): {"contact_policy": "open"|"ask"|"allowlist"|"closed",
// "allowlist": ["<64-hex node id>", ...], "offers": [...]}.
//
// Why this package exists (found investigating the ring-answering UX,
// 2026-09-06): macula-mcp's actual contact_policy tiers don't support
// "known contacts auto-accept, strangers still get asked" -- under real
// "allowlist" policy, ring_service.ts DECLINES anyone not on the list
// outright, it never defers to ask. So lazymesh's own "auto-accept-known"
// ring policy is a layered design: contact_policy stays "ask" in the file
// always (every ring genuinely defers, mesh-side), and lazymesh itself
// (this package's IsTrusted, consulted by internal/tui before ever
// showing the ring pop-up) decides whether a peer already on the SAME
// allowlist mesh_trust_agent manages should skip the pop-up. Reading the
// identical file mesh_trust_agent writes, rather than a separate trust
// list, means "Answer + Trust" in the pop-up and this check are always
// looking at the same data.
package contactpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// file mirrors the on-disk shape. Only the fields this package actually
// reads/writes are kept -- unknown keys are preserved separately by
// Ensure (see its own doc comment) rather than modeled here, matching
// policy.ts's own "never clobber a field this module doesn't understand"
// discipline.
type file struct {
	ContactPolicy string   `json:"contact_policy,omitempty"`
	Allowlist     []string `json:"allowlist,omitempty"`
}

// load reads and parses path. A missing file, or one that fails to
// parse, returns a zero file and no error -- mirroring policy.ts's own
// "a missing or malformed file is the default, never a hard failure"
// stance. This package is read for a live UI decision (show a pop-up or
// not); erring on "not trusted, ask" is always the safe default here.
func load(path string) file {
	if path == "" {
		return file{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return file{}
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}
	}
	return f
}

// IsTrusted reports whether nodeID (case-insensitive) is on path's own
// allowlist. False for a missing/malformed file or an empty path --
// "not trusted" is always the safe default, never assumed.
func IsTrusted(path, nodeID string) bool {
	f := load(path)
	nodeID = strings.ToLower(nodeID)
	for _, id := range f.Allowlist {
		if strings.ToLower(id) == nodeID {
			return true
		}
	}
	return false
}

// Ensure sets path's contact_policy field to policy ("open", "ask",
// "allowlist", or "closed"), creating the file if it doesn't exist,
// while preserving every other key already in it (allowlist, offers, and
// anything a human or mesh_trust_agent added) -- the same discipline
// policy.ts's own writeRawPolicyFile documents: overwriting a file this
// package doesn't fully understand would destroy work that isn't this
// package's to destroy.
func Ensure(path, policy string) error {
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &raw) // malformed existing file: proceed with an empty map rather than fail the whole spawn over it
	}
	raw["contact_policy"] = policy

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

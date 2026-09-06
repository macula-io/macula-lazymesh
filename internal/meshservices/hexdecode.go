package meshservices

import (
	"encoding/hex"
	"encoding/json"
)

// decodeHexASCII best-effort-decodes hex-encoded ASCII string values
// found anywhere in a JSON result before it's handed back to the model.
// Found live (a teammate's mesh survey, 2026-09-06): hecate-llm's
// check_health returned "ok"/"econnrefused" as hex ("6f6b"/"65636f6e6e...")
// -- an agent reading the raw hex would misread a healthy status as
// garbage. Not confirmed to occur in this package's own curated
// procedures (hecate-rag/hecate_agora/hecate_graph), but cheap and safe
// to apply defensively to whatever they do return.
//
// If raw isn't valid JSON, it's returned unchanged -- this is a
// best-effort post-processing step, never a hard requirement for a
// result to be usable.
func decodeHexASCII(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	out, err := json.Marshal(walkDecode(v))
	if err != nil {
		return raw
	}
	return string(out)
}

func walkDecode(v any) any {
	switch t := v.(type) {
	case string:
		if decoded, ok := tryHexASCII(t); ok {
			return decoded
		}
		return t
	case map[string]any:
		for k, vv := range t {
			t[k] = walkDecode(vv)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = walkDecode(vv)
		}
		return t
	default:
		return v
	}
}

// tryHexASCII decodes s as hex and reports success only if the decoded
// bytes are entirely printable ASCII (0x20-0x7e) and non-empty. This is
// what keeps a real hex identifier (a node_id, a realm, a chunk_id) from
// being mangled: decoding an identifier's bytes almost always produces at
// least one non-printable byte, while genuinely hex-encoded ASCII text
// decodes to something fully readable. Known limitation: a short hex
// string that coincidentally decodes to printable ASCII (e.g. "3030" ->
// "00") will still be "decoded" -- the ambiguity is inherent to the
// encoding, not something this heuristic can fully resolve.
func tryHexASCII(s string) (string, bool) {
	if len(s) == 0 || len(s)%2 != 0 {
		return "", false
	}
	decoded, err := hex.DecodeString(s)
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	for _, b := range decoded {
		if b < 0x20 || b > 0x7e {
			return "", false
		}
	}
	return string(decoded), true
}

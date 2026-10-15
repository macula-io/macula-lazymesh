package counters

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWritePrometheus_ExposesCountersAndTotals(t *testing.T) {
	var buf bytes.Buffer
	WritePrometheus(&buf,
		Snapshot{Turns: 3, ToolCalls: 5, ToolResults: 4, Errors: 1, Backoffs: 2, Approvals: 1, ByTool: map[string]uint64{"mesh_say": 4, `we"ird\tool`: 1}},
		Totals{PromptTokens: 900, CompletionTokens: 80, Rounds: 7, ToolSchemaBytes: 2800})
	out := buf.String()
	for _, want := range []string{
		"# TYPE lazymesh_turns_total counter\nlazymesh_turns_total 3\n",
		"lazymesh_tool_calls_total 5\n",
		"lazymesh_tool_results_total 4\n",
		"lazymesh_errors_total 1\n",
		"lazymesh_backoffs_total 2\n",
		"lazymesh_approvals_total 1\n",
		`lazymesh_tool_events_total{tool="mesh_say"} 4` + "\n",
		`lazymesh_tool_events_total{tool="we\"ird\\tool"} 1` + "\n",
		"lazymesh_prompt_tokens_total 900\n",
		"lazymesh_completion_tokens_total 80\n",
		"lazymesh_completion_rounds_total 7\n",
		"lazymesh_tool_schema_bytes_total 2800\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition lacks %q:\n%s", want, out)
		}
	}
}

// TestServeMetrics_ServesOnlyMetrics: the endpoint answers /metrics with
// the text exposition and nothing else.
func TestServeMetrics_ServesOnlyMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := ServeMetrics(ctx, "127.0.0.1:0", func() (Snapshot, Totals) {
		return Snapshot{Turns: 9}, Totals{}
	}, nil)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "lazymesh_turns_total 9") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("GET /metrics = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	other, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / = %d, want 404", other.StatusCode)
	}
}

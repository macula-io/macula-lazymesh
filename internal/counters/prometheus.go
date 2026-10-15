package counters

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Totals are the session's cumulative token and round counts, read from
// the session at scrape time rather than kept here.
type Totals struct {
	PromptTokens     int
	CompletionTokens int
	Rounds           int
	ToolSchemaBytes  int
}

// WritePrometheus writes s and t in the Prometheus text exposition format
// (version 0.0.4). It is the "Prometheus export later" step this
// package's doc names: no client library, the format is a few lines.
func WritePrometheus(w io.Writer, s Snapshot, t Totals) {
	counter := func(name, help string, v uint64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	counter("lazymesh_turns_total", "Agent turns completed.", s.Turns)
	counter("lazymesh_tool_calls_total", "Tool calls the model made.", s.ToolCalls)
	counter("lazymesh_tool_results_total", "Tool calls that returned a result.", s.ToolResults)
	counter("lazymesh_errors_total", "Loop and tool errors.", s.Errors)
	counter("lazymesh_backoffs_total", "Times the driver backed off after a failed turn.", s.Backoffs)
	counter("lazymesh_approvals_total", "Approval prompts raised for ask-listed tools.", s.Approvals)

	fmt.Fprint(w, "# HELP lazymesh_tool_events_total Tool calls plus approval prompts, per tool.\n# TYPE lazymesh_tool_events_total counter\n")
	tools := make([]string, 0, len(s.ByTool))
	for name := range s.ByTool {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	for _, name := range tools {
		fmt.Fprintf(w, "lazymesh_tool_events_total{tool=\"%s\"} %d\n", escapeLabel(name), s.ByTool[name])
	}

	counter("lazymesh_prompt_tokens_total", "Prompt tokens the provider reported.", uint64(t.PromptTokens))
	counter("lazymesh_completion_tokens_total", "Completion tokens the provider reported.", uint64(t.CompletionTokens))
	counter("lazymesh_completion_rounds_total", "Chat-completion rounds sent.", uint64(t.Rounds))
	counter("lazymesh_tool_schema_bytes_total", "Bytes of tool schemas sent, all rounds (each round resends the array).", uint64(t.ToolSchemaBytes))
}

// escapeLabel escapes a label value per the exposition format: backslash,
// double quote and newline.
func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// ServeMetrics listens on addr and serves GET /metrics from read until
// ctx ends, returning the address it bound (so ":0" can be used). Any
// other path is 404. The operator picks addr; nothing listens unless
// they configure one.
func ServeMetrics(ctx context.Context, addr string, read func() (Snapshot, Totals), logger *log.Logger) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("metrics: listen on %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		s, t := read()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		WritePrometheus(w, s, t)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed && logger != nil {
			logger.Printf("metrics: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	return ln.Addr().String(), nil
}

//go:build live

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// maxAcceptableFixedPrefixTokens is a real regression ceiling, not the
// fixedPrefixTargetTokens design target (1,500): this test's own job is
// to catch the fixed prefix creeping back UP (a new default-allowlisted
// tool, a reverted trim, a macula-mcp release that fattens a
// description) before it becomes the next runaway-context incident, not
// to enforce hitting the aspirational target exactly. Real, measured
// baseline after R2's own work (2026-09-07): ~1,864 tokens against a
// real macula-mcp spawn (23 tools: 7 default-allowlisted + 16 curated
// mesh_service_* ones live-discovered at measurement time) -- down from
// ~7,931 before. Generous headroom above that measured number, not a
// tight bound: this is a regression guard, not a precision target.
const maxAcceptableFixedPrefixTokens = 3000

// TestLiveR2FixedPrefixStaysUnderRegressionCeiling measures the real
// fixed prefix (system prompt + every allowlisted tool's Description/
// InputSchema, marshaled exactly as checkStartupBudget and agent.Say
// both do it) against a real macula-mcp spawn -- the same measurement
// this repo's own R2 work (macula-comm-docs and this file's own history)
// used to go from ~7,931 to ~1,864 tokens. See maxAcceptableFixedPrefixTokens's
// own doc comment for why the assertion here is a generous regression
// ceiling, not the tighter 1,500-token design target.
func TestLiveR2FixedPrefixStaysUnderRegressionCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	tools, err := buildToolSource(config.Config{}, client, nil)
	if err != nil {
		t.Fatalf("buildToolSource: %v", err)
	}

	listed, err := tools.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	specs := agent.ToolSpecsFrom(listed)
	toolsJSON, err := json.Marshal(specs)
	if err != nil {
		t.Fatalf("marshal tool specs: %v", err)
	}

	systemPrompt := buildSystemPrompt("", "", false, false)
	fixedPrefixTokens := estimateTokens(len(systemPrompt) + len(toolsJSON))

	t.Logf("fixed prefix: %d tools, system prompt %d bytes, tools JSON %d bytes, ~%d tokens total (design target <%d, regression ceiling %d)",
		len(listed), len(systemPrompt), len(toolsJSON), fixedPrefixTokens, fixedPrefixTargetTokens, maxAcceptableFixedPrefixTokens)
	for _, tool := range listed {
		toolJSON, _ := json.Marshal(tool)
		t.Logf("  %-45s %5d bytes", tool.Name, len(toolJSON))
	}

	if fixedPrefixTokens > maxAcceptableFixedPrefixTokens {
		t.Errorf("fixed prefix grew to ~%d tokens, over the %d-token regression ceiling -- see R2's own writeup (plans/PLAN_LAZYMESH_MVP.md) before adding more default-allowlisted tools or reverting a trim",
			fixedPrefixTokens, maxAcceptableFixedPrefixTokens)
	}
}

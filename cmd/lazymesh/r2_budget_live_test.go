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
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
)

// maxAcceptableFixedPrefixTokens is a real regression ceiling, not the
// fixedPrefixTargetTokens design target (1,500): this test's own job is
// to catch the fixed prefix creeping back UP (a new default-allowlisted
// tool, a reverted trim, a macula-mcp release that fattens a
// description) before it becomes the next runaway-context incident, not
// to enforce hitting the aspirational target exactly. Real, measured
// baseline after R2's close-out (2026-09-07, mesh_services_enabled
// flipped to default-off): the room-chat-only common case now measures
// against a real macula-mcp spawn with just the 7 default-allowlisted
// macula-mcp tools, no mesh_service_* catalog -- see this test's own
// t.Logf output for the exact number each run, and
// TestLiveR2ToolSelectionMeshServicesEnabledStillFitsRegressionCeiling
// for the opted-in (mesh_services_enabled: true) case, which still
// carries the full 16-tool catalog and needs its own, separate, higher
// ceiling. Generous headroom above the measured number, not a tight
// bound: this is a regression guard, not a precision target.
const maxAcceptableFixedPrefixTokens = 1500

// maxAcceptableFixedPrefixTokensMeshServicesEnabled is the same regression
// ceiling for the opted-in case (mesh_services_enabled: true), which still
// carries the full 16-tool mesh_service_* catalog on top of the 7 default
// macula-mcp tools -- R2's own pre-close-out measurement was ~1,864 tokens
// for that combination (down from ~7,931 before R2's other trims).
const maxAcceptableFixedPrefixTokensMeshServicesEnabled = 3000

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

	systemPrompt := buildSystemPrompt("", "", false, false, false)
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

// TestLiveR2ToolSelectionMeshServicesEnabledStillFitsRegressionCeiling is
// the opted-in counterpart (2026-09-07, R2's close-out): an operator who
// sets mesh_services_enabled: true still carries the full 16-tool
// mesh_service_* catalog, so it needs its own, separate, higher ceiling
// (maxAcceptableFixedPrefixTokensMeshServicesEnabled) rather than being
// silently exempt from a regression guard just because the default case
// moved to a tighter one.
func TestLiveR2ToolSelectionMeshServicesEnabledStillFitsRegressionCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	// See main_live_test.go's own comment on the same pattern: buildToolSource
	// no longer constructs meshservices.Source internally, so a nil here
	// would silently drop the very tools this test measures.
	tools, err := buildToolSource(config.Config{MeshServicesEnabled: true}, client, meshservices.New(client))
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

	systemPrompt := buildSystemPrompt("", "", false, false, true)
	fixedPrefixTokens := estimateTokens(len(systemPrompt) + len(toolsJSON))

	t.Logf("fixed prefix (mesh_services_enabled): %d tools, system prompt %d bytes, tools JSON %d bytes, ~%d tokens total (regression ceiling %d)",
		len(listed), len(systemPrompt), len(toolsJSON), fixedPrefixTokens, maxAcceptableFixedPrefixTokensMeshServicesEnabled)

	if fixedPrefixTokens > maxAcceptableFixedPrefixTokensMeshServicesEnabled {
		t.Errorf("fixed prefix (mesh_services_enabled) grew to ~%d tokens, over the %d-token regression ceiling",
			fixedPrefixTokens, maxAcceptableFixedPrefixTokensMeshServicesEnabled)
	}
}

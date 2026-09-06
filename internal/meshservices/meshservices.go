// Package meshservices is lazymesh's Phase 3 tool source: real mesh RPC
// procedures, discovered dynamically via macula-mcp's own
// mesh_find_records_by_type, exposed as synthetic tools instead of
// hardcoded local integrations -- dogfooding the mesh's own service
// directory as the preferred capability source. See Curated in catalog.go
// for the specific procedures and why each one is trusted to expose.
//
// This never gives the model a generic "call any mesh procedure" tool --
// only individually named, curated, currently-discovered procedures ever
// become tools. A generic passthrough would reopen exactly the
// mesh-content-to-arbitrary-call risk internal/agent's AllowlistSource
// exists to close off.
package meshservices

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// discoveryCacheTTL bounds how often Source re-queries
// mesh_find_records_by_type. DHT procedure_advertisement records expire
// roughly every 2 minutes on their own (a teammate's survey, 2026-09-06),
// and the raw discovery payload is large (hundreds of KB across the whole
// mesh) -- re-querying on literally every agent conversation turn (Loop
// calls ListTools once per Say()) would mean a full DHT dump every few
// seconds during an active room. A minute-scale cache keeps this cheap
// while still refreshing well inside that expiry window.
const discoveryCacheTTL = 60 * time.Second

// mcpCaller is the subset of *mcpclient.Client Source needs: calling
// macula-mcp's own mesh_find_records_by_type and mesh_call tools. Source
// never talks to the mesh protocol directly -- it's a client of
// macula-mcp, same as everything else in this codebase.
type mcpCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

type discoveredProcedure struct {
	Realm     string
	Procedure string
}

// Source implements agent.ToolSource, exposing Curated's procedures as
// tools whenever they're currently discoverable on the mesh.
type Source struct {
	mcp mcpCaller

	mu            sync.Mutex
	index         map[string]discoveredProcedure
	cachedTools   []mcpclient.Tool
	lastDiscovery time.Time
}

// New wraps mcp (typically a *mcpclient.Client already spawned for
// macula-mcp's own tools) as a mesh-service tool source.
func New(mcp mcpCaller) *Source {
	return &Source{mcp: mcp}
}

func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	s.mu.Lock()
	if !s.lastDiscovery.IsZero() && time.Since(s.lastDiscovery) < discoveryCacheTTL {
		tools := s.cachedTools
		s.mu.Unlock()
		return tools, nil
	}
	s.mu.Unlock()

	raw, err := s.mcp.CallTool(ctx, "mesh_find_records_by_type", map[string]any{"record_type": "procedure_advertisement"})
	if err != nil {
		return nil, fmt.Errorf("discover mesh procedures: %w", err)
	}

	var parsed struct {
		Records []struct {
			ProcedureAdvertisement struct {
				Realm     string `json:"realm"`
				Procedure string `json:"procedure"`
			} `json:"procedure_advertisement"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decode mesh_find_records_by_type: %w", err)
	}

	// Some records' decoded procedure field carries a leading "_/" path
	// segment before the domain.method name (an artifact of how the
	// advertisement's URI is split, not something mesh_call's own
	// procedure parameter accepts) -- strip it so lookups match
	// Curated's plain "domain.method" form.
	realmByProcedure := make(map[string]string, len(parsed.Records))
	for _, r := range parsed.Records {
		proc := strings.TrimPrefix(r.ProcedureAdvertisement.Procedure, "_/")
		realmByProcedure[proc] = r.ProcedureAdvertisement.Realm
	}

	var tools []mcpclient.Tool
	index := make(map[string]discoveredProcedure)
	for _, cp := range Curated {
		realm, ok := realmByProcedure[cp.Procedure()]
		if !ok {
			continue // not currently advertised -- real dynamic discovery, not a fixed catalog
		}
		name := cp.ToolName()
		tools = append(tools, mcpclient.Tool{
			Name:        name,
			Description: cp.Description,
			InputSchema: map[string]any{"type": "object"},
		})
		index[name] = discoveredProcedure{Realm: realm, Procedure: cp.Procedure()}
	}

	s.mu.Lock()
	s.index = index
	s.cachedTools = tools
	s.lastDiscovery = time.Now()
	s.mu.Unlock()
	return tools, nil
}

func (s *Source) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	s.mu.Lock()
	dp, ok := s.index[name]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("mesh service tool %q is not currently available (not discovered on the last refresh)", name)
	}

	args := map[string]any{}
	if strings.TrimSpace(argumentsJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("decode arguments for %s: %w", name, err)
		}
	}

	callArgs := map[string]any{
		"procedure": dp.Procedure,
		"realm":     dp.Realm,
		"args":      args,
	}

	// One retry, no backoff: a teammate's survey (2026-09-06) hit a
	// transient QUIC-level error mid-investigation that succeeded on
	// immediate retry with identical arguments -- known mesh flakiness,
	// not a signal the service is actually down. A single blind call
	// without retry often won't succeed; treating one failure as
	// "unavailable" would be wrong here specifically.
	result, err := s.mcp.CallTool(ctx, "mesh_call", callArgs)
	if err != nil {
		result, err = s.mcp.CallTool(ctx, "mesh_call", callArgs)
		if err != nil {
			return "", fmt.Errorf("mesh_call %s (retried once): %w", dp.Procedure, err)
		}
	}
	return decodeHexASCII(result), nil
}

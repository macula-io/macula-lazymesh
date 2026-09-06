//go:build live

package meshservices

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveSource_DiscoversAndCallsARealCuratedProcedure confirms Phase 3's
// actual dynamic-discovery-plus-curation pipeline against the real mesh,
// not just the parsing/filtering logic in isolation: a real macula-mcp
// spawn discovers at least one Curated procedure currently advertised,
// and a real call to it round-trips successfully.
func TestLiveSource_DiscoversAndCallsARealCuratedProcedure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()

	src := New(client)
	tools, err := src.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("expected at least one curated procedure to be currently discovered on the live mesh")
	}

	// hecate_agora.get_posts_page is documented (and previously
	// live-verified by hand) to work with an empty args object -- the
	// least likely of the curated set to fail on argument shape alone,
	// so a failure here is more likely about discovery/routing than args.
	const wantTool = "mesh_service_hecate_agora_get_posts_page"
	found := false
	for _, tool := range tools {
		if tool.Name == wantTool {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("hecate_agora.get_posts_page not currently discovered (mesh state can vary) -- got %d other curated tools instead", len(tools))
	}

	result, err := src.CallToolRaw(ctx, wantTool, `{}`)
	if err != nil {
		t.Fatalf("CallToolRaw(%s): %v", wantTool, err)
	}
	if !strings.Contains(result, `"result"`) {
		t.Fatalf("expected a result field in the response, got: %s", result)
	}
}

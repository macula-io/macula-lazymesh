package meshservices

import "testing"

func TestCuratedProcedure_ToolName_SanitizesHyphenAndDot(t *testing.T) {
	p := CuratedProcedure{Domain: "hecate-rag", Method: "search_chunks_semantic"}
	got := p.ToolName()
	want := "mesh_service_hecate_rag_search_chunks_semantic"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCuratedProcedure_Procedure(t *testing.T) {
	p := CuratedProcedure{Domain: "hecate_agora", Method: "get_posts_page"}
	if got := p.Procedure(); got != "hecate_agora.get_posts_page" {
		t.Fatalf("expected hecate_agora.get_posts_page, got %q", got)
	}
}

func TestAllowedToolNames_MatchesCuratedLength(t *testing.T) {
	names := AllowedToolNames()
	if len(names) != len(Curated) {
		t.Fatalf("expected %d names, got %d", len(Curated), len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate tool name in catalog: %s", n)
		}
		seen[n] = true
	}
}

// Regression guard for the specific things the catalog's own doc comment
// says are deliberately excluded -- if one of these ever gets added back,
// it should be a conscious decision, not a copy-paste accident.
func TestCurated_ExcludesKnownMutatingOrGatedProcedures(t *testing.T) {
	excluded := []string{
		"hecate-rag.ingest_document",
		"hecate-rag.add_knowledge",
		"hecate-rag.upload_knowledge",
		"hecate-rag.prune_chunks",
		"hecate-rag.schedule_reembed",
		"hecate-rag.retire_document",
		"hecate_graph.learn_link",
	}
	procedures := map[string]bool{}
	for _, p := range Curated {
		procedures[p.Procedure()] = true
	}
	for _, e := range excluded {
		if procedures[e] {
			t.Fatalf("%s must not be in Curated -- it's mutating/gated", e)
		}
	}
}

func TestCurated_ExcludesEntireDomains(t *testing.T) {
	excludedDomains := []string{"hecate-llm", "hecate_mail", "hecate_citizens", "warden", "sentinel"}
	for _, p := range Curated {
		for _, d := range excludedDomains {
			if p.Domain == d {
				t.Fatalf("domain %q must not appear in Curated at all, found %s", d, p.Procedure())
			}
		}
	}
}

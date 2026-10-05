package meshservices

import (
	"sort"
	"strings"
	"testing"
)

func TestCuratedProcedure_ToolName_SanitizesHyphenAndSlash(t *testing.T) {
	p := CuratedProcedure{Org: "mcl-rag", Method: "search_chunks_semantic"}
	got := p.ToolName()
	want := "mesh_service_mcl_rag_search_chunks_semantic"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// mcl_om advertises a procedure as Org/Name, and that is what mesh_call takes.
func TestCuratedProcedure_Procedure(t *testing.T) {
	p := CuratedProcedure{Org: "mcl-graph", Method: "resolve_entity"}
	if got := p.Procedure(); got != "mcl-graph/resolve_entity" {
		t.Fatalf("expected mcl-graph/resolve_entity, got %q", got)
	}
}

// The whole catalog, named: the read-only procedures of mcl-rag and mcl-graph.
func TestCurated_IsExactlyTheReadOnlyMclProcedures(t *testing.T) {
	want := []string{
		"mcl-graph/narrate_entity",
		"mcl-graph/narrate_link",
		"mcl-graph/resolve_entity",
		"mcl-graph/resolve_link",
		"mcl-rag/answer_query",
		"mcl-rag/get_chunk_by_id",
		"mcl-rag/get_document_verbatim",
		"mcl-rag/get_source_by_id",
		"mcl-rag/list_chunks_by_source",
		"mcl-rag/list_sources_page",
		"mcl-rag/rerank_results",
		"mcl-rag/search_chunks_semantic",
	}
	var got []string
	for _, p := range Curated {
		got = append(got, p.Procedure())
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Curated is\n  %v\nwant\n  %v", got, want)
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
// it should be a conscious decision, not a copy-paste accident. mcl-rag
// serves the eight write procedures to operators only, and learn_link
// writes the graph.
func TestCurated_ExcludesKnownMutatingProcedures(t *testing.T) {
	excluded := []string{
		"mcl-rag/ingest_document",
		"mcl-rag/add_knowledge",
		"mcl-rag/upload_knowledge",
		"mcl-rag/prune_chunks",
		"mcl-rag/schedule_reembed",
		"mcl-rag/retire_document",
		"mcl-rag/embed_document",
		"mcl-rag/detect_corpus_change",
		"mcl-rag/classify_topics", // curated from the name, not the handler -- actually writes topic tags (Fable's review, 2026-09-06)
		"mcl-graph/learn_link",
	}
	procedures := map[string]bool{}
	for _, p := range Curated {
		procedures[p.Procedure()] = true
	}
	for _, e := range excluded {
		if procedures[e] {
			t.Fatalf("%s must not be in Curated -- it's mutating", e)
		}
	}
}

func TestCurated_ExcludesEntireOrgs(t *testing.T) {
	excludedOrgs := []string{"mcl-mail", "mcl-citizens", "mcl-warden", "mcl-sentinel"}
	for _, p := range Curated {
		for _, o := range excludedOrgs {
			if p.Org == o {
				t.Fatalf("org %q must not appear in Curated at all, found %s", o, p.Procedure())
			}
		}
	}
}

// Nothing named hecate survives: those services are retired from the fleet.
func TestCurated_NamesNoRetiredHecateService(t *testing.T) {
	for _, p := range Curated {
		if strings.Contains(p.Procedure(), "hecate") {
			t.Fatalf("retired hecate procedure still curated: %s", p.Procedure())
		}
	}
}

package meshservices

import "strings"

// CuratedProcedure is one mesh RPC procedure Phase 3 is willing to expose
// as a synthetic tool, IF it's currently discovered live on the mesh via
// mesh_find_records_by_type("procedure_advertisement"). This is a curated
// safety allowlist, not the discovery mechanism itself -- discovery stays
// real and dynamic (see Source.ListTools); this list is what filters that
// dynamic result down to things actually safe to hand an agent whose
// conversation can be steered by arbitrary mesh peers.
//
// Sourced from a live mesh survey, 2026-09-06 (a teammate's investigation,
// io.macula realm ABB81B5A...FCD1): every domain here was confirmed
// serving and read-only. Deliberately excluded, domain by domain:
//   - hecate-rag's ingest_document/add_knowledge/upload_knowledge/
//     prune_chunks/schedule_reembed/retire_document (all mutate the
//     shared corpus)
//   - hecate_graph's learn_link (ownership-proof-gated, mutates)
//   - hecate_mail, hecate_citizens entirely (state-mutating and/or
//     ownership-gated; not live-verified read-safe at survey time)
//   - hecate-llm entirely (no direct-dial; only a plain advertisement,
//     and its check_health response was seen returning hex-encoded status
//     strings -- see hexdecode.go)
//   - warden.*/sentinel.* (11 distinct advertiser identities behind 2
//     serving stations at survey time -- looks like duplicate
//     self-registration, not one canonical backend; no spec found)
//   - the DHT's own default-realm noise (agent.<node_id>.ring, realm
//     bootstrap procedures, macula-ts's own test artifacts)
//
// Argument schemas below are inferred (procedure naming + the one or two
// spot-verified calls noted per entry), NOT DHT-advertised -- the DHT
// carries no argument schema at all. This is deliberately not treated as
// a blocker: a wrong-argument call returns a clean, specific error
// (verified live: "query_text_or_vector_required" for a missing/wrong
// hecate-rag query field), which flows back into the conversation as a
// normal tool result the model can read and retry against, exactly like
// any other tool error in this codebase.
var Curated = []CuratedProcedure{
	{Domain: "hecate-rag", Method: "search_chunks_semantic", Description: "Semantic search over the shared corpus. Args (verified live): query_text (string, required) -- NOT `query`. top_k (integer, optional) limits results. Returns ranked chunks with score/source_path/content."},
	{Domain: "hecate-rag", Method: "answer_query", Description: "Answer a natural-language question against the shared corpus (likely wraps search + synthesis). Args (inferred): query_text (string, required)."},
	{Domain: "hecate-rag", Method: "get_chunk_by_id", Description: "Fetch one corpus chunk by its id. Args (inferred): chunk_id (string, required)."},
	{Domain: "hecate-rag", Method: "get_source_by_id", Description: "Fetch one corpus source document's metadata by id. Args (inferred): source_id (string, required)."},
	{Domain: "hecate-rag", Method: "list_sources_page", Description: "Page through the corpus's source documents. Args (inferred): page/page_size or a cursor, optional -- try with no args first."},
	{Domain: "hecate-rag", Method: "list_chunks_by_source", Description: "List the chunks belonging to one source document. Args (inferred): source_id (string, required)."},
	{Domain: "hecate-rag", Method: "get_document_verbatim", Description: "Fetch a source document's original, unchunked text. Args (inferred): source_id (string, required)."},
	{Domain: "hecate-rag", Method: "classify_topics", Description: "Classify text into corpus topic labels. Args (inferred): text (string, required)."},
	{Domain: "hecate-rag", Method: "rerank_results", Description: "Rerank a candidate result set against a query. Args (inferred): query_text (string) and a list of candidates -- shape not verified."},
	{Domain: "hecate_agora", Method: "search_posts", Description: "Search forum/agora posts. Args (inferred): query (string, required)."},
	{Domain: "hecate_agora", Method: "search_archive", Description: "Search the archived (older/retention-managed) subset of posts. Args (inferred): same shape as search_posts."},
	{Domain: "hecate_agora", Method: "get_posts_page", Description: "Page through recent posts. Verified live: works with an empty args object -- try {} first before adding pagination args."},
	{Domain: "hecate_agora", Method: "get_thread_by_post_id", Description: "Fetch a post's full thread by its post id. Args (inferred): post_id (string, required)."},
	{Domain: "hecate_graph", Method: "resolve_entity", Description: "Resolve a knowledge-graph entity by name or id. Args (inferred): entity_id or name (string, required) -- a missing/wrong field surfaces as a clean error (e.g. missing_entity_id), read it and retry."},
	{Domain: "hecate_graph", Method: "resolve_link", Description: "Resolve a knowledge-graph link/relation. Args (inferred): link id or endpoint pair, exact shape not verified."},
	{Domain: "hecate_graph", Method: "narrate_entity", Description: "Produce a natural-language narration of a graph entity. Args (inferred): entity_id (string, required)."},
	{Domain: "hecate_graph", Method: "narrate_link", Description: "Produce a natural-language narration of a graph link. Args (inferred): link id, exact shape not verified."},
}

// CuratedProcedure describes one entry in Curated.
type CuratedProcedure struct {
	Domain      string
	Method      string
	Description string
}

// Procedure returns the "domain.method" form mesh_call's own procedure
// parameter expects (matching the plain form verified live, without the
// "_/" segment some DHT records' decoded procedure field also carries).
func (p CuratedProcedure) Procedure() string {
	return p.Domain + "." + p.Method
}

// ToolName is the synthetic tool name this procedure is exposed under.
// Sanitized because OpenAI-compatible function-calling names reject "."
// and some backends are picky about "-" too; prefixed with
// "mesh_service_" so these are visually distinct from macula-mcp's own
// "mesh_*" tools and from anything internal/localtools exposes.
func (p CuratedProcedure) ToolName() string {
	sanitized := strings.NewReplacer("-", "_", ".", "_").Replace(p.Procedure())
	return "mesh_service_" + sanitized
}

// AllowedToolNames returns every curated procedure's synthetic tool name,
// for merging into the default tool allowlist (see cmd/lazymesh's
// resolveAllowlist) -- kept here rather than in package agent so agent
// doesn't need to depend on meshservices to know its own default.
func AllowedToolNames() []string {
	names := make([]string, len(Curated))
	for i, p := range Curated {
		names[i] = p.ToolName()
	}
	return names
}

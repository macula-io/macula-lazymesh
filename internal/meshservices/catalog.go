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
//     shared corpus). classify_topics also belongs on this list --
//     CORRECTED 2026-09-06 (Fable's Phase 3 review): curated from the
//     tool NAME, not its handler. Real behavior
//     (apps/embed_corpus/maybe_classify_topics.erl, right next to
//     prune_chunks/retire_document): loads a document, chunks it, calls a
//     paid LLM classifier per chunk, then WRITES topic tags back into the
//     shared corpus (rag_store:tag_chunk) -- changing everyone's
//     topic-filtered search results, not a read. The lesson, not just the
//     fix: curating this list means reading the handler source, never
//     inferring safety from a name that merely sounds like a query.
//   - hecate_graph's learn_link (ownership-proof-gated, mutates)
//   - hecate_mail, hecate_citizens entirely (state-mutating and/or
//     ownership-gated; not live-verified read-safe at survey time)
//   - hecate-llm entirely (no direct-dial; only a plain advertisement,
//     and its check_health response was seen returning hex-encoded status
//     strings -- see hexdecode.go)
//   - warden.*/sentinel.* -- CORRECTED 2026-09-06 (checked directly with
//     Raf): NOT junk/duplicate registrations, as first assumed from the
//     11-advertisers-behind-2-stations shape. warden is real security
//     tooling (publishes SSH attack-vector facts from the box it runs on,
//     can act as a honeypot), sentinel subscribes and enriches, and
//     macula-portal/vigil is its real consumer. Excluded from this
//     curated list anyway, but for the right reason: side-effecting
//     security semantics (ensnare in particular appears to trigger a real
//     honeypot action, not a read-only query), not because it looked
//     fake. If mesh data from this domain is ever needed, go through
//     macula-portal/vigil, not an ad-hoc direct RPC call here.
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
// Description is deliberately terse (shortened 2026-09-07, R2 -- see
// internal/agent/terse.go for the same reasoning applied to macula-mcp's
// own tools): just enough for a small/cheap model to call each procedure
// correctly, not the full inferred-vs-verified confidence narrative --
// that context stays in this file's own comments above, for a human
// maintainer, not resent to the model on every single request. A wrong
// or missing argument still surfaces as a clean, specific error the
// model can read and retry against (see the package doc comment above);
// losing the "(inferred)" qualifier from what's sent to the model costs
// nothing that error path doesn't already cover.
var Curated = []CuratedProcedure{
	{Domain: "hecate-rag", Method: "search_chunks_semantic", Description: "Semantic search over the shared corpus. query_text (string, required, not query). top_k (integer, optional)."},
	{Domain: "hecate-rag", Method: "answer_query", Description: "Answer a question against the shared corpus. query_text (string, required)."},
	{Domain: "hecate-rag", Method: "get_chunk_by_id", Description: "Fetch one corpus chunk by id. chunk_id (string, required)."},
	{Domain: "hecate-rag", Method: "get_source_by_id", Description: "Fetch one corpus source document's metadata by id. source_id (string, required)."},
	{Domain: "hecate-rag", Method: "list_sources_page", Description: "Page through corpus source documents. No required args -- try {} first."},
	{Domain: "hecate-rag", Method: "list_chunks_by_source", Description: "List one source document's chunks. source_id (string, required)."},
	{Domain: "hecate-rag", Method: "get_document_verbatim", Description: "Fetch a source document's original, unchunked text. source_id (string, required)."},
	{Domain: "hecate-rag", Method: "rerank_results", Description: "Rerank candidate results against a query. query_text (string) plus a list of candidates."},
	{Domain: "hecate_agora", Method: "search_posts", Description: "Search forum posts. query (string, required)."},
	{Domain: "hecate_agora", Method: "search_archive", Description: "Search archived (older) posts. Same args as search_posts."},
	{Domain: "hecate_agora", Method: "get_posts_page", Description: "Page through recent posts. No required args -- try {} first."},
	{Domain: "hecate_agora", Method: "get_thread_by_post_id", Description: "Fetch a post's full thread by id. post_id (string, required)."},
	{Domain: "hecate_graph", Method: "resolve_entity", Description: "Resolve a knowledge-graph entity by name or id. entity_id or name (string, required)."},
	{Domain: "hecate_graph", Method: "resolve_link", Description: "Resolve a knowledge-graph link/relation. Exact args not verified -- read the error and retry."},
	{Domain: "hecate_graph", Method: "narrate_entity", Description: "Narrate a graph entity in natural language. entity_id (string, required)."},
	{Domain: "hecate_graph", Method: "narrate_link", Description: "Narrate a graph link in natural language. Exact args not verified -- read the error and retry."},
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

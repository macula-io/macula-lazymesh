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
// First sourced from a live mesh survey, 2026-09-06 (a teammate's
// investigation, io.macula realm ABB81B5A...FCD1). It names mcl-rag and
// mcl-graph, which serve these procedures under Org/Name.
// Deliberately excluded, org by org:
//   - mcl-rag's ingest_document/add_knowledge/upload_knowledge/
//     prune_chunks/schedule_reembed/retire_document/embed_document/
//     detect_corpus_change (all write the shared corpus; mcl-rag serves
//     them to its operators only). classify_topics also belongs on this
//     list -- CORRECTED 2026-09-06 (Fable's Phase 3 review): curated from
//     the tool NAME, not its handler. Real behavior (maybe_classify_topics,
//     right next to prune_chunks/retire_document): loads a document,
//     chunks it, calls a paid LLM classifier per chunk, then WRITES topic
//     tags back into the shared corpus (rag_store:tag_chunk) -- changing
//     everyone's topic-filtered search results, not a read. The lesson, not
//     just the fix: curating this list means reading the handler source,
//     never inferring safety from a name that merely sounds like a query.
//   - mcl-graph's learn_link (writes the graph, with the caller as
//     provenance)
//   - mcl-mail, mcl-citizens entirely (state-mutating and/or identity-gated)
//   - mcl-warden/mcl-sentinel -- real security tooling (warden publishes SSH
//     attack-vector facts from the box it runs on and can act as a
//     honeypot; sentinel subscribes and enriches; macula-portal/vigil is
//     the real consumer). Excluded for side-effecting security semantics
//     (ensnare triggers a real honeypot action, not a read-only query). If
//     that data is ever needed, go through macula-portal/vigil, not an
//     ad-hoc direct RPC call here.
//   - the DHT's own default-realm noise (agent.<node_id>.ring, realm
//     bootstrap procedures, macula-ts's own test artifacts)
//
// Argument names below are read from each handler's source (2026-09-24,
// mcl-rag and mcl-graph origin/main), NOT DHT-advertised -- the DHT carries
// no argument schema at all. A wrong-argument call still returns a clean,
// specific error (e.g. "query_text_or_vector_required" for a missing query
// field), which flows back into the conversation as a normal tool result
// the model can read and retry against, exactly like any other tool error.
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
	{Org: "mcl-rag", Method: "search_chunks_semantic", Description: "Semantic search over the shared corpus. query_text (string, required, not query). top_k (integer), topics (list) optional."},
	{Org: "mcl-rag", Method: "answer_query", Description: "Answer a question against the shared corpus. query_text (string, required). top_k (integer) optional."},
	{Org: "mcl-rag", Method: "get_chunk_by_id", Description: "Fetch one corpus chunk by id. chunk_id (string, required)."},
	{Org: "mcl-rag", Method: "get_source_by_id", Description: "Fetch one corpus source document's metadata by id. source_id (string, required)."},
	{Org: "mcl-rag", Method: "list_sources_page", Description: "Page through corpus source documents. offset, limit (integers) optional -- try {} first."},
	{Org: "mcl-rag", Method: "list_chunks_by_source", Description: "List one source document's chunks. source_path (string, required). limit (integer) optional."},
	{Org: "mcl-rag", Method: "get_document_verbatim", Description: "Fetch a source document's original, unchunked text. source_path (string, required)."},
	{Org: "mcl-rag", Method: "rerank_results", Description: "Rerank candidate hits against a query. query_id, query_text (strings) and hits (list), all required."},
	{Org: "mcl-graph", Method: "resolve_entity", Description: "Resolve a knowledge-graph entity. entity_id (string, required)."},
	{Org: "mcl-graph", Method: "resolve_link", Description: "Resolve links from a subject. subject (string, required). predicate (string), depth (integer, default 1), direction (out|in|both, default out) optional."},
	{Org: "mcl-graph", Method: "narrate_entity", Description: "Narrate a graph entity in natural language. entity_id (string, required)."},
	{Org: "mcl-graph", Method: "narrate_link", Description: "Narrate a subject's links in natural language. subject (string, required)."},
}

// CuratedProcedure describes one entry in Curated.
type CuratedProcedure struct {
	Org         string
	Method      string
	Description string
}

// Procedure returns the "org/method" form: the org-namespaced name mcl_om
// advertises in a procedure_advertisement and mesh_call takes. The realm
// authorizes each org to serve its names (D25), which is why discovery
// matches this exact string and nothing looser.
func (p CuratedProcedure) Procedure() string {
	return p.Org + "/" + p.Method
}

// ToolName is the synthetic tool name this procedure is exposed under.
// Sanitized because OpenAI-compatible function-calling names reject "/"
// and "." and some backends are picky about "-" too; prefixed with
// "mesh_service_" so these are visually distinct from macula-mcp's own
// "mesh_*" tools and from anything internal/localtools exposes.
func (p CuratedProcedure) ToolName() string {
	sanitized := strings.NewReplacer("-", "_", "/", "_", ".", "_").Replace(p.Procedure())
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

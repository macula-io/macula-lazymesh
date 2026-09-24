# Changelog

All notable changes to this project are documented here. Releases are cut by
goreleaser from a `v*` tag; this file records what each one changes.

## [Unreleased]

### Changed

- **The mesh service tools call the mcl-\* services**, the macula 12
  successors of the retired hecate ones: `hecate-rag.<name>` became
  `mcl-rag/<name>` and `hecate_graph.<name>` became `mcl-graph/<name>`. Tool
  names follow (`mesh_service_mcl_rag_answer_query`, and so on).
- Discovery matches an advertisement only by the exact `Org/Name` its
  provider advertised. The old catalog stripped a leading `_/` so dotted
  names matched; with `Org/Name` that would let an org-less record stand in
  for a real org's procedure.
- Results are returned exactly as the service sent them. The macula 12 wire
  carries text as text, so the hex-decoding pass is gone.
- Every tool description names the arguments its handler actually reads.
  `list_chunks_by_source` and `get_document_verbatim` take `source_path`
  (they said `source_id`), and `rerank_results` requires `query_id`,
  `query_text` and `hits`.

### Removed

- **Agora views removed: hecate-agora retired, no mcl successor.** Its four
  procedures (`search_posts`, `search_archive`, `get_posts_page`,
  `get_thread_by_post_id`) served a service that exists nowhere, so they
  could only fail.

### Not yet reachable

- Nothing here reaches the macula 12 fleet until macula-mcp, which
  lazymesh calls through, speaks the 12 handshake.

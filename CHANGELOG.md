# Changelog

All notable changes to this project are documented here. Releases are cut by
goreleaser from a `v*` tag; this file records what each one changes.

## [Unreleased]

### Added

- **Rings reach a headless controller.** A ring still awaiting an answer is
  sent on the control socket as `{"type":"ring_pending","ring_id",
  "from","purpose"}` the moment it arrives, not when the agent next idles.
  The Go SDK's `Turn.PendingRings` and the Python client's `pending_rings`
  collect the ones that arrive during a turn (#19).
- README: "Driving lazymesh from another program".

### Security

- **The control socket serves only controllers of the same user.** Each
  connection is checked against the kernel's record of who connected
  (SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS); a peer of another uid,
  root included, is closed before the handshake. Windows has no peer
  credentials on unix sockets, so `--socket` and `--headless` now refuse to
  start there instead of running unchecked (#19).
- **Agents started from different configs no longer share one contact
  policy file.** `contact_policy_file` now defaults to `contact_policy.json`
  beside the config file it was loaded from. Each agent writes its ring
  policy into that file at start, so with one shared file an agent set to
  `accept-everyone` opened a `do-not-disturb` agent to every caller. The
  default config keeps its old path, so an existing trust list stays put; an
  explicit `contact_policy_file` is honoured as before (#19).

## [0.1.0] - 2026-10-05

### Fixed

- **An old macula-mcp is refused at startup, by name.** On Node older than
  `@macula-io/mcp`'s engines floor (>= 24.18.1), npx resolved 0.16.0 without a
  word and lazymesh ran a macula-mcp that cannot reach the fleet (#17). The
  version macula-mcp reports in its MCP handshake must now be 0.38.0 or later
  (handshake v5, the fleet's wire), on every spawn and respawn; otherwise
  lazymesh stops, naming the version it got and pointing at `node --version`.

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

// Package sessionhost hosts lazymesh agent sessions as supervised Ergo
// processes — the actor-core walking skeleton behind
// plans/PLAN_LAZYMESH_AGENT_QUALITY_GAPS.md decision D4 (Ergo, conditions
// from RESEARCH_ERGO_FOR_LAZYMESH.md's GO-WITH-CONDITIONS verdict).
//
// What this increment proves, and nothing more:
//
//   - the embedded Ergo node boots with networking completely disabled
//     (condition 1: Ergo's default silently binds TCP :11144 — a lazymesh
//     node opens no listener; the mesh is the transport and the
//     unix-socket control plane is the only local surface);
//   - one agent conversation (a real agent.Loop: provider, tools, system
//     prompt) lives in a supervised session actor;
//   - a panic inside the session — a panicking provider is the exact
//     failure mode supervision exists for — is recovered by Ergo, the
//     session dies with TerminateReasonPanic, monitors are told, and the
//     root supervisor restarts it with fresh state (condition Q3);
//   - subscribers receive loop events, and status is a synchronous call.
//
// Known next increments, deliberately NOT here: dynamic N-session hosting
// (OD2: one supervisor child per session instead of one static child),
// the unix-socket control plane (D1), and JSONL persistence (D2). The
// walking skeleton stands on its own and is what those build on.
package sessionhost

import (
	"sync"

	"ergo.services/ergo"
	"ergo.services/ergo/gen"
)

// NodeName is the single embedded node's name: one node per process, ever.
// Ergo requires the FQDN shape (name@host); with networking disabled the
// host part is a label, not an address.
const NodeName gen.Atom = "lazymesh@localhost"

var (
	bootOnce sync.Once
	bootNode gen.Node
	bootErr  error
)

// Node returns this process's single embedded Ergo node, started on first
// use. Networking is completely disabled (D4 condition 1), logging is
// capped at error so the node stays quiet while the TUI owns the screen.
// Boot failure is permanent: a process that cannot start its node cannot
// host sessions, and retrying cannot change that.
func Node() (gen.Node, error) {
	bootOnce.Do(func() {
		bootNode, bootErr = ergo.StartNode(NodeName, gen.NodeOptions{
			Network: gen.NetworkOptions{Mode: gen.NetworkModeDisabled},
			// The logo banner writes to stdout on every node start; the
			// TUI owns the screen, so the node must stay silent.
			Log: gen.LogOptions{Level: gen.LogLevelError, DefaultLogger: gen.DefaultLoggerOptions{DisableBanner: true}},
		})
	})
	return bootNode, bootErr
}

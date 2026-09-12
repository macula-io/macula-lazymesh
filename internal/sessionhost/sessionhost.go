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
//   - any number of agent conversations (real agent.Loops) live as
//     supervised session actors under one simple_one_for_one root, each
//     owned, addressed by pid, and started/stopped/listed by the host
//     API (OD2);
//   - a panic inside a session — a panicking provider is the exact
//     failure mode supervision exists for — is recovered by Ergo, the
//     session dies with TerminateReasonPanic, monitors are told, and the
//     root supervisor restarts it with fresh state and the same
//     SessionArgs (condition Q3);
//   - a normal stop ends a session for good (transient strategy: no
//     resurrection of closed conversations);
//   - subscribers receive loop events, and status is a synchronous call.
//
// Known next increments, deliberately NOT here: the unix-socket control
// plane (D1), JSONL persistence (D2), and wiring cmd/lazymesh onto this
// tree. The walking skeleton stands on its own and is what those build on.
package sessionhost

import (
	"io"
	"sync"

	"ergo.services/ergo"
	"ergo.services/ergo/gen"
)

// UnsupportedReply is the answer to a request the receiver does not
// recognize. It is delivered as a normal reply rather than an error
// because, in Ergo, a non-nil error from HandleCall TERMINATES the
// process — an unknown request must never be able to kill a session or
// the root.
type UnsupportedReply struct {
	Request any
}

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
// capped at error so the node stays quiet while the TUI owns the screen,
// and every error/panic line Ergo emits goes to logOutput (agent.log in
// production) — actor crashes are invisible otherwise, because Ergo's
// default logger writes to stdout (found live 2026-09-12). Boot failure
// is permanent: a process that cannot start its node cannot host
// sessions, and retrying cannot change that.
func Node(logOutput io.Writer) (gen.Node, error) {
	bootOnce.Do(func() {
		bootNode, bootErr = ergo.StartNode(NodeName, gen.NodeOptions{
			Network: gen.NetworkOptions{Mode: gen.NetworkModeDisabled},
			// The logo banner writes to stdout on every node start; the
			// TUI owns the screen, so the node must stay silent — and
			// what it DOES log belongs in the agent log, not stdout.
			Log: gen.LogOptions{Level: gen.LogLevelError, DefaultLogger: gen.DefaultLoggerOptions{
				DisableBanner: true,
				Output:        logOutput,
			}},
		})
	})
	return bootNode, bootErr
}

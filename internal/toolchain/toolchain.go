// Package toolchain assembles the one chain of guards every tool call
// passes through, so a guard cannot exist in one profile only
// (macula-io/macula-lazymesh#100).
//
// Why this is not agent.Chain, as #100 first worded it: internal/rostergate
// wraps an agent.ToolSource and therefore imports internal/agent, so a chain
// built inside agent could not include the roster gate without an import
// cycle. The chain is one capability -- "what a model call must get past to
// run" -- with one declared order, and it lives in the one package that can
// see every guard: agent's wrappers, rostergate's, and the profile's own.
//
// The order, outermost first, is fixed here and nowhere else:
//
//	denial -> refuse -> hook -> ask -> no-blocking-wait -> terse ->
//	handoff -> watched-rooms -> allowlist -> the profile's sources
//
// Each position has a reason, not a history:
//
//   - denial is outermost so every refusal is recorded, whichever layer
//     made it (the allowlist's included);
//   - refuse comes before hook and ask, so a call that can never run is
//     never put to a controller or to the operator as a question;
//   - hook comes before ask, so a controller's policy refuses before a
//     human is interrupted;
//   - ask comes before the argument-clamping wrapper and every gate below
//     it, so the operator approves a call that has not yet been rewritten;
//   - terse and handoff only reshape what the model sees and what it may
//     say; they sit above the allowlist, which filters what exists at all;
//   - watched-rooms above the allowlist, because the roster limits a call
//     the allowlist already admits (mesh_say to a room this session does
//     not watch);
//   - allowlist innermost, the last thing between the model and a source.
package toolchain

import (
	"context"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/rostergate"
)

// Policy is everything a profile declares about its chain: what the guards
// wrap, and which of them are on. A zero-valued option is off, and off
// means absent from the chain, never present-and-passing.
type Policy struct {
	// Base is what the guards wrap: the profile's tool sources, already
	// combined into one.
	Base agent.ToolSource
	// Allowlist is the names reachable at all. The guard is always in the
	// chain and fails closed: a nil or empty list admits nothing, so a
	// profile that means to offer tools must say which.
	Allowlist []string
	// WatchedRooms, when set, limits mesh_say to the rooms this session
	// watches (the coding profile's roster).
	WatchedRooms func(room string) bool
	// Handoff enforces the reply kinds' in_reply_to rule on mesh_say.
	Handoff bool
	// Terse shortens mesh tools' descriptions for the model.
	Terse bool
	// NoBlockingWait clamps the wait a call may ask for, so a model's own
	// choice cannot stall the turn.
	NoBlockingWait bool
	// Ask names the tools that wait for the operator's approval; Consent
	// is how the session asks. Both are needed for the guard to be in the
	// chain.
	Ask     []string
	Consent agent.Consent
	// Describe, when set, is what the operator is shown instead of the raw
	// arguments.
	Describe agent.DescribeFunc
	// PreTool is a controller's pre-tool hook (the control socket's G8).
	PreTool agent.Gate
	// RefuseBeforeAsking settles a call from its own arguments.
	RefuseBeforeAsking agent.RefuseFunc
	// RecordDenial receives every refused call of the chain.
	RecordDenial func(agent.Denial)
}

// Guards lists the guards p turns on, in the order a call meets them. It is
// for tests and logs; Chain is the only thing that builds the chain.
func (p Policy) Guards() []string {
	var on []string
	if p.RecordDenial != nil {
		on = append(on, "denial")
	}
	if p.RefuseBeforeAsking != nil {
		on = append(on, "refuse")
	}
	if p.PreTool != nil {
		on = append(on, "hook")
	}
	if len(p.Ask) > 0 {
		on = append(on, "ask")
	}
	if p.NoBlockingWait {
		on = append(on, "no-blocking-wait")
	}
	if p.Terse {
		on = append(on, "terse")
	}
	if p.Handoff {
		on = append(on, "handoff")
	}
	if p.WatchedRooms != nil {
		on = append(on, "watched-rooms")
	}
	on = append(on, "allowlist")
	return on
}

// Chain wraps p.Base in the guards p turns on, in the one declared order.
// Every caller that reaches a source does so through this function: the
// profiles' builders declare a Policy, the session builds it here.
func Chain(p Policy) agent.ToolSource {
	inner := p.Base
	inner = agent.NewAllowlistSource(inner, p.Allowlist)
	if p.WatchedRooms != nil {
		inner = rostergate.NewSayOnlyToWatchedRooms(inner, p.WatchedRooms)
	}
	if p.Handoff {
		inner = agent.NewHandoffSource(inner)
	}
	if p.Terse {
		inner = agent.NewTerseDescriptionSource(inner)
	}
	if p.NoBlockingWait {
		inner = agent.NewNoBlockingWaitSource(inner)
	}
	if len(p.Ask) > 0 {
		// The ask guard is never dropped: a list that names a tool as
		// needing approval admits nothing about that tool when there is
		// nobody to ask. The fail-closed form refuses those calls, so a
		// policy cannot open a door by leaving Consent unset (found by
		// review on #100: dropping the guard failed OPEN).
		if p.Consent == nil {
			inner = newUnansweredAsk(inner, p.Ask)
		} else {
			asked := agent.NewAskSource(inner, p.Ask, p.Consent)
			if p.Describe != nil {
				asked.Describe(p.Describe)
			}
			inner = asked
		}
	}
	if p.PreTool != nil {
		inner = agent.NewHookSource(inner, p.PreTool)
	}
	if p.RefuseBeforeAsking != nil {
		inner = agent.NewRefuseSource(inner, p.RefuseBeforeAsking)
	}
	if p.RecordDenial != nil {
		inner = agent.NewDenialSource(inner, p.RecordDenial)
	}
	return inner
}

// unansweredAsk is the ask guard when the policy names tools that need
// approval but supplies no way to ask. It refuses exactly those calls and
// passes every other name through, at the ask guard's own position in the
// chain, so the guard is present in its declared place rather than
// silently absent: an operation nobody can approve must not run.
type unansweredAsk struct {
	inner agent.ToolSource
	ask   map[string]bool
}

func newUnansweredAsk(inner agent.ToolSource, ask []string) *unansweredAsk {
	set := make(map[string]bool, len(ask))
	for _, name := range ask {
		set[name] = true
	}
	return &unansweredAsk{inner: inner, ask: set}
}

func (u *unansweredAsk) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return u.inner.ListTools(ctx)
}

func (u *unansweredAsk) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	if u.ask[name] {
		return "", agent.Refused(fmt.Errorf("%q needs approval, but this profile supplies no consent to ask for it", name))
	}
	return u.inner.CallToolRaw(ctx, name, argumentsJSON)
}

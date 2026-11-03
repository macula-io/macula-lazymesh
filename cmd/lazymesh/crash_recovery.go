package main

import (
	"context"
	"log"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// reattachAndSubscribe finds the replacement of a session that died and
// subscribes the event bridge to it. A restarted session starts with no
// subscribers, so without the subscription the TUI and the control socket
// would see nothing from it.
func reattachAndSubscribe(ctx context.Context, n gen.Node, root, oldPid, bridge gen.PID, agentLog *log.Logger) (gen.PID, error) {
	newPid, err := reattachSession(ctx, n, root, oldPid, agentLog)
	if err != nil {
		return gen.PID{}, err
	}
	if _, err := n.Call(bridge, bridgeSubscribeTo{Session: newPid}); err != nil {
		return gen.PID{}, err
	}
	return newPid, nil
}

// recoverFromCrash handles a turn the session crashed in (#99): it
// reattaches to the replacement and tells the operator which steps of the
// turn ran, instead of sending the prompt again. A step such as mesh_say
// has already reached a peer; running the turn again would repeat it.
func recoverFromCrash(ctx context.Context, n gen.Node, root, oldPid, bridge gen.PID, tuiEvents, frontendEvents *agent.Outlet, agentLog *log.Logger) (gen.PID, error) {
	newPid, err := reattachAndSubscribe(ctx, n, root, oldPid, bridge, agentLog)
	if err != nil {
		return gen.PID{}, err
	}
	report, err := sessionhost.TakeCrashReport(n, newPid)
	if err != nil {
		agentLog.Printf("lazymesh agent: crash report of %s unreadable: %s", newPid, err)
		report = sessionhost.CrashReport{}
	}
	reportCrash(tuiEvents, frontendEvents, agentLog, report)
	return newPid, nil
}

// reportCrash ends the crashed turn on both outlets: the notice, then the
// listening beat, which a controller that does not know turn_crashed
// settles on.
func reportCrash(tuiEvents, frontendEvents *agent.Outlet, agentLog *log.Logger, report sessionhost.CrashReport) {
	notice := report.Notice()
	agentLog.Printf("lazymesh agent: %s", notice)
	emitTui(tuiEvents, frontendEvents, agent.Event{Kind: agent.EventTurnCrashed, Text: notice})
	emitTui(tuiEvents, frontendEvents, agent.Event{Kind: agent.EventListening})
}

// reportEarlierCrash tells the operator about a turn the previous run of
// this session crashed in (a kill, not a restart under supervision): the
// session found it in its log when it started. No turn is running, so
// there is no listening beat to end one; and a controller may not be
// connected yet, so announce (the control socket's Announce, nil without
// one) tells each controller whenever it connects.
func reportEarlierCrash(n gen.Node, session gen.PID, tuiEvents *agent.Outlet, announce func(agent.Event), agentLog *log.Logger) {
	report, err := sessionhost.TakeCrashReport(n, session)
	if err != nil {
		agentLog.Printf("lazymesh agent: crash report of %s unreadable: %s", session, err)
		return
	}
	if len(report.Steps) == 0 {
		return
	}
	notice := report.Notice()
	agentLog.Printf("lazymesh agent: %s", notice)
	ev := agent.Event{Kind: agent.EventTurnCrashed, Text: notice}
	tuiEvents.Send(ev)
	if announce != nil {
		announce(ev)
	}
}

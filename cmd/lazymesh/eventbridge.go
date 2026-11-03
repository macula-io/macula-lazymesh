package main

import (
	"context"
	"log"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
)

// eventBridge is the frontend bus's first occupant: an Ergo process that
// subscribes to the session actor and hands every loop event to the parts
// of lazymesh that are NOT actors yet — the TUI's event channel, the
// agent log, and the room-waiter's reactive churn detection. It preserves
// today's event-forwarding behavior (logEvent + waiterMgr sync), moved
// from a goroutine inside runAgent to a node-level process the session
// broadcasts to.
//
// It lives at node level, not under the root supervisor: the root's
// simple_one_for_one template hosts sessions and only sessions, and the
// multi-supervisor tree (root → sessions, plus a second supervisor for
// view-model processes like this bridge) is the next increment's shape.
type eventBridge struct {
	act.Actor

	ctx       context.Context
	tui       *agent.Outlet
	frontend  *agent.Outlet
	agentLog  *log.Logger
	waiterMgr *roomwaiter.Manager
	counters  *counters.Registry
}

func eventBridgeFactory() gen.ProcessBehavior { return &eventBridge{} }

func (b *eventBridge) Init(args ...any) error {
	b.ctx = args[0].(context.Context)
	b.tui = args[1].(*agent.Outlet)
	b.frontend = args[2].(*agent.Outlet)
	b.agentLog = args[3].(*log.Logger)
	b.waiterMgr = args[4].(*roomwaiter.Manager)
	b.counters = args[5].(*counters.Registry)
	return nil
}

// HandleMessage receives sessionhost.Event broadcasts and re-routes them
// exactly as runAgent's old forwarding goroutine did.
func (b *eventBridge) HandleMessage(from gen.PID, message any) error {
	msg, ok := message.(sessionhost.Event)
	if !ok {
		return nil
	}
	logEvent(b.agentLog, msg.Event)
	b.counters.Record(msg.Event)
	if msg.Event.Kind == agent.EventListening {
		// G16: one structured counters line per turn boundary — the
		// cheapest structured telemetry that still surfaces "tool X
		// keeps failing" or "no turns ran all day" at a glance.
		b.agentLog.Print(b.counters.Snapshot().Line())
	}

	// Reactive room-churn detection, ported verbatim from runAgent's old
	// forwarding goroutine: tool results drive waiterMgr.Sync/Add/Remove,
	// never a poll loop of the waiter's own. See roomwaiter.Manager.Add's
	// doc comment for why mesh_join_room/mesh_ring/mesh_answer_ring each
	// add one room, and mesh_rooms drives a full Sync.
	if b.waiterMgr != nil && msg.Event.Kind == agent.EventToolResult {
		switch msg.Event.ToolName {
		case "mesh_rooms":
			b.waiterMgr.Sync(b.ctx, parseJoinedRooms(msg.Event.Text))
		case "mesh_join_room", "mesh_ring":
			if room := parseRoomTopic(msg.Event.Text); room != "" {
				b.waiterMgr.Add(b.ctx, room)
			}
		case "mesh_answer_ring":
			if room, joined := parseAnsweredRingRoom(msg.Event.Text); joined {
				b.waiterMgr.Add(b.ctx, room)
			}
		case "mesh_leave_room":
			if room := parseRoomTopic(msg.Event.Text); room != "" {
				b.waiterMgr.Remove(room)
			}
		}
	}

	// Each consumer has its own Outlet, so a TUI that is not reading never
	// holds back the socket, and neither ever loses an approval request or
	// a turn-complete marker (#98).
	b.tui.Send(msg.Event)
	b.frontend.Send(msg.Event)
	return nil
}

// HandleCall answers subscribeTo: the bridge subscribes to the session in
// its own name, because events are broadcast to the sender PID and an
// external Call would register the calling node instead.
func (b *eventBridge) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch req := request.(type) {
	case bridgeSubscribeTo:
		if err := b.Send(req.Session, sessionhost.Subscribe{}); err != nil {
			return nil, err
		}
		return true, nil
	}
	return nil, gen.ErrUnsupported
}

// bridgeSubscribeTo asks the bridge to subscribe itself to one session.
type bridgeSubscribeTo struct {
	Session gen.PID
}

// startEventBridge spawns the bridge on n and subscribes it to session.
func startEventBridge(n gen.Node, ctx context.Context, session gen.PID, tuiEvents, frontendEvents *agent.Outlet, agentLog *log.Logger, waiterMgr *roomwaiter.Manager, counters *counters.Registry) (gen.PID, error) {
	bridge, err := n.Spawn(eventBridgeFactory, gen.ProcessOptions{}, ctx, tuiEvents, frontendEvents, agentLog, waiterMgr, counters)
	if err != nil {
		return gen.PID{}, err
	}
	if _, err := n.Call(bridge, bridgeSubscribeTo{Session: session}); err != nil {
		return gen.PID{}, err
	}
	return bridge, nil
}

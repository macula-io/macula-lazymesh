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
// agent log, and room membership: a membership tool result goes to the
// session's one roomChanged door (#97). It preserves
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
	// roomChanged is the session's one door for room membership (#97),
	// built in run(): the bridge hands it the membership change each
	// mesh_join_room, mesh_ring, mesh_answer_ring and mesh_leave_room
	// result reports.
	roomChanged func(agent.Event)
	counters    *counters.Registry
}

func eventBridgeFactory() gen.ProcessBehavior { return &eventBridge{} }

func (b *eventBridge) Init(args ...any) error {
	b.ctx = args[0].(context.Context)
	b.tui = args[1].(*agent.Outlet)
	b.frontend = args[2].(*agent.Outlet)
	b.agentLog = args[3].(*log.Logger)
	b.waiterMgr = args[4].(*roomwaiter.Manager)
	b.roomChanged = args[5].(func(agent.Event))
	b.counters = args[6].(*counters.Registry)
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

	// Reactive room-churn detection: tool results, never a poll loop of
	// the waiter's own. A single-room change goes through roomChanged
	// (#97); mesh_rooms reports the whole joined set, which re-syncs the
	// waiter. See roomwaiter.Manager.Add's doc comment for why
	// mesh_join_room/mesh_ring/mesh_answer_ring each add one room.
	if msg.Event.Kind == agent.EventToolResult {
		if change, ok := agent.RoomChangeOf(msg.Event.ToolName, msg.Event.Text); ok {
			b.roomChanged(change)
		}
		if msg.Event.ToolName == "mesh_rooms" && b.waiterMgr != nil {
			b.waiterMgr.Sync(b.ctx, parseJoinedRooms(msg.Event.Text))
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
func startEventBridge(n gen.Node, ctx context.Context, session gen.PID, tuiEvents, frontendEvents *agent.Outlet, agentLog *log.Logger, waiterMgr *roomwaiter.Manager, roomChanged func(agent.Event), counters *counters.Registry) (gen.PID, error) {
	bridge, err := n.Spawn(eventBridgeFactory, gen.ProcessOptions{}, ctx, tuiEvents, frontendEvents, agentLog, waiterMgr, roomChanged, counters)
	if err != nil {
		return gen.PID{}, err
	}
	if _, err := n.Call(bridge, bridgeSubscribeTo{Session: session}); err != nil {
		return gen.PID{}, err
	}
	return bridge, nil
}

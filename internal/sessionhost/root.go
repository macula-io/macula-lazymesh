package sessionhost

import (
	"fmt"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"
)

// sessionTemplate is the simple_one_for_one child template every session
// instance is started from. Sessions are anonymous: no per-instance
// registered name exists, which is exactly what N independent
// conversations (OD2) need — names would be collision management for no
// benefit, and PIDs are the handles.
const sessionTemplate gen.Atom = "session"

// root is the supervision tree's root: a simple_one_for_one supervisor
// whose template child is the session actor. The transient strategy
// restarts a session exactly when it dies abnormally — a panic restarts
// it with fresh state and the SAME SessionArgs (SOFO carries each
// instance's args across restarts) — while a normal stop ends it for
// good. A supervisor-level permanent strategy would resurrect every
// closed conversation; transient is the deliberate OTP-faithful choice.
type root struct{ act.Supervisor }

func rootFactory() gen.ProcessBehavior { return &root{} }

func (r *root) Init(args ...any) (act.SupervisorSpec, error) {
	return act.SupervisorSpec{
		Type:    act.SupervisorTypeSimpleOneForOne,
		Restart: act.SupervisorRestart{Strategy: act.SupervisorStrategyTransient},
		Children: []act.SupervisorChildSpec{
			{Name: sessionTemplate, Factory: sessionFactory},
		},
	}, nil
}

// StartRoot spawns the root supervisor on n and returns its pid. Roots
// are anonymous: every later operation addresses them by pid. Two roots
// can coexist — one per process is the production shape, but nothing
// enforces it here.
func StartRoot(n gen.Node) (gen.PID, error) {
	return n.Spawn(rootFactory, gen.ProcessOptions{})
}

// startSessionRequest asks the root to start one session from its
// template with the given args.
type startSessionRequest struct {
	Args SessionArgs
}

// startSessionReply carries the new session's pid back to StartSession.
type startSessionReply struct {
	PID gen.PID
}

// listSessionsRequest asks the root for every live session pid.
type listSessionsRequest struct{}

type listSessionsReply struct {
	PIDs []gen.PID
}

// HandleCall answers the host API. startSessionRequest discovers the new
// child's pid by diffing Children() across StartChild — StartChild
// reports only an error, never the pid, and handleAction spawns the
// child synchronously, so the new pid is visible the moment StartChild
// returns. Anything unknown gets UnsupportedReply: a non-nil error here
// would terminate the ROOT.
func (r *root) HandleCall(from gen.PID, ref gen.Ref, request any) (any, error) {
	switch request.(type) {
	case startSessionRequest:
		before := make(map[gen.PID]struct{}, len(r.Children()))
		for _, c := range r.Children() {
			before[c.PID] = struct{}{}
		}
		req := request.(startSessionRequest)
		if err := r.StartChild(sessionTemplate, req.Args); err != nil {
			return nil, err
		}
		for _, c := range r.Children() {
			if _, seen := before[c.PID]; !seen {
				return startSessionReply{PID: c.PID}, nil
			}
		}
		return nil, fmt.Errorf("sessionhost: session started but no new child appeared in Children()")
	case listSessionsRequest:
		pids := make([]gen.PID, 0, len(r.Children()))
		for _, c := range r.Children() {
			pids = append(pids, c.PID)
		}
		return listSessionsReply{PIDs: pids}, nil
	}
	r.Log().Warning("sessionhost: root answered unknown request %T from %s with UnsupportedReply", request, from)
	if err := r.SendResponse(from, ref, UnsupportedReply{Request: request}); err != nil {
		return nil, err
	}
	return nil, nil
}

// StartSession starts a new session child under root and returns its pid.
// The new session owns its own conversation; nothing is shared with its
// siblings by construction.
func StartSession(n gen.Node, root gen.PID, args SessionArgs) (gen.PID, error) {
	reply, err := n.Call(root, startSessionRequest{Args: args})
	if err != nil {
		return gen.PID{}, err
	}
	r, ok := reply.(startSessionReply)
	if !ok {
		return gen.PID{}, fmt.Errorf("sessionhost: root answered with %T, want startSessionReply", reply)
	}
	return r.PID, nil
}

// Sessions lists every live session pid under root.
func Sessions(n gen.Node, root gen.PID) ([]gen.PID, error) {
	reply, err := n.Call(root, listSessionsRequest{})
	if err != nil {
		return nil, err
	}
	r, ok := reply.(listSessionsReply)
	if !ok {
		return nil, fmt.Errorf("sessionhost: root answered with %T, want listSessionsReply", reply)
	}
	return r.PIDs, nil
}

// StopSession ends one session for good. The exit reason is normal, which
// under the transient strategy means no restart: the supervisor drops the
// child and the conversation is over. Stopping is node-level — the root
// is not involved, and a caller holding only the session pid can stop it.
func StopSession(n gen.Node, pid gen.PID) error {
	return n.SendExit(pid, gen.TerminateReasonNormal)
}

package sessionhost

import (
	"fmt"

	"ergo.services/ergo/act"
	"ergo.services/ergo/gen"
)

// root is the supervision tree's root: a one_for_one supervisor whose
// children are agent sessions. The walking skeleton declares ONE static
// session child (registered under its own name, so tests can each have a
// distinct root); dynamic N-session hosting under a simple_one_for_one
// root is the next increment (OD2).
type root struct{ act.Supervisor }

func rootFactory() gen.ProcessBehavior { return &root{} }

// Init builds the one_for_one spec with the single session child. args[0]
// is the child's SessionArgs; args[1] is the child's registered name.
func (r *root) Init(args ...any) (act.SupervisorSpec, error) {
	sessArgs, ok := args[0].(SessionArgs)
	if !ok {
		return act.SupervisorSpec{}, fmt.Errorf("sessionhost: expected SessionArgs, got %T", args[0])
	}
	name, ok := args[1].(gen.Atom)
	if !ok || name == "" {
		return act.SupervisorSpec{}, fmt.Errorf("sessionhost: expected a session name atom, got %T", args[1])
	}
	return act.SupervisorSpec{
		Type: act.SupervisorTypeOneForOne,
		// Permanent: a session that dies — panic included — is always
		// restarted with fresh state; the default intensity (5 restarts
		// per 5s) is the bound that keeps a crash-loop from spinning the
		// node forever.
		Restart: act.SupervisorRestart{Strategy: act.SupervisorStrategyPermanent},
		Children: []act.SupervisorChildSpec{
			{Name: name, Factory: sessionFactory, Args: []any{sessArgs}},
		},
	}, nil
}

// StartRoot spawns the root supervisor on n, hosting one session built
// from args and registered under sessionName. name/sessionName must be
// unique per node; a second StartRoot with the same names fails with
// gen.ErrTaken rather than silently sharing a tree.
func StartRoot(n gen.Node, name, sessionName gen.Atom, args SessionArgs) (gen.PID, error) {
	return n.Spawn(rootFactory, gen.ProcessOptions{}, args, sessionName)
}

// SessionPID resolves the session child registered under sessionName.
func SessionPID(n gen.Node, sessionName gen.Atom) (gen.PID, error) {
	return n.ProcessPID(sessionName)
}

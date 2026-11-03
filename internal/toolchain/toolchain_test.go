package toolchain_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
)

// These tests assert the declared order by what a call DOES -- which
// callbacks fire, in what order, and with which arguments -- rather than
// by reading Policy.Guards' list of names back. A wrapper moved to a
// different position changes a trace here; a wrapper merely renamed or
// reordered in a list does not, which is the point.

// recordingBase is the innermost source: it records every call it sees,
// arguments included, so a test can prove what the guards let through.
type recordingBase struct {
	calls []string
	tools []mcpclient.Tool
}

func (b *recordingBase) ListTools(context.Context) ([]mcpclient.Tool, error) {
	return b.tools, nil
}

func (b *recordingBase) CallToolRaw(_ context.Context, name, argumentsJSON string) (string, error) {
	b.calls = append(b.calls, name+" "+argumentsJSON)
	return `{"sent":{"message_id":"0123456789abcdef0123456789abcdef"}}`, nil
}

// trace records the callbacks that fired, in order. Denials are recorded
// by the same recorder every guard shares, so an unexpected denial is
// visible in the trace where it happened.
type trace struct {
	steps  []string
	denied []agent.Denial
}

func (tr *trace) record(step string) { tr.steps = append(tr.steps, step) }

// guards wires a Policy whose every callback appends its own name to the
// trace, then returns the chain and the base so a test can inspect both.
// A nil entry in Policy leaves that guard out, exactly as a profile would.
type policyOpts struct {
	allowlist      []string
	ask            []string
	denyConsent    bool
	denyHook       bool
	refuse         bool
	refusing       bool
	handoff        bool
	watchedRooms   func(string) bool
	terse          bool
	noBlockingWait bool
	recordDenial   bool
}

func (o policyOpts) build(tr *trace, base *recordingBase) toolchain.Policy {
	p := toolchain.Policy{Base: base, Allowlist: o.allowlist, Handoff: o.handoff, Terse: o.terse, NoBlockingWait: o.noBlockingWait, WatchedRooms: o.watchedRooms}
	if o.recordDenial {
		p.RecordDenial = func(d agent.Denial) {
			tr.record("denial")
			tr.denied = append(tr.denied, d)
		}
	}
	if o.refuse || o.refusing {
		refusing := o.refusing
		p.RefuseBeforeAsking = func(string, string) error {
			tr.record("refuse")
			if refusing {
				return errors.New("a push to main is never approved")
			}
			return nil
		}
	}
	p.PreTool = func(context.Context, string, string) (bool, string, error) {
		tr.record("hook")
		if o.denyHook {
			return false, "the controller said no", nil
		}
		return true, "", nil
	}
	// Passing no ask tools would leave the guard out; every test that does
	// not care about consent still runs it, so the trace shows its place.
	p.Ask = o.ask
	if len(p.Ask) == 0 {
		p.Ask = []string{"__always_asked__"}
	}
	p.Consent = func(context.Context, string, string) (bool, error) {
		tr.record("ask")
		return !o.denyConsent, nil
	}
	return p
}

// TestChainOrderACleanCallMeetsEveryGuardInTheDeclaredOrder: a call every
// guard admits reaches the base through each of them, in the documented
// order, and nothing else fires.
func TestChainOrderACleanCallMeetsEveryGuardInTheDeclaredOrder(t *testing.T) {
	tr := &trace{}
	base := &recordingBase{}
	p := policyOpts{
		allowlist:    []string{"mesh_rooms"},
		ask:          []string{"mesh_rooms"},
		refuse:       true,
		handoff:      true,
		recordDenial: true,
		terse:        true,
	}.build(tr, base)

	if _, err := toolchain.Chain(p).CallToolRaw(context.Background(), "mesh_rooms", `{}`); err != nil {
		t.Fatalf("a clean call was refused: %v", err)
	}
	want := []string{"refuse", "hook", "ask", "base"}
	if got := strings.Join(append(tr.steps, "base"), ","); got != strings.Join(want, ",") {
		t.Fatalf("order = %s, want %s", got, strings.Join(want, ","))
	}
	if len(base.calls) != 1 || base.calls[0] != "mesh_rooms {}" {
		t.Fatalf("base saw %v, want exactly the one clean call", base.calls)
	}
	if len(tr.denied) != 0 {
		t.Fatalf("a clean call was recorded as a denial: %+v", tr.denied)
	}
}

// TestChainOrderTheClampSitsInsideAskAndOutsideTheBase: the operator
// approves a call as the model made it, the model's own wait is clamped
// after that, and the base only ever sees the clamped value -- so consent
// is asked about what will really run, and the no-blocking-wait guard is
// between them.
func TestChainOrderTheClampSitsInsideAskAndOutsideTheBase(t *testing.T) {
	var asked string
	base := &recordingBase{}
	p := toolchain.Policy{
		Base:           base,
		Allowlist:      []string{"mesh_ring"},
		Ask:            []string{"mesh_ring"},
		NoBlockingWait: true,
		Consent: func(_ context.Context, _, argumentsJSON string) (bool, error) {
			asked = argumentsJSON
			return true, nil
		},
	}
	if _, err := toolchain.Chain(p).CallToolRaw(context.Background(), "mesh_ring", `{"to":"them","wait_join_seconds":600}`); err != nil {
		t.Fatalf("call refused: %v", err)
	}
	if !strings.Contains(asked, "600") {
		t.Fatalf("consent was asked about %s, want the model's own 600", asked)
	}
	if len(base.calls) != 1 {
		t.Fatalf("base calls = %v, want one", base.calls)
	}
	if !strings.Contains(base.calls[0], `"wait_join_seconds":10`) {
		t.Fatalf("base saw %s, want the wait clamped to 10", base.calls[0])
	}
}

// TestChainRecordsARefusalFromEveryLayer: the denial recorder is
// outermost, so a refusal made by any guard is recorded, with the tool
// and the reason, and no call reaches the base.
func TestChainRecordsARefusalFromEveryLayer(t *testing.T) {
	const watched = "agents.room.0123456789abcdef0123456789abcdef"
	cases := []struct {
		name     string
		opts     policyOpts
		tool     string
		args     string
		wantWord string
	}{
		{
			name:     "refuse",
			opts:     policyOpts{allowlist: []string{"mesh_rooms"}, refusing: true, recordDenial: true},
			tool:     "mesh_rooms",
			args:     `{}`,
			wantWord: "refused before approval was asked",
		},
		{
			name:     "hook",
			opts:     policyOpts{allowlist: []string{"mesh_rooms"}, denyHook: true, recordDenial: true},
			tool:     "mesh_rooms",
			args:     `{}`,
			wantWord: "pre-tool hook",
		},
		{
			name:     "ask",
			opts:     policyOpts{allowlist: []string{"mesh_rooms"}, ask: []string{"mesh_rooms"}, denyConsent: true, recordDenial: true},
			tool:     "mesh_rooms",
			args:     `{}`,
			wantWord: "denied by the operator",
		},
		{
			name:     "handoff",
			opts:     policyOpts{allowlist: []string{"mesh_say"}, handoff: true, recordDenial: true},
			tool:     "mesh_say",
			args:     `{"room_topic":"` + watched + `","text":"done","kind":"result_reported"}`,
			wantWord: "REQUIRES in_reply_to",
		},
		{
			name: "watched-rooms",
			opts: policyOpts{allowlist: []string{"mesh_say"}, recordDenial: true, watchedRooms: func(string) bool { return false }},
			tool: "mesh_say",
			args: `{"room_topic":"` + watched + `","text":"hi"}`,
			// The roster's own refusal, not the handoff's: the envelope
			// above is a plain remark and passes handoff first.
			wantWord: "limited to the rooms",
		},
		{
			name:     "allowlist",
			opts:     policyOpts{allowlist: []string{"mesh_rooms"}, recordDenial: true},
			tool:     "shell_exec",
			args:     `{}`,
			wantWord: "not on the allowlist",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &trace{}
			base := &recordingBase{}
			_, err := toolchain.Chain(tc.opts.build(tr, base)).CallToolRaw(context.Background(), tc.tool, tc.args)
			if err == nil {
				t.Fatalf("the %s guard let the call through", tc.name)
			}
			if !agent.IsRefusal(err) {
				t.Fatalf("the %s refusal is not marked as a refusal: %v", tc.name, err)
			}
			if len(tr.denied) != 1 {
				t.Fatalf("denials recorded = %+v, want exactly the %s refusal", tr.denied, tc.name)
			}
			d := tr.denied[0]
			if d.Tool != tc.tool || d.Arguments != tc.args || !strings.Contains(d.Reason, tc.wantWord) {
				t.Fatalf("denial = %+v, want tool %s and reason containing %q", d, tc.tool, tc.wantWord)
			}
			if len(base.calls) != 0 {
				t.Fatalf("base was called despite the %s refusal: %v", tc.name, base.calls)
			}
		})
	}
}

// TestChainRefuseAndHookDecideBeforeConsent: a call settled by its own
// arguments or by the controller is never put to the operator as a
// question.
func TestChainRefuseAndHookDecideBeforeConsent(t *testing.T) {
	cases := []struct {
		name string
		opts policyOpts
	}{
		{"refuse", policyOpts{allowlist: []string{"mesh_rooms"}, ask: []string{"mesh_rooms"}, denyConsent: true}},
		{"hook", policyOpts{allowlist: []string{"mesh_rooms"}, ask: []string{"mesh_rooms"}, denyHook: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"-refuses", func(t *testing.T) {
			tr := &trace{}
			// denyConsent makes consent refuse too, so if ask ran the
			// trace would show it -- and the call would have already been
			// refused by the earlier guard.
			tc.opts.denyConsent = true
			if tc.name == "refuse" {
				tc.opts.refusing = true
			}
			_, err := toolchain.Chain(tc.opts.build(tr, &recordingBase{})).CallToolRaw(context.Background(), "mesh_rooms", `{}`)
			if err == nil {
				t.Fatal("the call was allowed")
			}
			for _, step := range tr.steps {
				if step == "ask" {
					t.Fatalf("consent was asked about a call the %s guard had already refused: %v", tc.name, tr.steps)
				}
			}
		})
	}
}

// TestChainHandoffBeforeWatchedRoomsBeforeAllowlist:
//   - a reply envelope is refused by handoff even in a room this session
//     does not watch, so handoff sits outside watched-rooms;
//   - a well-formed say in a room it does not watch is refused by the
//     roster even though the allowlist admits mesh_say, so watched-rooms
//     sits outside the allowlist, and the base never sees the call;
//   - a tool the allowlist does not name never reaches the base, so the
//     allowlist is the last guard before it.
func TestChainHandoffBeforeWatchedRoomsBeforeAllowlist(t *testing.T) {
	unwatched := "agents.room.0123456789abcdef0123456789abcdef"

	t.Run("handoff-outside-watched-rooms", func(t *testing.T) {
		tr := &trace{}
		p := policyOpts{
			allowlist:    []string{"mesh_say"},
			handoff:      true,
			watchedRooms: func(string) bool { return false },
			recordDenial: true,
		}.build(tr, &recordingBase{})
		_, err := toolchain.Chain(p).CallToolRaw(context.Background(), "mesh_say",
			`{"room_topic":"`+unwatched+`","text":"done","kind":"result_reported"}`)
		if err == nil || !strings.Contains(err.Error(), "REQUIRES in_reply_to") {
			t.Fatalf("err = %v, want the handoff refusal before the roster's", err)
		}
	})

	t.Run("watched-rooms-outside-allowlist", func(t *testing.T) {
		base := &recordingBase{}
		// mesh_say deliberately NOT on the allowlist: with it admitted,
		// both orders refuse with the roster's message and the test would
		// pass on a swapped chain too (the observation on #100's review).
		// Without it, the refusal's own words say which guard decided
		// first -- the roster's when the order is right, the allowlist's
		// if the two were swapped.
		p := policyOpts{
			allowlist:    []string{"mesh_rooms"},
			handoff:      true,
			watchedRooms: func(string) bool { return false },
			recordDenial: true,
		}.build(&trace{}, base)
		_, err := toolchain.Chain(p).CallToolRaw(context.Background(), "mesh_say",
			`{"room_topic":"`+unwatched+`","text":"hi"}`)
		if err == nil || !strings.Contains(err.Error(), "limited to the rooms") {
			t.Fatalf("err = %v, want the roster's refusal before the allowlist's", err)
		}
		if len(base.calls) != 0 {
			t.Fatalf("base was called despite the roster refusal: %v", base.calls)
		}
	})

	t.Run("allowlist-innermost", func(t *testing.T) {
		base := &recordingBase{}
		p := policyOpts{allowlist: []string{"mesh_rooms"}, recordDenial: true}.build(&trace{}, base)
		_, err := toolchain.Chain(p).CallToolRaw(context.Background(), "shell_exec", `{"command":"id"}`)
		if err == nil || !strings.Contains(err.Error(), "not on the allowlist") {
			t.Fatalf("err = %v, want the allowlist refusal", err)
		}
		if len(base.calls) != 0 {
			t.Fatalf("base was called for an unlisted tool: %v", base.calls)
		}
	})
}

// TestChainListToolsThroughTerse: the terse guard reshapes what the model
// is told exists, so a chain with Terse on advertises the shortened
// mesh_rooms description and the hand-authored mesh_join_room schema.
func TestChainListToolsThroughTerse(t *testing.T) {
	const longRooms = "Rooms this agent is in, with participants, plus public rooms on central you have not joined. Instant, a local read, never blocks, and it says a great deal more here than a small model needs."
	base := &recordingBase{tools: []mcpclient.Tool{
		{Name: "mesh_rooms", Description: longRooms, InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"host": map[string]any{"type": "string"}},
		}},
		{Name: "mesh_join_room", Description: longRooms, InputSchema: map[string]any{"type": "object"}},
	}}
	p := toolchain.Policy{Base: base, Allowlist: []string{"mesh_rooms", "mesh_join_room"}, Terse: true}

	tools, err := toolchain.Chain(p).ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]mcpclient.Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	rooms, ok := byName["mesh_rooms"]
	if !ok {
		t.Fatal("mesh_rooms missing")
	}
	if rooms.Description == longRooms || len(rooms.Description) >= len(longRooms) {
		t.Fatalf("mesh_rooms description was not shortened: %q", rooms.Description)
	}
	if props, _ := rooms.InputSchema.(map[string]any)["properties"].(map[string]any); props != nil {
		if _, has := props["host"]; has {
			t.Fatal("mesh_rooms still offers the host parameter")
		}
	}
	join, ok := byName["mesh_join_room"]
	if !ok {
		t.Fatal("mesh_join_room missing")
	}
	if join.Description == longRooms {
		t.Fatal("mesh_join_room description was not replaced")
	}
	// Schema replaced, not just decorated: base had no properties at all,
	// and the chain's replacement requires room_topic.
	required, _ := join.InputSchema.(map[string]any)["required"].([]string)
	if len(required) != 1 || required[0] != "room_topic" {
		t.Fatalf("mesh_join_room schema = %+v, want the hand-authored room_topic-only schema", join.InputSchema)
	}
}

// TestChainAnAskListWithoutConsentRefusesTheCall pins the chain's
// fail-closed rule: naming a tool as needing approval while supplying
// nobody to ask refuses THAT tool, and only that one. Before this was
// pinned, the chain dropped the guard instead, so an ask list with no
// consent ran the very calls it named (found by the review of #100).
func TestChainAnAskListWithoutConsentRefusesTheCall(t *testing.T) {
	var denied []agent.Denial
	base := &recordingBase{}
	p := toolchain.Policy{
		Base:         base,
		Allowlist:    []string{"shell_exec", "mesh_rooms"},
		Ask:          []string{"shell_exec"},
		RecordDenial: func(d agent.Denial) { denied = append(denied, d) },
	}
	src := toolchain.Chain(p)

	_, err := src.CallToolRaw(context.Background(), "shell_exec", `{"command":"id"}`)
	if err == nil {
		t.Fatal("an ask-listed call ran with no consent to approve it")
	}
	if !agent.IsRefusal(err) || !strings.Contains(err.Error(), "needs approval") {
		t.Fatalf("err = %v, want the chain's own refusal", err)
	}
	if len(base.calls) != 0 {
		t.Fatalf("base was called although nothing could approve the call: %v", base.calls)
	}
	if len(denied) != 1 || denied[0].Tool != "shell_exec" {
		t.Fatalf("denials = %+v, want the refused call recorded", denied)
	}

	// A tool the ask list does not name is untouched: this is the ask
	// guard's position, not a blanket denial.
	if _, err := src.CallToolRaw(context.Background(), "mesh_rooms", `{}`); err != nil {
		t.Fatalf("an unlisted tool was refused: %v", err)
	}
	if len(base.calls) != 1 || base.calls[0] != "mesh_rooms {}" {
		t.Fatalf("base saw %v, want exactly the unlisted call", base.calls)
	}
}

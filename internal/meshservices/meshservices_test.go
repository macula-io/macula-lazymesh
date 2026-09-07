package meshservices

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args map[string]any
}

type fakeMCP struct {
	calls              []call
	discoveryResponses []string // successive mesh_find_records_by_type responses, last one repeats
	discoveryIdx       int
	callToolFunc       func(name string, args map[string]any) (string, error) // for mesh_call and anything else
}

func (f *fakeMCP) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.calls = append(f.calls, call{name: name, args: args})
	if name == "mesh_find_records_by_type" {
		if len(f.discoveryResponses) == 0 {
			return `{"records":[]}`, nil
		}
		idx := f.discoveryIdx
		if idx >= len(f.discoveryResponses) {
			idx = len(f.discoveryResponses) - 1
		}
		f.discoveryIdx++
		return f.discoveryResponses[idx], nil
	}
	if f.callToolFunc != nil {
		return f.callToolFunc(name, args)
	}
	return "", fmt.Errorf("fakeMCP: no handler for %s", name)
}

func discoveryRecordFor(realm, procedure string) string {
	return fmt.Sprintf(`{"records":[{"procedure_advertisement":{"realm":%q,"procedure":%q}}]}`, realm, procedure)
}

const testRealm = "ABB81B5A614B63551B400B810648C0C8A78EFAD845442630C94B46CC95D2FCD1"

func TestListTools_OnlyIncludesCuratedAndDiscoveredProcedures(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{
		fmt.Sprintf(`{"records":[
			{"procedure_advertisement":{"realm":%q,"procedure":"hecate_agora.get_posts_page"}},
			{"procedure_advertisement":{"realm":%q,"procedure":"some_random.unrelated_procedure"}}
		]}`, testRealm, testRealm),
	}}
	src := New(fake)

	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected exactly 1 tool (only the curated+discovered one), got %d: %+v", len(tools), tools)
	}
	if tools[0].Name != "mesh_service_hecate_agora_get_posts_page" {
		t.Fatalf("unexpected tool name: %s", tools[0].Name)
	}
}

func TestListTools_StripsLeadingUnderscoreSlashPrefix(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{
		discoveryRecordFor(testRealm, "_/hecate_agora.get_posts_page"),
	}}
	src := New(fake)

	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected the \"_/\"-prefixed record to still match after stripping, got %d tools", len(tools))
	}
}

func TestListTools_UndiscoveredCuratedProcedureIsNotListed(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{`{"records":[]}`}}
	src := New(fake)

	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools when nothing is discovered, got %d", len(tools))
	}
}

// Covers R2 (2026-09-07): discovery resolves once per process lifetime,
// not on a TTL -- Fable's review flagged a re-discovering tool schema
// array as directly self-defeating for provider-side prompt caching.
// Calls ListTools 5 times (not just twice) specifically to distinguish
// "once per session, forever" from a TTL that just hasn't expired yet
// within the test's own short runtime.
func TestListTools_DiscoversOnceNeverAgain(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{
		discoveryRecordFor(testRealm, "hecate_agora.get_posts_page"),
	}}
	src := New(fake)

	for i := 0; i < 5; i++ {
		if _, err := src.ListTools(context.Background()); err != nil {
			t.Fatalf("ListTools call %d returned error: %v", i, err)
		}
	}

	discoveryCalls := 0
	for _, c := range fake.calls {
		if c.name == "mesh_find_records_by_type" {
			discoveryCalls++
		}
	}
	if discoveryCalls != 1 {
		t.Fatalf("expected exactly 1 discovery call across 5 ListTools calls, got %d", discoveryCalls)
	}
}

// Covers the logging half of the same fix: SetLogger's line prints
// exactly once, the moment discovery actually happens, not on every
// call -- an operator watching agent.log needs to see this happened,
// but not be spammed by it repeating on a source that never re-checks.
func TestListTools_LogsDiscoveryExactlyOnce(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{
		discoveryRecordFor(testRealm, "hecate_agora.get_posts_page"),
	}}
	src := New(fake)
	var buf bytes.Buffer
	src.SetLogger(log.New(&buf, "", 0))

	for i := 0; i < 3; i++ {
		if _, err := src.ListTools(context.Background()); err != nil {
			t.Fatalf("ListTools call %d returned error: %v", i, err)
		}
	}

	got := buf.String()
	if strings.Count(got, "mesh_service discovery") != 1 {
		t.Fatalf("expected exactly one discovery log line across 3 calls, got: %q", got)
	}
}

func TestCallToolRaw_RoutesWithCorrectRealmAndProcedure(t *testing.T) {
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			if name != "mesh_call" {
				t.Fatalf("expected mesh_call, got %s", name)
			}
			if args["procedure"] != "hecate_agora.get_posts_page" {
				t.Fatalf("expected procedure hecate_agora.get_posts_page, got %v", args["procedure"])
			}
			if args["realm"] != testRealm {
				t.Fatalf("expected realm %s, got %v", testRealm, args["realm"])
			}
			return `{"result":{"ok":1}}`, nil
		},
	}
	src := New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	result, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", `{}`)
	if err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	if result != `{"result":{"ok":1}}` {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestCallToolRaw_UnknownToolIsAnError(t *testing.T) {
	src := New(&fakeMCP{discoveryResponses: []string{`{"records":[]}`}})
	if _, err := src.CallToolRaw(context.Background(), "mesh_service_never_discovered", "{}"); err == nil {
		t.Fatalf("expected an error calling a tool that was never discovered")
	}
}

func TestCallToolRaw_RetriesOnceOnTransientFailure(t *testing.T) {
	attempts := 0
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			attempts++
			if attempts == 1 {
				return "", fmt.Errorf("connection: read stream: Application error 0x0 (remote): closed")
			}
			return `{"result":"ok"}`, nil
		},
	}
	src := New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	result, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}")
	if err != nil {
		t.Fatalf("expected the retry to succeed, got error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (1 failure + 1 retry), got %d", attempts)
	}
	if result != `{"result":"ok"}` {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestCallToolRaw_TransportFailureRetriesOnceThenGivesUp(t *testing.T) {
	attempts := 0
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			attempts++
			return "", fmt.Errorf("connection: read stream: Application error 0x0 (remote): closed")
		},
	}
	src := New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err == nil {
		t.Fatalf("expected an error when both the call and its retry fail")
	}
	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts (1 + 1 retry) for a persistent transport failure, got %d", attempts)
	}
}

// This is the specific regression Fable's review flagged: an
// application-level error (the remote procedure ran and rejected the
// arguments) must NOT be retried with identical arguments -- retrying
// would just fail identically again, wasting a real call against shared
// infra for no chance of a different outcome.
func TestCallToolRaw_ApplicationErrorIsNotRetried(t *testing.T) {
	attempts := 0
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			attempts++
			return "", fmt.Errorf("mesh_call failed: macula-ts: CALL failed: unknown_error (bolt4 code 15): query_text_or_vector_required (bolt4=unknown_error, retryable=true)")
		},
	}
	src := New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err == nil {
		t.Fatalf("expected an error to propagate")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry) for an application-level error, got %d", attempts)
	}
}

func TestCallToolRaw_PassesExplicitTimeout(t *testing.T) {
	var gotTimeout any
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			gotTimeout = args["timeout_ms"]
			return `{"result":"ok"}`, nil
		},
	}
	src := New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	if gotTimeout != callTimeoutMS {
		t.Fatalf("expected timeout_ms %v, got %v", callTimeoutMS, gotTimeout)
	}
}

// The actual point of pinning: a record advertising a curated procedure
// name under any OTHER realm must be invisible to this package entirely,
// not merely deprioritized against a real record under the pinned realm.
func TestListTools_IgnoresRecordsUnderAnyOtherRealm(t *testing.T) {
	spoofedRealm := "0000000000000000000000000000000000000000000000000000000000000000"
	fake := &fakeMCP{discoveryResponses: []string{
		discoveryRecordFor(spoofedRealm, "hecate_agora.get_posts_page"),
	}}
	src := New(fake)

	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected a record under a non-pinned realm to be ignored entirely, got %+v", tools)
	}
}

// The concrete attack Fable's review described: a spoofed record under
// the public/default realm and a real record under the pinned realm both
// exist for the same procedure name. Only the pinned-realm one may ever
// be used, regardless of which one appears first or last in the dump.
func TestListTools_RealRecordWinsOverSpoofedRecordRegardlessOfOrder(t *testing.T) {
	spoofedRealm := "0000000000000000000000000000000000000000000000000000000000000000"
	real := fmt.Sprintf(`{"procedure_advertisement":{"realm":%q,"procedure":"hecate_agora.get_posts_page"}}`, testRealm)
	spoofed := fmt.Sprintf(`{"procedure_advertisement":{"realm":%q,"procedure":"hecate_agora.get_posts_page"}}`, spoofedRealm)

	// spoofed record LAST -- the exact ordering that broke the old
	// last-one-wins map-based logic.
	fake := &fakeMCP{discoveryResponses: []string{
		fmt.Sprintf(`{"records":[%s,%s]}`, real, spoofed),
	}}
	src := New(fake)

	var calledRealm any
	fake.callToolFunc = func(name string, args map[string]any) (string, error) {
		calledRealm = args["realm"]
		return `{"result":"ok"}`, nil
	}

	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	if calledRealm != testRealm {
		t.Fatalf("expected the pinned realm %s to be used regardless of record order, got %v", testRealm, calledRealm)
	}
}

func TestPinnedRealm_MatchesKnownIoMaculaRealm(t *testing.T) {
	if pinnedRealm != testRealm {
		t.Fatalf("computed pinnedRealm %s does not match the known io.macula realm %s -- discovery would silently find nothing", pinnedRealm, testRealm)
	}
}

func TestIsTransportError(t *testing.T) {
	transport := []string{
		"connection: read stream: Application error 0x0 (remote): closed",
		"unknown_next_peer",
		"temporary_relay_failure",
		"procedure has no direct-dial advertisement",
		"context deadline exceeded",
	}
	for _, msg := range transport {
		if !isTransportError(fmt.Errorf("%s", msg)) {
			t.Fatalf("expected %q to be classified as a transport error", msg)
		}
	}

	application := []string{
		"mesh_call failed: macula-ts: CALL failed: unknown_error (bolt4 code 15): query_text_or_vector_required (bolt4=unknown_error, retryable=true)",
		"missing_entity_id",
		"decode arguments for mesh_service_hecate_agora_get_posts_page: invalid character",
	}
	for _, msg := range application {
		if isTransportError(fmt.Errorf("%s", msg)) {
			t.Fatalf("expected %q to NOT be classified as a transport error", msg)
		}
	}
}

func TestFixedWindowLimiter_AllowsUpToMaxThenRefusesUntilReset(t *testing.T) {
	now := time.Now()
	lim := newFixedWindowLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !lim.allow(now) {
			t.Fatalf("expected call %d to be allowed within budget", i+1)
		}
	}
	if lim.allow(now) {
		t.Fatalf("expected the 4th call in the same window to be refused")
	}
	if !lim.allow(now.Add(time.Minute + time.Second)) {
		t.Fatalf("expected a call after the window elapsed to be allowed again")
	}
}

func TestCallToolRaw_RefusesOverBudget(t *testing.T) {
	attempts := 0
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			attempts++
			return `{"result":"ok"}`, nil
		},
	}
	src := New(fake)
	src.limit = newFixedWindowLimiter(1, time.Minute) // shrink the budget so the test doesn't need 20 real calls
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err != nil {
		t.Fatalf("first call should be within budget, got error: %v", err)
	}
	if _, err := src.CallToolRaw(context.Background(), "mesh_service_hecate_agora_get_posts_page", "{}"); err == nil {
		t.Fatalf("expected the second call to be refused once the budget is exhausted")
	}
	if attempts != 1 {
		t.Fatalf("expected the refused call to never actually reach mesh_call, got %d real attempts", attempts)
	}
}

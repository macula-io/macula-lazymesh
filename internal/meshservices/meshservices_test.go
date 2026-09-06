package meshservices

import (
	"context"
	"fmt"
	"testing"
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

func TestListTools_CachesWithinTTL(t *testing.T) {
	fake := &fakeMCP{discoveryResponses: []string{
		discoveryRecordFor(testRealm, "hecate_agora.get_posts_page"),
	}}
	src := New(fake)

	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("first ListTools returned error: %v", err)
	}
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("second ListTools returned error: %v", err)
	}

	discoveryCalls := 0
	for _, c := range fake.calls {
		if c.name == "mesh_find_records_by_type" {
			discoveryCalls++
		}
	}
	if discoveryCalls != 1 {
		t.Fatalf("expected exactly 1 discovery call within the cache TTL, got %d", discoveryCalls)
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

func TestCallToolRaw_FailsAfterRetryAlsoFails(t *testing.T) {
	attempts := 0
	fake := &fakeMCP{
		discoveryResponses: []string{discoveryRecordFor(testRealm, "hecate_agora.get_posts_page")},
		callToolFunc: func(name string, args map[string]any) (string, error) {
			attempts++
			return "", fmt.Errorf("persistent failure")
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
		t.Fatalf("expected exactly 2 attempts total, got %d", attempts)
	}
}

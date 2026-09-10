package mcpclient

import (
	"os"
	"strings"
	"testing"
)

func TestFilteredEnv_OnlyIncludesAllowlistedVarsThatAreSet(t *testing.T) {
	t.Setenv("LAZYMESH_TEST_ALLOWED", "yes")
	os.Unsetenv("LAZYMESH_TEST_NOT_SET")
	t.Setenv("LAZYMESH_TEST_SECRET", "should-not-appear")

	env := filteredEnv([]string{"LAZYMESH_TEST_ALLOWED", "LAZYMESH_TEST_NOT_SET"})

	found := map[string]bool{}
	for _, kv := range env {
		found[kv] = true
	}
	if !found["LAZYMESH_TEST_ALLOWED=yes"] {
		t.Fatalf("expected LAZYMESH_TEST_ALLOWED=yes in filtered env, got %v", env)
	}
	for _, kv := range env {
		if kv == "LAZYMESH_TEST_SECRET=should-not-appear" {
			t.Fatalf("filteredEnv leaked a variable not on the allowlist: %v", env)
		}
	}
	if len(env) != 1 {
		t.Fatalf("expected exactly 1 entry (unset var should be skipped, not empty-valued), got %v", env)
	}
}

func TestFilteredEnv_EmptyAllowlistProducesEmptyEnv(t *testing.T) {
	env := filteredEnv(nil)
	if len(env) != 0 {
		t.Fatalf("expected an empty environment, got %v", env)
	}
}

func TestLaunchCommand_UsesGivenVersion(t *testing.T) {
	got := launchCommand("1.2.3")
	want := []string{"npx", "-y", "-p", "@macula-io/mcp@1.2.3", "macula-mcp"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestLaunchCommand_EmptyVersionFloatsToLatest(t *testing.T) {
	got := launchCommand("")
	want := "@macula-io/mcp"
	if got[3] != want {
		t.Fatalf("expected no @version suffix (%q), got %q", want, got[3])
	}
}

func TestSpawnEnv_IncludesIdentityFileWhenSet(t *testing.T) {
	env := spawnEnv(SpawnOptions{IdentityFile: "/tmp/identity"})
	if !containsEnv(env, "MACULA_MCP_IDENTITY=/tmp/identity") {
		t.Fatalf("expected MACULA_MCP_IDENTITY in env, got %v", env)
	}
}

func TestSpawnEnv_OmitsIdentityFileWhenEmpty(t *testing.T) {
	env := spawnEnv(SpawnOptions{})
	for _, kv := range env {
		if strings.HasPrefix(kv, "MACULA_MCP_IDENTITY=") {
			t.Fatalf("expected no MACULA_MCP_IDENTITY entry when IdentityFile is empty, got %v", env)
		}
	}
}

func TestSpawnEnv_IncludesContactPolicyFileWhenSet(t *testing.T) {
	env := spawnEnv(SpawnOptions{ContactPolicyFile: "/tmp/contact_policy.json"})
	if !containsEnv(env, "MACULA_MCP_CONTACT_POLICY_FILE=/tmp/contact_policy.json") {
		t.Fatalf("expected MACULA_MCP_CONTACT_POLICY_FILE in env, got %v", env)
	}
}

// This is the specific isolation property the fix exists for: without it,
// every macula-mcp instance on a machine shares one contact_policy.json
// regardless of identity.
func TestSpawnEnv_OmitsContactPolicyFileWhenEmpty(t *testing.T) {
	env := spawnEnv(SpawnOptions{})
	for _, kv := range env {
		if strings.HasPrefix(kv, "MACULA_MCP_CONTACT_POLICY_FILE=") {
			t.Fatalf("expected no MACULA_MCP_CONTACT_POLICY_FILE entry when unset, got %v", env)
		}
	}
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

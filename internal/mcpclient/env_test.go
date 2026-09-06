package mcpclient

import (
	"os"
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

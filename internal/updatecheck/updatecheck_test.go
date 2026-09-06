package updatecheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotice_ReportsNewerVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"0.24.3"}`))
	}))
	defer srv.Close()

	c := Checker{RegistryURL: srv.URL}
	got := c.Notice(context.Background(), "0.24.2")
	want := "macula-mcp v0.24.3 is available (pinned to v0.24.2) -- review the diff before bumping"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNotice_EmptyWhenVersionsMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"0.24.2"}`))
	}))
	defer srv.Close()

	c := Checker{RegistryURL: srv.URL}
	if got := c.Notice(context.Background(), "0.24.2"); got != "" {
		t.Fatalf("expected empty notice when versions match, got %q", got)
	}
}

func TestNotice_EmptyOnNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := Checker{RegistryURL: srv.URL}
	if got := c.Notice(context.Background(), "0.24.2"); got != "" {
		t.Fatalf("expected empty notice on registry error status, got %q", got)
	}
}

func TestNotice_EmptyOnMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := Checker{RegistryURL: srv.URL}
	if got := c.Notice(context.Background(), "0.24.2"); got != "" {
		t.Fatalf("expected empty notice on malformed registry response, got %q", got)
	}
}

// TestNotice_EmptyOnUnreachableRegistry is the specific property this
// package exists for: an unreachable registry (offline use, firewalled
// network) must never surface as an error, only as "nothing to report."
func TestNotice_EmptyOnUnreachableRegistry(t *testing.T) {
	// A closed listener's address: nothing is listening, so the dial
	// fails fast without needing a real network-down simulation.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	c := Checker{RegistryURL: "http://" + addr}
	if got := c.Notice(context.Background(), "0.24.2"); got != "" {
		t.Fatalf("expected empty notice when registry is unreachable, got %q", got)
	}
}

// TestNotice_NeverBlocksPastTimeout confirms the whole point of
// DefaultTimeout: a registry that never responds must not be allowed to
// hang startup indefinitely.
func TestNotice_NeverBlocksPastTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Registered in this order so LIFO unwinding closes block (unblocking
	// the handler goroutine above) before srv.Close() runs -- Close()
	// waits for in-flight handlers to return, so the reverse order
	// deadlocks Close() forever instead of exercising the timeout.
	defer srv.Close()
	defer close(block)

	c := Checker{RegistryURL: srv.URL}
	start := time.Now()
	got := c.Notice(context.Background(), "0.24.2")
	elapsed := time.Since(start)

	if got != "" {
		t.Fatalf("expected empty notice from a hung registry, got %q", got)
	}
	if elapsed > DefaultTimeout+2*time.Second {
		t.Fatalf("Notice took %v, expected it to give up around DefaultTimeout (%v)", elapsed, DefaultTimeout)
	}
}

func TestNotice_DefaultRegistryURLIsSet(t *testing.T) {
	if DefaultRegistryURL == "" {
		t.Fatalf("expected a non-empty default registry URL")
	}
}

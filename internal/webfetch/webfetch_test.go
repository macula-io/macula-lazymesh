package webfetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestSource returns a Source whose address guard admits loopback, so
// tests can point it at an httptest server on 127.0.0.1 — the same
// permissive seam any test of a network guard needs.
func newTestSource() *Source {
	s := New()
	s.AllowAddr = func(ip net.IP) bool { return true }
	return s
}

// TestWebFetchReturnsBodyText proves the happy path: a page's content
// comes back as text.
func TestWebFetchReturnsBodyText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from the web"))
	}))
	defer srv.Close()

	src := newTestSource()
	got, err := src.CallToolRaw(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "hello from the web" {
		t.Fatalf("body = %q", got)
	}
}

// TestWebFetchRejectsNonHTTPSchemes pins the scheme guard.
func TestWebFetchRejectsNonHTTPSchemes(t *testing.T) {
	src := newTestSource()
	for _, url := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://x"} {
		if _, err := src.CallToolRaw(context.Background(), "web_fetch", `{"url":"`+url+`"}`); err == nil {
			t.Fatalf("expected %q to be refused", url)
		}
	}
}

// TestWebFetchTruncatesOversizedBodies pins the size cap: content past
// maxBodyBytes is cut with a marker.
func TestWebFetchTruncatesOversizedBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxBodyBytes+5000)))
	}))
	defer srv.Close()

	src := newTestSource()
	got, err := src.CallToolRaw(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(got, "truncated") || len(got) > maxBodyBytes+200 {
		t.Fatalf("truncation contract broken: %d bytes, marker=%v", len(got), strings.Contains(got, "truncated"))
	}
}

// TestSSRFGuardRefusesLoopback pins the guard the whole package exists
// for: a URL resolving to 127.0.0.1 is refused by the default Source,
// even though the test server genuinely answers there.
func TestSSRFGuardRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never be read"))
	}))
	defer srv.Close()

	src := New() // default guard: public addresses only
	if _, err := src.CallToolRaw(context.Background(), "web_fetch", `{"url":"`+srv.URL+`"}`); err == nil {
		t.Fatal("expected a loopback fetch to be refused by the SSRF guard")
	}
}

// TestSSRFGuardRefusesPrivateRanges pins the address classification:
// 10.x, 192.168.x, 169.254.x and ::1 are all non-public.
func TestSSRFGuardRefusesPrivateRanges(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"172.16.0.1", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fe80::1", false},
	}
	for _, c := range cases {
		if got := PublicAddr(net.ParseIP(c.ip)); got != c.want {
			t.Fatalf("PublicAddr(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

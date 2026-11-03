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

// TestSSRFGuardRefusesPrivateRanges pins the address classification: one
// address inside every non-public range is refused, wrapped forms
// (IPv4-mapped, NAT64, 6to4) are judged by the IPv4 address they carry,
// and real public addresses pass (#93).
func TestSSRFGuardRefusesPrivateRanges(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// public
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"2001:4860:4860::8888", true},
		{"64:ff9b::808:808", true}, // NAT64 of 8.8.8.8
		{"2002:808:808::1", true},  // 6to4 of 8.8.8.8
		// IPv4 special-purpose
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"100.64.0.1", false},
		{"100.100.100.200", false},
		{"100.127.255.254", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"169.254.169.254", false},
		{"172.16.0.1", false},
		{"192.0.0.1", false},
		{"192.0.2.1", false},
		{"192.88.99.1", false},
		{"192.168.1.1", false},
		{"198.18.0.1", false},
		{"198.19.255.254", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"224.0.0.1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		// IPv6 special-purpose
		{"::", false},
		{"::1", false},
		{"::7f00:1", false},           // IPv4-compatible
		{"::ffff:127.0.0.1", false},   // IPv4-mapped loopback
		{"::ffff:10.0.0.1", false},    // IPv4-mapped private
		{"64:ff9b::7f00:1", false},    // NAT64 of loopback
		{"64:ff9b::a9fe:a9fe", false}, // NAT64 of link-local
		{"64:ff9b::a00:1", false},     // NAT64 of private
		{"64:ff9b:1::1", false},
		{"2002:7f00:1::1", false},   // 6to4 of loopback
		{"2002:c0a8:101::1", false}, // 6to4 of private
		{"2002:6440:1::1", false},   // 6to4 of CGNAT
		{"100::1", false},
		{"2001::1", false},
		{"2001:db8::1", false},
		{"3fff::1", false},
		{"5f00::1", false},
		{"fc00::1", false},
		{"fd00:ec2::254", false},
		{"fe80::1", false},
		{"fec0::1", false},
		{"ff02::1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test address %q", c.ip)
		}
		if got := PublicAddr(ip); got != c.want {
			t.Errorf("PublicAddr(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// listenOn starts srv on addr, for a server that must not sit on
// 127.0.0.1.
func listenOn(t *testing.T, addr string, h http.Handler) *httptest.Server {
	t.Helper()
	srv := unstartedOn(t, addr, h)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// unstartedOn is listenOn without the start, for a TLS server.
func unstartedOn(t *testing.T, addr string, h http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = ln
	return srv
}

// guardedAgainst returns a Source whose guard refuses only the internal
// address 127.0.0.1 and whose resolver answers "localhost" with the
// approved address 127.0.0.2 (and "example.com", the name the test TLS
// certificate carries, likewise), while the system resolver still answers
// 127.0.0.1 for it. A fetch that dials what the guard checked never
// reaches 127.0.0.1; one that resolves again at dial time does (#93).
func guardedAgainst() *Source {
	s := New()
	internal := net.ParseIP("127.0.0.1")
	s.AllowAddr = func(ip net.IP) bool { return !ip.Equal(internal) }
	s.LookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "localhost" || host == "example.com" {
			return []net.IP{net.ParseIP("127.0.0.2")}, nil
		}
		return systemLookupIP(ctx, host)
	}
	return s
}

// TestTheFetchDialsTheAddressTheGuardChecked pins #93 on the first
// request: the connection goes to the address the guard approved, so a
// second, different answer for the same name can never be dialed.
func TestTheFetchDialsTheAddressTheGuardChecked(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	defer internal.Close()
	_, port, _ := net.SplitHostPort(internal.Listener.Addr().String())

	got, err := guardedAgainst().CallToolRaw(context.Background(), "web_fetch", `{"url":"http://localhost:`+port+`/"}`)
	if strings.Contains(got, "internal") {
		t.Fatalf("the fetch reached the refused address: %q", got)
	}
	if err == nil {
		t.Fatal("expected the dial to the checked address (nothing listens there) to fail")
	}
}

// TestARedirectDialsTheAddressTheGuardChecked pins #93 on a redirect hop.
func TestARedirectDialsTheAddressTheGuardChecked(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	defer internal.Close()
	_, port, _ := net.SplitHostPort(internal.Listener.Addr().String())
	approved := listenOn(t, "127.0.0.2:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+"/", http.StatusFound)
	}))

	got, err := guardedAgainst().CallToolRaw(context.Background(), "web_fetch", `{"url":"`+approved.URL+`"}`)
	if strings.Contains(got, "internal") {
		t.Fatalf("the redirect reached the refused address: %q", got)
	}
	if err == nil {
		t.Fatal("expected the redirect's dial to the checked address (nothing listens there) to fail")
	}
}

// TestTheHostnameStillTravelsInTheHostHeader pins that dialing the checked
// address keeps the URL's name for the server: Host (and SNI) are not the IP.
func TestTheHostnameStillTravelsInTheHostHeader(t *testing.T) {
	approved := listenOn(t, "127.0.0.2:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Host))
	}))
	_, port, _ := net.SplitHostPort(approved.Listener.Addr().String())

	got, err := guardedAgainst().CallToolRaw(context.Background(), "web_fetch", `{"url":"http://localhost:`+port+`/"}`)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "localhost:"+port {
		t.Fatalf("Host = %q, want localhost:%s", got, port)
	}
}

// TestTheHostnameStillTravelsAsTheTLSServerName pins the same for https:
// the TLS server name is the URL's name, so the certificate is checked
// against it.
func TestTheHostnameStillTravelsAsTheTLSServerName(t *testing.T) {
	approved := unstartedOn(t, "127.0.0.2:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.TLS.ServerName))
	}))
	approved.StartTLS()
	t.Cleanup(approved.Close)
	_, port, _ := net.SplitHostPort(approved.Listener.Addr().String())

	src := guardedAgainst()
	trust := approved.Client().Transport.(*http.Transport).TLSClientConfig
	src.HTTP.Transport.(*http.Transport).TLSClientConfig = trust.Clone()

	got, err := src.CallToolRaw(context.Background(), "web_fetch", `{"url":"https://example.com:`+port+`/"}`)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "example.com" {
		t.Fatalf("TLS server name = %q, want example.com", got)
	}
}

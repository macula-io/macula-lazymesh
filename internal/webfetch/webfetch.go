// Package webfetch gives the agent one more tool: web_fetch, a bounded
// fetch of a URL's text content (G12). It is deliberately NOT a general
// network client:
//
//   - the SSRF guard: only http/https schemes, and every address the
//     connection or a redirect would touch must resolve to a PUBLIC IP —
//     loopback, private, link-local, multicast and unspecified ranges
//     are refused — and the connection goes to the addresses checked,
//     never to the name resolved a second time. An agent whose context can be steered by arbitrary
//     mesh peers must not become a probe for the operator's LAN;
//   - bounded size: the response body is capped and truncated with a
//     marker, never read unbounded;
//   - bounded time: every fetch runs under a 15-second timeout, matching
//     the mesh content transfer's own bounded-operation posture.
//
// The source exists whenever lazymesh runs, but the DEFAULT allowlist
// excludes web_fetch — reachability stays an explicit operator choice
// (tool_allowlist, or tool_asklist for per-call approval), never a side
// effect of this package existing.
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// fetchTimeout bounds one whole fetch (connect + body), the same
// bounded-operation posture as the mesh content transfer's own timeouts.
const fetchTimeout = 15 * time.Second

// dialAttemptTimeout bounds one address's connect, so an address that
// never answers leaves time to try the next one within fetchTimeout.
const dialAttemptTimeout = 5 * time.Second

// maxBodyBytes caps how much of a response ever reaches the model: a
// fetch is for reading a page, not for downloading the internet.
const maxBodyBytes = 100_000

// Source is the web_fetch ToolSource.
type Source struct {
	HTTP *http.Client
	// AllowAddr approves one resolved address for dialing or redirecting.
	// Defaults to PublicAddr (the SSRF guard); a test may substitute a
	// permissive function to point the client at a loopback test server.
	AllowAddr func(ip net.IP) bool
	// LookupIP resolves a host for the guard. Defaults to the system
	// resolver; a test substitutes its own answers.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
}

// New returns a Source with the SSRF-guarding client: every dial and
// every redirect hop validates its resolved addresses against AllowAddr
// (public-only by default).
func New() *Source {
	s := &Source{AllowAddr: PublicAddr, LookupIP: systemLookupIP}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("webfetch: bad address %q: %w", addr, err)
			}
			ips, err := s.LookupIP(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("webfetch: resolve %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("webfetch: resolve %s: no addresses", host)
			}
			for _, ip := range ips {
				if !s.AllowAddr(ip) {
					return nil, fmt.Errorf("webfetch: refused %s (%s is not a public address)", host, ip)
				}
			}
			// Dial the addresses just checked, never the name: resolving it
			// again could give an answer the guard never saw (#93). The
			// transport still sends the name as Host and as the TLS server
			// name, since both come from the request URL, not from addr.
			var dialErr error
			for _, ip := range ips {
				dialer := &net.Dialer{Timeout: dialAttemptTimeout}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				dialErr = err
			}
			return nil, dialErr
		},
	}
	client := &http.Client{Transport: transport, Timeout: fetchTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("webfetch: too many redirects")
		}
		host := req.URL.Hostname()
		ips, err := s.LookupIP(req.Context(), host)
		if err != nil {
			return fmt.Errorf("webfetch: resolve redirect target %s: %w", host, err)
		}
		for _, ip := range ips {
			if !s.AllowAddr(ip) {
				return fmt.Errorf("webfetch: refused redirect to %s (%s is not a public address)", host, ip)
			}
		}
		return nil
	}
	s.HTTP = client
	return s
}

// systemLookupIP is the default LookupIP: the system resolver.
func systemLookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// PublicAddr approves exactly the addresses a mesh-facing fetch should
// ever touch: none of the IANA special-purpose ranges in nonPublic. An
// IPv4-mapped address is judged as its IPv4 address, and a NAT64 or 6to4
// address as the IPv4 address it carries, so wrapping a private address
// in IPv6 does not make it public.
func PublicAddr(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return publicAddr(addr.Unmap().WithZone(""))
}

func publicAddr(addr netip.Addr) bool {
	for _, prefix := range nonPublic {
		if prefix.Contains(addr) {
			return false
		}
	}
	if v4, ok := embeddedV4(addr); ok {
		return publicAddr(v4)
	}
	return true
}

// nat64 and sixToFour carry an IPv4 address inside an IPv6 one: in the
// last 32 bits (RFC 6052) and in bits 16-47 (RFC 3056).
var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func embeddedV4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	if nat64.Contains(addr) {
		return netip.AddrFrom4([4]byte(b[12:16])), true
	}
	if sixToFour.Contains(addr) {
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// nonPublic is every range that is not the public internet, from the IANA
// IPv4 and IPv6 special-purpose address registries plus multicast and the
// reserved space. Global-but-special ranges are refused too: a fetch has no
// business reaching any of them.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8",       // this network
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // shared address space (CGNAT), also cloud metadata
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, also cloud metadata
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, and the limited broadcast address
		// IPv6
		"::/96",          // unspecified, loopback and IPv4-compatible
		"64:ff9b:1::/48", // local-use NAT64
		"100::/64",       // discard-only
		"2001::/23",      // IETF protocol assignments, Teredo included
		"2001:db8::/32",  // documentation
		"3fff::/20",      // documentation
		"5f00::/16",      // segment routing SIDs
		"fc00::/7",       // unique local
		"fe80::/10",      // link-local
		"fec0::/10",      // site-local (deprecated)
		"ff00::/8",       // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// webFetchTool is the one tool this source advertises.
const webFetchTool = "web_fetch"

// ListTools advertises web_fetch. Reachability is the allowlist's job
// (see the package doc): this source merely exists.
func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{
		Name:        webFetchTool,
		Description: "Fetch the text content of a public web URL (http/https only, bounded to ~100KB) and return it as plain text. Use it to look something up on the public web; it cannot reach private or local addresses.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "the full http(s) URL to fetch"},
			},
			"required": []string{"url"},
		},
	}}, nil
}

// CallToolRaw serves web_fetch and refuses anything else — the allowlist
// already gates which tools the model can call, this is the source's own
// boundary.
func (s *Source) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if name != webFetchTool {
		return "", fmt.Errorf("webfetch: unknown tool %q", name)
	}
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("webfetch: bad arguments: %w", err)
	}
	if args.URL == "" {
		return "", fmt.Errorf("webfetch: url is required")
	}
	parsed, err := url.Parse(args.URL)
	if err != nil {
		return "", fmt.Errorf("webfetch: invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("webfetch: only http and https are supported, got %q", parsed.Scheme)
	}

	resp, err := s.HTTP.Get(args.URL)
	if err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("webfetch: read body: %w", err)
	}
	truncated := len(body) > maxBodyBytes
	if truncated {
		body = body[:maxBodyBytes]
	}
	text := strings.TrimSpace(string(body))
	if truncated {
		text += fmt.Sprintf("\n... [truncated: %d bytes of the response shown]", maxBodyBytes)
	}
	return text, nil
}

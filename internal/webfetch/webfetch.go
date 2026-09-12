// Package webfetch gives the agent one more tool: web_fetch, a bounded
// fetch of a URL's text content (G12). It is deliberately NOT a general
// network client:
//
//   - the SSRF guard: only http/https schemes, and every address the
//     connection or a redirect would touch must resolve to a PUBLIC IP —
//     loopback, private, link-local, multicast and unspecified ranges
//     are refused. An agent whose context can be steered by arbitrary
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
	"net/url"
	"strings"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// fetchTimeout bounds one whole fetch (connect + body), the same
// bounded-operation posture as the mesh content transfer's own timeouts.
const fetchTimeout = 15 * time.Second

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
}

// New returns a Source with the SSRF-guarding client: every dial and
// every redirect hop validates its resolved addresses against AllowAddr
// (public-only by default).
func New() *Source {
	s := &Source{AllowAddr: PublicAddr}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("webfetch: bad address %q: %w", addr, err)
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("webfetch: resolve %s: %w", host, err)
			}
			for _, ip := range ips {
				if !s.AllowAddr(ip) {
					return nil, fmt.Errorf("webfetch: refused %s (%s is not a public address)", host, ip)
				}
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	client := &http.Client{Transport: transport, Timeout: fetchTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("webfetch: too many redirects")
		}
		host := req.URL.Hostname()
		ips, err := net.DefaultResolver.LookupIP(req.Context(), "ip", host)
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

// PublicAddr approves exactly the addresses a mesh-facing fetch should
// ever touch: everything not loopback, private, link-local, multicast or
// unspecified.
func PublicAddr(ip net.IP) bool {
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}

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

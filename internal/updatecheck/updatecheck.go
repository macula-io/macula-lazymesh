// Package updatecheck performs a lightweight, best-effort check for a newer
// @macula-io/mcp release than the one lazymesh is pinned to (see
// config.Config.MaculaMCPVersion for why lazymesh pins an exact version
// instead of npx's floating "latest" tag -- that pin is a real security
// fix and this package never touches it). Staleness was previously
// invisible until someone remembered to check manually, which is exactly
// what left a running lazymesh two versions behind real upstream bugfixes,
// 2026-09-06. This exists purely to surface that gap: it never blocks
// startup for long, never treats an unreachable or malformed registry
// response as fatal, and never adopts a new version on its own -- reporting
// only.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultRegistryURL is npm's own "latest dist-tag" endpoint for
// @macula-io/mcp: it resolves directly to the current latest version's
// package.json, so no separate dist-tags lookup is needed.
const DefaultRegistryURL = "https://registry.npmjs.org/@macula-io/mcp/latest"

// DefaultTimeout bounds how long Notice will wait on the registry before
// giving up. Startup must never hang on a slow or unreachable network, so
// this is deliberately short -- a few seconds, not the minutes a default
// http.Client would otherwise allow a hung connection to take.
const DefaultTimeout = 3 * time.Second

// Checker queries npm's registry for @macula-io/mcp's current latest
// version. RegistryURL and HTTP are overridable for tests; the zero value
// checks the real npm registry with http.DefaultClient.
type Checker struct {
	RegistryURL string
	HTTP        *http.Client
}

// registryResponse is the only field this package reads out of npm's
// package.json response -- everything else npm returns is ignored.
type registryResponse struct {
	Version string `json:"version"`
}

// Notice returns a one-line, human-readable notice if npm reports a
// @macula-io/mcp version other than pinned, or "" when there's nothing to
// report: versions match, or the registry could not be reached or parsed
// within DefaultTimeout. Every failure mode is silent by design -- this
// check is purely informational and must never make startup noisier or
// less reliable than not having it at all.
func (c Checker) Notice(ctx context.Context, pinned string) string {
	latest, err := c.latestVersion(ctx)
	if err != nil || latest == "" || latest == pinned {
		return ""
	}
	return fmt.Sprintf("macula-mcp v%s is available (pinned to v%s) -- review the diff before bumping", latest, pinned)
}

func (c Checker) latestVersion(ctx context.Context) (string, error) {
	url := c.RegistryURL
	if url == "" {
		url = DefaultRegistryURL
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry returned status %d", resp.StatusCode)
	}

	var parsed registryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	return parsed.Version, nil
}

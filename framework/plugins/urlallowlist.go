package plugins

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// PluginURLAllowlistEnv lists the hosts plugins may be downloaded from, comma
// separated. "*" allows every host, and an entry starting with "." or "*." also
// matches subdomains. The allowlist is always enforced and empty by default, so no
// plugin is downloaded until an operator lists its host. It applies to every
// redirect hop as well as to the first URL.
const PluginURLAllowlistEnv = "BIFROST_PLUGIN_URL_ALLOWLIST"

// CheckPluginURLAllowed refuses a plugin URL whose host is not in
// BIFROST_PLUGIN_URL_ALLOWLIST.
func CheckPluginURLAllowed(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid plugin URL: %w", err)
	}
	if !PluginHostAllowed(strings.ToLower(parsed.Hostname()), os.Getenv(PluginURLAllowlistEnv)) {
		return fmt.Errorf("plugin host %q is not in %s; add the host there to allow downloads from it, or use a local plugin path", parsed.Hostname(), PluginURLAllowlistEnv)
	}
	return nil
}

// PluginHostAllowed reports whether host is allowed by the comma separated
// allowlist. An empty allowlist allows no host.
func PluginHostAllowed(host, allowlist string) bool {
	if host == "" || strings.TrimSpace(allowlist) == "" {
		return false
	}
	for _, entry := range strings.Split(allowlist, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "*" {
			return true
		}
		entry = strings.TrimPrefix(entry, "*")
		switch {
		case entry == "" || entry == ".":
			continue
		case strings.HasPrefix(entry, "."):
			if host == entry[1:] || strings.HasSuffix(host, entry) {
				return true
			}
		case host == entry:
			return true
		}
	}
	return false
}

// withAllowlistedRedirects returns a copy of client that refuses a redirect to a
// host outside the allowlist, then applies client's own redirect policy.
func withAllowlistedRedirects(client *http.Client) *http.Client {
	restricted := *client
	previous := client.CheckRedirect
	restricted.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := CheckPluginURLAllowed(req.URL.String()); err != nil {
			return fmt.Errorf("blocked plugin redirect: %w", err)
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects downloading plugin")
		}
		return nil
	}
	return &restricted
}

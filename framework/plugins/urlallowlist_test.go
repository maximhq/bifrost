package plugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPluginHostAllowed covers the allowlist matching rules, including the empty
// default that allows no host.
func TestPluginHostAllowed(t *testing.T) {
	cases := []struct {
		host, allowlist string
		want            bool
	}{
		{"plugins.example.com", "", false},
		{"plugins.example.com", "  ", false},
		{"plugins.example.com", "*", true},
		{"any.host", "plugins.example.com, *", true},
		{"plugins.example.com", "plugins.example.com", true},
		{"PLUGINS.example.com", "plugins.example.com", false},
		{"cdn.example.com", ".example.com", true},
		{"example.com", ".example.com", true},
		{"cdn.example.com", "*.example.com", true},
		{"evilexample.com", ".example.com", false},
		{"uguu.se", "plugins.example.com", false},
		{"", "*", false},
	}
	for _, tc := range cases {
		if got := PluginHostAllowed(tc.host, tc.allowlist); got != tc.want {
			t.Errorf("PluginHostAllowed(%q, %q) = %v, want %v", tc.host, tc.allowlist, got, tc.want)
		}
	}
}

// TestDownloadPlugin_RefusedByDefault verifies no plugin is downloaded while the
// allowlist is empty, and the request never reaches the server.
func TestDownloadPlugin_RefusedByDefault(t *testing.T) {
	t.Setenv(PluginURLAllowlistEnv, "")
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer server.Close()
	_, err := DownloadPlugin(server.URL+"/p.so", ".so", newPlainClient())
	if err == nil || !strings.Contains(err.Error(), PluginURLAllowlistEnv) {
		t.Fatalf("error = %v, want an allowlist refusal", err)
	}
	if hit {
		t.Fatal("refused download still contacted the server")
	}
}

// TestDownloadPlugin_RedirectToUnlistedHostRefused verifies a listed host cannot
// redirect the download to a host outside the allowlist.
func TestDownloadPlugin_RedirectToUnlistedHostRefused(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.Write([]byte("plugin"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+"/p.so", http.StatusFound)
	}))
	defer redirector.Close()
	t.Setenv(PluginURLAllowlistEnv, "127.0.0.1")

	_, err := DownloadPlugin(redirector.URL+"/p.so", ".so", newPlainClient())
	if err == nil || !strings.Contains(err.Error(), "blocked plugin redirect") {
		t.Fatalf("error = %v, want a blocked redirect", err)
	}
	if targetHit {
		t.Fatal("redirect target outside the allowlist was contacted")
	}
}

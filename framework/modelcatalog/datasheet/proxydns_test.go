package datasheet

import (
	"context"
	"net/url"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestDatasheetFetchDefersUnresolvableHostToProxy guards issue #5001: in a
// proxy-only egress deployment the local resolver cannot answer public names
// (getbifrost.ai), only the proxy can. The fetch must still go to the proxy
// with the hostname for it to resolve, not fail on a local DNS lookup before
// any request is made.
func TestDatasheetFetchDefersUnresolvableHostToProxy(t *testing.T) {
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	// .invalid is reserved (RFC 6761) and never resolves locally.
	const host = "datasheet.invalid"
	store := New(nil, bifrost.NewDefaultLogger(schemas.LogLevelError), Config{
		URL:                "https://" + host + "/pricing.json",
		ModelParametersURL: "https://" + host + "/model-parameters.json",
	})

	for _, tc := range []struct {
		name  string
		fetch func(context.Context) error
	}{
		{"pricing", func(ctx context.Context) error { _, err := store.loadPricingFromURL(ctx); return err }},
		{"model parameters", func(ctx context.Context) error { _, err := store.loadModelParametersFromURL(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set.Reset()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// The recorder only answers CONNECT, so the fetch itself fails; what
			// matters is that it reached the proxy with the unresolved hostname.
			_ = tc.fetch(ctx)
			proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, host+":443", nil)
		})
	}
}

func TestValidateRemoteFeedURL(t *testing.T) {
	for _, tc := range []struct {
		url     string
		wantErr bool
	}{
		{"https://getbifrost.ai/datasheet", false},
		{"http://mirror.internal:8080/pricing.json", false},
		{"https://unresolvable.invalid/pricing.json", false},
		{"ftp://getbifrost.ai/datasheet", true},
		{"gopher://getbifrost.ai/datasheet", true},
		{"https:///no-host", true},
	} {
		parsed := mustParseURL(t, tc.url)
		if err := ValidateRemoteFeedURL(parsed); (err != nil) != tc.wantErr {
			t.Errorf("ValidateRemoteFeedURL(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
		}
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return parsed
}

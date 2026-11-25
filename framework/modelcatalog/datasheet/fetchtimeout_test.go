package datasheet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestSyncFallsBackToDBWhenURLHangs guards BF-2486: a pricing URL that never
// answers used to burn the whole sync deadline, so the DB fallback ran on an
// expired ctx and force sync failed with "failed to get pricing records:
// context deadline exceeded" even though the DB had data.
func TestSyncFallsBackToDBWhenURLHangs(t *testing.T) {
	origPricing, origParams := pricingFetchTimeout, paramsFetchTimeout
	pricingFetchTimeout, paramsFetchTimeout = 500*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pricingFetchTimeout, paramsFetchTimeout = origPricing, origParams })

	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	ctx := context.Background()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	if err != nil {
		t.Fatalf("failed to create config store: %v", err)
	}
	if err := cs.UpsertModelPricesBatch(ctx, []tables.TableModelPricing{{Model: "gpt-4o", Provider: "openai", Mode: "chat"}}); err != nil {
		t.Fatalf("failed to seed pricing: %v", err)
	}
	if err := cs.UpsertModelParametersBatch(ctx, []tables.TableModelParameters{{Model: "gpt-4o", Data: "{}"}}); err != nil {
		t.Fatalf("failed to seed model parameters: %v", err)
	}

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	store := newForTest(t, cs, logger, Config{URL: hang.URL, ModelParametersURL: hang.URL})

	// Mirrors a caller-level deadline like force sync's; the fetch must not consume all of it.
	syncCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := store.SyncFromURL(syncCtx); err != nil {
		t.Fatalf("SyncFromURL should fall back to DB records, got: %v", err)
	}
	if err := store.SyncModelParamsFromURL(syncCtx); err != nil {
		t.Fatalf("SyncModelParamsFromURL should fall back to DB records, got: %v", err)
	}
}

// TestSyncErrorKeepsURLCause checks that a hung URL with an empty DB reports
// the failing request, not just "context deadline exceeded".
func TestSyncErrorKeepsURLCause(t *testing.T) {
	orig := pricingFetchTimeout
	pricingFetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { pricingFetchTimeout = orig })

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	store := newForTest(t, nil, bifrost.NewDefaultLogger(schemas.LogLevelError), Config{URL: hang.URL})

	err := store.SyncFromURL(context.Background())
	if err == nil {
		t.Fatal("expected an error for a hung pricing URL with no DB data")
	}
	if !strings.Contains(err.Error(), "failed to download pricing data") {
		t.Fatalf("error should carry the URL failure, got: %v", err)
	}
}

// TestLoadFromURLRefusesRedirectToLinkLocal: the entry URL passes
// ValidateExternalURL (it is a reachable loopback server), but its 302 points
// at the cloud metadata address. The redirect hop must be refused and the
// error must say so; the datasheet must never be read from there.
func TestLoadFromURLRefusesRedirectToLinkLocal(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirect.Close()
	store := newForTest(t, nil, bifrost.NewDefaultLogger(schemas.LogLevelError), Config{URL: redirect.URL, ModelParametersURL: redirect.URL})

	_, err := store.loadPricingFromURL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("pricing: expected link-local refusal, got %v", err)
	}
	_, err = store.loadModelParametersFromURL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("model parameters: expected link-local refusal, got %v", err)
	}
}

// TestLoadFromURLHoldsAPISavedURLToPublicAddresses: a URL saved through the API
// was accepted under the public-address rule, so it is fetched under the same
// rule. A host on the loopback interface is refused before any request is sent,
// by literal address and by a name that resolves to it, and so is a file://
// URL. A URL from config.json keeps reaching the same server.
func TestLoadFromURLHoldsAPISavedURLToPublicAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	byName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	for _, rawURL := range []string{srv.URL, byName} {
		store := newForTest(t, nil, logger, Config{URL: rawURL, ModelParametersURL: rawURL, URLPublicOnly: true, ModelParametersURLPublicOnly: true})
		_, err := store.loadPricingFromURL(context.Background())
		if err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("pricing %s: expected a non-public address refusal, got %v", rawURL, err)
		}
		_, err = store.loadModelParametersFromURL(context.Background())
		if err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("model parameters %s: expected a non-public address refusal, got %v", rawURL, err)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("an API-saved loopback URL was requested %d times", got)
	}

	fileURL := "file://" + filepath.Join(t.TempDir(), "sheet.json")
	store := newForTest(t, nil, logger, Config{URL: fileURL, ModelParametersURL: fileURL, URLPublicOnly: true, ModelParametersURLPublicOnly: true})
	if _, err := store.loadPricingFromURL(context.Background()); !errors.Is(err, ErrFileURLNeedsConfigFile) {
		t.Fatalf("pricing: expected ErrFileURLNeedsConfigFile, got %v", err)
	}
	if _, err := store.loadModelParametersFromURL(context.Background()); !errors.Is(err, ErrFileURLNeedsConfigFile) {
		t.Fatalf("model parameters: expected ErrFileURLNeedsConfigFile, got %v", err)
	}

	// The same server set in config.json still syncs.
	store = newForTest(t, nil, logger, Config{URL: srv.URL, ModelParametersURL: srv.URL})
	if _, err := store.loadPricingFromURL(context.Background()); err != nil {
		t.Fatalf("pricing from a config.json URL: %v", err)
	}
	if _, err := store.loadModelParametersFromURL(context.Background()); err != nil {
		t.Fatalf("model parameters from a config.json URL: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("config.json URL requests = %d, want 2", got)
	}

	// UpdateSyncConfig carries the rule with the URL, in both directions.
	store.UpdateSyncConfig(Config{URL: srv.URL, ModelParametersURL: srv.URL, URLPublicOnly: true})
	if _, err := store.loadPricingFromURL(context.Background()); err == nil || !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("pricing after an API save: expected a non-public address refusal, got %v", err)
	}
	if _, err := store.loadModelParametersFromURL(context.Background()); err != nil {
		t.Fatalf("model parameters URL was not saved through the API: %v", err)
	}
	store.UpdateSyncConfig(Config{URL: srv.URL, ModelParametersURL: srv.URL})
	if _, err := store.loadPricingFromURL(context.Background()); err != nil {
		t.Fatalf("pricing after config.json took the URL back: %v", err)
	}
}

// TestFeedHTTPClientDoesNotLeaveAnAPISavedNameToTheProxy: behind an egress proxy the dialer
// only ever sees the proxy, so the public-address rule runs on the name before it is sent.
// A name the local resolver cannot answer cannot be checked. For a URL saved through the API
// it is refused rather than handed to the proxy, which could resolve it to a host on the
// operator's network. The client for a config.json URL keeps the private-network rule and
// leaves the name to the proxy; this test is about the client alone, and the sync paths still
// resolve every feed host locally in CheckFeedURL before they use either client.
func TestFeedHTTPClientDoesNotLeaveAnAPISavedNameToTheProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is %T", http.DefaultTransport)
	}
	original := transport.Proxy
	transport.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	t.Cleanup(func() { transport.Proxy = original })

	const feed = "http://only-the-proxy-knows.invalid/feed.json"
	resp, err := FeedHTTPClient(5*time.Second, true).Get(feed)
	if err == nil {
		resp.Body.Close()
		t.Fatal("an API-saved feed name the local resolver cannot answer was fetched")
	}
	if !strings.Contains(err.Error(), "DNS lookup failed") {
		t.Fatalf("expected the unresolved name to be refused, got %v", err)
	}
	if got := proxied.Load(); got != 0 {
		t.Fatalf("the unchecked name reached the proxy %d times", got)
	}

	resp, err = FeedHTTPClient(5*time.Second, false).Get(feed)
	if err != nil {
		t.Fatalf("a config.json feed name is left to the proxy: %v", err)
	}
	resp.Body.Close()
	if got := proxied.Load(); got != 1 {
		t.Fatalf("proxy saw %d requests for the config.json feed, want 1", got)
	}
}

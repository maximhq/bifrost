package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// metadataHost serves Client ID Metadata Documents over TLS and counts fetches.
type metadataHost struct {
	server  *httptest.Server
	fetches atomic.Int32
	docs    map[string]func(base string) (status int, header http.Header, body string)
}

func newMetadataHost(t *testing.T) *metadataHost {
	t.Helper()
	h := &metadataHost{docs: map[string]func(string) (int, http.Header, string){}}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fetches.Add(1)
		serve, ok := h.docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		status, header, body := serve(h.server.URL)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.server.Close)
	return h
}

// document serves a JSON document at path; fields are merged over a valid public client.
func (h *metadataHost) document(path string, fields map[string]any, header http.Header) string {
	h.docs[path] = func(base string) (int, http.Header, string) {
		doc := map[string]any{
			"client_id":     base + path,
			"client_name":   "Example MCP Client",
			"redirect_uris": []string{"http://127.0.0.1:3000/callback"},
		}
		for k, v := range fields {
			if v == nil {
				delete(doc, k)
				continue
			}
			doc[k] = v
		}
		body, _ := json.Marshal(doc)
		if header == nil {
			header = http.Header{}
		}
		header.Set("Content-Type", "application/json")
		return http.StatusOK, header, string(body)
	}
	return h.server.URL + path
}

func TestOAuth2ClientMetadataResolver(t *testing.T) {
	_, _, cfg := newIssuanceHandler(t)
	host := newMetadataHost(t)
	resolver := newOAuth2ClientMetadataResolver(host.server.Client())

	t.Run("valid document becomes a public client", func(t *testing.T) {
		clientID := host.document("/valid.json", nil, nil)
		client, err := resolver.resolve(context.Background(), cfg, clientID)
		require.Nil(t, err)
		assert.Equal(t, clientID, client.ClientID)
		assert.Equal(t, "Example MCP Client", client.ClientName)
		assert.Equal(t, []string{"http://127.0.0.1:3000/callback"}, client.RedirectURIs)
		assert.Equal(t, []string{"authorization_code"}, client.GrantTypes)
		assert.Equal(t, "mcp", client.Scope)
	})

	t.Run("long URL is accepted", func(t *testing.T) {
		clientID := host.document("/"+strings.Repeat("a", 4096)+"/client.json", nil, nil)
		client, err := resolver.resolve(context.Background(), cfg, clientID)
		require.Nil(t, err)
		assert.Equal(t, clientID, client.ClientID)
	})

	rejected := map[string]struct {
		fields map[string]any
		want   string
	}{
		"client_id differs from the document URL": {map[string]any{"client_id": "https://evil.example.com/client.json"}, "client_id"},
		"no client_name":           {map[string]any{"client_name": nil}, "client_name"},
		"no redirect_uris":         {map[string]any{"redirect_uris": nil}, "redirect_uris"},
		"redirect_uri not allowed": {map[string]any{"redirect_uris": []string{"http://attacker.example.com/cb"}}, "redirect_uri"},
		"confidential client":      {map[string]any{"token_endpoint_auth_method": "private_key_jwt"}, "token_endpoint_auth_method"},
		"unsupported grant_type":   {map[string]any{"grant_types": []string{"client_credentials"}}, "grant_type"},
	}
	for name, tc := range rejected {
		t.Run(name, func(t *testing.T) {
			clientID := host.document("/"+strings.ReplaceAll(name, " ", "-")+".json", tc.fields, nil)
			_, err := resolver.resolve(context.Background(), cfg, clientID)
			require.NotNil(t, err)
			assert.Equal(t, "invalid_client", err.code)
			assert.Contains(t, err.description, tc.want)
		})
	}

	broken := map[string]func(base string) (int, http.Header, string){
		"not found": func(string) (int, http.Header, string) { return http.StatusNotFound, nil, "" },
		"not JSON":  func(string) (int, http.Header, string) { return http.StatusOK, nil, "<html>" },
		"too large": func(string) (int, http.Header, string) {
			return http.StatusOK, nil, strings.Repeat(" ", maxClientMetadataDocumentBytes+1)
		},
		"a redirect": func(base string) (int, http.Header, string) {
			return http.StatusFound, http.Header{"Location": {base + "/valid.json"}}, ""
		},
	}
	for name, serve := range broken {
		t.Run(name, func(t *testing.T) {
			path := "/" + strings.ReplaceAll(name, " ", "-") + ".json"
			host.docs[path] = serve
			_, err := resolver.resolve(context.Background(), cfg, host.server.URL+path)
			require.NotNil(t, err, "a document that cannot be fetched as-is is refused, never followed")
			assert.Equal(t, "invalid_client", err.code)
		})
	}
}

// Documents are cached per Cache-Control (clamped), failures briefly, and concurrent misses share a fetch.
func TestOAuth2ClientMetadataResolver_Caches(t *testing.T) {
	_, _, cfg := newIssuanceHandler(t)
	host := newMetadataHost(t)
	resolver := newOAuth2ClientMetadataResolver(host.server.Client())
	now := time.Now()
	resolver.now = func() time.Time { return now }

	clientID := host.document("/cached.json", nil, http.Header{"Cache-Control": {"max-age=600"}})
	for range 3 {
		_, err := resolver.resolve(context.Background(), cfg, clientID)
		require.Nil(t, err)
	}
	assert.Equal(t, int32(1), host.fetches.Load(), "served from cache while fresh")

	now = now.Add(11 * time.Minute)
	_, err := resolver.resolve(context.Background(), cfg, clientID)
	require.Nil(t, err)
	assert.Equal(t, int32(2), host.fetches.Load(), "refetched once max-age has passed")

	before := host.fetches.Load()
	for range 3 {
		_, err := resolver.resolve(context.Background(), cfg, host.server.URL+"/missing.json")
		require.NotNil(t, err)
	}
	assert.Equal(t, before+1, host.fetches.Load(), "a failure is cached too")

	release := make(chan struct{})
	host.docs["/concurrent.json"] = func(base string) (int, http.Header, string) {
		<-release
		return http.StatusOK, nil, `{"client_id":"` + base + `/concurrent.json","client_name":"c","redirect_uris":["http://127.0.0.1:3000/callback"]}`
	}
	before = host.fetches.Load()
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, err := resolver.resolve(context.Background(), cfg, host.server.URL+"/concurrent.json")
			assert.Nil(t, err)
		})
	}
	require.Eventually(t, func() bool { return host.fetches.Load() > before }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, before+1, host.fetches.Load(), "concurrent misses share one fetch")
}

// Outbound fetches are capped across all client_ids: past the cap a lookup fails at once with
// temporarily_unavailable, and that failure is not cached, so a retry once a slot frees succeeds.
func TestOAuth2ClientMetadataResolver_LimitsConcurrentFetches(t *testing.T) {
	_, _, cfg := newIssuanceHandler(t)
	host := newMetadataHost(t)
	resolver := newOAuth2ClientMetadataResolver(host.server.Client())
	resolver.fetchSlots = make(chan struct{}, 1)

	release := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) }) // runs before the host closes
	host.docs["/slow.json"] = func(base string) (int, http.Header, string) {
		<-release
		return http.StatusOK, nil, `{"client_id":"` + base + `/slow.json","client_name":"c","redirect_uris":["http://127.0.0.1:3000/callback"]}`
	}
	other := host.document("/other.json", nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := resolver.resolve(context.Background(), cfg, host.server.URL+"/slow.json")
		assert.Nil(t, err)
	}()
	require.Eventually(t, func() bool { return host.fetches.Load() == 1 }, time.Second, time.Millisecond)

	_, err := resolver.resolve(context.Background(), cfg, other)
	require.NotNil(t, err, "a fetch past the cap must not start")
	assert.Equal(t, "temporarily_unavailable", err.code)
	assert.Equal(t, int32(1), host.fetches.Load())

	unblock.Do(func() { close(release) })
	<-done
	_, err = resolver.resolve(context.Background(), cfg, other)
	assert.Nil(t, err, "the refusal is not cached")
}

// A full cache evicts failed lookups before documents that resolved, so a flood of bad client_ids
// cannot push out the clients that work.
func TestOAuth2ClientMetadataResolver_EvictsFailuresFirst(t *testing.T) {
	resolver := newOAuth2ClientMetadataResolver(http.DefaultClient)
	now := time.Now()
	for i := range maxClientMetadataEntries - 1 {
		resolver.cache[fmt.Sprintf("good-%d", i)] = clientMetadataEntry{client: &configtables.TableOAuth2Client{}, expires: now.Add(time.Hour)}
	}
	resolver.cache["failed"] = clientMetadataEntry{err: &oauth2ClientMetadataError{code: "invalid_client"}, expires: now.Add(time.Minute)}

	resolver.store("new", clientMetadataEntry{client: &configtables.TableOAuth2Client{}, expires: now.Add(time.Hour)}, now)

	_, failedKept := resolver.cache["failed"]
	assert.False(t, failedKept, "the failed lookup is evicted first")
	for i := range maxClientMetadataEntries - 1 {
		_, ok := resolver.cache[fmt.Sprintf("good-%d", i)]
		require.True(t, ok, "resolved documents are kept while a failure can go")
	}
	assert.Contains(t, resolver.cache, "new")
}

// An authorize request naming a Client ID Metadata Document is served like a registered client.
func TestHandleAuthorize_ClientIDMetadataDocument(t *testing.T) {
	h, _, _ := newIssuanceHandler(t)
	host := newMetadataHost(t)
	h.clientMetadata = newOAuth2ClientMetadataResolver(host.server.Client())
	clientID := host.document("/client.json", nil, nil)

	query := func(redirect string) string {
		v := url.Values{}
		v.Set("client_id", clientID)
		v.Set("redirect_uri", redirect)
		v.Set("response_type", "code")
		v.Set("code_challenge", "challenge")
		v.Set("code_challenge_method", "S256")
		v.Set("resource", testMCPResource)
		v.Set("state", "xyz")
		return v.Encode()
	}

	ctx := getCtx("/oauth2/authorize?" + query("http://127.0.0.1:3000/callback"))
	h.handleAuthorize(ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "/oauth/consent?flow=")

	ctx = getCtx("/oauth2/authorize?" + query("http://127.0.0.1:3000/elsewhere"))
	h.handleAuthorize(ctx)
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "a redirect_uri the document does not list is refused")
}

// BenchmarkOAuth2ClientMetadataResolver_Cached measures a cached lookup, the per-request cost once a
// client's document is known: the lock and the map read.
func BenchmarkOAuth2ClientMetadataResolver_Cached(b *testing.B) {
	resolver := newOAuth2ClientMetadataResolver(http.DefaultClient)
	clientID := "https://client.example.com/oauth/client-metadata.json"
	resolver.cache[clientID] = clientMetadataEntry{client: &configtables.TableOAuth2Client{ClientID: clientID}, expires: time.Now().Add(time.Hour)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := resolver.resolve(context.Background(), nil, clientID); err != nil {
			b.Fatal(err)
		}
	}
}

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

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
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

	t.Run("grants this server does not offer are dropped", func(t *testing.T) {
		clientID := host.document("/extra-grant.json", map[string]any{
			"grant_types": []string{"authorization_code", "urn:ietf:params:oauth:grant-type:jwt-bearer", "refresh_token"},
		}, nil)
		client, err := resolver.resolve(context.Background(), cfg, clientID)
		require.Nil(t, err)
		assert.Equal(t, []string{"authorization_code", "refresh_token"}, client.GrantTypes)
	})

	// ChatGPT's document prefers private_key_jwt and also lists none among the methods it supports.
	t.Run("another auth method is accepted when the document also supports none", func(t *testing.T) {
		clientID := host.document("/prefers-jwt.json", map[string]any{
			"token_endpoint_auth_method":            "private_key_jwt",
			"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		}, nil)
		_, err := resolver.resolve(context.Background(), cfg, clientID)
		require.Nil(t, err)
	})

	t.Run("another auth method is refused when the document does not support none", func(t *testing.T) {
		for path, extra := range map[string]map[string]any{
			"/jwt-only.json":      {"token_endpoint_auth_method": "private_key_jwt"},
			"/jwt-and-basic.json": {"token_endpoint_auth_method": "private_key_jwt", "token_endpoint_auth_methods_supported": []string{"private_key_jwt", "client_secret_basic"}},
		} {
			_, err := resolver.resolve(context.Background(), cfg, host.document(path, extra, nil))
			require.NotNil(t, err, path)
			assert.Contains(t, err.description, "only token_endpoint_auth_method=none is supported", path)
		}
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

	// claude.ai's document also lists the jwt-bearer grant, which this server does not offer.
	clientID = host.document("/extra-grant.json", map[string]any{
		"grant_types": []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:jwt-bearer"},
	}, nil)
	ctx = getCtx("/oauth2/authorize?" + query("http://127.0.0.1:3000/callback"))
	h.handleAuthorize(ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), "a document listing a grant this server does not offer still authorizes: %s", ctx.Response.Body())

	// ChatGPT's document prefers private_key_jwt and also supports none.
	clientID = host.document("/prefers-jwt.json", map[string]any{
		"token_endpoint_auth_method":            "private_key_jwt",
		"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
	}, nil)
	ctx = getCtx("/oauth2/authorize?" + query("http://127.0.0.1:3000/callback"))
	h.handleAuthorize(ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), "a document preferring another auth method but supporting none still authorizes: %s", ctx.Response.Body())
	assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "/oauth/consent?flow=")
}

// With disable_client_metadata_documents on, Bifrost stops advertising support but still accepts a
// client that sends a metadata-document client_id, so clients that ignore discovery keep working.
func TestHandleAuthorize_ClientIDMetadataDocumentAcceptedWhenNotAdvertised(t *testing.T) {
	h, _, cfg := newIssuanceHandler(t)
	cfg.ClientConfig.OAuth2ServerConfig.DisableClientMetadataDocuments = true
	host := newMetadataHost(t)
	h.clientMetadata = newOAuth2ClientMetadataResolver(host.server.Client())
	v := url.Values{}
	v.Set("client_id", host.document("/client.json", nil, nil))
	v.Set("redirect_uri", "http://127.0.0.1:3000/callback")
	v.Set("response_type", "code")
	v.Set("code_challenge", "challenge")
	v.Set("code_challenge_method", "S256")
	v.Set("resource", testMCPResource)
	v.Set("state", "xyz")

	ctx := getCtx("/oauth2/authorize?" + v.Encode())
	h.handleAuthorize(ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Header.Peek("Location")), "/oauth/consent?flow=")
}

// TestHandleToken_RefreshAsAMetadataDocumentClient pins that a client identified by a Client ID
// Metadata Document refreshes the grant it was issued whatever grant types its document lists, and
// without its document being fetched again: a refresh needs only the refresh token and the client_id
// it was issued to, so a document that has since gone away does not end the grant.
func TestHandleToken_RefreshAsAMetadataDocumentClient(t *testing.T) {
	for _, tc := range []struct {
		name       string
		grantTypes any
		gone       bool
	}{
		{name: "document lists refresh_token", grantTypes: []string{"authorization_code", "refresh_token"}},
		{name: "document also lists a grant this server does not offer", grantTypes: []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:jwt-bearer"}},
		{name: "document omits grant_types", grantTypes: nil},
		{name: "document is no longer served", grantTypes: []string{"authorization_code", "refresh_token"}, gone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _ := newIssuanceHandler(t)
			host := newMetadataHost(t)
			h.clientMetadata = newOAuth2ClientMetadataResolver(host.server.Client())
			clientID := host.document("/client.json", map[string]any{"grant_types": tc.grantTypes}, nil)
			require.NoError(t, store.CreateVirtualKey(context.Background(), &configtables.TableVirtualKey{
				ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: new(true),
			}))
			seedConsentedRequest(t, store, "fam-vk", clientID, "auth-code-vk", pkceChallenge("v"), "vk", "vk-1", time.Now().Add(time.Minute))
			require.NoError(t, store.ConsumeOAuth2AuthorizeRequest(context.Background(), "fam-vk", &configtables.TableOAuth2RefreshToken{
				ID: "rt-vk", TokenHash: hashSHA256Hex("refresh-vk-1"), FamilyID: "fam-vk", ClientID: clientID,
				BfMode: "vk", BfSub: "vk-1", Scope: "mcp", Resource: testMCPResource, CreatedAt: time.Now(),
			}))

			if tc.gone {
				delete(host.docs, "/client.json")
			}

			ctx := formPostCtx(url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {"refresh-vk-1"}, "client_id": {clientID},
			}.Encode())
			h.handleToken(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Zero(t, host.fetches.Load(), "a refresh does not fetch the client's document")
		})
	}
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

// realClientDocuments are Client ID Metadata Documents real MCP clients publish, verbatim as fetched
// on 2026-11-03 from the URL in each comment. A client publishes one document for every authorization
// server, so it can list grants this server does not offer (claude.ai: jwt-bearer, VS Code:
// device_code); each must still resolve.
var realClientDocuments = []struct {
	name     string
	document string
}{
	// https://claude.ai/oauth/mcp-oauth-client-metadata
	{"claude.ai", `{"client_id":"https://claude.ai/oauth/mcp-oauth-client-metadata","client_name":"Claude","client_uri":"https://claude.ai","redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"grant_types":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:jwt-bearer"],"response_types":["code"],"token_endpoint_auth_method":"none"}`},
	// https://claude.ai/oauth/claude-code-client-metadata
	{"Claude Code", `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","client_name":"Claude Code","client_uri":"https://claude.ai","redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`},
	// https://chatgpt.com/oauth/codex/client.json
	{"Codex", `{"client_id":"https://chatgpt.com/oauth/codex/client.json","client_uri":"https://chatgpt.com/codex","application_type":"native","redirect_uris":["http://127.0.0.1/callback","http://localhost/callback"],"token_endpoint_auth_method":"none","token_endpoint_auth_methods_supported":["none"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"client_name":"Codex","logo_uri":"https://persistent.oaistatic.com/sonic/misc/openai-logo.png"}`},
	// https://vscode.dev/oauth/client-metadata.json
	{"VS Code", `{"client_name":"Visual Studio Code","logo_uri":"https://code.visualstudio.com/assets/branding/code-stable.png","grant_types":["authorization_code","refresh_token","urn:ietf:params:oauth:grant-type:device_code"],"response_types":["code"],"token_endpoint_auth_method":"none","application_type":"native","client_id":"https://vscode.dev/oauth/client-metadata.json","client_uri":"https://vscode.dev/product","redirect_uris":["http://127.0.0.1:33418/","https://vscode.dev/redirect"]}`},
	// https://cursor.com/oauth/mcp-client.json
	{"Cursor", `{"client_id":"https://cursor.com/oauth/mcp-client.json","client_name":"Cursor","client_uri":"https://cursor.com","logo_uri":"https://ptht05hbb1ssoooe.public.blob.vercel-storage.com/assets/uploads/cursorlogomcpv3.svg","redirect_uris":["https://www.cursor.com/agents/mcp/oauth/callback","https://www.cursor.com/bot/mcp/oauth/callback","https://grok.com/connectors-oauth-exchange-code/","http://localhost:8787/callback","http://127.0.0.1:8787/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`},
	// https://chatgpt.com/oauth/client.json
	{"ChatGPT", `{"client_id":"https://chatgpt.com/oauth/client.json","client_uri":"https://chatgpt.com/","redirect_uris":["https://chatgpt.com/connector_platform_oauth_redirect"],"token_endpoint_auth_method":"private_key_jwt","token_endpoint_auth_methods_supported":["none","private_key_jwt"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"client_name":"ChatGPT","logo_uri":"https://persistent.oaistatic.com/sonic/misc/openai-logo.png","token_endpoint_auth_signing_alg":"RS256","jwks_uri":"https://chatgpt.com/oauth/jwks.json"}`},
	// https://zed.dev/oauth/client-metadata.json
	{"Zed", `{"client_id":"https://zed.dev/oauth/client-metadata.json","client_name":"Zed","client_uri":"https://zed.dev","redirect_uris":["http://127.0.0.1/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","logo_uri":"https://cdn.zed.dev/images/logo-dark.png"}`},
}

// TestOAuth2ClientMetadataResolver_RealClientDocuments resolves each real client's document, served from
// a test host (so its client_id names that host, the one change), with its remote callbacks approved as
// an operator using that client would. Each becomes a public client with the grants this server offers.
func TestOAuth2ClientMetadataResolver_RealClientDocuments(t *testing.T) {
	_, _, cfg := newIssuanceHandler(t)
	host := newMetadataHost(t)
	resolver := newOAuth2ClientMetadataResolver(host.server.Client())
	for i, tc := range realClientDocuments {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.document), &doc))
			var redirects []string
			for _, uri := range doc["redirect_uris"].([]any) {
				redirects = append(redirects, uri.(string))
			}
			cfg.ClientConfig.OAuth2ServerConfig.AllowedRedirectURIs = redirects
			path := fmt.Sprintf("/real-%d.json", i)
			host.docs[path] = func(base string) (int, http.Header, string) {
				doc["client_id"] = base + path
				body, _ := json.Marshal(doc)
				return http.StatusOK, http.Header{"Content-Type": []string{"application/json"}}, string(body)
			}

			client, err := resolver.resolve(context.Background(), cfg, host.server.URL+path)
			require.Nil(t, err, "the %s document must resolve", tc.name)
			assert.Equal(t, doc["client_name"], client.ClientName)
			assert.Equal(t, redirects, client.RedirectURIs)
			assert.Equal(t, []string{"authorization_code", "refresh_token"}, client.GrantTypes)
		})
	}
}

// The harness build's private allowlist lets documents come from an allow-listed private host,
// over http too, and nowhere else; unset, such a client_id is not a document at all.
func TestOAuth2ClientMetadataResolver_PrivateAllowlist(t *testing.T) {
	_, _, cfg := newIssuanceHandler(t)
	var fetches atomic.Int32
	var docServer *httptest.Server
	docServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": docServer.URL + r.URL.Path, "client_name": "Private host client", "redirect_uris": []string{"http://127.0.0.1:3000/callback"}})
	}))
	t.Cleanup(docServer.Close)
	clientID := docServer.URL + "/client.json"
	resolver := newOAuth2ClientMetadataResolver(docServer.Client())

	assert.False(t, isClientMetadataDocumentID(clientID), "unset, an http client_id is not a document")
	_, metadataErr, err := lookupOAuth2Client(context.Background(), cfg, resolver, clientID)
	assert.Nil(t, metadataErr)
	assert.Error(t, err, "it is looked up as a registered client, of which there is none")
	assert.Zero(t, fetches.Load())

	allow, err := network.ValidatePrivateTargetAllowlist([]string{"127.0.0.1"})
	require.NoError(t, err)
	setClientMetadataPrivateAllowlist(allow)
	t.Cleanup(func() { setClientMetadataPrivateAllowlist(nil) })

	assert.True(t, isClientMetadataDocumentID(clientID))
	client, metadataErr, err := lookupOAuth2Client(context.Background(), cfg, resolver, clientID)
	require.NoError(t, err)
	require.Nil(t, metadataErr)
	assert.Equal(t, "Private host client", client.ClientName)
	assert.Equal(t, int32(1), fetches.Load())

	for _, other := range []string{"http://localhost:1/client.json", "http://10.0.0.1/client.json", "http://example.com/client.json"} {
		assert.False(t, isClientMetadataDocumentID(other), "%s is not on the allowlist", other)
	}
}

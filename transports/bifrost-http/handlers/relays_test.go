package handlers

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newRelaysTestHandler(t *testing.T) (*RelaysHandler, configstore.ConfigStore, *relay.Manager) {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "pte.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	manager := relay.NewManager(store, nil)
	return NewRelaysHandler(manager, nil, nil, nil), store, manager
}

func pteRequest(method, body string, name string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetBodyString(body)
	if name != "" {
		ctx.SetUserValue("name", name)
	}
	return ctx
}

func TestRelaysHandlerCRUD(t *testing.T) {
	h, store, manager := newRelaysTestHandler(t)

	create := pteRequest(fasthttp.MethodPost, `{"name":"tavily","base_url":"https://search.example.com","auth":{"type":"bearer"},"credential":{"value":"tvly-secret-123456789"},"usage":{"cost_per_request":0.001},"routes":[{"method":"POST","path":"/search","usage":{"units_path":"usage.credits","cost_per_unit":0.008}}]}`, "")
	h.create(create)
	require.Equal(t, fasthttp.StatusCreated, create.Response.StatusCode(), string(create.Response.Body()))
	assert.NotContains(t, string(create.Response.Body()), "tvly-secret-123456789", "the credential is redacted in responses")
	assert.Equal(t, []string{"tavily"}, manager.Names(), "a created endpoint is live")

	dup := pteRequest(fasthttp.MethodPost, `{"name":"tavily","base_url":"https://search.example.com","credential":{"value":"x"}}`, "")
	h.create(dup)
	assert.Equal(t, fasthttp.StatusConflict, dup.Response.StatusCode())

	invalid := pteRequest(fasthttp.MethodPost, `{"name":"nocred","base_url":"https://search.example.com"}`, "")
	h.create(invalid)
	assert.Equal(t, fasthttp.StatusBadRequest, invalid.Response.StatusCode(), "a bearer endpoint without a credential is refused")

	exa := pteRequest(fasthttp.MethodPost, `{"name":"exa","base_url":"https://docs.example.com","auth":{"type":"none"}}`, "")
	h.create(exa)
	require.Equal(t, fasthttp.StatusCreated, exa.Response.StatusCode(), string(exa.Response.Body()))
	// Relays are listed oldest first, so a page of one is tavily.
	for query, want := range map[string][]string{"": {"exa", "tavily"}, "search=TAV": {"tavily"}, "limit=1": {"tavily"}, "limit=1&offset=1": {"exa"}} {
		list := pteRequest(fasthttp.MethodGet, "", "")
		list.Request.URI().SetQueryString(query)
		h.list(list)
		require.Equal(t, fasthttp.StatusOK, list.Response.StatusCode(), string(list.Response.Body()))
		var page struct {
			Relays     []struct{ Name string } `json:"relays"`
			TotalCount int                     `json:"total_count"`
		}
		require.NoError(t, json.Unmarshal(list.Response.Body(), &page))
		names := []string{}
		for _, r := range page.Relays {
			names = append(names, r.Name)
		}
		assert.ElementsMatch(t, want, names, "list ?%s", query)
	}
	bad := pteRequest(fasthttp.MethodGet, "", "")
	bad.Request.URI().SetQueryString("limit=0")
	h.list(bad)
	assert.Equal(t, fasthttp.StatusBadRequest, bad.Response.StatusCode())

	get := pteRequest(fasthttp.MethodGet, "", "tavily")
	h.get(get)
	require.Equal(t, fasthttp.StatusOK, get.Response.StatusCode())
	var view map[string]any
	require.NoError(t, json.Unmarshal(get.Response.Body(), &view))
	assert.Equal(t, "/relay/tavily", view["url"])

	// The UI sends back what it read: the redacted credential keeps the stored secret.
	view["description"] = "edited"
	edited, _ := json.Marshal(view)
	update := pteRequest(fasthttp.MethodPut, string(edited), "tavily")
	h.update(update)
	require.Equal(t, fasthttp.StatusOK, update.Response.StatusCode(), string(update.Response.Body()))
	stored, err := store.GetRelay(context.Background(), "tavily")
	require.NoError(t, err)
	assert.Equal(t, "edited", stored.Description)
	assert.Equal(t, "tvly-secret-123456789", stored.Credential.Value.GetValue())
	require.Len(t, stored.Routes, 1)
	assert.InDelta(t, 0.008, *stored.Routes[0].Usage.CostPerUnit, 1e-12)

	rename := pteRequest(fasthttp.MethodPut, `{"name":"other","base_url":"https://search.example.com","credential":{"value":"x"}}`, "tavily")
	h.update(rename)
	assert.Equal(t, fasthttp.StatusBadRequest, rename.Response.StatusCode(), "names are immutable")

	preview := pteRequest(fasthttp.MethodPost, `{"name":"tavily","sample":{"method":"POST","path":"/search","response_content_type":"application/json","response_body":"{\"usage\":{\"credits\":4}}"}}`, "")
	h.preview(preview)
	require.Equal(t, fasthttp.StatusOK, preview.Response.StatusCode(), string(preview.Response.Body()))
	var out relay.UsagePreview
	require.NoError(t, json.Unmarshal(preview.Response.Body(), &out))
	assert.InDelta(t, 0.001+4*0.008, out.Cost, 1e-12, "the preview prices a saved endpoint's route override")

	del := pteRequest(fasthttp.MethodDelete, "", "tavily")
	h.delete(del)
	assert.Equal(t, fasthttp.StatusNoContent, del.Response.StatusCode())
	assert.Equal(t, []string{"exa"}, manager.Names(), "a deleted relay stops serving; the other keeps serving")
	missing := pteRequest(fasthttp.MethodGet, "", "tavily")
	h.get(missing)
	assert.Equal(t, fasthttp.StatusNotFound, missing.Response.StatusCode())
}

// relayChangeRecorder stands in for the virtual-key reloader and records what it was asked to
// reload; a failed reload is logged, never surfaced, as for agent grants.
type relayChangeRecorder struct {
	reloads     []string
	failReloads bool
}

func (r *relayChangeRecorder) ReloadVirtualKey(_ context.Context, id string) (*tables.TableVirtualKey, error) {
	r.reloads = append(r.reloads, id)
	if r.failReloads {
		return nil, errors.New("governance store unavailable")
	}
	return nil, nil
}

// A delete reloads every key whose grant it took away, and a failed reload does not fail the
// delete or block the name, exactly as the Agent Gateway handler treats agent grants.
func TestRelaysHandlerReloadsGrantedKeysOnDelete(t *testing.T) {
	_, store, _ := newRelaysTestHandler(t)
	recorder := &relayChangeRecorder{failReloads: true}
	h := NewRelaysHandler(relay.NewManager(store, nil), nil, nil, recorder)
	h.create(pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"}}`, ""))
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-granted", Name: "granted", Value: *schemas.NewSecretVar("sk-bf-granted"), IsActive: schemas.Ptr(true), RelayGrants: []tables.TableVirtualKeyRelayGrant{{VirtualKeyID: "vk-granted", RelayName: "tavily"}}}))
	del := pteRequest("DELETE", "", "tavily")
	h.delete(del)
	assert.Equal(t, fasthttp.StatusNoContent, del.Response.StatusCode(), "a failed key reload does not fail the delete")
	assert.Equal(t, []string{"vk-granted"}, recorder.reloads, "the granted key is reloaded")

	ctx := pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-2"}}`, "")
	h.create(ctx)
	assert.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "the name is free to reuse at once")
}

// relayMutationRecorder stands in for a clustered server's RelayManager: it mutates locally and
// records what it would broadcast, so the handler's delegation can be checked.
type relayMutationRecorder struct {
	localRelayManager
	changes []string
}

func (r *relayMutationRecorder) CreateRelay(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	stored, err := r.localRelayManager.CreateRelay(ctx, cfg)
	if err == nil {
		r.changes = append(r.changes, "create:"+stored.Name)
	}
	return stored, err
}

func (r *relayMutationRecorder) UpdateRelay(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	updated, err := r.localRelayManager.UpdateRelay(ctx, name, cfg)
	if err == nil {
		r.changes = append(r.changes, "update:"+name)
	}
	return updated, err
}

func (r *relayMutationRecorder) DeleteRelay(ctx context.Context, name string) ([]string, error) {
	ids, err := r.localRelayManager.DeleteRelay(ctx, name)
	if err == nil {
		r.changes = append(r.changes, "delete:"+name+":"+strings.Join(ids, ","))
	}
	return ids, err
}

// Every relay mutation goes through the RelayManager, so a clustered deployment can wrap it with a
// broadcast; a peer told of one brings its live relay in line with the store: upserted while the
// row exists, removed once it is gone.
func TestRelaysHandlerDelegatesMutationsAndReloads(t *testing.T) {
	_, store, _ := newRelaysTestHandler(t)
	manager := relay.NewManager(store, nil)
	recorder := &relayMutationRecorder{localRelayManager: localRelayManager{manager: manager}}
	h := NewRelaysHandler(manager, recorder, nil, nil)

	h.create(pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"}}`, ""))
	h.update(pteRequest("PUT", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"},"description":"edited"}`, "tavily"))
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-granted", Name: "granted", Value: *schemas.NewSecretVar("sk-bf-granted"), IsActive: schemas.Ptr(true), RelayGrants: []tables.TableVirtualKeyRelayGrant{{VirtualKeyID: "vk-granted", RelayName: "tavily"}}}))
	h.delete(pteRequest("DELETE", "", "tavily"))
	assert.Equal(t, []string{"create:tavily", "update:tavily", "delete:tavily:vk-granted"}, recorder.changes)
	assert.Empty(t, manager.Names(), "the local runtime followed the delete")

	// A peer replays the change from the store.
	peer := relay.NewManager(store, nil)
	peerHandler := NewRelaysHandler(peer, nil, nil, nil)
	require.NoError(t, store.CreateRelay(context.Background(), &schemas.RelayConfig{Name: "docs", BaseURL: "https://docs.example.com", Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}, Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("k")}}, ""))
	require.NoError(t, peerHandler.ReloadRelay(context.Background(), "docs"))
	assert.Contains(t, peer.Names(), "docs", "a created relay is served on the peer")
	require.NoError(t, store.DeleteRelay(context.Background(), "docs"))
	assert.ErrorIs(t, peerHandler.ReloadRelay(context.Background(), "docs"), configstore.ErrNotFound, "a reload of a deleted relay reports the missing row")
	peerHandler.RemoveRelay("docs")
	assert.NotContains(t, peer.Names(), "docs", "a deleted relay stops being served on the peer")
}

// TestRelaysHandlerRequiresGenuineAuthForDialChanges: a caller let through the fail-open bypass
// (dashboard auth disabled or unconfigured) cannot choose where a relay sends its credential or
// which gateway secret it sends, the relay counterpart of the provider, provider-key, plugin and
// webhook gates. Repointing base_url while sending back the redacted credential would hand the
// stored secret to the new host; an env. reference would send the gateway's own environment.
// Edits that keep every such setting still pass, and an authenticated admin is never refused.
func TestRelaysHandlerRequiresGenuineAuthForDialChanges(t *testing.T) {
	t.Setenv("RELAY_GATE_TEST_SECRET", "gateway-env-secret")
	h, store, manager := newRelaysTestHandler(t)
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsServer.Close)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}))
	const createBody = `{"name":"tavily","base_url":"https://search.example.com","auth":{"type":"bearer"},"credential":{"value":"tvly-secret-123456789"},"extra_headers":{"X-Tenant":"acme"}}`
	created := pteRequest(fasthttp.MethodPost, createBody, "")
	h.create(created)
	require.Equal(t, fasthttp.StatusCreated, created.Response.StatusCode(), string(created.Response.Body()))

	// The body the UI would send back: what GET returned, credential redacted.
	readView := func(t *testing.T) map[string]any {
		t.Helper()
		get := pteRequest(fasthttp.MethodGet, "", "tavily")
		h.get(get)
		require.Equal(t, fasthttp.StatusOK, get.Response.StatusCode(), string(get.Response.Body()))
		var view map[string]any
		require.NoError(t, json.Unmarshal(get.Response.Body(), &view))
		return view
	}
	bypassed := func(method string, body any, name string) *fasthttp.RequestCtx {
		raw, ok := body.(string)
		if !ok {
			data, err := json.Marshal(body)
			require.NoError(t, err)
			raw = string(data)
		}
		ctx := pteRequest(method, raw, name)
		ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
		return ctx
	}
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		stored, err := store.GetRelay(context.Background(), "tavily")
		require.NoError(t, err)
		assert.Equal(t, "https://search.example.com", stored.BaseURL)
		assert.Equal(t, "tvly-secret-123456789", stored.Credential.Value.GetValue())
		assert.Nil(t, stored.ProxyConfig)
		assert.Nil(t, stored.Network)
		tenant := stored.ExtraHeaders["X-Tenant"]
		assert.Equal(t, "acme", tenant.GetValue())
	}

	t.Run("create is refused", func(t *testing.T) {
		ctx := bypassed(fasthttp.MethodPost, `{"name":"exfil","base_url":"https://attacker.example.com","auth":{"type":"bearer"},"credential":{"value":"env.RELAY_GATE_TEST_SECRET"}}`, "")
		h.create(ctx)
		assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Contains(t, string(ctx.Response.Body()), "base_url")
		assert.Contains(t, string(ctx.Response.Body()), "credential.value")
		assert.Equal(t, []string{"tavily"}, manager.Names(), "the refused relay is not served")
	})

	refused := map[string]func(view map[string]any){
		"base_url": func(view map[string]any) { view["base_url"] = "https://attacker.example.com" },
		"credential.value": func(view map[string]any) {
			view["credential"] = map[string]any{"value": "env.RELAY_GATE_TEST_SECRET"}
		},
		"extra_headers.X-Tenant": func(view map[string]any) {
			view["extra_headers"] = map[string]any{"X-Tenant": "env.RELAY_GATE_TEST_SECRET"}
		},
		"network.insecure_skip_verify": func(view map[string]any) {
			view["network"] = map[string]any{"insecure_skip_verify": true}
		},
		"network.ca_cert_pem": func(view map[string]any) {
			view["network"] = map[string]any{"ca_cert_pem": caPEM}
		},
		"proxy_config.url": func(view map[string]any) {
			view["proxy_config"] = map[string]any{"type": "http", "url": "http://attacker.example.com:3128"}
		},
	}
	for path, mutate := range refused {
		t.Run("update of "+path+" is refused", func(t *testing.T) {
			view := readView(t)
			mutate(view)
			ctx := bypassed(fasthttp.MethodPut, view, "tavily")
			h.update(ctx)
			assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			assert.Contains(t, string(ctx.Response.Body()), path)
			assertUnchanged(t)
		})
	}

	t.Run("update that keeps every dial setting passes", func(t *testing.T) {
		view := readView(t)
		view["description"] = "edited while auth is off"
		ctx := bypassed(fasthttp.MethodPut, view, "tavily")
		h.update(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		stored, err := store.GetRelay(context.Background(), "tavily")
		require.NoError(t, err)
		assert.Equal(t, "edited while auth is off", stored.Description)
		assertUnchanged(t)
	})

	t.Run("authenticated admin may repoint the relay", func(t *testing.T) {
		view := readView(t)
		view["base_url"] = "https://search-eu.example.com"
		data, err := json.Marshal(view)
		require.NoError(t, err)
		ctx := pteRequest(fasthttp.MethodPut, string(data), "tavily")
		h.update(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		stored, err := store.GetRelay(context.Background(), "tavily")
		require.NoError(t, err)
		assert.Equal(t, "https://search-eu.example.com", stored.BaseURL)
	})
}

// TestRelaysHandlerFetchOpenAPISpec: the route import's URL source returns the document text as
// served (YAML stays YAML, nothing is parsed) with the URL it was read from after redirects, and
// refuses what the MCP preview refuses: a non-http(s) URL, an unreachable document, and loopback
// for an unauthenticated caller.
func TestRelaysHandlerFetchOpenAPISpec(t *testing.T) {
	h, _, _ := newRelaysTestHandler(t)
	const yamlSpec = "openapi: 3.0.0\ninfo: {title: Search, version: '1'}\npaths:\n  /search:\n    post: {responses: {'200': {description: ok}}}\n"
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.yaml":
			http.Redirect(w, r, "/v2/openapi.yaml", http.StatusFound)
		case "/v2/openapi.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte(yamlSpec))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(specServer.Close)

	body, _ := json.Marshal(relayOpenAPISpecRequest{SpecURL: specServer.URL + "/openapi.yaml"})
	status, respBody := doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	var resp relayOpenAPISpecResponse
	require.NoError(t, json.Unmarshal([]byte(respBody), &resp), respBody)
	assert.Equal(t, yamlSpec, resp.Spec, "the document comes back as served, for the browser to parse")
	assert.Equal(t, specServer.URL+"/v2/openapi.yaml", resp.SpecURL, "the URL after redirects, for resolving relative servers")

	status, respBody = doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, string(body), "", true)
	assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
	assert.Contains(t, respBody, "loopback")

	missing, _ := json.Marshal(relayOpenAPISpecRequest{SpecURL: specServer.URL + "/missing.json"})
	status, respBody = doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, string(missing), "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "could not load spec_url")

	for _, bad := range []string{`{}`, `{"spec_url":"  "}`, `{"spec_url":"ftp://example.com/openapi.json"}`, `{"spec_url":"/openapi.json"}`} {
		status, respBody = doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, bad, "", false)
		assert.Equal(t, fasthttp.StatusBadRequest, status, bad)
		assert.Contains(t, respBody, "spec_url must be an absolute http(s) URL", bad)
	}
	status, _ = doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, `not json`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

// TestOpenAPISpecFetchClientsByCaller: an unauthenticated caller's spec URL is fetched with the strict
// SSRF-safe client (core/network pins what strict refuses on the proxied path); an authenticated one
// may reach the operator's private network.
func TestOpenAPISpecFetchClientsByCaller(t *testing.T) {
	assert.Equal(t, reflect.ValueOf(network.NewStrictSSRFSafeHTTPClient).Pointer(), reflect.ValueOf(newUnauthenticatedSpecClient).Pointer())
	assert.Equal(t, reflect.ValueOf(network.NewPrivateNetworkHTTPClient).Pointer(), reflect.ValueOf(newAuthenticatedSpecClient).Pointer())

	h, _, _ := newRelaysTestHandler(t)
	var used []string
	strict, private := newUnauthenticatedSpecClient, newAuthenticatedSpecClient
	t.Cleanup(func() { newUnauthenticatedSpecClient, newAuthenticatedSpecClient = strict, private })
	// The stand-in clients fail at once, so nothing leaves the test.
	refuse := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("not dialed in tests") })}
	newUnauthenticatedSpecClient = func(time.Duration) *http.Client { used = append(used, "strict"); return refuse }
	newAuthenticatedSpecClient = func(time.Duration) *http.Client { used = append(used, "private"); return refuse }

	// A public IP literal passes the up-front private-target check without a DNS lookup.
	body := `{"spec_url":"https://1.1.1.1/openapi.json"}`
	doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, body, "", true)
	doMCPRequest(t, h.fetchOpenAPISpec, fasthttp.MethodPost, body, "", false)
	assert.Equal(t, []string{"strict", "private"}, used)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

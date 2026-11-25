package vertex

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
)

// TestVertexInteractionsTarget pins where Vertex sends Interactions calls: the key's own project and
// region under v1beta1 (Vertex answers 404 on v1), global when the key names no region.
func TestVertexInteractionsTarget(t *testing.T) {
	cases := []struct {
		name, region, wantURL string
	}{
		{"no region is global", "", "https://aiplatform.googleapis.com/v1beta1/projects/proj-1/locations/global/interactions"},
		{"global region", "global", "https://aiplatform.googleapis.com/v1beta1/projects/proj-1/locations/global/interactions"},
		{"a regional key keeps its region", "us-central1", "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/proj-1/locations/us-central1/interactions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantURL, vertexInteractionsCollectionURL(tc.region, "proj-1"))
		})
	}

	provider := &VertexProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.interactionsTarget(ctx, schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{}})
	require.NotNil(t, bifrostErr, "a key with no project cannot address an interaction")
}

// TestUseVertexInteractions pins which Vertex Responses requests run on Interactions: a Gemini model
// or an agent that needs stored state, a background run, or came in on the Interactions route. A
// Claude model on Vertex keeps its Anthropic surface even when it names a previous response.
func TestUseVertexInteractions(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	withPrevious := func(model string) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_prev")}}
	}
	assert.True(t, useVertexInteractions(ctx, withPrevious("gemini-3.8-flash")))
	assert.True(t, useVertexInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "deep-research-preview-04-2026"}))
	assert.False(t, useVertexInteractions(ctx, withPrevious("claude-sonnet-5-5")))
	assert.False(t, useVertexInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash"}))

	routed := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	routed.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	assert.True(t, useVertexInteractions(routed, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash"}))
	assert.False(t, useVertexInteractions(routed, &schemas.BifrostResponsesRequest{Model: "claude-sonnet-5-5"}))
}

// TestUseVertexInteractionsCustomAgent pins that an agent under the caller's own name, which the model
// name alone cannot tell from a Model Garden model, runs on Interactions when the request names it.
func TestUseVertexInteractionsCustomAgent(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	agent := &schemas.BifrostResponsesRequest{Model: "data-analyst", Params: &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"agent": "data-analyst"}}}
	assert.True(t, useVertexInteractions(ctx, agent))
	agent.Model = "claude-sonnet-5-5"
	assert.False(t, useVertexInteractions(ctx, agent))
}

// TestVertexInteractionsKeys pins which keys a call on a stored run may use: those in a location that
// serves Interactions, then the caller's own location and project. Nothing matching narrows nothing.
func TestVertexInteractionsKeys(t *testing.T) {
	key := func(id, project, region string) schemas.Key {
		return schemas.Key{ID: id, VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar(project), Region: *schemas.NewSecretVar(region)}}
	}
	regional, global, unset, eu, other := key("regional", "p1", "us-central1"), key("global", "p1", "global"), key("unset", "p1", ""), key("eu", "p1", "eu"), key("other", "p2", "global")
	all := []schemas.Key{regional, global, unset, eu, other}
	ids := func(keys []schemas.Key) (out []string) {
		for _, k := range keys {
			out = append(out, k.ID)
		}
		return out
	}
	assert.Equal(t, []string{"global", "unset", "eu", "other"}, ids(InteractionsKeys(all, "", "", "")))
	assert.Equal(t, []string{"global", "unset", "other"}, ids(InteractionsKeys(all, "", "", "global")))
	assert.Equal(t, []string{"other"}, ids(InteractionsKeys(all, "", "p2", "global")))
	assert.Equal(t, []string{"eu"}, ids(InteractionsKeys(all, "", "", "eu")))
	assert.Equal(t, []string{"global", "unset", "eu", "other"}, ids(InteractionsKeys(all, "", "p9", "asia-east1")))
	assert.Equal(t, []string{"regional"}, ids(InteractionsKeys([]schemas.Key{regional}, "", "", "")), "the only key is kept, so Google answers for its region")
	assert.Equal(t, []string{"regional", "global", "unset", "eu", "other"}, ids(all), "the caller's slice is not modified")
	padded := key("padded", " p1 ", "global")
	assert.Equal(t, []string{"padded"}, ids(InteractionsKeys([]schemas.Key{padded, other}, "", "p1", "global")), "the target trims a key's project")
	padded.Aliases = schemas.KeyAliases{"same": {ModelID: "gemini-3.6-flash", ProjectID: schemas.NewSecretVar("p1")}}
	assert.Len(t, interactionsPlaceKeys(padded, ""), 1)
}

// TestVertexInteractionsKeysUseAliasRegion pins that a key counts where the requested model's alias sends
// it, as the request's target does, and that a call on a stored run, which names no model, keeps a key
// any of whose aliases could have made the run.
func TestVertexInteractionsKeysUseAliasRegion(t *testing.T) {
	key := func(id, region, aliasRegion string) schemas.Key {
		return schemas.Key{ID: id,
			VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("p1"), Region: *schemas.NewSecretVar(region)},
			Aliases:         schemas.KeyAliases{"fast": {ModelID: "gemini-3.6-flash", Region: schemas.NewSecretVar(aliasRegion)}}}
	}
	a, b := key("a", "us-central1", "global"), key("b", "global", "us-central1")
	ids := func(keys []schemas.Key) (out []string) {
		for _, k := range keys {
			out = append(out, k.ID)
		}
		return out
	}
	assert.Equal(t, []string{"a"}, ids(InteractionsKeys([]schemas.Key{a, b}, "fast", "", "global")))
	assert.Equal(t, []string{"b"}, ids(InteractionsKeys([]schemas.Key{a, b}, "gemini-3.6-flash", "", "global")), "a model with no alias uses the key's region")
	assert.Equal(t, []string{"a", "b"}, ids(InteractionsKeys([]schemas.Key{a, b}, "", "", "")))
}

// TestVertexInteractionsContinuationKeys pins that a continued turn runs where its previous run is stored:
// of keys in more than one project or location, only those that reach it are kept. Nothing is read when the
// keys share a place or the request continues nothing, and keys are unchanged when no place has the run.
func TestVertexInteractionsContinuationKeys(t *testing.T) {
	key := func(id, project, region string) schemas.Key {
		return schemas.Key{ID: id, Value: schemas.SecretVar{}, VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar(project), Region: *schemas.NewSecretVar(region), AuthCredentials: *schemas.NewSecretVar(`{"type":"service_account"}`)}}
	}
	run := func(home, model, previous string, keys ...schemas.Key) (ids []string, paths []string) {
		provider, seen := vertexInteractionsServer(t, home)
		for _, k := range keys {
			provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(k)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"}))
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		// A fallback still holds the last attempt's alias, which places nothing here.
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: &schemas.AliasConfig{ProjectID: schemas.NewSecretVar("stale")}})
		request := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{}}
		if previous != "" {
			request.Params.PreviousResponseID = &previous
		}
		before := slices.Clone(keys)
		kept, bifrostErr := provider.ContinuationKeys(ctx, request, keys)
		require.Nil(t, bifrostErr)
		for _, k := range kept {
			ids = append(ids, k.ID)
		}
		assert.Equal(t, before, keys, "the caller's slice is not modified")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders), "the read leaves nothing on the request")
		return ids, seen()
	}
	const flash = "gemini-3.6-flash"
	p1, p2, p2eu, p2b, regional := key("p1", "p1", "global"), key("p2", "p2", "global"), key("p2eu", "p2", "eu"), key("p2b", "p2", ""), key("regional", "p2", "us-central1")

	ids, paths := run("/projects/p2/locations/global/", flash, "v1_x", p1, p2, p2eu, p2b, regional)
	assert.Equal(t, []string{"p2", "p2b"}, ids)
	assert.Len(t, paths, 2, "%v", paths)

	ids, paths = run("/projects/p2/locations/eu/", flash, "v1_x", p1, p2, p2eu)
	assert.Equal(t, []string{"p2eu"}, ids)
	assert.Len(t, paths, 3)

	ids, paths = run("/projects/nowhere/", flash, "v1_x", p1, p2)
	assert.Equal(t, []string{"p1", "p2"}, ids, "Google answers for a run no key reaches")
	assert.Len(t, paths, 2)

	for name, c := range map[string]struct {
		model, previous string
		keys            []schemas.Key
	}{
		"no previous turn":           {flash, "", []schemas.Key{p1, p2}},
		"one place":                  {flash, "v1_x", []schemas.Key{p2, p2b}},
		"one key":                    {flash, "v1_x", []schemas.Key{p2}},
		"a generateContent response": {flash, "resp_x", []schemas.Key{p1, p2}},
		"not on Interactions":        {"claude-sonnet-5", "v1_x", []schemas.Key{p1, p2}},
	} {
		ids, paths = run("/projects/p2/", c.model, c.previous, c.keys...)
		assert.Len(t, ids, len(c.keys), name)
		assert.Empty(t, paths, name)
	}

	aliased := key("aliased", "p1", "us-central1")
	aliased.Aliases = schemas.KeyAliases{"fast": {ModelID: flash, ProjectID: schemas.NewSecretVar("p9"), Region: schemas.NewSecretVar("eu")}}
	plain := p1
	plain.Aliases = schemas.KeyAliases{"fast": {ModelID: flash}}
	ids, _ = run("/projects/p9/locations/eu/", "fast", "v1_x", plain, aliased)
	assert.Equal(t, []string{"aliased"}, ids, "a key is where its alias sends the model")
}

// TestVertexInteractionsContinuationUndecided pins a lookup that a place did not answer: the turn goes to the one
// place not ruled out, and when more than one is left the lookup's error is returned, since any of them may hold
// the run.
func TestVertexInteractionsContinuationUndecided(t *testing.T) {
	run := func(codes map[string]int) ([]string, *schemas.BifrostError) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for project, code := range codes {
				if strings.Contains(r.URL.Path, "/projects/"+project+"/") {
					w.WriteHeader(code)
					_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(code) + `,"message":"no"}}`))
				}
			}
		}))
		defer server.Close()
		client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) }, TLSConfig: &tls.Config{InsecureSkipVerify: true}}
		provider := &VertexProvider{client: client, streamingClient: client}
		var keys []schemas.Key
		for _, project := range []string{"p1", "p2", "p3"} {
			k := schemas.Key{ID: project, VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar(project), Region: *schemas.NewSecretVar("global"), AuthCredentials: *schemas.NewSecretVar(`{"type":"service_account","client_email":"` + project + `"}`)}}
			provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(k)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"}))
			keys = append(keys, k)
		}
		kept, bifrostErr := provider.ContinuationKeys(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
			&schemas.BifrostResponsesRequest{Model: "gemini-3.6-flash", Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_x")}}, keys)
		var ids []string
		for _, k := range kept {
			ids = append(ids, k.ID)
		}
		return ids, bifrostErr
	}
	for _, code := range []int{403, 429, 500} {
		ids, bifrostErr := run(map[string]int{"p1": 404, "p2": code, "p3": 404})
		require.Nil(t, bifrostErr, code)
		assert.Equal(t, []string{"p2"}, ids, code)

		ids, bifrostErr = run(map[string]int{"p1": 404, "p2": code, "p3": code})
		require.NotNil(t, bifrostErr, code)
		assert.Equal(t, code, *bifrostErr.StatusCode)
		assert.Empty(t, ids)
	}
	ids, bifrostErr := run(map[string]int{"p1": 404, "p2": 404, "p3": 404})
	require.Nil(t, bifrostErr)
	assert.Len(t, ids, 3, "Google answers for a run no key reaches")
}

// BenchmarkVertexInteractionsKeys measures narrowing a pool of keys with aliases, local work only.
func BenchmarkVertexInteractionsKeys(b *testing.B) {
	var keys []schemas.Key
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		keys = append(keys, schemas.Key{ID: id, VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar(id), Region: *schemas.NewSecretVar("global")},
			Aliases: schemas.KeyAliases{"fast": {ModelID: "gemini-3.6-flash", Region: schemas.NewSecretVar("eu")}}})
	}
	for name, c := range map[string]struct{ model, project, location string }{"lifecycle": {}, "create at a location": {"fast", "p2", "eu"}} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				InteractionsKeys(keys, c.model, c.project, c.location)
			}
		})
	}
}

// BenchmarkVertexInteractionsContinuationKeys measures the local work of placing a continued turn: keys in one
// place, which reads nothing, and keys in two with no lookup possible, which leaves out the upstream wait.
func BenchmarkVertexInteractionsContinuationKeys(b *testing.B) {
	key := func(project string) schemas.Key {
		return schemas.Key{ID: project, VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar(project), Region: *schemas.NewSecretVar("us-central1")}}
	}
	provider := &VertexProvider{}
	request := &schemas.BifrostResponsesRequest{Model: "gemini-3.6-flash", Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_x")}}
	for name, keys := range map[string][]schemas.Key{"one place": {key("p1"), key("p1")}, "two places": {key("p1"), key("p2")}} {
		b.Run(name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			b.ReportAllocs()
			for b.Loop() {
				_, _ = provider.ContinuationKeys(ctx, request, keys)
			}
		})
	}
}

// vertexInteractionsServer answers like Vertex for one run stored at home, and records the paths asked.
func vertexInteractionsServer(t *testing.T, home string) (*VertexProvider, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, home) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"v1_x","object":"interaction","model":"gemini-3.6-flash","status":"completed","steps":[]}`))
	}))
	t.Cleanup(server.Close)
	client := &fasthttp.Client{
		Dial:      func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) },
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &VertexProvider{client: client, streamingClient: client}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

// TestVertexInteractionsAliasRunsReachable pins that a run created through an alias that changes the
// project or region can be read, cancelled, deleted and polled for billing afterwards, though those calls
// name no model. The key's own project and region are asked first, and no other place once one answers.
func TestVertexInteractionsAliasRunsReachable(t *testing.T) {
	const credentials = `{"type":"service_account","client_email":"a@b"}`
	key := schemas.Key{
		VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("own"), Region: *schemas.NewSecretVar("global"), AuthCredentials: *schemas.NewSecretVar(credentials)},
		Aliases: schemas.KeyAliases{
			"elsewhere": {ModelID: "gemini-3.6-flash", Region: schemas.NewSecretVar("eu"), ProjectID: schemas.NewSecretVar("other")},
			"regional":  {ModelID: "gemini-3.6-flash", Region: schemas.NewSecretVar("us-central1")},
			"plain":     {ModelID: "gemini-3.6-flash"},
		},
	}
	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	calls := map[string]func(*VertexProvider) *schemas.BifrostError{
		"retrieve": func(p *VertexProvider) *schemas.BifrostError {
			_, err := p.ResponsesRetrieve(newCtx(), key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
			return err
		},
		"cancel": func(p *VertexProvider) *schemas.BifrostError {
			_, err := p.ResponsesCancel(newCtx(), key, &schemas.BifrostResponsesCancelRequest{ResponseID: "v1_x"})
			return err
		},
		"delete": func(p *VertexProvider) *schemas.BifrostError {
			_, err := p.ResponsesDelete(newCtx(), key, &schemas.BifrostResponsesDeleteRequest{ResponseID: "v1_x"})
			return err
		},
		"input items": func(p *VertexProvider) *schemas.BifrostError {
			_, err := p.ResponsesInputItems(newCtx(), key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_x"})
			return err
		},
		"billing poll": func(p *VertexProvider) *schemas.BifrostError {
			resp, err := p.Passthrough(newCtx(), key, &schemas.BifrostPassthroughRequest{Method: http.MethodGet, Path: "/interactions/v1_x", APIVersion: "v1beta1"})
			if err == nil && resp.StatusCode != http.StatusOK {
				t.Errorf("poll status = %d", resp.StatusCode)
			}
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for home, asked := range map[string]int{"/projects/other/locations/eu/": 2, "/projects/own/locations/global/": 1} {
				provider, paths := vertexInteractionsServer(t, home)
				provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(key)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"}))
				require.Nil(t, call(provider))
				assert.Len(t, paths(), asked, "%v", paths())
				assert.Contains(t, (paths())[0], "/projects/own/locations/global/", "the key's own place first")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestVertexInteractionsBillingPollPlaces pins the poll's two harder cases: a key whose own region serves no
// Interactions polls the one alias place that does, and a project that refuses the key is passed over, as it
// is for the other calls on a stored run.
func TestVertexInteractionsBillingPollPlaces(t *testing.T) {
	poll := func(region, refused, home string) (int, []string) {
		var mu sync.Mutex
		var paths []string
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch {
			case refused != "" && strings.Contains(r.URL.Path, refused):
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"denied"}}`))
			case strings.Contains(r.URL.Path, home):
				_, _ = w.Write([]byte(`{"id":"v1_x","status":"completed"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			}
		}))
		defer server.Close()
		client := &fasthttp.Client{
			Dial:      func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) },
			TLSConfig: &tls.Config{InsecureSkipVerify: true},
		}
		// A refusal drops the cached token, so the next place mints one.
		mint := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))}, nil
		})}
		provider := &VertexProvider{client: client, streamingClient: client, authHTTPClient: mint}
		key := schemas.Key{
			VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("own"), Region: *schemas.NewSecretVar(region), AuthCredentials: *schemas.NewSecretVar(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`)},
			Aliases:         schemas.KeyAliases{"elsewhere": {ModelID: "gemini-3.6-flash", Region: schemas.NewSecretVar("global"), ProjectID: schemas.NewSecretVar("other")}},
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{Method: http.MethodGet, Path: "/interactions/v1_x", APIVersion: "v1beta1"})
		require.Nil(t, bifrostErr)
		mu.Lock()
		defer mu.Unlock()
		return resp.StatusCode, paths
	}
	status, paths := poll("us-central1", "", "/projects/other/")
	assert.Equal(t, http.StatusOK, status)
	require.Len(t, paths, 1)
	assert.Contains(t, paths[0], "/projects/other/locations/global/")

	status, paths = poll("global", "/projects/own/", "/projects/other/")
	assert.Equal(t, http.StatusOK, status, "%v", paths)
	assert.Len(t, paths, 2)

	status, _ = poll("global", "/projects/own/", "/projects/nowhere/")
	assert.Equal(t, http.StatusForbidden, status, "a refusal is reported over a 404, since the run may sit behind it")
}

// TestVertexInteractionsRefusesAPIKey pins that a key holding only an API key, which Interactions does not
// accept, is refused rather than run with the gateway host's own credentials. A key set up for the
// default credentials has no API key and is unaffected.
func TestVertexInteractionsRefusesAPIKey(t *testing.T) {
	provider, paths := vertexInteractionsServer(t, "/")
	provider.tokenSources.Store(getClientKey(""), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "host", TokenType: "Bearer"}))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("p1")}
	_, bifrostErr := provider.ResponsesRetrieve(ctx, schemas.Key{Value: *schemas.NewSecretVar("AIza-key"), VertexKeyConfig: config}, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "API key")
	assert.Empty(t, paths())

	_, bifrostErr = provider.ResponsesRetrieve(ctx, schemas.Key{VertexKeyConfig: config}, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
	assert.Nil(t, bifrostErr)
}

// TestVertexInteractionsEvictsTokenSourceOnAuthError pins that a 401 or 403 from an Interactions
// call drops the key's cached token source, as the generateContent paths do, so the next call
// mints a fresh token instead of reusing the rejected one. Other failures keep it.
func TestVertexInteractionsEvictsTokenSourceOnAuthError(t *testing.T) {
	var status atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"denied","status":"UNAUTHENTICATED"}}`))
	}))
	defer server.Close()
	client := &fasthttp.Client{
		Dial:      func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) },
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	}
	provider := &VertexProvider{client: client, streamingClient: client, logger: nil}
	key := schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("proj-1"), AuthCredentials: *schemas.NewSecretVar(`{"type":"service_account","client_email":"a@b"}`)}}
	clientKey := getClientKey(vertexCredentialIdentity(key))
	seed := func() {
		provider.tokenSources.Store(clientKey, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "stale", TokenType: "Bearer"}))
	}
	cached := func() bool {
		_, ok := provider.tokenSources.Load(clientKey)
		return ok
	}
	ctx := func() *schemas.BifrostContext {
		c := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		c.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		return c
	}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	create := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}}}

	calls := map[string]func() *schemas.BifrostError{
		"create": func() *schemas.BifrostError { _, err := provider.interactionsResponses(ctx(), key, create); return err },
		"create stream": func() *schemas.BifrostError {
			_, err := provider.interactionsResponsesStream(ctx(), postHook, nil, key, create)
			return err
		},
		"retrieve": func() *schemas.BifrostError {
			_, err := provider.ResponsesRetrieve(ctx(), key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
			return err
		},
		"retrieve stream": func() *schemas.BifrostError {
			_, err := provider.ResponsesRetrieveStream(ctx(), postHook, nil, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
			return err
		},
		"cancel": func() *schemas.BifrostError {
			_, err := provider.ResponsesCancel(ctx(), key, &schemas.BifrostResponsesCancelRequest{ResponseID: "v1_x"})
			return err
		},
		"delete": func() *schemas.BifrostError {
			_, err := provider.ResponsesDelete(ctx(), key, &schemas.BifrostResponsesDeleteRequest{ResponseID: "v1_x"})
			return err
		},
		"input items": func() *schemas.BifrostError {
			_, err := provider.ResponsesInputItems(ctx(), key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_x"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
				status.Store(int32(code))
				seed()
				require.NotNil(t, call())
				assert.False(t, cached(), "a %d drops the cached token source", code)
			}
			status.Store(http.StatusInternalServerError)
			seed()
			require.NotNil(t, call())
			assert.True(t, cached(), "a 500 keeps the cached token source")
		})
	}
}

// TestVertexInteractionsServiceTierHeader pins that a Responses request routed to Interactions
// sends the service-tier header under the same conditions as generateContent: a supported tier on
// the global endpoint, unless the provider's extra headers already set it.
func TestVertexInteractionsServiceTierHeader(t *testing.T) {
	var mu sync.Mutex
	var got []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get(VertexServiceTierHeader))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"v1_t","object":"interaction","model":"gemini-3.5-flash-lite","status":"completed","steps":[]}`))
	}))
	defer server.Close()
	client := &fasthttp.Client{
		Dial:      func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) },
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	}
	key := func(region string) schemas.Key {
		return schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("proj-1"), Region: *schemas.NewSecretVar(region), AuthCredentials: *schemas.NewSecretVar(`{"type":"service_account","client_email":"a@b"}`)}}
	}
	seed := func(provider *VertexProvider, k schemas.Key) {
		provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(k)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", TokenType: "Bearer"}))
	}
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.5-flash-lite",
		Input:  []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params: &schemas.ResponsesParameters{ServiceTier: schemas.Ptr(schemas.BifrostServiceTierFlex)},
	}
	run := func(provider *VertexProvider, k schemas.Key) {
		seed(provider, k)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		_, bifrostErr := provider.interactionsResponses(ctx, k, req)
		require.Nil(t, bifrostErr)
	}
	run(&VertexProvider{client: client, streamingClient: client}, key("global"))
	run(&VertexProvider{client: client, streamingClient: client}, key("us-central1"))
	run(&VertexProvider{client: client, streamingClient: client, networkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{VertexServiceTierHeader: *schemas.NewSecretVar("priority")}}}, key("global"))
	run(&VertexProvider{client: client, streamingClient: client}, key(""))
	lower := func() *VertexProvider {
		return &VertexProvider{client: client, streamingClient: client, networkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{"x-vertex-ai-llm-shared-request-type": *schemas.NewSecretVar("priority")}}}
	}
	run(lower(), key("global"))
	// The streamed create takes the same path; the header is sent before the reply is read.
	streamed := lower()
	seed(streamed, key("global"))
	sctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	sctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	stream, _ := streamed.interactionsResponsesStream(sctx, func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}, nil, key("global"), req)
	if stream != nil {
		for range stream {
		}
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 6)
	assert.Equal(t, "priority", got[4], "a lowercase override set by the operator wins")
	assert.Equal(t, "priority", got[5], "a lowercase override wins on the streamed create too")
	assert.Equal(t, "flex", got[0], "global endpoint sends the tier")
	assert.Equal(t, "", got[1], "a regional endpoint does not")
	assert.Equal(t, "priority", got[2], "an extra header set by the operator wins")
	assert.Equal(t, "flex", got[3], "a key with no region uses the global endpoint")
}

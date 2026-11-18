package vertex

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
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

// TestVertexInteractionsEvictsTokenSourceOnAuthError pins that a 401 or 403 from an Interactions
// call drops the key's cached token source, as the generateContent paths do, so the next call
// mints a fresh token instead of reusing the rejected one. Other failures keep it.
func TestVertexInteractionsEvictsTokenSourceOnAuthError(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
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
				status = code
				seed()
				require.NotNil(t, call())
				assert.False(t, cached(), "a %d drops the cached token source", code)
			}
			status = http.StatusInternalServerError
			seed()
			require.NotNil(t, call())
			assert.True(t, cached(), "a 500 keeps the cached token source")
		})
	}
}

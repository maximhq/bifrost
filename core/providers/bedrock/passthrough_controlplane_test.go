package bedrock

import (
	"net/http"
	"net/url"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS SDK clients pointed at the /bedrock integration call the read-only control-plane listings on the
// same base URL as the runtime APIs (Claude Code calls ListInferenceProfiles at startup). They are
// forwarded to the control-plane host, query string included, and AWS's JSON comes back untouched.
func TestBedrockPassthroughForwardsControlPlaneListings(t *testing.T) {
	cases := []struct {
		name, path, query string
	}{
		{"ListInferenceProfiles", "/inference-profiles", "type=SYSTEM_DEFINED&maxResults=2"},
		{"GetInferenceProfile", "/inference-profiles/us.anthropic.claude-sonnet-4-6", ""},
		{"ListFoundationModels", "/foundation-models", "byProvider=Anthropic"},
		{"GetFoundationModel", "/foundation-models/anthropic.claude-3-haiku-20240307-v1:0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"inferenceProfileSummaries":[{"inferenceProfileId":"us.anthropic.claude-sonnet-4-6","status":"ACTIVE"}],"nextToken":"t"}`
			target := newPassthroughTarget(t, http.StatusOK, body, map[string]string{"Content-Type": "application/json"})
			resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{
				Method:   http.MethodGet,
				Path:     tc.path,
				RawQuery: tc.query,
			})
			require.Nil(t, bifrostErr, "a control-plane listing must not be refused")
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.JSONEq(t, body, string(resp.Body))
			assert.Equal(t, "application/json", resp.Headers["Content-Type"])

			got := target.requests()
			require.Len(t, got, 1)
			assert.Equal(t, http.MethodGet, got[0].Method)
			// SigV4 signing puts the query in canonical order; the parameters themselves must all arrive.
			wantQuery, _ := url.ParseQuery(tc.query)
			gotQuery, _ := url.ParseQuery(got[0].RawQuery)
			assert.Equal(t, wantQuery, gotQuery)
			assert.Empty(t, got[0].Body)
			auth := got[0].Header.Get("Authorization")
			assert.Contains(t, auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/", "request must be SigV4 signed")
			assert.Contains(t, auth, "/us-west-2/bedrock/aws4_request")
			assert.True(t, IsControlPlaneListingPath(http.MethodGet, tc.path))
			assert.False(t, IsPassthroughStreamPath(http.MethodGet, tc.path), "listings answer with plain JSON")
		})
	}
}

// A Bedrock API key authenticates control-plane calls with a bearer token, as the native list-models
// call already does.
func TestBedrockPassthroughUsesBearerForAPIKeysOnControlPlaneListings(t *testing.T) {
	target := newPassthroughTarget(t, http.StatusOK, `{}`, nil)
	_, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.apiKeyOnlyKey("bedrock-api-key-123"), &schemas.BifrostPassthroughRequest{Method: http.MethodGet, Path: "/inference-profiles"})
	require.Nil(t, bifrostErr)
	got := target.requests()
	require.Len(t, got, 1)
	assert.Equal(t, "Bearer bedrock-api-key-123", got[0].Header.Get("Authorization"))
}

// A listing error (AccessDeniedException when the key's role lacks bedrock:ListInferenceProfiles)
// reaches the caller as AWS sent it, so SDKs raise the right exception.
func TestBedrockPassthroughForwardsControlPlaneErrorsVerbatim(t *testing.T) {
	target := newPassthroughTarget(t, http.StatusForbidden, `{"message":"not authorized"}`, map[string]string{"X-Amzn-Errortype": "AccessDeniedException", "Content-Type": "application/json"})
	resp, bifrostErr := target.provider.Passthrough(passthroughCtx(t), target.key(""), &schemas.BifrostPassthroughRequest{Method: http.MethodGet, Path: "/inference-profiles"})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "AccessDeniedException", resp.Headers["X-Amzn-Errortype"])
}

func TestIsControlPlaneListingPathIsLimitedToListings(t *testing.T) {
	assert.False(t, IsControlPlaneListingPath(http.MethodPost, "/inference-profiles"))
	assert.False(t, IsControlPlaneListingPath(http.MethodPost, retrievePassthroughPath))
	assert.False(t, IsControlPlaneListingPath(http.MethodGet, "/model-invocation-jobs"))
}

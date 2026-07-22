package bedrock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListInferenceProfilesForwardsAWSQueryAndFiltersByKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/inference-profiles", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("maxResults"))
		assert.Equal(t, "next-page", r.URL.Query().Get("nextToken"))
		assert.Equal(t, "SYSTEM_DEFINED", r.URL.Query().Get("type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "inferenceProfileSummaries": [
              {"inferenceProfileName":"Allowed","inferenceProfileArn":"arn:allowed","models":[{"modelArn":"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet"}],"inferenceProfileId":"us.anthropic.claude-sonnet","status":"ACTIVE","type":"SYSTEM_DEFINED"},
              {"inferenceProfileName":"Blocked","inferenceProfileArn":"arn:blocked","models":[{"modelArn":"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus"}],"inferenceProfileId":"us.anthropic.claude-opus","status":"ACTIVE","type":"SYSTEM_DEFINED"}
            ],
            "nextToken":"aws-next-token"
          }`))
	}))
	defer server.Close()

	provider := newTestProviderWithServer(t, server)
	key := testBedrockKey()
	key.Models = schemas.WhiteList{"us.anthropic.claude-sonnet"}
	maxResults := 1
	nextToken := "next-page"
	profileType := "SYSTEM_DEFINED"

	response, bifrostErr := provider.ListInferenceProfiles(testBedrockCtx(), []schemas.Key{key}, &schemas.BifrostListInferenceProfilesRequest{
		Provider:   schemas.Bedrock,
		MaxResults: &maxResults,
		NextToken:  &nextToken,
		Type:       &profileType,
	})
	require.Nil(t, bifrostErr)
	require.Len(t, response.InferenceProfileSummaries, 1)
	assert.Equal(t, "us.anthropic.claude-sonnet", response.InferenceProfileSummaries[0].InferenceProfileID)
	require.NotNil(t, response.NextToken)
	assert.Equal(t, "aws-next-token", *response.NextToken)
}

func TestGetInferenceProfileUsesProfileIdentifierForPolicyAndAWSPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/inference-profiles/us.anthropic.claude-sonnet", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "inferenceProfileName":"Sonnet",
          "inferenceProfileArn":"arn:aws:bedrock:us-east-1::inference-profile/us.anthropic.claude-sonnet",
          "models":[{"modelArn":"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet"}],
          "inferenceProfileId":"us.anthropic.claude-sonnet",
          "status":"ACTIVE",
          "type":"SYSTEM_DEFINED"
        }`))
	}))
	defer server.Close()

	provider := newTestProviderWithServer(t, server)
	key := testBedrockKey()
	key.Models = schemas.WhiteList{"claude-sonnet"}
	key.Aliases = schemas.KeyAliases{"claude-sonnet": {ModelID: "us.anthropic.claude-sonnet"}}

	response, bifrostErr := provider.GetInferenceProfile(testBedrockCtx(), key, &schemas.BifrostGetInferenceProfileRequest{
		Provider:                   schemas.Bedrock,
		InferenceProfileIdentifier: "us.anthropic.claude-sonnet",
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "us.anthropic.claude-sonnet", response.InferenceProfileID)
	require.Len(t, response.Models, 1)
}

func TestInferenceProfileAllowedForKeyRejectsDirectAndAliasBlacklists(t *testing.T) {
	identifier := "us.anthropic.claude-sonnet"
	assert.False(t, inferenceProfileAllowedForKey(identifier, schemas.Key{
		Models:            schemas.WhiteList{"*"},
		BlacklistedModels: schemas.BlackList{identifier},
	}))
	assert.False(t, inferenceProfileAllowedForKey(identifier, schemas.Key{
		Models:            schemas.WhiteList{"claude-sonnet"},
		BlacklistedModels: schemas.BlackList{"claude-sonnet"},
		Aliases:           schemas.KeyAliases{"claude-sonnet": {ModelID: identifier}},
	}))
}

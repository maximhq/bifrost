package bedrock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelIDs(models []schemas.Model) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestInferenceProfilesToBifrostListModelsResponse(t *testing.T) {
	foundation := map[string]BedrockModel{
		"anthropic.claude-sonnet-4-6": {
			ModelID:          "anthropic.claude-sonnet-4-6",
			ProviderName:     "Anthropic",
			InputModalities:  []string{"TEXT", "IMAGE"},
			OutputModalities: []string{"TEXT"},
		},
	}
	profiles := &BedrockListInferenceProfilesResponse{
		InferenceProfileSummaries: []BedrockInferenceProfileSummary{
			{
				InferenceProfileID:   "us.anthropic.claude-sonnet-4-6",
				InferenceProfileName: "US Anthropic Claude Sonnet 4.6",
				Status:               "ACTIVE",
				Models: []BedrockInferenceProfileModel{
					{ModelArn: "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-6"},
					{ModelArn: "arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-sonnet-4-6"},
				},
			},
			{
				InferenceProfileID:   "global.anthropic.claude-sonnet-4-6",
				InferenceProfileName: "Global Anthropic Claude Sonnet 4.6",
				Status:               "ACTIVE",
				Models:               []BedrockInferenceProfileModel{{ModelArn: "arn:aws:bedrock:::foundation-model/anthropic.claude-sonnet-4-6"}},
			},
			{
				InferenceProfileID:   "us.meta.llama3-2-1b-instruct-v1:0",
				InferenceProfileName: "US Meta Llama 3.2 1B Instruct",
				Status:               "ACTIVE",
				Models:               []BedrockInferenceProfileModel{{ModelArn: "arn:aws:bedrock:us-east-1::foundation-model/meta.llama3-2-1b-instruct-v1:0"}},
			},
			{
				InferenceProfileID: "us.anthropic.claude-legacy",
				Status:             "LEGACY",
			},
		},
	}

	t.Run("lists active profile IDs with foundation metadata", func(t *testing.T) {
		resp := profiles.ToBifrostListModelsResponse(schemas.Bedrock, schemas.WhiteList{"*"}, nil, nil, false, foundation, false)
		require.NotNil(t, resp)
		assert.Equal(t, []string{
			"bedrock/us.anthropic.claude-sonnet-4-6",
			"bedrock/global.anthropic.claude-sonnet-4-6",
			"bedrock/us.meta.llama3-2-1b-instruct-v1:0",
		}, modelIDs(resp.Data))

		us := resp.Data[0]
		require.NotNil(t, us.Name)
		assert.Equal(t, "US Anthropic Claude Sonnet 4.6", *us.Name)
		require.NotNil(t, us.OwnedBy)
		assert.Equal(t, "Anthropic", *us.OwnedBy)
		require.NotNil(t, us.Architecture)
		assert.Equal(t, []string{"TEXT", "IMAGE"}, us.Architecture.InputModalities)

		// Foundation model not in ListFoundationModels: listed, without inherited metadata.
		assert.Nil(t, resp.Data[2].OwnedBy)
		assert.Nil(t, resp.Data[2].Architecture)
	})

	t.Run("requireFoundationModel drops profiles whose foundation model was filtered out", func(t *testing.T) {
		resp := profiles.ToBifrostListModelsResponse(schemas.Bedrock, schemas.WhiteList{"*"}, nil, nil, false, foundation, true)
		assert.Equal(t, []string{
			"bedrock/us.anthropic.claude-sonnet-4-6",
			"bedrock/global.anthropic.claude-sonnet-4-6",
		}, modelIDs(resp.Data))
	})

	t.Run("key allowlist and blacklist apply to profile IDs", func(t *testing.T) {
		resp := profiles.ToBifrostListModelsResponse(schemas.Bedrock, schemas.WhiteList{"us.anthropic.claude-sonnet-4-6", "global.anthropic.claude-sonnet-4-6"}, schemas.BlackList{"global.anthropic.claude-sonnet-4-6"}, nil, false, foundation, false)
		assert.Equal(t, []string{"bedrock/us.anthropic.claude-sonnet-4-6"}, modelIDs(resp.Data))
	})

	t.Run("empty allowlist lists nothing", func(t *testing.T) {
		resp := profiles.ToBifrostListModelsResponse(schemas.Bedrock, nil, nil, nil, false, foundation, false)
		assert.Empty(t, resp.Data)
	})
}

// newListModelsServer serves ListFoundationModels and a two-page ListInferenceProfiles whose
// contents depend on the bearer key, standing in for one Bedrock control plane per region.
// Mantle /v1/models is not served (404), which listMantleModels treats as best-effort.
func newListModelsServer(t *testing.T, profileStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var profileQueries []string
	profilesByKey := map[string][2]string{
		"Bearer us-key": {
			`{"inferenceProfileSummaries":[{"inferenceProfileId":"us.anthropic.claude-sonnet-4-6","inferenceProfileName":"US Claude Sonnet 4.6","status":"ACTIVE","type":"SYSTEM_DEFINED","models":[{"modelArn":"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-6"}]}],"nextToken":"page2"}`,
			`{"inferenceProfileSummaries":[{"inferenceProfileId":"global.anthropic.claude-sonnet-4-6","inferenceProfileName":"Global Claude Sonnet 4.6","status":"ACTIVE","type":"SYSTEM_DEFINED","models":[{"modelArn":"arn:aws:bedrock:::foundation-model/anthropic.claude-sonnet-4-6"}]}]}`,
		},
		"Bearer eu-key": {
			`{"inferenceProfileSummaries":[{"inferenceProfileId":"eu.anthropic.claude-sonnet-4-6","inferenceProfileName":"EU Claude Sonnet 4.6","status":"ACTIVE","type":"SYSTEM_DEFINED","models":[{"modelArn":"arn:aws:bedrock:eu-west-1::foundation-model/anthropic.claude-sonnet-4-6"}]}],"nextToken":"page2"}`,
			`{"inferenceProfileSummaries":[{"inferenceProfileId":"global.anthropic.claude-sonnet-4-6","inferenceProfileName":"Global Claude Sonnet 4.6","status":"ACTIVE","type":"SYSTEM_DEFINED","models":[{"modelArn":"arn:aws:bedrock:::foundation-model/anthropic.claude-sonnet-4-6"}]}]}`,
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/foundation-models":
			if bp := r.URL.Query().Get("byProvider"); bp != "" && bp != "Anthropic" {
				_, _ = w.Write([]byte(`{"modelSummaries":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"modelSummaries":[{"modelId":"anthropic.claude-sonnet-4-6","modelName":"Claude Sonnet 4.6","providerName":"Anthropic","inputModalities":["TEXT"],"outputModalities":["TEXT"],"inferenceTypesSupported":["INFERENCE_PROFILE"]}]}`))
		case "/inference-profiles":
			profileQueries = append(profileQueries, r.URL.RawQuery)
			if profileStatus != http.StatusOK {
				w.WriteHeader(profileStatus)
				_, _ = w.Write([]byte(`{"message":"not authorized to perform bedrock:ListInferenceProfiles"}`))
				return
			}
			pages := profilesByKey[r.Header.Get("Authorization")]
			if r.URL.Query().Get("nextToken") == "page2" {
				_, _ = w.Write([]byte(pages[1]))
				return
			}
			_, _ = w.Write([]byte(pages[0]))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &profileQueries
}

func listModelsTestKey(value, region string) schemas.Key {
	return schemas.Key{
		ID:               value,
		Value:            *schemas.NewSecretVar(value),
		Models:           schemas.WhiteList{"*"},
		BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: schemas.NewSecretVar(region)},
	}
}

func TestListModels_MergesInferenceProfiles(t *testing.T) {
	t.Run("single key lists bare and cross-region IDs across pages", func(t *testing.T) {
		ts, queries := newListModelsServer(t, http.StatusOK)
		p := newTestProviderWithServer(t, ts)

		resp, bifrostErr := p.listModelsByKey(testBedrockCtx(), listModelsTestKey("us-key", "us-east-1"), &schemas.BifrostListModelsRequest{Provider: schemas.Bedrock})
		require.Nil(t, bifrostErr)
		assert.ElementsMatch(t, []string{
			"bedrock/anthropic.claude-sonnet-4-6",
			"bedrock/us.anthropic.claude-sonnet-4-6",
			"bedrock/global.anthropic.claude-sonnet-4-6",
		}, modelIDs(resp.Data))
		require.Len(t, *queries, 2)
		assert.Contains(t, (*queries)[0], "type=SYSTEM_DEFINED")
		assert.Contains(t, (*queries)[1], "nextToken=page2")
	})

	t.Run("us and eu keys list both geo profiles and one global", func(t *testing.T) {
		ts, _ := newListModelsServer(t, http.StatusOK)
		p := newTestProviderWithServer(t, ts)

		resp, bifrostErr := p.ListModels(testBedrockCtx(), []schemas.Key{
			listModelsTestKey("us-key", "us-east-1"),
			listModelsTestKey("eu-key", "eu-west-1"),
		}, &schemas.BifrostListModelsRequest{Provider: schemas.Bedrock})
		require.Nil(t, bifrostErr)
		assert.Equal(t, []string{
			"bedrock/anthropic.claude-sonnet-4-6",
			"bedrock/eu.anthropic.claude-sonnet-4-6",
			"bedrock/global.anthropic.claude-sonnet-4-6",
			"bedrock/us.anthropic.claude-sonnet-4-6",
		}, modelIDs(resp.Data))
	})

	t.Run("profiles access denied still returns foundation models", func(t *testing.T) {
		ts, _ := newListModelsServer(t, http.StatusForbidden)
		p := newTestProviderWithServer(t, ts)

		resp, bifrostErr := p.listModelsByKey(testBedrockCtx(), listModelsTestKey("us-key", "us-east-1"), &schemas.BifrostListModelsRequest{Provider: schemas.Bedrock})
		require.Nil(t, bifrostErr)
		assert.Equal(t, []string{"bedrock/anthropic.claude-sonnet-4-6"}, modelIDs(resp.Data))
	})

	t.Run("by* filters keep only profiles of surviving foundation models", func(t *testing.T) {
		ts, _ := newListModelsServer(t, http.StatusOK)
		p := newTestProviderWithServer(t, ts)

		resp, bifrostErr := p.listModelsByKey(testBedrockCtx(), listModelsTestKey("us-key", "us-east-1"), &schemas.BifrostListModelsRequest{
			Provider:    schemas.Bedrock,
			ExtraParams: map[string]any{"byProvider": "Meta"},
		})
		require.Nil(t, bifrostErr)
		assert.Empty(t, resp.Data)
	})
}

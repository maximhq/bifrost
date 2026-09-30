package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestListModelsByKeyResponseShapes verifies that supported upstream model-list
// envelope shapes normalize into the same Bifrost model representation.
func TestListModelsByKeyResponseShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantID      string
		wantOwnedBy string
		wantContext int
	}{
		{
			name:        "OpenAI envelope",
			body:        `{"object":"list","data":[{"id":"gpt-5","owned_by":"openai","context_window":128000}]}`,
			wantID:      "test/gpt-5",
			wantOwnedBy: "openai",
			wantContext: 128000,
		},
		{
			name:        "Together array",
			body:        `[{"id":"zai-org/GLM-5.2","organization":"Z.ai","context_length":131072}]`,
			wantID:      "test/zai-org/GLM-5.2",
			wantOwnedBy: "Z.ai",
			wantContext: 131072,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			response, bifrostErr := ListModelsByKey(
				schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{},
				server.URL,
				schemas.Key{Models: schemas.WhiteList{"*"}},
				false,
				nil,
				schemas.ModelProvider("test"),
				false,
				false,
			)

			require.Nil(t, bifrostErr)
			require.Len(t, response.Data, 1)
			require.Equal(t, test.wantID, response.Data[0].ID)
			require.Equal(t, schemas.Ptr(test.wantOwnedBy), response.Data[0].OwnedBy)
			require.Equal(t, schemas.Ptr(test.wantContext), response.Data[0].ContextLength)
		})
	}
}

// TestListModelsPreservesDisplayNameAndReasoningMetadata verifies that optional
// OpenAI-compatible catalog metadata survives the complete conversion round trip.
func TestListModelsPreservesDisplayNameAndReasoningMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{
				"id":"vendor/reasoner",
				"owned_by":"vendor",
				"display_name":"Reasoner Pro",
				"description":"A readable description",
				"default_reasoning_level":"medium",
				"supported_reasoning_levels":[
					{"effort":"low","description":"Fast"},
					{"effort":"medium","description":"Balanced"},
					{"effort":"high","description":"Deep"}
				]
			}]
		}`))
	}))
	defer server.Close()

	response, bifrostErr := ListModelsByKey(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{},
		server.URL,
		schemas.Key{Models: schemas.WhiteList{"*"}},
		false,
		nil,
		schemas.ModelProvider("test"),
		false,
		false,
	)
	require.Nil(t, bifrostErr)
	require.Len(t, response.Data, 1)
	require.Equal(t, schemas.Ptr("Reasoner Pro"), response.Data[0].Name)
	require.Equal(t, schemas.Ptr("A readable description"), response.Data[0].Description)
	require.Equal(t, schemas.Ptr("medium"), response.Data[0].DefaultReasoningLevel)
	require.JSONEq(t, `[
		{"effort":"low","description":"Fast"},
		{"effort":"medium","description":"Balanced"},
		{"effort":"high","description":"Deep"}
	]`, string(response.Data[0].SupportedReasoningLevels))

	roundTrip := ToOpenAIListModelsResponse(response)
	require.Len(t, roundTrip.Data, 1)
	require.Equal(t, schemas.Ptr("Reasoner Pro"), roundTrip.Data[0].DisplayName)
	require.Equal(t, schemas.Ptr("medium"), roundTrip.Data[0].DefaultReasoningLevel)
	require.JSONEq(t, string(response.Data[0].SupportedReasoningLevels), string(roundTrip.Data[0].SupportedReasoningLevels))
}

package openai

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestToBifrostListModelsResponse_ActivePreservedWhenFlagEnabled is a regression test: Active
// (Groq's model-availability flag) is a known OpenAIModel field, so it never lands in Extra on
// its own - but schemas.Model has no field for it, so without folding it in separately it would
// be silently dropped even with includeCustomModelFields enabled, contradicting the flag's
// promise to preserve provider-specific fields on retrieval.
func TestToBifrostListModelsResponse_ActivePreservedWhenFlagEnabled(t *testing.T) {
	active := true
	response := &OpenAIListModelsResponse{
		Data: []OpenAIModel{
			{ID: "llama3-groq", Object: "model", OwnedBy: "groq", Active: &active},
		},
	}

	bifrostResponse := response.ToBifrostListModelsResponse(schemas.Groq, schemas.WhiteList{"*"}, nil, nil, true, true)
	if len(bifrostResponse.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(bifrostResponse.Data))
	}

	model := bifrostResponse.Data[0]
	if len(model.ProviderExtra) == 0 {
		t.Fatal("expected ProviderExtra to be populated with the Active field")
	}
	if !strings.Contains(string(model.ProviderExtra), `"active":true`) {
		t.Errorf("expected ProviderExtra to contain active:true, got: %s", model.ProviderExtra)
	}
}

// TestToOpenAIModel_ActiveSurvivesRetrieveRoundTrip is a regression test: withActiveField folds
// Groq's typed Active field into ProviderExtra since schemas.Model has no field for it, but
// ToOpenAIModel used to just dump ProviderExtra into OpenAIModel.Extra verbatim - and because
// "active" is a known field, OpenAIModel.MarshalJSON skips merging it back from Extra ("known
// fields always win"), while the typed Active field stayed nil and got omitted. Net effect:
// "active" was present in ProviderExtra but silently missing from the final retrieve response
// on the OpenAI/Cursor SDK-compatible routes. ToOpenAIModel must rehydrate it into the typed
// field instead of leaving it in Extra.
func TestToOpenAIModel_ActiveSurvivesRetrieveRoundTrip(t *testing.T) {
	active := true
	response := &OpenAIListModelsResponse{
		Data: []OpenAIModel{
			{ID: "llama3-groq", Object: "model", OwnedBy: "groq", Active: &active},
		},
	}
	bifrostResponse := response.ToBifrostListModelsResponse(schemas.Groq, schemas.WhiteList{"*"}, nil, nil, true, true)
	if len(bifrostResponse.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(bifrostResponse.Data))
	}

	openaiModel := ToOpenAIModel(&bifrostResponse.Data[0])
	if openaiModel.Active == nil || !*openaiModel.Active {
		t.Fatalf("expected Active to be true, got %v", openaiModel.Active)
	}

	body, err := sonic.Marshal(openaiModel)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	if !strings.Contains(string(body), `"active":true`) {
		t.Errorf(`expected marshaled response to contain "active":true, got: %s`, body)
	}
}

func TestToBifrostListModelsResponse_ActiveOmittedWhenFlagDisabled(t *testing.T) {
	active := true
	response := &OpenAIListModelsResponse{
		Data: []OpenAIModel{
			{ID: "llama3-groq", Object: "model", OwnedBy: "groq", Active: &active},
		},
	}

	bifrostResponse := response.ToBifrostListModelsResponse(schemas.Groq, schemas.WhiteList{"*"}, nil, nil, true, false)
	if len(bifrostResponse.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(bifrostResponse.Data))
	}
	if len(bifrostResponse.Data[0].ProviderExtra) != 0 {
		t.Errorf("expected ProviderExtra to stay empty when flag disabled, got: %s", bifrostResponse.Data[0].ProviderExtra)
	}
}

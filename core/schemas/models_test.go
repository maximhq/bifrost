package schemas

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Raw types that mirror the real KeyStatus → BifrostError → ExtraFields → KeyStatus
// chain but without any custom MarshalJSON. Used to reproduce the cycle error
// that would occur without the fix.
type rawKeyStatusExtraFields struct {
	KeyStatuses []rawKeyStatus `json:"key_statuses,omitempty"`
}

type rawKeyStatusBifrostError struct {
	IsBifrostError bool                    `json:"is_bifrost_error"`
	Error          *ErrorField             `json:"error"`
	ExtraFields    rawKeyStatusExtraFields `json:"extra_fields"`
}

type rawKeyStatus struct {
	KeyID    string                    `json:"key_id"`
	Status   KeyStatusType             `json:"status"`
	Provider ModelProvider             `json:"provider"`
	Error    *rawKeyStatusBifrostError `json:"error,omitempty"`
}

// TestKeyStatusMarshalJSON_ReproduceCycle proves that without the custom MarshalJSON,
// the circular reference between KeyStatus and BifrostError causes a marshaling failure.
func TestKeyStatusMarshalJSON_ReproduceCycle(t *testing.T) {
	bifrostErr := &rawKeyStatusBifrostError{
		IsBifrostError: true,
		Error:          &ErrorField{Message: "test error"},
	}
	keyStatus := rawKeyStatus{
		KeyID:    "key-1",
		Status:   KeyStatusListModelsFailed,
		Provider: "test-provider",
		Error:    bifrostErr,
	}
	// Create the same cycle that HandleKeylessListModelsRequest creates
	bifrostErr.ExtraFields.KeyStatuses = []rawKeyStatus{keyStatus}

	// Without any custom MarshalJSON, this must fail with a cycle error
	_, err := json.Marshal(keyStatus)
	require.Error(t, err, "expected cycle error without the MarshalJSON fix")
	assert.Contains(t, err.Error(), "cycle", "error should mention a cycle")
}

func TestKeyStatusMarshalJSON_NoCycle(t *testing.T) {
	bifrostErr := &BifrostError{
		IsBifrostError: true,
		Error:          &ErrorField{Message: "test error"},
	}
	keyStatus := KeyStatus{
		KeyID:    "key-1",
		Status:   KeyStatusListModelsFailed,
		Provider: "test-provider",
		Error:    bifrostErr,
	}
	// Create the same cycle that HandleKeylessListModelsRequest creates
	bifrostErr.ExtraFields.KeyStatuses = []KeyStatus{keyStatus}

	data, err := Marshal(keyStatus)
	require.NoError(t, err, "Marshal should not fail on circular KeyStatus")

	// Verify the output doesn't contain nested key_statuses
	assert.False(t, bytes.Contains(data, []byte(`"key_statuses"`)),
		"expected key_statuses to be omitted from nested error")
}

func TestKeyStatusMarshalJSON_NilError(t *testing.T) {
	keyStatus := KeyStatus{
		KeyID:    "key-2",
		Status:   "success",
		Provider: "test-provider",
	}

	data, err := Marshal(keyStatus)
	require.NoError(t, err, "Marshal should not fail on KeyStatus with nil error")
	assert.Contains(t, string(data), `"key_id":"key-2"`)
	assert.NotContains(t, string(data), `"error"`)
}

func TestKeyStatusMarshalJSON_PreservesErrorFields(t *testing.T) {
	statusCode := 401
	bifrostErr := &BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &ErrorField{Message: "unauthorized"},
		ExtraFields: BifrostErrorExtraFields{
			Provider:               "openai",
			OriginalModelRequested: "gpt-4",
		},
	}
	keyStatus := KeyStatus{
		KeyID:    "key-3",
		Status:   KeyStatusListModelsFailed,
		Provider: "openai",
		Error:    bifrostErr,
	}
	// Create cycle
	bifrostErr.ExtraFields.KeyStatuses = []KeyStatus{keyStatus}

	data, err := Marshal(keyStatus)
	require.NoError(t, err)

	// Error fields other than key_statuses should be preserved
	dataStr := string(data)
	assert.Contains(t, dataStr, `"unauthorized"`)
	assert.Contains(t, dataStr, `"original_model_requested":"gpt-4"`)
	assert.Contains(t, dataStr, `"status_code":401`)
}

// TestModelReasoningRoundTrip verifies that provider list-models payloads
// carrying an OpenRouter-style `reasoning` object survive decode/encode
// through schemas.Model, including partial shapes with no effort levels.
func TestModelReasoningRoundTrip(t *testing.T) {
	payload := []byte(`{
		"id": "google/gemini-3.6-flash",
		"supported_parameters": ["reasoning", "include_reasoning"],
		"reasoning": {
			"mandatory": true,
			"default_enabled": true,
			"supported_efforts": ["high", "medium", "low", "minimal"],
			"default_effort": "medium"
		}
	}`)

	var model Model
	require.NoError(t, json.Unmarshal(payload, &model))
	require.NotNil(t, model.Reasoning)
	assert.Equal(t, Ptr(true), model.Reasoning.Mandatory)
	assert.Equal(t, Ptr(true), model.Reasoning.DefaultEnabled)
	assert.Equal(t, []string{"high", "medium", "low", "minimal"}, model.Reasoning.SupportedEfforts)
	assert.Equal(t, Ptr("medium"), model.Reasoning.DefaultEffort)

	encoded, err := json.Marshal(model)
	require.NoError(t, err)
	var decoded Model
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, model.Reasoning, decoded.Reasoning)

	// Partial shape: reasoning supported but no selectable effort.
	var partial Model
	require.NoError(t, json.Unmarshal([]byte(`{"id":"x","reasoning":{"mandatory":false,"default_enabled":true}}`), &partial))
	require.NotNil(t, partial.Reasoning)
	assert.Nil(t, partial.Reasoning.SupportedEfforts)
	assert.Nil(t, partial.Reasoning.DefaultEffort)

	// No reasoning object → field stays nil and is omitted on encode.
	var absent Model
	require.NoError(t, json.Unmarshal([]byte(`{"id":"x"}`), &absent))
	assert.Nil(t, absent.Reasoning)
	encoded, err = json.Marshal(absent)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "reasoning")
}

// A shallow copy of the nested structs would leave every field aliased: they
// are themselves all pointers, so copying the pointee alone still shares each
// one. Writing through a copy would then rewrite the cache it came from.
func TestModelDeepCopyDoesNotAliasNestedStructs(t *testing.T) {
	orig := Model{
		ID:                   "m",
		Name:                 Ptr("original"),
		ContextLength:        Ptr(100),
		Pricing:              &Pricing{Prompt: Ptr("0.001"), InputCacheRead: Ptr("0.0001")},
		TopProvider:          &TopProvider{ContextLength: Ptr(200), IsModerated: Ptr(true)},
		PerRequestLimits:     &PerRequestLimits{PromptTokens: Ptr(300)},
		DefaultParameters:    &DefaultParameters{Temperature: Ptr(0.5)},
		Architecture:         &Architecture{Modality: Ptr("text"), InputModalities: []string{"text"}},
		Reasoning:            &ModelReasoning{Mandatory: Ptr(true), SupportedEfforts: []string{"low"}},
		SupportedParameters:  []string{"tools"},
		AdditionalAttributes: map[string]string{"tier": "original"},
	}

	c := orig.DeepCopy()

	// Every pointer must be a fresh allocation, one level down included.
	assert.NotSame(t, orig.Name, c.Name)
	assert.NotSame(t, orig.Pricing.Prompt, c.Pricing.Prompt)
	assert.NotSame(t, orig.TopProvider.ContextLength, c.TopProvider.ContextLength)
	assert.NotSame(t, orig.PerRequestLimits.PromptTokens, c.PerRequestLimits.PromptTokens)
	assert.NotSame(t, orig.DefaultParameters.Temperature, c.DefaultParameters.Temperature)
	assert.NotSame(t, orig.Architecture.Modality, c.Architecture.Modality)
	assert.NotSame(t, orig.Reasoning.Mandatory, c.Reasoning.Mandatory)

	// Writing through the copy must not reach the original.
	*c.Name = "mutated"
	*c.Pricing.Prompt = "mutated"
	*c.TopProvider.ContextLength = 1
	*c.PerRequestLimits.PromptTokens = 1
	*c.DefaultParameters.Temperature = 1
	*c.Architecture.Modality = "mutated"
	*c.Reasoning.Mandatory = false
	c.Architecture.InputModalities[0] = "mutated"
	c.Reasoning.SupportedEfforts[0] = "mutated"
	c.SupportedParameters[0] = "mutated"
	c.AdditionalAttributes["tier"] = "mutated"

	assert.Equal(t, "original", *orig.Name)
	assert.Equal(t, "0.001", *orig.Pricing.Prompt)
	assert.Equal(t, 200, *orig.TopProvider.ContextLength)
	assert.Equal(t, 300, *orig.PerRequestLimits.PromptTokens)
	assert.Equal(t, 0.5, *orig.DefaultParameters.Temperature)
	assert.Equal(t, "text", *orig.Architecture.Modality)
	assert.True(t, *orig.Reasoning.Mandatory)
	assert.Equal(t, "text", orig.Architecture.InputModalities[0])
	assert.Equal(t, "low", orig.Reasoning.SupportedEfforts[0])
	assert.Equal(t, "tools", orig.SupportedParameters[0])
	assert.Equal(t, "original", orig.AdditionalAttributes["tier"])
}

// Nil nested structs stay nil rather than becoming empty ones.
func TestModelDeepCopyPreservesNils(t *testing.T) {
	c := Model{ID: "bare"}.DeepCopy()

	assert.Equal(t, "bare", c.ID)
	assert.Nil(t, c.Name)
	assert.Nil(t, c.Pricing)
	assert.Nil(t, c.TopProvider)
	assert.Nil(t, c.PerRequestLimits)
	assert.Nil(t, c.DefaultParameters)
	assert.Nil(t, c.Architecture)
	assert.Nil(t, c.Reasoning)
}

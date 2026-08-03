package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBifrostOverrides_RoundTrip verifies marshal/unmarshal preserves every
// field. This is the primary safety net against silent field drift between
// the datasheet wire shape and the Go struct.
func TestBifrostOverrides_RoundTrip(t *testing.T) {
	truePtr := true
	intPtr := 4096
	str := "converse"
	condStr := "when_effort_none"

	original := BifrostOverrides{
		SupportsCachePoint:       &truePtr,
		SupportsContext1M:        &truePtr,
		ServerTools:              map[string]string{"web_search": "web_search_20260209"},
		BetaHeaders:              map[string]string{"compaction": "compact-2026-01-12"},
		FieldNames:               map[string]string{"max_tokens": "max_completion_tokens"},
		ServerToolAutoInjects:    map[string][]string{"web_search_20260209": {"code_execution_20260120"}},
		ServerToolImplicitBetas:  map[string]string{"memory_20250818": "context-management-2025-06-27"},
		ExtraHeaders:             map[string]string{"OpenAI-Beta": "realtime=v1"},
		EffortRenames:            map[string]string{"minimal": "low"},
		UnsupportedFields:        map[string]bool{"top_p": true, "temperature": true},
		ConditionallyUnsupportedFields: map[string]string{"top_p": condStr},
		Reasoning: &BifrostReasoningConfig{
			Style:        ptr("adaptive"),
			Field:        ptr("output_config.effort"),
			EffortLevels: []string{"low", "medium", "high"},
		},
		Thinking: &BifrostThinkingConfig{
			Field: ptr("thinkingBudget"),
			Min:   intptr(128),
			Max:   intptr(32768),
			SpecialValues: map[string]string{"-1": "dynamic", "0": "disabled"},
		},
		DefaultMaxTokens:   &intPtr,
		RequestPath:        &str,
		IsReasoningModel:   &truePtr,
		AcceptsTemperature: &truePtr,
	}

	bytes, err := json.Marshal(original)
	require.NoError(t, err)

	var roundTripped BifrostOverrides
	err = json.Unmarshal(bytes, &roundTripped)
	require.NoError(t, err)

	assert.Equal(t, *original.SupportsCachePoint, *roundTripped.SupportsCachePoint)
	assert.Equal(t, *original.SupportsContext1M, *roundTripped.SupportsContext1M)
	assert.Equal(t, original.ServerTools, roundTripped.ServerTools)
	assert.Equal(t, original.BetaHeaders, roundTripped.BetaHeaders)
	assert.Equal(t, original.FieldNames, roundTripped.FieldNames)
	assert.Equal(t, original.ServerToolAutoInjects, roundTripped.ServerToolAutoInjects)
	assert.Equal(t, original.ServerToolImplicitBetas, roundTripped.ServerToolImplicitBetas)
	assert.Equal(t, original.ExtraHeaders, roundTripped.ExtraHeaders)
	assert.Equal(t, original.EffortRenames, roundTripped.EffortRenames)
	assert.Equal(t, original.UnsupportedFields, roundTripped.UnsupportedFields)
	assert.Equal(t, original.ConditionallyUnsupportedFields, roundTripped.ConditionallyUnsupportedFields)
	require.NotNil(t, roundTripped.Reasoning)
	assert.Equal(t, *original.Reasoning.Style, *roundTripped.Reasoning.Style)
	assert.Equal(t, *original.Reasoning.Field, *roundTripped.Reasoning.Field)
	assert.Equal(t, original.Reasoning.EffortLevels, roundTripped.Reasoning.EffortLevels)
	require.NotNil(t, roundTripped.Thinking)
	assert.Equal(t, *original.Thinking.Field, *roundTripped.Thinking.Field)
	assert.Equal(t, *original.Thinking.Min, *roundTripped.Thinking.Min)
	assert.Equal(t, *original.Thinking.Max, *roundTripped.Thinking.Max)
	assert.Equal(t, original.Thinking.SpecialValues, roundTripped.Thinking.SpecialValues)
	assert.Equal(t, *original.DefaultMaxTokens, *roundTripped.DefaultMaxTokens)
	assert.Equal(t, *original.RequestPath, *roundTripped.RequestPath)
	assert.Equal(t, *original.IsReasoningModel, *roundTripped.IsReasoningModel)
	assert.Equal(t, *original.AcceptsTemperature, *roundTripped.AcceptsTemperature)
}

// TestBifrostOverrides_Empty verifies an empty struct serializes to "{}"
// rather than carrying noise into the wire.
func TestBifrostOverrides_Empty(t *testing.T) {
	var empty BifrostOverrides
	bytes, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(bytes))
}

// TestBifrostOverrides_UnsupportedFieldsParsing verifies the
// `unsupported_fields` map round-trips with explicit boolean values, since
// the legacy `accepts_*` booleans are still on the struct alongside it.
func TestBifrostOverrides_UnsupportedFieldsParsing(t *testing.T) {
	wire := `{"unsupported_fields":{"top_p":true,"top_k":true,"temperature":true},"accepts_top_k":false,"accepts_temperature":false}`
	var ov BifrostOverrides
	require.NoError(t, json.Unmarshal([]byte(wire), &ov))
	assert.Equal(t, map[string]bool{"top_p": true, "top_k": true, "temperature": true}, ov.UnsupportedFields)
	require.NotNil(t, ov.AcceptsTopK)
	assert.False(t, *ov.AcceptsTopK)
	require.NotNil(t, ov.AcceptsTemperature)
	assert.False(t, *ov.AcceptsTemperature)
}

// TestBifrostOverrides_AcceptsTopPStringIgnored verifies that the special
// string value `accepts_top_p: "conditional_when_effort_none"` does NOT
// fail unmarshalling. The Go struct has no AcceptsTopP field (the JSON
// decoder ignores unknown fields by default), so the value is silently
// dropped on the Go side; callers should read
// ConditionallyUnsupportedFields["top_p"] for that semantic.
func TestBifrostOverrides_AcceptsTopPStringIgnored(t *testing.T) {
	wire := `{"accepts_top_p":"conditional_when_effort_none","conditionally_unsupported_fields":{"top_p":"when_effort_none"}}`
	var ov BifrostOverrides
	require.NoError(t, json.Unmarshal([]byte(wire), &ov))
	assert.Equal(t, "when_effort_none", ov.ConditionallyUnsupportedFields["top_p"])
}

func ptr(s string) *string { return &s }
func intptr(n int) *int    { return &n }

package datasheet

import (
	"slices"
	"testing"
)

// GetSupportedParameters used to look up only the exact model string, while
// every sibling lookup in this store resolves through modelParameterCandidates.
// That is how bifrost#8091 happened: the datasheet carries a bare
// "us.anthropic.claude-opus-5-5" row but no bare
// "us.anthropic.claude-sonnet-5-5", so the Bedrock sonnet id resolved to
// nothing, the compat plugin logged "no supported-parameter list" and dropped
// nothing, and Bedrock rejected the request for sending a temperature that
// Sonnet 5.5 no longer accepts. Opus worked only because its exact key exists.
//
// Every sonnet-5-5 row in the sheet omits "temperature", so resolving to any of
// them is what lets the parameter be dropped.
func TestGetSupportedParameters_ResolvesBedrockInferenceProfileID(t *testing.T) {
	s := NewTestStore(nil)
	// The shape the live sheet has: a base row, and no row for the Bedrock
	// region-prefixed spelling the caller actually sends.
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens", "stop_sequences", "stream"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")

	if got == nil {
		t.Fatal("no supported-parameter list for a Bedrock inference-profile id whose base model has a row")
	}
	if slices.Contains(got, "temperature") {
		t.Errorf("temperature reported as supported; got %v", got)
	}
	if !slices.Contains(got, "max_tokens") {
		t.Errorf("resolved to the wrong row; got %v", got)
	}
}

// Other region and vendor prefixes are the same shape, and must resolve too.
func TestGetSupportedParameters_ResolvesEveryVendorPrefix(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens"},
	})

	for _, model := range []string{
		"us.anthropic.claude-sonnet-5-5",
		"eu.anthropic.claude-sonnet-5-5",
		"apac.anthropic.claude-sonnet-5-5",
		"global.anthropic.claude-sonnet-5-5",
		"anthropic.claude-sonnet-5-5",
	} {
		if got := s.GetSupportedParameters(model); got == nil {
			t.Errorf("%s resolved to nothing", model)
		}
	}
}

// An exact row still wins, so a model that resolves today keeps resolving to
// the same list rather than to its base model's.
func TestGetSupportedParameters_PrefersTheExactRow(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"us.anthropic.claude-opus-5-5": {"max_tokens"},
		"claude-opus-5-5":              {"max_tokens", "temperature", "top_p"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-opus-5-5")

	if !slices.Equal(got, []string{"max_tokens"}) {
		t.Errorf("exact row not preferred; got %v", got)
	}
}

// A model the sheet says nothing about must still report nothing, so the compat
// plugin keeps passing its parameters through rather than dropping them all.
func TestGetSupportedParameters_UnknownModelStillReportsNothing(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens"},
	})

	if got := s.GetSupportedParameters("some-model-nobody-has-heard-of"); got != nil {
		t.Errorf("unknown model resolved to %v", got)
	}
}

// A digit-dotted name must not be mistaken for a vendor-prefixed one: stripping
// "gpt-3." would resolve gpt-3.5-turbo to an unrelated row.
func TestGetSupportedParameters_DoesNotStripADigitDottedName(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"gpt-3.5-turbo": {"max_tokens", "temperature"},
		"5-turbo":       {"stream"},
	})

	got := s.GetSupportedParameters("gpt-3.5-turbo")

	if !slices.Equal(got, []string{"max_tokens", "temperature"}) {
		t.Errorf("digit-dotted name mis-resolved; got %v", got)
	}
}

// The returned slice is a copy, as it was before: a caller mutating it must not
// corrupt the store for the next request.
func TestGetSupportedParameters_ReturnsACopy(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens", "stream"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")
	if len(got) == 0 {
		t.Fatal("nothing resolved")
	}
	got[0] = "mutated"

	again := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")
	if slices.Contains(again, "mutated") {
		t.Error("the store handed out its own slice")
	}
}

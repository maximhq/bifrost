package datasheet

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression test for issue #6276: a datasheet row that declares ONLY pricing (no
// capability flags, no model_parameters) must not produce a supported-parameters
// allowlist. The default "reasoning_with_tool_calls" marker used to be added even to
// capability-empty rows, turning them into a one-element allowlist; the compat plugin
// (should_drop_params) then stripped tools / tool_choice / temperature / everything
// else from requests to that model.
func TestCostOnlyRowProducesNoParamAllowlist(t *testing.T) {
	// Hosted-datasheet shape reported in the issue: pricing keys only, none of
	// which map to ModelCapabilities fields. The issue's original row
	// (gemini-3.6-flash) has since been fixed upstream; claude-opus-4-7-20260416
	// ({"deprecation_date": "2027-04-16"}) still reproduces on the live feed and
	// parses to the same empty ModelCapabilities.
	costOnlyRow := json.RawMessage(`{
		"input_cost_per_token_batches": 0.000000075,
		"output_cost_per_token_batches": 0.0000003
	}`)

	t.Run("extractSupportedParams on a capability-empty row", func(t *testing.T) {
		var caps schemas.ModelCapabilities
		if err := json.Unmarshal(costOnlyRow, &caps); err != nil {
			t.Fatalf("unmarshal cost-only row: %v", err)
		}
		if !IsEmptyModelCapabilities(&caps) {
			t.Fatalf("precondition: cost-only row should parse to empty ModelCapabilities")
		}
		if got := extractSupportedParams(&caps); len(got) != 0 {
			t.Errorf("extractSupportedParams(cost-only row) = %v, want empty — a row declaring no request parameters must not synthesize an allowlist", got)
		}
	})

	t.Run("applyModelParameters end-to-end", func(t *testing.T) {
		s := &Store{}
		// A second, genuinely populated row keeps applied > 0 so the index swap
		// happens, mirroring the real datasheet where thousands of populated
		// rows sit alongside the cost-only gemini-3.6-flash row.
		s.applyModelParameters(map[string]json.RawMessage{
			"claude-opus-4-7-20260416": costOnlyRow,
			"gpt-4o":                   json.RawMessage(`{"supports_function_calling":true,"supports_tool_choice":true}`),
		})

		if got := s.GetSupportedParameters("claude-opus-4-7-20260416"); got != nil {
			t.Errorf("GetSupportedParameters(claude-opus-4-7-20260416) = %v, want nil (unknown) — compat treats any non-nil list as a complete allowlist and drops tools/tool_choice", got)
		}
	})

	t.Run("explicit-false-only row keeps an authoritative allowlist", func(t *testing.T) {
		// A row saying only {"supports_function_calling": false} IS a statement
		// about the parameter surface: it must keep a non-nil allowlist (with the
		// default marker) so compat still drops tools, instead of degrading to
		// "unknown, do not drop".
		var caps schemas.ModelCapabilities
		if err := json.Unmarshal(json.RawMessage(`{"supports_function_calling":false}`), &caps); err != nil {
			t.Fatalf("unmarshal explicit-false-only row: %v", err)
		}
		got := extractSupportedParams(&caps)
		if len(got) == 0 {
			t.Fatalf("extractSupportedParams(explicit-false-only row) = empty, want the default reasoning_with_tool_calls marker so the allowlist stays authoritative")
		}
		if !slices.Contains(got, "reasoning_with_tool_calls") {
			t.Errorf("extractSupportedParams(explicit-false-only row) = %v, want reasoning_with_tool_calls present", got)
		}
		if slices.Contains(got, "tools") {
			t.Errorf("extractSupportedParams(explicit-false-only row) = %v, want tools absent", got)
		}
	})

	t.Run("populated row keeps the default marker", func(t *testing.T) {
		// Backward-compat contract from #4630: rows declaring real capabilities but
		// lacking the reasoning_with_tool_calls flag still get the default marker.
		var caps schemas.ModelCapabilities
		if err := json.Unmarshal(json.RawMessage(`{"supports_function_calling":true}`), &caps); err != nil {
			t.Fatalf("unmarshal populated row: %v", err)
		}
		got := extractSupportedParams(&caps)
		if !slices.Contains(got, "reasoning_with_tool_calls") {
			t.Errorf("extractSupportedParams(populated row) = %v, want it to include the default reasoning_with_tool_calls marker", got)
		}
		if !slices.Contains(got, "tools") {
			t.Errorf("extractSupportedParams(populated row) = %v, want it to include tools", got)
		}
	})

	t.Run("explicit flag is honored regardless of other capabilities", func(t *testing.T) {
		var explicitTrue schemas.ModelCapabilities
		if err := json.Unmarshal(json.RawMessage(`{"supports_reasoning_with_tool_calls":true}`), &explicitTrue); err != nil {
			t.Fatalf("unmarshal explicit-true row: %v", err)
		}
		if got := extractSupportedParams(&explicitTrue); !slices.Contains(got, "reasoning_with_tool_calls") {
			t.Errorf("extractSupportedParams(explicit true) = %v, want reasoning_with_tool_calls present", got)
		}

		var explicitFalse schemas.ModelCapabilities
		if err := json.Unmarshal(json.RawMessage(`{"supports_function_calling":true,"supports_reasoning_with_tool_calls":false}`), &explicitFalse); err != nil {
			t.Fatalf("unmarshal explicit-false row: %v", err)
		}
		if got := extractSupportedParams(&explicitFalse); slices.Contains(got, "reasoning_with_tool_calls") {
			t.Errorf("extractSupportedParams(explicit false) = %v, want reasoning_with_tool_calls absent", got)
		}

		// Explicit false must also override a marker sourced from model_parameters ids.
		var falseWithParamID schemas.ModelCapabilities
		if err := json.Unmarshal(json.RawMessage(`{"model_parameters":[{"id":"reasoning_with_tool_calls"}],"supports_reasoning_with_tool_calls":false}`), &falseWithParamID); err != nil {
			t.Fatalf("unmarshal explicit-false row with param id: %v", err)
		}
		if got := extractSupportedParams(&falseWithParamID); slices.Contains(got, "reasoning_with_tool_calls") {
			t.Errorf("extractSupportedParams(explicit false + model_parameters id) = %v, want reasoning_with_tool_calls absent", got)
		}
	})
}

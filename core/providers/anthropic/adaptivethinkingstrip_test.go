package anthropic

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Adaptive-only models (Opus 4.7+, Opus 5, Sonnet 5, Fable/Mythos 5) removed
// extended thinking. A request carrying thinking:{type:"enabled",budget_tokens:N}
// is rejected upstream with:
//
//	"thinking.type.enabled" is not supported for this model. Use
//	"thinking.type.adaptive" and "output_config.effort" to control thinking behavior.
//
// The converted (Chat/Responses) paths already emit "adaptive" for these models,
// but the passthrough sanitizers forward the caller's legacy shape verbatim, so an
// Anthropic-native client (Claude Code, the Anthropic SDK) 400s through Bifrost.
//
// Source: https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
// ("Configurations each model rejects" table: Opus 4.7, Opus 4.8, Opus 5,
// Sonnet 5, Fable 5, Mythos 5 all reject "enabled" with a 400.)
func TestAdaptiveOnlyThinkingStrip(t *testing.T) {
	// Models that reject "enabled" and must be rewritten to "adaptive".
	adaptiveOnly := []string{
		"claude-opus-5",
		"claude-opus-5-20260115",
		"claude-sonnet-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-fable-5",
		"claude-mythos-5",
	}

	// Models that still accept the legacy shape and must be left untouched.
	legacyOK := []string{
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-opus-4-5",
		"claude-haiku-4-5",
		"claude-sonnet-4-5",
	}

	t.Run("raw_body_rewrites_enabled_to_adaptive", func(t *testing.T) {
		for _, model := range adaptiveOnly {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":10000}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "adaptive" {
				t.Errorf("%s: thinking.type = %q, want \"adaptive\" (upstream rejects \"enabled\"); body: %s",
					model, got, string(result))
			}
			if providerUtils.JSONFieldExists(result, "thinking.budget_tokens") {
				t.Errorf("%s: budget_tokens survived the strip; body: %s", model, string(result))
			}
		}
	})

	t.Run("raw_body_preserves_legacy_shape_on_older_models", func(t *testing.T) {
		for _, model := range legacyOK {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":10000}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "enabled" {
				t.Errorf("%s: thinking.type = %q, want \"enabled\" preserved; body: %s",
					model, got, string(result))
			}
			if got := providerUtils.GetJSONField(result, "thinking.budget_tokens").Int(); got != 10000 {
				t.Errorf("%s: budget_tokens = %d, want 10000 preserved; body: %s",
					model, got, string(result))
			}
		}
	})

	t.Run("typed_request_rewrites_enabled_to_adaptive", func(t *testing.T) {
		for _, model := range adaptiveOnly {
			req := &AnthropicMessageRequest{
				Model:     model,
				MaxTokens: 4096,
				Thinking:  &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(10000)},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

			if req.Thinking == nil {
				t.Fatalf("%s: thinking was dropped entirely, want type \"adaptive\"", model)
			}
			if req.Thinking.Type != "adaptive" {
				t.Errorf("%s: thinking.Type = %q, want \"adaptive\"", model, req.Thinking.Type)
			}
			if req.Thinking.BudgetTokens != nil {
				t.Errorf("%s: BudgetTokens = %d, want nil", model, *req.Thinking.BudgetTokens)
			}
		}
	})

	t.Run("typed_request_preserves_legacy_shape_on_older_models", func(t *testing.T) {
		for _, model := range legacyOK {
			req := &AnthropicMessageRequest{
				Model:     model,
				MaxTokens: 4096,
				Thinking:  &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(10000)},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

			if req.Thinking == nil || req.Thinking.Type != "enabled" {
				t.Errorf("%s: expected \"enabled\" preserved, got %+v", model, req.Thinking)
				continue
			}
			if req.Thinking.BudgetTokens == nil || *req.Thinking.BudgetTokens != 10000 {
				t.Errorf("%s: expected budget_tokens 10000 preserved, got %+v", model, req.Thinking.BudgetTokens)
			}
		}
	})

	// budget_tokens is the extended-thinking lever and has no meaning under
	// adaptive thinking, where effort is the knob instead ("effort is the primary
	// thinking lever only in adaptive mode"). A caller that already sends
	// {type:"adaptive",budget_tokens:N} therefore gets no benefit from the field,
	// while it still participates in the cached prompt prefix - "changing
	// budget_tokens" invalidates message cache breakpoints. The enabled ->
	// adaptive rewrite above already drops it; a request that arrives adaptive
	// must land in the same shape, otherwise the same conversation cache-misses
	// depending on which shape the client happened to send.
	//
	// Source: https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
	// ("Setting effort does not change thinking" and "Cache hits drop after
	// changing thinking settings".)
	t.Run("raw_body_strips_budget_tokens_when_already_adaptive", func(t *testing.T) {
		for _, model := range adaptiveOnly {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"adaptive","budget_tokens":10000}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "adaptive" {
				t.Errorf("%s: thinking.type = %q, want \"adaptive\" preserved; body: %s",
					model, got, string(result))
			}
			if providerUtils.JSONFieldExists(result, "thinking.budget_tokens") {
				t.Errorf("%s: budget_tokens survived on an already-adaptive request; body: %s",
					model, string(result))
			}
		}
	})

	t.Run("typed_request_strips_budget_tokens_when_already_adaptive", func(t *testing.T) {
		for _, model := range adaptiveOnly {
			req := &AnthropicMessageRequest{
				Model:     model,
				MaxTokens: 4096,
				Thinking:  &AnthropicThinking{Type: "adaptive", BudgetTokens: schemas.Ptr(10000)},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

			if req.Thinking == nil {
				t.Fatalf("%s: thinking was dropped entirely, want type \"adaptive\"", model)
			}
			if req.Thinking.Type != "adaptive" {
				t.Errorf("%s: thinking.Type = %q, want \"adaptive\" preserved", model, req.Thinking.Type)
			}
			if req.Thinking.BudgetTokens != nil {
				t.Errorf("%s: BudgetTokens = %d on an already-adaptive request, want nil",
					model, *req.Thinking.BudgetTokens)
			}
		}
	})

	// Legacy models are untouched: on Opus 4.5 / Haiku 4.5 / Sonnet 4.5 "adaptive"
	// itself is rejected, and on Opus 4.6 / Sonnet 4.6 both modes are accepted and
	// budget_tokens still works. Either way the sanitizer must not rewrite them.
	t.Run("raw_body_preserves_budget_tokens_on_legacy_models", func(t *testing.T) {
		for _, model := range legacyOK {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"adaptive","budget_tokens":10000}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.budget_tokens").Int(); got != 10000 {
				t.Errorf("%s: budget_tokens = %d, want 10000 preserved; body: %s",
					model, got, string(result))
			}
		}
	})
}

// Always-on models reject thinking:{type:"disabled"} with a 400:
//
//	"thinking.type.disabled" is not supported for this model. Thinking defaults
//	to adaptive mode when not specified; use "thinking.type.enabled" with
//	"budget_tokens" for extended thinking.
//
// Claude Fable 5, Mythos 5 and Mythos Preview reject it unconditionally. Opus 5
// accepts it only at effort "high" or below - combining "disabled" with effort
// "xhigh" or "max" is a 400, enforced per request. Opus 4.7/4.8 and Sonnet 5
// accept "disabled" at any effort and must not be touched.
//
// Like the enabled -> adaptive rewrite, this rewrites rather than deletes: the
// caller may have set thinking.display alongside, and Type has no omitempty so
// a display-only block would serialize as {"type":""}. On these models
// "adaptive" is what omitting the parameter already resolves to.
//
// Source: https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
// ("Configurations each model rejects" table and "A 400 error says
// \"thinking.type.disabled\" is not supported".)
func TestDisabledThinkingStrip(t *testing.T) {
	// Models that reject "disabled" regardless of effort.
	alwaysOn := []string{"claude-fable-5", "claude-mythos-5", "claude-mythos-preview"}

	// Models that accept "disabled" at any effort.
	disabledOK := []string{"claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5", "claude-opus-4-6", "claude-sonnet-4-5"}

	t.Run("typed_request_rewrites_disabled_on_always_on_models", func(t *testing.T) {
		for _, model := range alwaysOn {
			req := &AnthropicMessageRequest{
				Model:     model,
				MaxTokens: 4096,
				Thinking:  &AnthropicThinking{Type: "disabled"},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

			if req.Thinking != nil && req.Thinking.Type == "disabled" {
				t.Errorf("%s: thinking.Type = \"disabled\" survived; upstream rejects it with a 400", model)
			}
		}
	})

	t.Run("typed_request_preserves_display_when_rewriting_disabled", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model:     "claude-fable-5",
			MaxTokens: 4096,
			Thinking:  &AnthropicThinking{Type: "disabled", Display: schemas.Ptr("omitted")},
		}

		stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-fable-5")

		if req.Thinking == nil {
			t.Fatalf("thinking dropped entirely; display:\"omitted\" was the caller's way to hide thinking text")
		}
		if req.Thinking.Type != "adaptive" {
			t.Errorf("thinking.Type = %q, want \"adaptive\"", req.Thinking.Type)
		}
		if req.Thinking.Display == nil || *req.Thinking.Display != "omitted" {
			t.Errorf("display = %v, want \"omitted\" preserved", req.Thinking.Display)
		}
	})

	t.Run("typed_request_rewrites_disabled_on_opus5_at_high_effort", func(t *testing.T) {
		for _, effort := range []string{"xhigh", "max"} {
			req := &AnthropicMessageRequest{
				Model:        "claude-opus-5",
				MaxTokens:    4096,
				Thinking:     &AnthropicThinking{Type: "disabled"},
				OutputConfig: &AnthropicOutputConfig{Effort: schemas.Ptr(effort)},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-opus-5")

			if req.Thinking != nil && req.Thinking.Type == "disabled" {
				t.Errorf("effort %q: thinking.Type = \"disabled\" survived; Opus 5 rejects it above effort \"high\"", effort)
			}
			if req.OutputConfig == nil || req.OutputConfig.Effort == nil || *req.OutputConfig.Effort != effort {
				t.Errorf("effort %q: the explicitly requested effort must be preserved, got %+v", effort, req.OutputConfig)
			}
		}
	})

	t.Run("typed_request_preserves_disabled_on_opus5_at_low_effort", func(t *testing.T) {
		for _, effort := range []string{"low", "medium", "high"} {
			req := &AnthropicMessageRequest{
				Model:        "claude-opus-5",
				MaxTokens:    4096,
				Thinking:     &AnthropicThinking{Type: "disabled"},
				OutputConfig: &AnthropicOutputConfig{Effort: schemas.Ptr(effort)},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-opus-5")

			if req.Thinking == nil || req.Thinking.Type != "disabled" {
				t.Errorf("effort %q: Opus 5 accepts \"disabled\" at effort \"high\" or below, got %+v", effort, req.Thinking)
			}
		}
	})

	t.Run("typed_request_preserves_disabled_when_no_effort_set", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model:     "claude-opus-5",
			MaxTokens: 4096,
			Thinking:  &AnthropicThinking{Type: "disabled"},
		}

		stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-opus-5")

		if req.Thinking == nil || req.Thinking.Type != "disabled" {
			t.Errorf("no effort set means the default (below xhigh), so \"disabled\" is accepted; got %+v", req.Thinking)
		}
	})

	t.Run("typed_request_preserves_disabled_on_accepting_models", func(t *testing.T) {
		for _, model := range disabledOK {
			req := &AnthropicMessageRequest{
				Model:        model,
				MaxTokens:    4096,
				Thinking:     &AnthropicThinking{Type: "disabled"},
				OutputConfig: &AnthropicOutputConfig{Effort: schemas.Ptr("xhigh")},
			}

			stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

			if req.Thinking == nil || req.Thinking.Type != "disabled" {
				t.Errorf("%s: accepts \"disabled\" at any effort, got %+v", model, req.Thinking)
			}
		}
	})

	t.Run("raw_body_rewrites_disabled_on_always_on_models", func(t *testing.T) {
		for _, model := range alwaysOn {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"disabled"}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got == "disabled" {
				t.Errorf("%s: thinking.type = \"disabled\" survived; body: %s", model, string(result))
			}
		}
	})

	t.Run("raw_body_rewrites_disabled_on_opus5_at_high_effort", func(t *testing.T) {
		for _, effort := range []string{"xhigh", "max"} {
			body := []byte(`{"model":"claude-opus-5","max_tokens":4096,"output_config":{"effort":"` + effort +
				`"},"thinking":{"type":"disabled"}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, "claude-opus-5")
			if err != nil {
				t.Fatalf("effort %q: unexpected error: %v", effort, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got == "disabled" {
				t.Errorf("effort %q: thinking.type = \"disabled\" survived; body: %s", effort, string(result))
			}
			if got := providerUtils.GetJSONField(result, "output_config.effort").String(); got != effort {
				t.Errorf("effort %q: effort must be preserved, got %q; body: %s", effort, got, string(result))
			}
		}
	})

	t.Run("raw_body_preserves_disabled_on_opus5_at_low_effort", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5","max_tokens":4096,"output_config":{"effort":"high"},"thinking":{"type":"disabled"}}`)

		result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, "claude-opus-5")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "disabled" {
			t.Errorf("thinking.type = %q, want \"disabled\" preserved at effort \"high\"; body: %s", got, string(result))
		}
	})

	t.Run("raw_body_preserves_disabled_on_accepting_models", func(t *testing.T) {
		for _, model := range disabledOK {
			body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"disabled"}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", model, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "disabled" {
				t.Errorf("%s: thinking.type = %q, want \"disabled\" preserved; body: %s", model, got, string(result))
			}
		}
	})
}

// Claude Mythos Preview is the one Fable/Mythos model that still supports
// extended thinking: the rejection table lists it as "Adaptive, extended" and
// rejects only "disabled", where Fable 5 and Mythos 5 reject "enabled" too.
//
// The passthrough sanitizer must therefore leave its {type:"enabled",
// budget_tokens:N} alone. Rewriting it would not 400 - Mythos Preview accepts
// adaptive as well - but passthrough forwards a native caller's explicit choice
// and only rewrites what upstream would actually reject, so silently switching
// thinking modes is a behavior change the caller did not ask for.
//
// This is deliberately scoped to the thinking gate. IsAdaptiveOnlyThinkingModel
// also gates the temperature/top_p/top_k strip (chat.go:296-320), and nothing
// documents Mythos Preview accepting those, so that predicate is left as is.
//
// Source: https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
// ("Configurations each model rejects": Claude Mythos Preview | Adaptive,
// extended | Always on | rejected: "disabled".)
func TestMythosPreviewKeepsExtendedThinking(t *testing.T) {
	const model = "claude-mythos-preview"

	t.Run("raw_body_preserves_enabled_and_budget_tokens", func(t *testing.T) {
		body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":10000}}`)

		result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "enabled" {
			t.Errorf("thinking.type = %q, want \"enabled\" preserved (Mythos Preview supports extended thinking); body: %s",
				got, string(result))
		}
		if got := providerUtils.GetJSONField(result, "thinking.budget_tokens").Int(); got != 10000 {
			t.Errorf("budget_tokens = %d, want 10000 preserved; body: %s", got, string(result))
		}
	})

	t.Run("typed_request_preserves_enabled_and_budget_tokens", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model:     model,
			MaxTokens: 4096,
			Thinking:  &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(10000)},
		}

		stripUnsupportedAnthropicFields(req, schemas.Anthropic, model)

		if req.Thinking == nil {
			t.Fatalf("thinking dropped entirely, want \"enabled\" preserved")
		}
		if req.Thinking.Type != "enabled" {
			t.Errorf("thinking.Type = %q, want \"enabled\" preserved", req.Thinking.Type)
		}
		if req.Thinking.BudgetTokens == nil || *req.Thinking.BudgetTokens != 10000 {
			t.Errorf("BudgetTokens = %v, want 10000 preserved", req.Thinking.BudgetTokens)
		}
	})

	// Adaptive is still the mode where budget_tokens is inert, so a caller that
	// opts into adaptive on Mythos Preview gets the same shape convergence as
	// every other adaptive-capable model.
	t.Run("raw_body_still_strips_budget_tokens_when_adaptive", func(t *testing.T) {
		body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"adaptive","budget_tokens":10000}}`)

		result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "thinking.budget_tokens") {
			t.Errorf("budget_tokens survived an adaptive request; body: %s", string(result))
		}
	})

	// Mythos Preview is always-on, so "disabled" is still rejected upstream and
	// must still be rewritten - the carve-out above is only about "enabled".
	t.Run("raw_body_still_rewrites_disabled", func(t *testing.T) {
		body := []byte(`{"model":"` + model + `","max_tokens":4096,"thinking":{"type":"disabled"}}`)

		result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, model)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := providerUtils.GetJSONField(result, "thinking.type").String(); got == "disabled" {
			t.Errorf("thinking.type = \"disabled\" survived; body: %s", string(result))
		}
	})

	// Fable 5 and Mythos 5 must keep rejecting "enabled" - the carve-out is
	// Mythos Preview only, not the whole family.
	t.Run("siblings_still_rewrite_enabled", func(t *testing.T) {
		for _, sibling := range []string{"claude-fable-5", "claude-mythos-5"} {
			body := []byte(`{"model":"` + sibling + `","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":10000}}`)

			result, err := StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, sibling)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", sibling, err)
			}

			if got := providerUtils.GetJSONField(result, "thinking.type").String(); got != "adaptive" {
				t.Errorf("%s: thinking.type = %q, want \"adaptive\"; body: %s", sibling, got, string(result))
			}
		}
	})
}

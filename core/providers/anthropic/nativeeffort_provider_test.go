package anthropic

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// The NativeEffort provider feature is the provider-wide fallback for
// ModelCaps.SupportsNativeEffort. It must widen the answer only for providers
// that grant it, and leave every other provider on the Claude model ladder.
func TestDefaultSupportsNativeEffort_ProviderGrant(t *testing.T) {
	cases := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		// DeepSeek's Anthropic-compatible surface accepts output_config.effort on every model it serves.
		{schemas.DeepSeek, "deepseek-v4-flash", true},
		{schemas.DeepSeek, "deepseek-v4-pro", true},
		{schemas.DeepSeek, "some-future-deepseek-model", true},
		// Anthropic itself stays on the model ladder.
		{schemas.Anthropic, "claude-opus-4-5", true},
		{schemas.Anthropic, "claude-3-5-haiku-latest", false},
		// A Claude-shaped model name on a provider without the grant is unchanged.
		{schemas.Vertex, "claude-3-5-haiku@20241022", false},
	}
	for _, tc := range cases {
		caps := schemas.ResolveModelCaps(tc.provider, tc.model)
		if got := defaultSupportsNativeEffort(caps); got != tc.want {
			t.Errorf("defaultSupportsNativeEffort(%s, %s) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// The typed strip pass must keep output_config.effort on a provider that
// grants NativeEffort and still remove it where the model ladder says no.
func TestStripUnsupportedAnthropicFields_NativeEffortProviderGrant(t *testing.T) {
	effort := "medium"
	keep := &AnthropicMessageRequest{Model: "deepseek-v4-flash", OutputConfig: &AnthropicOutputConfig{Effort: &effort}}
	stripUnsupportedAnthropicFields(keep, schemas.DeepSeek, "deepseek-v4-flash")
	if keep.OutputConfig == nil || keep.OutputConfig.Effort == nil || *keep.OutputConfig.Effort != effort {
		t.Fatalf("DeepSeek: output_config.effort was stripped: %#v", keep.OutputConfig)
	}

	strip := &AnthropicMessageRequest{Model: "claude-3-5-haiku-latest", OutputConfig: &AnthropicOutputConfig{Effort: &effort}}
	stripUnsupportedAnthropicFields(strip, schemas.Anthropic, "claude-3-5-haiku-latest")
	if strip.OutputConfig != nil {
		t.Fatalf("Anthropic haiku: output_config.effort survived the strip: %#v", strip.OutputConfig)
	}
}

// Same contract on the raw-body path.
func TestStripUnsupportedFieldsFromRawBody_NativeEffortProviderGrant(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash","max_tokens":64,"output_config":{"effort":"low"},"messages":[]}`)
	out, err := StripUnsupportedFieldsFromRawBody(body, schemas.DeepSeek, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("StripUnsupportedFieldsFromRawBody: %v", err)
	}
	if got := providerUtils.GetJSONField(out, "output_config.effort").String(); got != "low" {
		t.Fatalf("DeepSeek raw: output_config.effort = %q, want low; body=%s", got, out)
	}

	body = []byte(`{"model":"claude-3-5-haiku-latest","max_tokens":64,"output_config":{"effort":"low"},"messages":[]}`)
	out, err = StripUnsupportedFieldsFromRawBody(body, schemas.Anthropic, "claude-3-5-haiku-latest")
	if err != nil {
		t.Fatalf("StripUnsupportedFieldsFromRawBody: %v", err)
	}
	if providerUtils.JSONFieldExists(out, "output_config.effort") {
		t.Fatalf("Anthropic haiku raw: output_config.effort survived the strip; body=%s", out)
	}
}

package anthropic

import (
	"encoding/json"
	"testing"
)

// An Anthropic output_config.format schema may leave properties optional, so
// the converted Responses text.format must not inherit OpenAI's strict=true
// default (which would require every property to be listed in required).
func TestConvertAnthropicOutputFormatSetsStrictFalse(t *testing.T) {
	raw := json.RawMessage(`{"type":"json_schema","schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"additionalProperties":false}}`)

	cfg := convertAnthropicOutputFormatToResponsesTextConfig(raw)
	if cfg == nil || cfg.Format == nil {
		t.Fatalf("expected a text config with a format, got %+v", cfg)
	}
	if cfg.Format.Strict == nil || *cfg.Format.Strict {
		t.Fatalf("expected strict=false, got %v", cfg.Format.Strict)
	}

	out, err := json.Marshal(cfg.Format)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := m["strict"]; !ok || v != false {
		t.Fatalf("expected serialized strict=false, got %s", out)
	}
}

func TestConvertAnthropicOutputFormatNonSchemaUnchanged(t *testing.T) {
	if cfg := convertAnthropicOutputFormatToResponsesTextConfig(json.RawMessage(`{"type":"text"}`)); cfg != nil {
		t.Fatalf("expected nil for non json_schema format, got %+v", cfg)
	}
}

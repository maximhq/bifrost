package schemas

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSpanAttributesPreserveTokenMeasurementPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
		want map[string]int
	}{
		{"empty", `{}`, map[string]int{}},
		{"input_only", `{"prompt_tokens":7}`, map[string]int{AttrInputTokens: 7}},
		{"output_only", `{"completion_tokens":3}`, map[string]int{AttrOutputTokens: 3}},
		{"total_only", `{"total_tokens":10}`, map[string]int{AttrTotalTokens: 10}},
		{"explicit_zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, map[string]int{AttrInputTokens: 0, AttrOutputTokens: 0, AttrTotalTokens: 0}},
		{"complete", `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`, map[string]int{AttrInputTokens: 7, AttrOutputTokens: 3, AttrTotalTokens: 10}},
		{"programmatic_zero", "", map[string]int{AttrInputTokens: 0, AttrOutputTokens: 0, AttrTotalTokens: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage BifrostLLMUsage
			if tc.wire != "" {
				if err := json.Unmarshal([]byte(tc.wire), &usage); err != nil {
					t.Fatal(err)
				}
			}
			d := &LLMSpanData{Usage: &usage}
			for name, attrs := range map[string]map[string]any{"full": d.Attributes(), "response": d.ResponseAttributes()} {
				got := map[string]int{}
				for _, key := range []string{AttrInputTokens, AttrOutputTokens, AttrTotalTokens} {
					if value, ok := attrs[key]; ok {
						got[key] = value.(int)
					}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s token attributes = %v; want observed counters %v", name, got, tc.want)
				}
			}
		})
	}
}

func BenchmarkSpanTokenMeasurementAttributes(b *testing.B) {
	usage := &BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}
	data := &LLMSpanData{Usage: usage}
	b.ReportAllocs()
	for b.Loop() {
		_ = data.ResponseAttributes()
	}
}

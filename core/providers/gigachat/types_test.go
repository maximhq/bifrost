package gigachat

import (
	"encoding/json"
	"testing"
)

func TestGigaChatResponsesToolMarshalBuiltInTypes(t *testing.T) {
	t.Parallel()

	stringPtr := func(value string) *string {
		return &value
	}

	tests := []struct {
		name string
		tool GigaChatResponsesTool
		want string
	}{
		{
			name: "CodeInterpreter",
			tool: GigaChatResponsesTool{
				CodeInterpreter: map[string]interface{}{"max_execution_time": 60},
			},
			want: `{"code_interpreter":{"max_execution_time":60}}`,
		},
		{
			name: "ImageGenerate",
			tool: GigaChatResponsesTool{
				ImageGenerate: map[string]interface{}{"model": "Kandinsky", "width": 1024},
			},
			want: `{"image_generate":{"model":"Kandinsky","width":1024}}`,
		},
		{
			name: "ImageGenerateEmpty",
			tool: GigaChatResponsesTool{
				ImageGenerate: map[string]interface{}{},
			},
			want: `{"image_generate":{}}`,
		},
		{
			name: "WebSearch",
			tool: GigaChatResponsesTool{
				WebSearch: &GigaChatResponsesWebSearchTool{
					Type:    stringPtr("search_plus"),
					Indexes: []string{"public"},
					Flags:   []string{"exact"},
				},
			},
			want: `{"web_search":{"type":"search_plus","indexes":["public"],"flags":["exact"]}}`,
		},
		{
			name: "URLContentExtraction",
			tool: GigaChatResponsesTool{
				URLContentExtraction: map[string]interface{}{"mode": "summary"},
			},
			want: `{"url_content_extraction":{"mode":"summary"}}`,
		},
		{
			name: "Model3DGenerate",
			tool: GigaChatResponsesTool{
				Model3DGenerate: map[string]interface{}{"format": "glb"},
			},
			want: `{"model_3d_generate":{"format":"glb"}}`,
		},
		{
			name: "Functions",
			tool: GigaChatResponsesTool{
				Functions: &GigaChatResponsesFunctionsTool{
					Specifications: []GigaChatResponsesFunctionSpecification{{
						Name:       "get_weather",
						Parameters: mustGigaChatToolParameters(t, `{"type":"object","properties":{}}`),
					}},
				},
			},
			want: `{"functions":{"specifications":[{"name":"get_weather","parameters":{"type":"object","properties":{}}}]}}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(test.tool)
			if err != nil {
				t.Fatalf("failed to marshal tool: %v", err)
			}
			if string(raw) != test.want {
				t.Fatalf("tool JSON mismatch:\n got: %s\nwant: %s", raw, test.want)
			}
		})
	}
}

package openai

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func TestCodexResponsesLiteAdditionalToolsRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.6-sol",
		"input": [
			{
				"type": "additional_tools",
				"role": "developer",
				"tools": [
					{
						"type": "custom",
						"name": "exec",
						"description": "Run JavaScript code",
						"format": {
							"type": "grammar",
							"syntax": "lark",
							"definition": "start: SOURCE"
						}
					},
					{
						"type": "namespace",
						"name": "collaboration",
						"description": "Tools for managing sub-agents.",
						"tools": [
							{
								"type": "function",
								"name": "spawn_agent",
								"description": "Spawn an agent.",
								"strict": false,
								"parameters": {
									"type": "object",
									"properties": {
										"task_name": {"type": "string"}
									},
									"required": ["task_name"],
									"additionalProperties": false
								}
							}
						]
					}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Reply with exactly: OK"}
				]
			}
		]
	}`)

	var incoming OpenAIResponsesRequest
	if err := json.Unmarshal(raw, &incoming); err != nil {
		t.Fatalf("unmarshal Codex request: %v", err)
	}

	bifrostRequest := incoming.ToBifrostResponsesRequest(nil)
	for i, item := range bifrostRequest.Input {
		bifrostRequest.Input[i] = schemas.DeepCopyResponsesMessage(item)
	}
	outgoing := ToOpenAIResponsesRequest(nil, bifrostRequest)
	encoded, err := json.Marshal(outgoing)
	if err != nil {
		t.Fatalf("marshal forwarded request: %v", err)
	}

	if got := gjson.GetBytes(encoded, "input.0.type").String(); got != "additional_tools" {
		t.Fatalf("expected additional_tools input item, got %q", got)
	}
	if got := gjson.GetBytes(encoded, "input.0.tools.0.type").String(); got != "custom" {
		t.Fatalf("expected custom tool type to survive, got %q; request: %s", got, encoded)
	}
	if got := gjson.GetBytes(encoded, "input.0.tools.0.format.syntax").String(); got != "lark" {
		t.Fatalf("expected custom tool format to survive, got %q; request: %s", got, encoded)
	}
	if got := gjson.GetBytes(encoded, "input.0.tools.1.type").String(); got != "namespace" {
		t.Fatalf("expected namespace tool type to survive, got %q; request: %s", got, encoded)
	}
	if got := gjson.GetBytes(encoded, "input.0.tools.1.tools.0.type").String(); got != "function" {
		t.Fatalf("expected nested function tool type to survive, got %q; request: %s", got, encoded)
	}
}

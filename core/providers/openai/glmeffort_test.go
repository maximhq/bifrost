package openai

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Exercise the inbound OpenAI dialect and the outbound shared converter used
// by vLLM, including Hugging Face namespaces and both streaming request shapes.
func TestGLM53FlashReasoningEffortRoundTrip(t *testing.T) {
	for _, model := range []string{"glm-5.3-flash", "zai-org/GLM-5.3-Flash"} {
		for _, stream := range []bool{false, true} {
			for _, effort := range []string{"", "low", "high", "max"} {
				t.Run(model+"/"+effort+"/stream="+map[bool]string{false: "false", true: "true"}[stream], func(t *testing.T) {
					payload := map[string]interface{}{
						"model":       "vllm/" + model,
						"messages":    []map[string]string{{"role": "user", "content": "What is 7 + 5?"}},
						"stream":      stream,
						"max_tokens":  100,
						"temperature": 0.0,
					}
					if effort != "" {
						payload["reasoning_effort"] = effort
					}
					raw, err := json.Marshal(payload)
					require.NoError(t, err)
					var inbound OpenAIChatRequest
					require.NoError(t, sonic.Unmarshal(raw, &inbound))
					ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
					request := inbound.ToBifrostChatRequest(ctx)
					require.Equal(t, schemas.VLLM, request.Provider)
					require.Equal(t, model, request.Model)
					outbound := ToOpenAIChatRequest(ctx, request)
					require.NotNil(t, outbound)
					outbound.Stream = inbound.Stream
					wire, err := json.Marshal(outbound)
					require.NoError(t, err)
					var body map[string]interface{}
					require.NoError(t, json.Unmarshal(wire, &body))
					if effort == "" {
						require.NotContains(t, body, "reasoning_effort")
					} else {
						require.Equal(t, effort, body["reasoning_effort"])
						require.Equal(t, effort, *request.Params.Reasoning.Effort, "conversion must not mutate input")
					}
					require.Equal(t, stream, body["stream"])
					require.Equal(t, 0.0, body["temperature"])
				})
			}
		}
	}
}

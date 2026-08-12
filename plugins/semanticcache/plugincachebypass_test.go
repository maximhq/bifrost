package semanticcache

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

const testCacheBypassKey schemas.BifrostContextKey = "semantic_cache-bypass"

func boolPointer(value bool) *bool {
	return &value
}

func basicCacheableChatRequest() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("cache eligibility", 0, 16),
	}
}

func basicCacheableResponsesRequest() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Model:  "gpt-test",
			Params: &schemas.ResponsesParameters{},
		},
	}
}

func TestPreRequestHook_MarksUnsafeRequestsForFullBypass(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*schemas.BifrostContext) *schemas.BifrostRequest
	}{
		{
			name: "trusted bypass header",
			prepare: func(ctx *schemas.BifrostContext) *schemas.BifrostRequest {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
					"x-edgeai-cache-bypass": "true",
				})
				return basicCacheableChatRequest()
			},
		},
		{
			name: "chat tools",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableChatRequest()
				req.ChatRequest.Params.Tools = []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction}}
				return req
			},
		},
		{
			name: "chat web search",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableChatRequest()
				req.ChatRequest.Params.WebSearchOptions = &schemas.ChatWebSearchOptions{}
				return req
			},
		},
		{
			name: "chat MCP",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableChatRequest()
				req.ChatRequest.Params.MCPServers = []schemas.ChatMCPServer{{}}
				return req
			},
		},
		{
			name: "chat stored response",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableChatRequest()
				req.ChatRequest.Params.Store = boolPointer(true)
				return req
			},
		},
		{
			name: "chat non-text input",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableChatRequest()
				req.ChatRequest.Input[0].Content = &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage}},
				}
				return req
			},
		},
		{
			name: "responses tools",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableResponsesRequest()
				req.ResponsesRequest.Params.Tools = []schemas.ResponsesTool{{}}
				return req
			},
		},
		{
			name: "responses background",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableResponsesRequest()
				req.ResponsesRequest.Params.Background = boolPointer(true)
				return req
			},
		},
		{
			name: "responses stateful continuation",
			prepare: func(_ *schemas.BifrostContext) *schemas.BifrostRequest {
				req := basicCacheableResponsesRequest()
				previous := "resp_previous"
				req.ResponsesRequest.Params.PreviousResponseID = &previous
				return req
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plugin := newTestPlugin(t, newObservableStore())
			ctx := newBaseTestContext()
			req := test.prepare(ctx)

			if err := plugin.PreRequestHook(ctx, req); err != nil {
				t.Fatalf("PreRequestHook failed: %v", err)
			}
			if bypass, _ := ctx.Value(testCacheBypassKey).(bool); !bypass {
				t.Fatal("unsafe request was not marked for cache lookup bypass")
			}
			if noStore, _ := ctx.Value(CacheNoStoreKey).(bool); !noStore {
				t.Fatal("unsafe request was not marked to skip cache writes")
			}
		})
	}
}

func TestPreRequestHook_LeavesPlainTextInferenceCacheable(t *testing.T) {
	for _, req := range []*schemas.BifrostRequest{
		basicCacheableChatRequest(),
		basicCacheableResponsesRequest(),
	} {
		plugin := newTestPlugin(t, newObservableStore())
		ctx := newBaseTestContext()
		if err := plugin.PreRequestHook(ctx, req); err != nil {
			t.Fatalf("PreRequestHook failed: %v", err)
		}
		if bypass, _ := ctx.Value(testCacheBypassKey).(bool); bypass {
			t.Fatal("plain text inference was marked for cache bypass")
		}
	}
}

func TestPreLLMHook_BypassSkipsLookupState(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	plugin.config.DefaultCacheKey = "shared-default"
	ctx := newBaseTestContext()
	ctx.SetValue(testCacheBypassKey, true)

	if _, shortCircuit, err := plugin.PreLLMHook(ctx, basicCacheableChatRequest()); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	} else if shortCircuit != nil {
		t.Fatal("bypassed request returned a cache hit")
	}

	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if state := plugin.getCacheState(requestID); state != nil {
		t.Fatal("bypassed request created cache lookup/write state")
	}
}

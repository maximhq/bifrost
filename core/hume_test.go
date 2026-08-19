package bifrost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type humeModelCatalogStub struct {
	models map[schemas.ModelProvider]map[string]*schemas.Model
}

func (s *humeModelCatalogStub) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	return s.models[provider][model]
}

func (s *humeModelCatalogStub) CalculateRequestCost(_ *schemas.BifrostContext, _ *schemas.BifrostResponse) float64 {
	return 0
}

type humeConstraintTracer struct {
	schemas.NoOpTracer
	logs []schemas.PluginLogEntry
}

func (t *humeConstraintTracer) AttachPluginLogs(_ string, logs []schemas.PluginLogEntry) {
	t.logs = append(t.logs, logs...)
}

type humeConstraintPlugin struct {
	name          string
	recoverError  bool
	suppressError bool
	addTool       *schemas.ChatTool
	recorder      *humeConstraintRecorder
}

type humeConstraintRecorder struct {
	preOrder           []string
	postOrder          []string
	postFinalState     []bool
	sawConstraintError bool
}

func (p *humeConstraintPlugin) GetName() string { return p.name }
func (p *humeConstraintPlugin) Cleanup() error  { return nil }
func (p *humeConstraintPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *humeConstraintPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.recorder.preOrder = append(p.recorder.preOrder, p.name)
	if p.addTool != nil {
		if req.ChatRequest.Params == nil {
			req.ChatRequest.Params = &schemas.ChatParameters{}
		}
		req.ChatRequest.Params.Tools = append(req.ChatRequest.Params.Tools, *p.addTool)
	}
	return req, nil, nil
}
func (p *humeConstraintPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.recorder.postOrder = append(p.recorder.postOrder, p.name)
	p.recorder.postFinalState = append(p.recorder.postFinalState, IsFinalChunk(ctx))
	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		p.recorder.sawConstraintError = true
	}
	ctx.Log(schemas.LogLevelInfo, "handled serial tool constraint error")
	if p.suppressError {
		return nil, nil, nil
	}
	if !p.recoverError {
		return resp, bifrostErr, nil
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "hume-post-hook-recovery"}}, nil, nil
}

func newHumeConstraintTestClient(t *testing.T, tracer schemas.Tracer, plugins ...schemas.LLMPlugin) *Bifrost {
	t.Helper()
	client := newCatalogProbeClient(t, &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
		schemas.OpenAI: {
			"o3-mini": {SupportedParameters: []string{"tools"}},
		},
	}}, plugins...)
	client.SetTracer(tracer)
	return client
}

func newRejectedHumeConstraintRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "o3-mini",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type: schemas.ChatToolTypeFunction,
		}}},
	}
}

func TestEnforceSerialToolConstraintOnAttempt(t *testing.T) {
	newRequest := func(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionStreamRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    model,
				Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
					Type: schemas.ChatToolTypeFunction,
				}}},
			},
		}
	}

	catalog := &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
		schemas.OpenAI: {
			"gpt-4o-mini": {SupportedParameters: []string{"parallel_tool_calls"}},
			"o3-mini":     {SupportedParameters: []string{"tools"}},
		},
		schemas.Gemini: {
			"gemini-test": {SupportedParameters: []string{"parallel_tool_calls"}},
		},
	}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
	ctx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)

	// withAlias builds a ctx carrying the ResolvedAlias exactly as the worker
	// stamps it on the attempt before the policy runs.
	withAlias := func(ra *schemas.ResolvedAlias) *schemas.BifrostContext {
		aliasCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		aliasCtx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		aliasCtx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)
		aliasCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, ra)
		return aliasCtx
	}

	t.Run("OpenAI model with wire support is constrained", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(ctx, schemas.OpenAI, schemas.OpenAI, "gpt-4o-mini", "gpt-4o-mini", req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Anthropic is translated by its request converter", func(t *testing.T) {
		req := newRequest(schemas.Anthropic, "claude-sonnet-4-5")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(ctx, schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-5", "claude-sonnet-4-5", req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("unsupported OpenAI model is rejected instead of receiving an invalid field", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "o3-mini")
		err := enforceSerialToolConstraintOnAttempt(ctx, schemas.OpenAI, schemas.OpenAI, "o3-mini", "o3-mini", req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee")
		assert.NotContains(t, err.Error.Message, "key alias resolves to")
		assert.Equal(t, schemas.Ptr(400), err.StatusCode)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("native provider without a wire mapping is rejected", func(t *testing.T) {
		req := newRequest(schemas.Gemini, "gemini-test")
		err := enforceSerialToolConstraintOnAttempt(ctx, schemas.Gemini, schemas.Gemini, "gemini-test", "gemini-test", req)
		require.NotNil(t, err)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("provider-bound tools are constrained", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		req.ChatRequest.Params.ParallelToolCalls = nil
		require.Nil(t, enforceSerialToolConstraintOnAttempt(ctx, schemas.OpenAI, schemas.OpenAI, "gpt-4o-mini", "gpt-4o-mini", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("custom OpenAI-compatible provider uses its base provider capability", func(t *testing.T) {
		customProvider := schemas.ModelProvider("custom-openai")
		req := newRequest(customProvider, "gpt-4o-mini")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(ctx, customProvider, schemas.OpenAI, "gpt-4o-mini", "gpt-4o-mini", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("catalog-absent OpenAI-compatible model is constrained", func(t *testing.T) {
		catalogAbsentCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		catalogAbsentCtx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		req := newRequest(schemas.Ollama, "local-model")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(catalogAbsentCtx, schemas.Ollama, schemas.Ollama, "local-model", "local-model", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Vertex all-digit endpoint IDs route to the Gemini builder and are rejected", func(t *testing.T) {
		req := newRequest(schemas.Vertex, "1234567890")
		err := enforceSerialToolConstraintOnAttempt(ctx, schemas.Vertex, schemas.Vertex, "1234567890", "1234567890", req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee")
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Vertex non-Gemini names still use the OpenAI wire", func(t *testing.T) {
		req := newRequest(schemas.Vertex, "meta/llama-3.1-405b-instruct-maas")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(ctx, schemas.Vertex, schemas.Vertex, "meta/llama-3.1-405b-instruct-maas", "meta/llama-3.1-405b-instruct-maas", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("key alias to an unsupported model is rejected with the alias detail", func(t *testing.T) {
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "voice", Config: &schemas.AliasConfig{ModelID: "o3-mini"}})
		req := newRequest(schemas.OpenAI, "voice")
		err := enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.OpenAI, schemas.OpenAI, "voice", "o3-mini", req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `model "voice" cannot guarantee serial tool execution (key alias resolves to "o3-mini")`)
		assert.Equal(t, schemas.Ptr(400), err.StatusCode)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("key alias to a supported model is constrained", func(t *testing.T) {
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "fast", Config: &schemas.AliasConfig{ModelID: "gpt-4o-mini"}})
		req := newRequest(schemas.OpenAI, "fast")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.OpenAI, schemas.OpenAI, "fast", "gpt-4o-mini", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("key alias to a Vertex Gemini model is rejected", func(t *testing.T) {
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "voice-fast", Config: &schemas.AliasConfig{ModelID: "gemini-2.5-flash"}})
		req := newRequest(schemas.Vertex, "voice-fast")
		err := enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.Vertex, schemas.Vertex, "voice-fast", "gemini-2.5-flash", req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `key alias resolves to "gemini-2.5-flash"`)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("an unsupported requested name the key aliases away is not validated", func(t *testing.T) {
		// The worker replaced the requested name with the alias target, so
		// "o3-mini" never reaches the provider and its missing
		// parallel_tool_calls support must not reject a request the target
		// model can serve.
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "o3-mini", Config: &schemas.AliasConfig{ModelID: "gpt-4o-mini"}})
		req := newRequest(schemas.OpenAI, "o3-mini")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.OpenAI, schemas.OpenAI, "o3-mini", "gpt-4o-mini", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("alias model_family gemini routes to the Gemini builder and is rejected", func(t *testing.T) {
		// The alias's explicit family tier outranks any substring match on the
		// wire model id: an opaque ModelID with model_family gemini uses the
		// Gemini request builder, which has no parallel_tool_calls field.
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "voice", Config: &schemas.AliasConfig{
			ModelID:     "my-tuned-flash",
			ModelFamily: schemas.Ptr(schemas.ModelFamilyGemini),
		}})
		req := newRequest(schemas.Vertex, "voice")
		err := enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.Vertex, schemas.Vertex, "voice", "my-tuned-flash", req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee serial tool execution")
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("alias model_family anthropic uses the Anthropic wire and is constrained", func(t *testing.T) {
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "voice", Config: &schemas.AliasConfig{
			ModelID:     "arn:aws:bedrock:us-east-1::inference-profile/abc",
			ModelFamily: schemas.Ptr(schemas.ModelFamilyAnthropic),
		}})
		req := newRequest(schemas.Vertex, "voice")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.Vertex, schemas.Vertex, "voice", "arn:aws:bedrock:us-east-1::inference-profile/abc", req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("a fresh nil alias overrides a prior leg's family", func(t *testing.T) {
		// Mimic a fallback attempt: the previous leg stamped an Anthropic
		// alias, then this attempt's worker overwrote it with nil. The policy
		// must classify the fallback model by its own name, not the stale
		// alias family.
		aliasCtx := withAlias(&schemas.ResolvedAlias{Key: "voice", Config: &schemas.AliasConfig{
			ModelID:     "claude-sonnet-4-5",
			ModelFamily: schemas.Ptr(schemas.ModelFamilyAnthropic),
		}})
		aliasCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, nil)
		req := newRequest(schemas.Vertex, "gemini-2.5-flash")
		err := enforceSerialToolConstraintOnAttempt(aliasCtx, schemas.Vertex, schemas.Vertex, "gemini-2.5-flash", "gemini-2.5-flash", req)
		require.NotNil(t, err, "a stale alias family must not let a Gemini-wire model through")
		assert.Contains(t, err.Error.Message, "cannot guarantee serial tool execution")
	})

	t.Run("a cataloged unsupported name the key always aliases away is not validated", func(t *testing.T) {
		// The worker replaces the requested name with the alias target, so "o3-mini"
		// never reaches the provider and its missing parallel_tool_calls support must
		// not reject a request the target model can serve.
		client := newAliasClient(schemas.OpenAI, aliasKey("configured", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"o3-mini": {ModelID: "gpt-4o-mini"}}))
		directCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		directCtx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		directCtx.SetValue(schemas.BifrostContextKeyModelCatalog, ctx.Value(schemas.BifrostContextKeyModelCatalog))
		directCtx.SetValue(schemas.BifrostContextKeyDirectKey, aliasKey("direct", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"o3-mini": {ModelID: "gpt-4o-mini"}}))
		req := newRequest(schemas.OpenAI, "o3-mini")
		require.Nil(t, client.enforceSingleToolConstraint(directCtx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("a key that sends the requested name unaliased still validates it", func(t *testing.T) {
		client := newAliasClient(schemas.OpenAI,
			aliasKey("aliased", true, schemas.WhiteList{"*"}, schemas.KeyAliases{"o3-mini": {ModelID: "gpt-4o-mini"}}),
			aliasKey("plain", true, schemas.WhiteList{"*"}, nil),
		)
		req := newRequest(schemas.OpenAI, "o3-mini")
		err := client.enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `model "o3-mini" cannot guarantee serial tool execution`)
		assert.NotContains(t, err.Error.Message, "key alias resolves to")
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("no eligible key keeps the requested model under the conservative check", func(t *testing.T) {
		// Dropping the requested name once every eligible key aliases it must not
		// degrade into an empty candidate set when no key can serve the request at
		// all: the policy has to stay on rather than pass everything through.
		client := newAliasClient(schemas.OpenAI,
			aliasKey("disabled", false, schemas.WhiteList{"*"}, nil),
			aliasKey("other-models", true, schemas.WhiteList{"gpt-4o-mini"}, nil),
		)
		req := newRequest(schemas.OpenAI, "o3-mini")
		err := client.enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `model "o3-mini" cannot guarantee serial tool execution`)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("requests without the policy are unchanged", func(t *testing.T) {
		otherCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		require.Nil(t, enforceSerialToolConstraintOnAttempt(otherCtx, schemas.OpenAI, schemas.OpenAI, "gpt-4o-mini", "gpt-4o-mini", req))
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})
}

func TestHumeConstraintAppliesToPluginAddedTools(t *testing.T) {
	recorder := &humeConstraintRecorder{}
	pluginTool := &schemas.ChatTool{
		Type:     schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{Name: "plugin_tool"},
	}
	plugin := &humeConstraintPlugin{
		name:     "tool-adder",
		addTool:  pluginTool,
		recorder: recorder,
	}
	client := newHumeConstraintTestClient(t, &humeConstraintTracer{}, plugin)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)

	request := newRejectedHumeConstraintRequest()
	request.Params.Tools = nil
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, request)

	assert.Nil(t, stream)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "cannot guarantee")
	assert.Equal(t, []string{"tool-adder"}, recorder.preOrder)
	assert.Equal(t, []string{"tool-adder"}, recorder.postOrder)
	assert.True(t, recorder.sawConstraintError)
	assert.Equal(t, []bool{true}, recorder.postFinalState)
}

func TestHumeConstraintErrorRunsPostHooks(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
	}{
		{name: "unary", streaming: false},
		{name: "streaming", streaming: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &humeConstraintRecorder{}
			tracer := &humeConstraintTracer{}
			outer := &humeConstraintPlugin{
				name:         "outer",
				recoverError: true,
				recorder:     recorder,
			}
			inner := &humeConstraintPlugin{
				name:     "inner",
				recorder: recorder,
			}
			client := newHumeConstraintTestClient(t, tracer, outer, inner)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
			ctx.SetValue(schemas.BifrostContextKeyTraceID, "hume-constraint-trace")

			if tt.streaming {
				stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, newRejectedHumeConstraintRequest())
				require.Nil(t, bifrostErr)
				require.NotNil(t, stream)
				chunk, ok := <-stream
				require.True(t, ok)
				require.NotNil(t, chunk)
				require.NotNil(t, chunk.BifrostChatResponse)
				assert.Equal(t, "hume-post-hook-recovery", chunk.BifrostChatResponse.ID)
				_, ok = <-stream
				assert.False(t, ok)
			} else {
				resp, bifrostErr := client.ChatCompletionRequest(ctx, newRejectedHumeConstraintRequest())
				require.Nil(t, bifrostErr)
				require.NotNil(t, resp)
				assert.Equal(t, "hume-post-hook-recovery", resp.ID)
			}

			assert.Equal(t, []string{"outer", "inner"}, recorder.preOrder)
			assert.Equal(t, []string{"inner", "outer"}, recorder.postOrder)
			assert.True(t, recorder.sawConstraintError)
			assert.Equal(t, []bool{tt.streaming, tt.streaming}, recorder.postFinalState)
			require.Len(t, tracer.logs, 2)
			assert.Nil(t, ctx.GetPluginLogs())
		})
	}
}

func TestUnaryConstraintKeepsOriginalErrorWhenPostHookReturnsNilNil(t *testing.T) {
	recorder := &humeConstraintRecorder{}
	plugin := &humeConstraintPlugin{name: "suppressor", suppressError: true, recorder: recorder}
	client := newHumeConstraintTestClient(t, &humeConstraintTracer{}, plugin)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)

	resp, bifrostErr := client.ChatCompletionRequest(ctx, newRejectedHumeConstraintRequest())

	assert.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "cannot guarantee serial tool execution")
}

// serialConstraintErrorObserver records, per attempt, the provider and message
// of every terminal error post-hooks observe, so fallback-leg rejections are
// visible even when the caller ultimately receives the primary attempt's error.
type serialConstraintErrorObserver struct {
	providers []schemas.ModelProvider
	messages  []string
}

func (p *serialConstraintErrorObserver) GetName() string { return "serial-constraint-observer" }
func (p *serialConstraintErrorObserver) Cleanup() error  { return nil }
func (p *serialConstraintErrorObserver) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *serialConstraintErrorObserver) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *serialConstraintErrorObserver) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		p.providers = append(p.providers, bifrostErr.ExtraFields.Provider)
		if bifrostErr.Error != nil {
			p.messages = append(p.messages, bifrostErr.Error.Message)
		} else {
			p.messages = append(p.messages, "")
		}
	}
	return resp, bifrostErr, nil
}

func newSerialConstraintChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: "emit_status"},
		}}},
	}
}

// The policy must judge the key the worker actually selected: an alias on a
// key a pin excludes from selection may neither veto the request (false 400)
// nor vouch for it.
func TestSerialConstraintPinnedKeyIgnoresOtherAliases(t *testing.T) {
	var mu sync.Mutex
	var recordedBodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recordedBodies = append(recordedBodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{
			ID:      "good",
			Value:   *schemas.NewSecretVar("sk-good"),
			Models:  schemas.WhiteList{"*"},
			Aliases: schemas.KeyAliases{"voice": {ModelID: "gpt-4o-mini"}},
		},
		{
			ID:      "bad",
			Value:   *schemas.NewSecretVar("sk-bad"),
			Models:  schemas.WhiteList{"*"},
			Aliases: schemas.KeyAliases{"voice": {ModelID: "o3-mini"}},
		},
	})
	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewNoOpLogger(),
		ModelCatalog: &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
			schemas.OpenAI: {
				"gpt-4o-mini": {SupportedParameters: []string{"parallel_tool_calls"}},
				"o3-mini":     {SupportedParameters: []string{"tools"}},
			},
		}},
	})
	require.NoError(t, initErr)
	t.Cleanup(client.Shutdown)

	t.Run("pin on a key with a supported alias target succeeds", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "good")

		resp, bifrostErr := client.ChatCompletionRequest(ctx, newSerialConstraintChatRequest(schemas.OpenAI, "voice"))
		require.Nil(t, bifrostErr, "the other key's alias must not veto a pinned request")
		require.NotNil(t, resp)

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, recordedBodies, 1)
		assert.Contains(t, recordedBodies[0], `"parallel_tool_calls": false`)
		assert.Contains(t, recordedBodies[0], `"model": "gpt-4o-mini"`)
	})

	t.Run("pin on a key with an unsupported alias target is rejected", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "bad")

		resp, bifrostErr := client.ChatCompletionRequest(ctx, newSerialConstraintChatRequest(schemas.OpenAI, "voice"))
		assert.Nil(t, resp)
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, `key alias resolves to "o3-mini"`)

		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, recordedBodies, 1, "the rejected request must never reach the upstream")
	})
}

// A direct key bypasses the account pool unconditionally, so its alias must be
// checked even when the key carries no Models allow-list.
func TestSerialConstraintDirectKeyAliasIsChecked(t *testing.T) {
	client := newHumeConstraintTestClient(t, &humeConstraintTracer{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID:      "direct",
		Value:   *schemas.NewSecretVar("sk-direct"),
		Aliases: schemas.KeyAliases{"voice": {ModelID: "o3-mini"}},
	})

	resp, bifrostErr := client.ChatCompletionRequest(ctx, newSerialConstraintChatRequest(schemas.OpenAI, "voice"))
	assert.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, `model "voice" cannot guarantee serial tool execution`)
	assert.Contains(t, bifrostErr.Error.Message, `key alias resolves to "o3-mini"`)
}

// A fallback attempt must be judged by its own key's (non-)alias, not the
// primary leg's: a stale Anthropic alias on the shared ctx previously let a
// Vertex Gemini fallback slip past the policy entirely.
func TestSerialConstraintFallbackUsesFreshAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"upstream rejected the request"}}`))
	}))
	t.Cleanup(upstream.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, upstream.URL)
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{
		ID:      "anthropic-alias-key",
		Value:   *schemas.NewSecretVar("sk-ant"),
		Models:  schemas.WhiteList{"*"},
		Aliases: schemas.KeyAliases{"voice": {ModelID: "claude-sonnet-4-5"}},
	}})
	account.AddProvider(schemas.Vertex, 1, 1)
	account.SetKeysForProvider(schemas.Vertex, []schemas.Key{{
		ID:              "vertex-key",
		Value:           *schemas.NewSecretVar(""),
		Models:          schemas.WhiteList{"*"},
		VertexKeyConfig: &schemas.VertexKeyConfig{},
	}})

	observer := &serialConstraintErrorObserver{}
	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewNoOpLogger(),
		LLMPlugins: []schemas.LLMPlugin{observer},
	})
	require.NoError(t, initErr)
	t.Cleanup(client.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)

	request := newSerialConstraintChatRequest(schemas.Anthropic, "voice")
	request.Fallbacks = []schemas.Fallback{{Provider: schemas.Vertex, Model: "gemini-2.5-flash"}}

	resp, bifrostErr := client.ChatCompletionRequest(ctx, request)
	assert.Nil(t, resp)
	require.NotNil(t, bifrostErr, "when every leg fails the primary error is returned")

	require.Len(t, observer.providers, 2, "post-hooks must observe the primary upstream error and the fallback rejection")
	assert.Equal(t, schemas.Anthropic, observer.providers[0])
	assert.Contains(t, observer.messages[0], "upstream rejected the request")
	assert.Equal(t, schemas.Vertex, observer.providers[1])
	assert.Contains(t, observer.messages[1], "cannot guarantee serial tool execution",
		"the Vertex Gemini fallback must be rejected on its own merits, not passed via the primary leg's stale Anthropic alias")
}

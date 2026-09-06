package bifrost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type stickyTestKVStore struct {
	mu       sync.Mutex
	data     map[string]any
	casError error
}

func newStickyTestKVStore() *stickyTestKVStore {
	return &stickyTestKVStore{data: make(map[string]any)}
}

func (s *stickyTestKVStore) Get(key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return v, nil
}

func (s *stickyTestKVStore) SetWithTTL(key string, value any, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *stickyTestKVStore) SetNXWithTTL(key string, value any, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.data[key] = value
	return true, nil
}

func (s *stickyTestKVStore) Delete(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	delete(s.data, key)
	return ok, nil
}

func (s *stickyTestKVStore) CompareAndSwapStringWithTTL(key, expected, replacement string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.casError != nil {
		return false, s.casError
	}
	v, ok := s.data[key]
	if !ok || v != expected {
		return false, nil
	}
	s.data[key] = replacement
	return true, nil
}

func stickyTestKeys() []schemas.Key {
	return []schemas.Key{
		{ID: "key-a", Name: "A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	}
}

func newStickyTestBifrost(t *testing.T, store schemas.KVStore, filter schemas.KeyPoolFilter) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 8)
	account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
	bf, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                      account,
		KVStore:                      store,
		KeyPoolFilter:                filter,
		EnableStickyKeyQuotaFailover: true,
		KeySelector: func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
			return keys[0], nil
		},
		Logger: NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(bf.Shutdown)
	return bf
}

func stickyTestContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	return ctx
}

func TestStickyKeyPlanQuotaFailoverPersistsWinner(t *testing.T) {
	store := newStickyTestKVStore()
	bf := newStickyTestBifrost(t, store, nil)
	ctx := stickyTestContext()

	plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || plan == nil {
		t.Fatalf("expected enabled sticky plan, handled=%v plan=%v err=%v", handled, plan, err)
	}

	provider := plan.keyProvider()
	first, err := provider(nil, nil)
	if err != nil || first.ID != "key-a" {
		t.Fatalf("attempt 0 should use bound key-a, got %q err=%v", first.ID, err)
	}
	plan.observeRetry(createBifrostError("quota exceeded", Ptr(429), nil, false), true)
	second, err := provider(map[string]bool{"key-a": true}, nil)
	if err != nil || second.ID != "key-b" {
		t.Fatalf("quota retry should rotate to key-b, got %q err=%v", second.ID, err)
	}

	raw, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4"))
	if err != nil || raw != "key-b" {
		t.Fatalf("expected winner key-b persisted, got %v err=%v", raw, err)
	}

	next, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || next == nil {
		t.Fatalf("expected second sticky plan, handled=%v plan=%v err=%v", handled, next, err)
	}
	selected, err := next.keyProvider()(nil, nil)
	if err != nil || selected.ID != "key-b" {
		t.Fatalf("next request should use persisted winner key-b, got %q err=%v", selected.ID, err)
	}
}

func TestChatCompletionRequestStickyQuotaFailoverUsesAndPersistsWinner(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer sk-a" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatCompletionBody)
	}))
	defer server.Close()

	store := newStickyTestKVStore()
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 8, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 0
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 0
	account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
	bf, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                      account,
		KVStore:                      store,
		EnableStickyKeyQuotaFailover: true,
		KeySelector: func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
			return keys[0], nil
		},
		Logger: NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(bf.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "request-session")
	response, bifrostErr := bf.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
	})
	if bifrostErr != nil {
		t.Fatalf("chat completion should recover on quota failover: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected chat response")
	}
	nextCtx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	nextCtx.SetValue(schemas.BifrostContextKeySessionID, "request-session")
	if _, e := bf.ChatCompletionRequest(nextCtx, &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("again")}}}}); e != nil {
		t.Fatalf("next request failed: %v", e)
	}
	mu.Lock()
	gotAuths := append([]string(nil), auths...)
	mu.Unlock()
	if len(gotAuths) != 3 || gotAuths[0] != "Bearer sk-a" || gotAuths[1] != "Bearer sk-b" || gotAuths[2] != "Bearer sk-b" {
		t.Fatalf("expected first key then persisted alternative, got auth sequence %v", gotAuths)
	}
	if raw, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "request-session", "gpt-4o-mini")); err != nil || raw != "key-b" {
		t.Fatalf("expected key-b winner binding, got %v err=%v", raw, err)
	}
}

func TestChatCompletionRequestStickyQuotaFailoverSkipsUnsafeTypedPayloads(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*schemas.BifrostChatRequest)
	}{
		{name: "raw request body", apply: func(req *schemas.BifrostChatRequest) {
			req.RawRequestBody = []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
		}},
		{name: "container", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Container: &schemas.ChatContainer{ContainerStr: schemas.Ptr("container-1")}}
		}},
		{name: "context management", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{ContextManagement: []byte(`{"clear_at_turn":3}`)}
		}},
		{name: "mcp server", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{MCPServers: []schemas.ChatMCPServer{{Type: "url", URL: "https://mcp.example.test", Name: "mcp"}}}
		}},
		{name: "extra params", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{ExtraParams: map[string]interface{}{"provider_state": "opaque"}}
		}},
		{name: "store", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Store: schemas.Ptr(true)}
		}},
		{name: "audio generation", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Audio: &schemas.ChatAudioParameters{Format: "wav", Voice: "alloy"}}
		}},
		{name: "audio modality", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Modalities: []string{"text", "audio"}}
		}},
		{name: "server-side tool", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Tools: []schemas.ChatTool{{Type: schemas.ChatToolType("web_search_20260209")}}}
		}},
		{name: "web search options", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{WebSearchOptions: &schemas.ChatWebSearchOptions{SearchContextSize: schemas.Ptr("low")}}
		}},
		{name: "server-side invocations", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{IncludeServerSideToolInvocations: schemas.Ptr(true)}
		}},
		{name: "custom tool", apply: func(req *schemas.BifrostChatRequest) {
			req.Params = &schemas.ChatParameters{Tools: []schemas.ChatTool{{
				Type:   schemas.ChatToolTypeCustom,
				Custom: &schemas.ChatToolCustom{},
			}}}
		}},
		{name: "image file id", apply: func(req *schemas.BifrostChatRequest) {
			fileID := "file-image-1"
			req.Input[0].Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{FileID: &fileID}}}}
		}},
		{name: "remote image url", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0].Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.test/image.png"}}}}
		}},
		{name: "assistant audio", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{Audio: &schemas.ChatAudioMessageAudio{ID: "audio-issued", Data: "inline"}}}
		}},
		{name: "assistant reasoning", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{Reasoning: schemas.Ptr("opaque reasoning")}}
		}},
		{name: "assistant reasoning signature", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ReasoningDetails: []schemas.ChatReasoningDetails{{Type: schemas.BifrostReasoningDetailsTypeEncrypted, Signature: schemas.Ptr("sig"), Data: schemas.Ptr("ciphertext")}}}}
		}},
		{name: "assistant refusal", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{Refusal: schemas.Ptr("refused")}}
		}},
		{name: "assistant annotation", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{Annotations: []schemas.ChatAssistantMessageAnnotation{{Type: "url_citation"}}}}
		}},
		{name: "tool extra content", apply: func(req *schemas.BifrostChatRequest) {
			name := "lookup"
			req.Input[0] = schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
				Type:         schemas.Ptr("function"),
				Function:     schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: `{}`},
				ExtraContent: []byte(`{"thought_signature":"opaque"}`),
			}}}}
		}},
		{name: "unknown content block", apply: func(req *schemas.BifrostChatRequest) {
			req.Input[0].Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockType("provider_result")}}}
		}},
	}

	var mu sync.Mutex
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer sk-a" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatCompletionBody)
	}))
	defer server.Close()

	store := newStickyTestKVStore()
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 8, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 0
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 0
	account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
	bf, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                      account,
		KVStore:                      store,
		EnableStickyKeyQuotaFailover: true,
		KeySelector: func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
			return keys[0], nil
		},
		Logger: NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(bf.Shutdown)

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			auths = nil
			mu.Unlock()
			sessionID := fmt.Sprintf("unsafe-session-%d", i)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
			req := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
			}
			tc.apply(req)
			if _, bifrostErr := bf.ChatCompletionRequest(ctx, req); bifrostErr == nil {
				t.Fatal("unsafe payload unexpectedly recovered through key-b")
			}
			mu.Lock()
			gotAuths := append([]string(nil), auths...)
			mu.Unlock()
			if len(gotAuths) == 0 {
				t.Fatal("expected upstream attempt")
			}
			for _, auth := range gotAuths {
				if auth != "Bearer sk-a" {
					t.Fatalf("unsafe payload contacted alternate key: auth sequence %v", gotAuths)
				}
			}
			if _, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, sessionID, req.Model)); err == nil {
				t.Fatal("unsafe payload persisted a sticky quota winner")
			}
		})
	}
}

func TestStickyChatPayloadPredicateAllowsFunctionTranscriptsAndInlineMedia(t *testing.T) {
	callName := "lookup"
	toolCallID := "call-1"
	imageData := "data:image/png;base64,AAAA"
	fileData := "SGVsbG8="
	toolRequest := &schemas.ChatTool{
		Type:     schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{Name: callName},
	}
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{*toolRequest},
		},
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageData}},
				{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: &fileData}},
				{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "data:audio/wav;base64,AAAA"}},
			}}},
			{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
				Type: schemas.Ptr("function"), ID: &toolCallID,
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: &callName, Arguments: `{}`},
			}}}},
			{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"ok":true}`)}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolCallID}},
		},
	}
	if !stickyChatPayloadEligible(req) {
		t.Fatal("function tool transcript and inline media should remain eligible for sticky quota rotation")
	}
}

func TestStickyChatPayloadPredicateRejectsRemoteFileURL(t *testing.T) {
	remote := "https://example.test/file.pdf"
	req := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
			Type: schemas.ChatContentBlockTypeFile,
			File: &schemas.ChatInputFile{FileURL: &remote},
		}}}}},
	}
	if stickyChatPayloadEligible(req) {
		t.Fatal("remote file URLs must not enter sticky quota rotation")
	}
}

func TestChatCompletionStreamStickyQuotaFailoverSkipsUnsafeTypedPayload(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		quota := `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`
		if r.Header.Get("Authorization") == "Bearer sk-a" {
			sseHandler(quota)(w, r)
			return
		}
		sseHandler(`{"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 8, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 0
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 0
	account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
	bf, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                      account,
		KVStore:                      newStickyTestKVStore(),
		EnableStickyKeyQuotaFailover: true,
		KeySelector: func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
			return keys[0], nil
		},
		Logger: NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(bf.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	ctx.SetValue(schemas.BifrostContextKeySessionID, "unsafe-stream-session")
	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Params:   &schemas.ChatParameters{Store: schemas.Ptr(true)},
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
	}
	ch, bifrostErr := bf.ChatCompletionStreamRequest(ctx, request)
	if bifrostErr == nil {
		_, errs := drainChatStream(ch)
		if len(errs) == 0 {
			t.Fatal("unsafe stream unexpectedly recovered through key-b")
		}
	}
	mu.Lock()
	gotAuths := append([]string(nil), auths...)
	mu.Unlock()
	for _, auth := range gotAuths {
		if auth != "Bearer sk-a" {
			t.Fatalf("unsafe stream contacted alternate key: auth sequence %v", gotAuths)
		}
	}
}

func TestStickyQuotaRetryStrictNegatives(t *testing.T) {
	tests := []struct {
		name string
		err  *schemas.BifrostError
	}{
		{name: "429 network sentinel", err: createBifrostError(schemas.ErrProviderNetworkError, Ptr(429), nil, false)},
		{name: "auth code without status", err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "quota exceeded", Code: Ptr("invalid_api_key")}}},
		{name: "auth type without status", err: createBifrostError("quota exceeded", nil, Ptr("authentication_error"), false)},
		{name: "billing type without status", err: createBifrostError("quota exceeded", nil, Ptr("billing_error"), false)},
		{name: "permission type without status", err: createBifrostError("quota exceeded", nil, Ptr("permission_denied"), false)},
		{name: "401 with quota text", err: createBifrostError("quota exceeded", Ptr(401), nil, false)},
		{name: "402 with quota text", err: createBifrostError("rate limit", Ptr(402), nil, false)},
		{name: "403 with quota text", err: createBifrostError("rate limit", Ptr(403), nil, false)},
		{name: "500 with quota text", err: createBifrostError("rate limit", Ptr(500), nil, false)},
		{name: "501 with quota text", err: createBifrostError("rate limit", Ptr(501), nil, false)},
		{name: "network with quota text", err: createBifrostError(schemas.ErrProviderDoRequest, nil, Ptr("rate limit"), false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if isStickyQuotaRetry(tc.err) {
				t.Fatalf("must not classify %s as sticky quota rotation", tc.name)
			}
		})
	}
	if !isStickyQuotaRetry(createBifrostError("quota exceeded", Ptr(429), nil, false)) {
		t.Fatal("429 must enable sticky quota rotation")
	}
	if !isStickyQuotaRetry(createBifrostError("quota exceeded", nil, nil, false)) {
		t.Fatal("recognized quota text must enable sticky quota rotation")
	}
}

func TestChatCompletionStickyInitialFilterRejectsWithServiceUnavailable(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("an empty filtered pool must not contact an upstream")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 1, 8, server.URL)
			account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
			bf, err := Init(context.Background(), schemas.BifrostConfig{
				Account: account, KVStore: newStickyTestKVStore(), EnableStickyKeyQuotaFailover: true,
				KeyPoolFilter: func(_ *schemas.BifrostContext, _ schemas.ModelProvider, _ string, _ []schemas.Key) ([]schemas.Key, error) {
					return nil, nil
				},
				Logger: NewDefaultLogger(schemas.LogLevelError),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bf.Shutdown)
			request := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI, Model: "gpt-4",
				Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
			}
			var be *schemas.BifrostError
			if stream {
				ch, streamErr := bf.ChatCompletionStreamRequest(stickyTestContext(), request)
				be = streamErr
				if ch != nil {
					for range ch {
					}
					t.Error("rejected key selection must not start a stream")
				}
			} else {
				_, be = bf.ChatCompletionRequest(stickyTestContext(), request)
			}
			if be == nil || be.StatusCode == nil || *be.StatusCode != 503 || be.Type == nil || *be.Type != "no_eligible_keys" {
				t.Fatalf("expected 503/no_eligible_keys from filtered key selection, got %v", be)
			}
		})
	}
}

func TestStickyKeyPlanInitialFilter(t *testing.T) {
	for _, seed := range []string{"fresh", "legacy", "dedicated"} {
		for _, mode := range []string{"veto", "empty", "error", "foreign", "modified"} {
			t.Run(seed+"/"+mode, func(t *testing.T) {
				store := newStickyTestKVStore()
				binding := ""
				if seed == "legacy" {
					binding = buildSessionKey(schemas.OpenAI, "session-1", "gpt-4")
				} else if seed == "dedicated" {
					binding = buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4")
				}
				if binding != "" {
					if err := store.SetWithTTL(binding, "key-a", time.Hour); err != nil {
						t.Fatal(err)
					}
				}
				bf := newStickyTestBifrost(t, store, func(_ *schemas.BifrostContext, _ schemas.ModelProvider, _ string, keys []schemas.Key) ([]schemas.Key, error) {
					switch mode {
					case "empty":
						return nil, nil
					case "error":
						return nil, errors.New("filter unavailable")
					case "foreign":
						return []schemas.Key{{ID: "outside-pool"}}, nil
					case "modified":
						key := keys[1]
						key.Value = *schemas.NewSecretVar("sk-replaced")
						return []schemas.Key{key}, nil
					default:
						return keys[1:], nil
					}
				})
				plan, handled, err := bf.buildStickyKeyPlan(stickyTestContext(), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
				if err != nil || !handled || plan == nil {
					t.Fatalf("expected handled filtered plan, handled=%v hasPlan=%v err=%v", handled, plan != nil, err)
				}
				if mode == "empty" || mode == "foreign" {
					if _, err := plan.keyProvider()(nil, nil); !errors.Is(err, errAllKeysFiltered) {
						t.Fatalf("expected filtered-pool error without selector fallback, err=%v", err)
					}
				} else {
					wantID, wantValue := "key-b", "sk-b"
					if mode == "error" {
						wantID, wantValue = "key-a", "sk-a"
					}
					key, err := plan.keyProvider()(nil, nil)
					if err != nil || key.ID != wantID || key.Value.GetValue() != wantValue {
						t.Fatalf("initial key must respect filter and canonical credentials: got ID=%q want=%q err=%v", key.ID, wantID, err)
					}
					if seed == "fresh" {
						stored, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4"))
						if err != nil || stored != wantID {
							t.Fatalf("unexpected initial binding: %v err=%v", stored, err)
						}
					}
				}
				if binding != "" {
					stored, err := store.Get(binding)
					if err != nil || stored != "key-a" {
						t.Fatalf("filtering must not overwrite existing binding: %v err=%v", stored, err)
					}
				}
			})
		}
	}
}

func TestStickyKeyPlanFilterErrorKeepsBinding(t *testing.T) {
	store := newStickyTestKVStore()
	bf := newStickyTestBifrost(t, store, func(_ *schemas.BifrostContext, _ schemas.ModelProvider, _ string, _ []schemas.Key) ([]schemas.Key, error) {
		return nil, errors.New("filter unavailable")
	})
	ctx := stickyTestContext()
	plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || plan == nil {
		t.Fatalf("expected sticky plan, handled=%v plan=%v err=%v", handled, plan, err)
	}
	first, err := plan.keyProvider()(nil, nil)
	if err != nil || first.ID != "key-a" {
		t.Fatalf("expected key-a, got %q err=%v", first.ID, err)
	}
	plan.observeRetry(createBifrostError("quota exceeded", Ptr(429), nil, false), true)
	second, err := plan.keyProvider()(map[string]bool{"key-a": true}, nil)
	if err != nil || second.ID != "key-a" {
		t.Fatalf("filter error must keep fixed key-a, got %q err=%v", second.ID, err)
	}
}

func TestStickyKeyPlanCASFailureKeepsBinding(t *testing.T) {
	store := newStickyTestKVStore()
	store.casError = errors.New("cas unavailable")
	bf := newStickyTestBifrost(t, store, nil)
	ctx := stickyTestContext()
	plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || plan == nil {
		t.Fatalf("expected sticky plan, handled=%v plan=%v err=%v", handled, plan, err)
	}
	first, err := plan.keyProvider()(nil, nil)
	if err != nil || first.ID != "key-a" {
		t.Fatalf("expected key-a, got %q err=%v", first.ID, err)
	}
	plan.observeRetry(createBifrostError("quota exceeded", Ptr(429), nil, false), true)
	second, err := plan.keyProvider()(map[string]bool{"key-a": true}, nil)
	if err != nil || second.ID != "key-a" {
		t.Fatalf("CAS error must keep fixed key-a, got %q err=%v", second.ID, err)
	}
}

func TestStickyKeyPlanDefaultOffAndRequestScope(t *testing.T) {
	store := newStickyTestKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 8)
	account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
	bf, err := Init(context.Background(), schemas.BifrostConfig{Account: account, KVStore: store, Logger: NewDefaultLogger(schemas.LogLevelError)})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(bf.Shutdown)
	ctx := stickyTestContext()
	if plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI); err != nil || handled || plan != nil {
		t.Fatalf("default-off must not build a plan: handled=%v plan=%v err=%v", handled, plan, err)
	}
	for _, requestType := range []schemas.RequestType{schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.EmbeddingRequest} {
		if plan, handled, err := newStickyTestBifrost(t, store, nil).buildStickyKeyPlan(ctx, requestType, schemas.OpenAI, "gpt-4", schemas.OpenAI); err != nil || handled || plan != nil {
			t.Fatalf("request type %s must not build a sticky plan: handled=%v plan=%v err=%v", requestType, handled, plan, err)
		}
	}
}

func TestStickyKeyPlanNoRetryDoesNotMutateBinding(t *testing.T) {
	store := newStickyTestKVStore()
	bf := newStickyTestBifrost(t, store, nil)
	ctx := stickyTestContext()
	plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || plan == nil {
		t.Fatalf("expected sticky plan, handled=%v plan=%v err=%v", handled, plan, err)
	}
	keyProvider := plan.keyProvider()
	first, err := keyProvider(nil, nil)
	if err != nil || first.ID != "key-a" {
		t.Fatalf("expected key-a, got %q err=%v", first.ID, err)
	}
	if _, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4")); err != nil {
		t.Fatalf("initial binding should exist: %v", err)
	}
	plan.observeRetry(createBifrostError("invalid request", Ptr(400), nil, false), false)
	second, err := keyProvider(nil, nil)
	if err != nil || second.ID != "key-a" {
		t.Fatalf("non-retryable outcome must keep key-a, got %q err=%v", second.ID, err)
	}
	raw, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4"))
	if err != nil || raw != "key-a" {
		t.Fatalf("non-retryable outcome must not mutate binding, got %v err=%v", raw, err)
	}
}

func TestStickyKeyPlanMaxRetriesZeroDoesNotMigrateBinding(t *testing.T) {
	store := newStickyTestKVStore()
	bf := newStickyTestBifrost(t, store, nil)
	ctx := stickyTestContext()
	plan, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || !handled || plan == nil {
		t.Fatalf("expected sticky plan, handled=%v plan=%v err=%v", handled, plan, err)
	}
	config := createTestConfig(0, 0, 0)
	_, retryErr := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
		return "", createBifrostError("quota exceeded", Ptr(429), nil, false)
	}, plan.keyProvider(), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, NewDefaultLogger(schemas.LogLevelError), plan)
	if retryErr == nil {
		t.Fatal("expected terminal quota error with zero retry budget")
	}
	if raw, err := store.Get(buildStickyQuotaKey(schemas.OpenAI, "session-1", "gpt-4")); err != nil || raw != "key-a" {
		t.Fatalf("zero retry budget must keep key-a binding, got %v err=%v", raw, err)
	}
}

var _ schemas.ConditionalStringKVStore = (*stickyTestKVStore)(nil)

// A losing migration and an old refresh must converge without touching the
// legacy binding used by stateful request types.
func TestStickyConcurrentMigrationAndStaleRefresh(t *testing.T) {
	store := newStickyTestKVStore()
	bf := newStickyTestBifrost(t, store, nil)
	legacy := buildSessionKey(schemas.OpenAI, "session-1", "gpt-4")
	_ = store.SetWithTTL(legacy, "key-a", time.Hour)
	plans := make([]*stickyKeyPlan, 3)
	for i := range plans {
		p, handled, err := bf.buildStickyKeyPlan(stickyTestContext(), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil || !handled {
			t.Fatalf("plan: %v", err)
		}
		plans[i] = p
	}
	extra := stickyTestKeys()[1]
	extra.ID = "key-c"
	for _, p := range plans {
		p.pool = append(p.pool, extra)
	}
	plans[1].selector = func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		return keys[len(keys)-1], nil
	}
	start := make(chan struct{})
	results := make(chan string, 2)
	for _, p := range plans[:2] {
		p.observeRetry(createBifrostError("quota exceeded", Ptr(429), nil, false), true)
		go func(p *stickyKeyPlan) {
			<-start
			k, e := p.keyProvider()(map[string]bool{"key-a": true}, nil)
			if e != nil {
				results <- "error"
			} else {
				results <- k.ID
			}
		}(p)
	}
	close(start)
	a, b := <-results, <-results
	if a != b || (a != "key-b" && a != "key-c") {
		t.Fatalf("concurrent requests disagree: %q %q", a, b)
	}
	plans[2].refreshExisting()
	raw, _ := store.Get(plans[0].key)
	if raw != a || plans[2].current.ID != a {
		t.Fatalf("stale refresh reverted winner: %v / %s", raw, plans[2].current.ID)
	}
	raw, _ = store.Get(legacy)
	if raw != "key-a" {
		t.Fatalf("legacy stateful binding changed: %v", raw)
	}
	keys, rotate, err := bf.selectKeyFromProviderForModelWithPool(stickyTestContext(), schemas.ResponsesRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || rotate || len(keys) != 1 || keys[0].ID != "key-a" {
		t.Fatalf("stateful selection moved: %v %v %v", keys, rotate, err)
	}
}

func TestStickyExplicitPinsAndUnsupportedStore(t *testing.T) {
	bf := newStickyTestBifrost(t, newStickyTestKVStore(), nil)
	for _, tc := range []struct {
		key   schemas.BifrostContextKey
		value any
	}{
		{schemas.BifrostContextKeyAPIKeyID, "key-a"},
		{schemas.BifrostContextKeyAPIKeyName, "A"},
		{schemas.BifrostContextKeyDirectKey, stickyTestKeys()[0]},
		{schemas.BifrostContextKeyUseRawRequestBody, true},
	} {
		ctx := stickyTestContext()
		ctx.SetValue(tc.key, tc.value)
		_, handled, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil || handled {
			t.Fatalf("strict %s entered migration: %v", tc.key, err)
		}
	}
	bf.kvStore = &legacyStickyTestStore{newStickyTestKVStore()}
	_, handled, err := bf.buildStickyKeyPlan(stickyTestContext(), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil || handled {
		t.Fatalf("unsupported store entered migration: %v", err)
	}
}

// Embed only the old interface so the optional capability is absent.
type legacyStickyTestStore struct{ schemas.KVStore }

func TestStickyStreamQuotaBoundary(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint("late=", late), func(t *testing.T) {
			var mu sync.Mutex
			var auths []string
			data := `{"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`
			quota := `{"error":{"message":"quota exceeded","type":"rate_limit_error"}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				mu.Lock()
				auths = append(auths, auth)
				mu.Unlock()
				if auth == "Bearer sk-a" {
					if late {
						sseHandler(data, quota)(w, r)
					} else {
						sseHandler(quota)(w, r)
					}
					return
				}
				sseHandler(data)(w, r)
			}))
			defer server.Close()
			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 1, 8, server.URL)
			account.SetKeysForProvider(schemas.OpenAI, stickyTestKeys())
			account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
			account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
			store := newStickyTestKVStore()
			bf, err := Init(context.Background(), schemas.BifrostConfig{Account: account, KVStore: store, EnableStickyKeyQuotaFailover: true, Logger: NewDefaultLogger(schemas.LogLevelError), KeySelector: func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
				return keys[0], nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bf.Shutdown)
			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
			ctx.SetValue(schemas.BifrostContextKeySessionID, "stream-session")
			ch, be := bf.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}}})
			if be != nil {
				t.Fatalf("stream setup: %v", be)
			}
			content, errs := drainChatStream(ch)
			if content != "hello" {
				t.Fatalf("content=%q", content)
			}
			mu.Lock()
			got := append([]string(nil), auths...)
			mu.Unlock()
			if late {
				if len(got) != 1 || len(errs) == 0 {
					t.Fatalf("late error replayed or lost: attempts=%v errors=%v", got, errs)
				}
			} else {
				if len(got) != 2 || got[1] != "Bearer sk-b" || len(errs) != 0 {
					t.Fatalf("quota did not migrate: attempts=%v errors=%v", got, errs)
				}
			}
		})
	}
}

func TestStickyTextQuotaAttribution(t *testing.T) {
	bf := newStickyTestBifrost(t, newStickyTestKVStore(), nil)
	ctx := stickyTestContext()
	plan, _, err := bf.buildStickyKeyPlan(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	_, be := executeRequestWithRetries(ctx, createTestConfig(1, 0, 0), func(k schemas.Key) (string, *schemas.BifrostError) {
		if k.ID == "key-a" {
			return "", createBifrostError("quota exceeded", nil, nil, false)
		}
		return "ok", nil
	}, plan.keyProvider(), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, NewDefaultLogger(schemas.LogLevelError), plan)
	if be != nil {
		t.Fatal(be)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != 2 || trail[0].FailReason == nil || *trail[0].FailReason != "rate_limit_error" || !trail[0].TriggeredRotation {
		t.Fatalf("quota route attribution missing: %+v", trail)
	}
}

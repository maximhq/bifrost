package openrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/openrouter"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestOpenRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.OpenRouter,
		ChatModel:            "openai/gpt-4.1",
		VisionModel:          "openai/gpt-4o",
		TextModel:            "google/gemini-2.5-flash",
		EmbeddingModel:       "qwen/qwen3-embedding-4b",
		ReasoningModel:       "openai/gpt-oss-120b",
		PromptCachingModel:   "anthropic/claude-sonnet-4", // Claude is the only OpenRouter model with explicit caching; its Responses half was broken until #6290
		TranscriptionModel:   "openai/gpt-4o-mini-transcribe",
		SpeechSynthesisModel: "minimax/speech-2.8-turbo",
		DecisionModel:        "typesafe/jev-1.13",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         false, // OpenRouter's responses API is in Beta
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false, // OpenRouter's responses API is in Beta
			ImageBase64:                false, // OpenRouter's responses API is in Beta
			MultipleImages:             false, // OpenRouter's responses API is in Beta
			FileBase64:                 false, // Responses API times out (300s+) with file input
			FileURL:                    false, // Responses API times out (300s+) with file input
			CompleteEnd2End:            false, // OpenRouter's responses API is in Beta
			Reasoning:                  true,
			PromptCaching:              true, // Gates the three tool-block scenarios; two of them issue Responses requests, which is what #6290 broke
			ListModels:                 true,
			StructuredOutputs:          true, // Structured outputs with nullable enum support
			Embedding:                  true,
			SpeechSynthesis:            true,  // Supported via OpenAI-compatible /v1/audio/speech
			SpeechSynthesisStream:      false, // Streaming not offered by upstream OpenRouter API
			Transcription:              true,  // Supported via OpenAI-compatible /v1/audio/transcriptions
			TranscriptionStream:        false, // Streaming not offered by upstream OpenRouter API
			Decision:                   true,  // Native via /api/alpha/decisions for TypeSafe System One models
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
func TestOpenRouterNativeBatchCreate(t *testing.T) {
	for _, endpoint := range []schemas.BatchEndpoint{schemas.BatchEndpointChatCompletions, schemas.BatchEndpointResponses, schemas.BatchEndpointMessages, schemas.BatchEndpointEmbeddings} {
		t.Run(string(endpoint), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/batches" || r.Method != "POST" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer batch-key" {
					t.Error("missing credential")
				}
				body, _ := io.ReadAll(r.Body)
				var wire map[string]any
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Error(err)
				}
				if wire["model"] != "openai/gpt-4o" || wire["endpoint"] != string(endpoint) || wire["completion_window"] != "24h" {
					t.Errorf("request %s", body)
				}
				for _, field := range []string{"endpoint", "model", "completion_window"} {
					if strings.Index(string(body), `"`+field+`"`) > strings.Index(string(body), `"requests"`) {
						t.Errorf("%s follows requests: %s", field, body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(202)
				fmt.Fprint(w, `{"id":"batch-1","object":"batch","model":"openai/gpt-4o","status":"validating","request_counts":{"total":1,"completed":0,"failed":0}}`)
			}))
			defer server.Close()
			p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, bifrost.NewNoOpLogger())
			body := map[string]any{"input": "hello"}
			if endpoint == schemas.BatchEndpointChatCompletions || endpoint == schemas.BatchEndpointMessages {
				body = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}, "max_tokens": 16}
			}
			q := &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenRouter, Model: schemas.Ptr("openai/gpt-4o"), Endpoint: endpoint, Requests: []schemas.BatchRequestItem{{CustomID: "request-1", Body: body}}}
			resp, e := p.BatchCreate(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("batch-key")}, q)
			if e != nil {
				t.Fatalf("native create failed: %s", e.GetErrorString())
			}
			if resp.ID != "batch-1" || resp.Status != schemas.BatchStatusValidating {
				t.Fatalf("response %+v", resp)
			}
		})
	}
}

const nativeBatchCompleted = `{"id":"batch-1","object":"batch","endpoint":"/v1/chat/completions","model":"openai/gpt-4o","provider":"OpenAI","completion_window":"24h","status":"completed","finalized_at":100,"request_counts":{"total":2,"completed":1,"failed":1},"usage":{"prompt_tokens":40,"completion_tokens":12,"total_tokens":52,"cost":0.001,"is_byok":true,"cost_details":{"upstream_inference_cost":0.5}},"results":[{"id":"batch-req-1","custom_id":"first","response":{"status_code":200,"request_id":"request-1","body":{"choices":[{"message":{"content":"hello"}}]}}},{"id":"batch-req-2","custom_id":"second","error":{"type":"invalid_request_error","message":"invalid max_tokens","param":"max_tokens","error_type":"invalid_request"}}]}`

func TestOpenRouterNativeBatchLifecycle(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer rejected" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":401,"message":"bad credential"}}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/batches":
			if r.URL.Query().Get("limit") != "10" || len(r.URL.Query()["status"]) != 2 {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"object":"list","data":[%s],"has_more":true,"last_id":"batch-1"}`, nativeBatchCompleted)
		case r.Method == "DELETE":
			fmt.Fprint(w, `{"id":"batch-1","object":"batch","deletion":{"openrouter":"deleted","upstream":{"provider":"OpenAI","status":"unsupported"}}}`)
		default:
			fmt.Fprint(w, nativeBatchCompleted)
		}
	}))
	defer server.Close()
	p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}, SendBackRawResponse: true}, bifrost.NewNoOpLogger())
	keys := []schemas.Key{{ID: "bad", Value: *schemas.NewSecretVar("rejected")}, {ID: "good", Value: *schemas.NewSecretVar("accepted")}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	retrieved, e := p.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{BatchID: "batch-1"})
	if e != nil {
		t.Fatal(e.GetErrorString())
	}
	if retrieved.Model != "openai/gpt-4o" || retrieved.Usage.Cost.TotalCost != 0.001 || !*retrieved.Usage.IsBYOK || retrieved.Usage.CostDetails["upstream_inference_cost"] != 0.5 || *retrieved.FinalizedAt != 100 {
		t.Fatalf("native metadata lost: %+v", retrieved)
	}
	if len(retrieved.Results) != 2 || retrieved.Results[0].ID != "batch-req-1" || retrieved.Results[1].Error.Param != "max_tokens" || retrieved.Results[1].Error.Type != "invalid_request_error" {
		t.Fatalf("results %+v", retrieved.Results)
	}
	if retrieved.ExtraFields.RawResponse == nil {
		t.Fatal("raw response not captured")
	}
	// An id pin takes precedence over a conflicting name, just as core key selection does.
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "good")
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "conflicting")
	results, e := p.BatchResults(ctx, keys, &schemas.BifrostBatchResultsRequest{BatchID: "batch-1"})
	if e != nil {
		t.Fatal(e.GetErrorString())
	}
	if results.Usage.Cost.TotalCost != 0.001 || results.Results[1].CustomID != "second" {
		t.Fatalf("results %+v", results)
	}
	listed, e := p.BatchList(ctx, keys, &schemas.BifrostBatchListRequest{Limit: 10, ExtraParams: map[string]any{"status": []string{"validating", "in_progress"}}})
	if e != nil {
		t.Fatal(e.GetErrorString())
	}
	if len(listed.Data) != 1 || !listed.HasMore {
		t.Fatalf("list %+v", listed)
	}
	deleted, e := p.BatchDelete(ctx, keys, &schemas.BifrostBatchDeleteRequest{BatchID: "batch-1"})
	if e != nil {
		t.Fatal(e.GetErrorString())
	}
	if deleted.Status != schemas.BatchStatusDeleted || deleted.Deletion["openrouter"] != "deleted" {
		t.Fatalf("delete %+v", deleted)
	}
	for _, path := range paths {
		if strings.Contains(path, "/results") || strings.Contains(path, "/files") {
			t.Fatalf("unexpected upstream route %s", path)
		}
	}
	if len(paths) != 5 {
		t.Fatalf("requests=%v; workspace list must not be duplicated across keys", paths)
	}
}

func TestOpenRouterBatchValidation(t *testing.T) {
	fresh := func() *schemas.BifrostBatchCreateRequest {
		return &schemas.BifrostBatchCreateRequest{Model: schemas.Ptr("openai/gpt-4o"), Endpoint: schemas.BatchEndpointChatCompletions, Requests: []schemas.BatchRequestItem{{CustomID: "a", Body: map[string]any{"messages": []any{}}}}}
	}
	for name, mutate := range map[string]func(*schemas.BifrostBatchCreateRequest){
		"empty requests":      func(q *schemas.BifrostBatchCreateRequest) { q.Requests = nil },
		"non POST method":     func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].Method = "GET" },
		"body and params":     func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].Params = map[string]any{"input": "hi"} },
		"metadata":            func(q *schemas.BifrostBatchCreateRequest) { q.Metadata = map[string]string{"label": "unsupported"} },
		"provider not object": func(q *schemas.BifrostBatchCreateRequest) { q.ExtraParams = map[string]any{"provider": "openai"} },
		"empty provider only": func(q *schemas.BifrostBatchCreateRequest) {
			q.ExtraParams = map[string]any{"provider": map[string]any{"only": []any{}}}
		},
		"invalid provider slug": func(q *schemas.BifrostBatchCreateRequest) {
			q.ExtraParams = map[string]any{"provider": map[string]any{"only": []any{12}}}
		},
		"invalid allow fallback": func(q *schemas.BifrostBatchCreateRequest) {
			q.ExtraParams = map[string]any{"provider": map[string]any{"allow_fallbacks": "false"}}
		},
		"HTTP callback": func(q *schemas.BifrostBatchCreateRequest) {
			q.ExtraParams = map[string]any{"callback_url": "http://example.com/hook"}
		},
		"invalid callback type":   func(q *schemas.BifrostBatchCreateRequest) { q.ExtraParams = map[string]any{"callback_url": 12} },
		"unknown extra parameter": func(q *schemas.BifrostBatchCreateRequest) { q.ExtraParams = map[string]any{"unsupported": true} },
		"missing model":           func(q *schemas.BifrostBatchCreateRequest) { q.Model = nil },
		"file input":              func(q *schemas.BifrostBatchCreateRequest) { q.InputFileID = "file-1" },
		"duplicate id":            func(q *schemas.BifrostBatchCreateRequest) { q.Requests = append(q.Requests, q.Requests[0]) },
		"blank id":                func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].CustomID = " " },
		"missing body":            func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].Body = nil },
		"mixed models":            func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].Body["model"] = "different" },
		"mixed endpoints":         func(q *schemas.BifrostBatchCreateRequest) { q.Requests[0].URL = "/v1/responses" },
		"completion window":       func(q *schemas.BifrostBatchCreateRequest) { q.CompletionWindow = "48h" },
		"unsupported endpoint":    func(q *schemas.BifrostBatchCreateRequest) { q.Endpoint = schemas.BatchEndpointCompletions },
		"unsupported options": func(q *schemas.BifrostBatchCreateRequest) {
			q.ExtraParams = map[string]any{"provider": map[string]any{"order": []string{"openai"}}}
		},
		"embedding options": func(q *schemas.BifrostBatchCreateRequest) {
			q.Endpoint = schemas.BatchEndpointEmbeddings
			q.ExtraParams = map[string]any{"provider": map[string]any{"only": []any{"openai"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			q := fresh()
			mutate(q)
			_, e := (&openrouter.OpenRouterProvider{}).BatchCreate(nil, schemas.Key{}, q)
			if e == nil || e.StatusCode == nil || *e.StatusCode != 400 {
				t.Fatalf("expected validation 400, got %+v", e)
			}
		})
	}
	q := fresh()
	q.ExtraParams = map[string]any{"provider": map[string]any{"only": []any{"openai"}, "allow_fallbacks": false}, "callback_url": "https://example.com/hook"}
	native, e := openrouter.ToOpenRouterBatchRequest(q)
	if e != nil {
		t.Fatal(e)
	}
	if native.Provider["allow_fallbacks"] != false || native.CallbackURL == "" {
		t.Fatalf("options %+v", native)
	}
	b, e := providerUtils.MarshalSorted(native)
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"provider", "callback_url", "completion_window"} {
		if strings.Index(string(b), `"`+field+`"`) > strings.Index(string(b), `"requests"`) {
			t.Fatalf("metadata ordering %s", b)
		}
	}
}

func TestOpenRouterBatchValidationFailureAndPendingResults(t *testing.T) {
	for _, status := range []string{"failed", "validating", "completed"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"batch-1","model":"openai/gpt-4o","status":%q,"request_counts":{"total":1,"completed":0,"failed":0},"results":null,"error":{"message":"request a has invalid input"}}`, status)
			}))
			defer server.Close()
			p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, bifrost.NewNoOpLogger())
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			keys := []schemas.Key{{Value: *schemas.NewSecretVar("key")}}
			got, e := p.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{BatchID: "batch-1"})
			if e != nil || got.Error.Message != "request a has invalid input" {
				t.Fatalf("async failure lost %+v %v", got, e)
			}
			_, e = p.BatchResults(ctx, keys, &schemas.BifrostBatchResultsRequest{BatchID: "batch-1"})
			if status == "failed" {
				if e != nil {
					t.Fatal(e.GetErrorString())
				}
			} else if e == nil || e.StatusCode == nil || *e.StatusCode != 409 {
				t.Fatalf("expected pending 409, got %+v", e)
			}
		})
	}
}

func BenchmarkOpenRouterBatchConversion(b *testing.B) {
	for _, size := range []int{1, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			request := &schemas.BifrostBatchCreateRequest{Model: schemas.Ptr("openai/gpt-4o"), Endpoint: schemas.BatchEndpointChatCompletions, Requests: make([]schemas.BatchRequestItem, size)}
			for i := range request.Requests {
				request.Requests[i] = schemas.BatchRequestItem{CustomID: fmt.Sprint(i), Body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}}}
			}
			b.ReportAllocs()
			for b.Loop() {
				native, err := openrouter.ToOpenRouterBatchRequest(request)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := providerUtils.MarshalSorted(native); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestOpenRouterBatchDeletePreservesUpstreamConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":{"code":409,"message":"batch is still running"}}`)
	}))
	defer server.Close()
	p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, bifrost.NewNoOpLogger())
	_, err := p.BatchDelete(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), []schemas.Key{{Value: *schemas.NewSecretVar("key")}}, &schemas.BifrostBatchDeleteRequest{BatchID: "batch-1"})
	if err == nil || err.StatusCode == nil || *err.StatusCode != 409 || !strings.Contains(err.GetErrorString(), "still running") {
		t.Fatalf("upstream conflict was lost: %+v", err)
	}
}

func TestOpenRouterBatchParseFailureRetainsRawCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":`)
	}))
	defer server.Close()
	p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}, SendBackRawRequest: true, SendBackRawResponse: true,
	}, bifrost.NewNoOpLogger())
	_, err := p.BatchCreate(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("key")}, &schemas.BifrostBatchCreateRequest{
		Model: schemas.Ptr("openai/gpt-4o"), Endpoint: schemas.BatchEndpointChatCompletions,
		Requests: []schemas.BatchRequestItem{{CustomID: "a", Body: map[string]any{"messages": []any{}}}},
	})
	if err == nil || err.ExtraFields.RawRequest == nil || err.ExtraFields.RawResponse == nil {
		t.Fatalf("parse error must retain configured raw captures: %+v", err)
	}
}

func TestOpenRouterBatchNonJSONErrorOwnsRawBody(t *testing.T) {
	// Keep response-pool reuse on one P so overwriting the released body is a
	// deterministic regression for captures that still borrow that buffer.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	const errorBody = "upstream unavailable: non-JSON response body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, errorBody)
	}))
	defer server.Close()
	provider := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}, SendBackRawResponse: true,
	}, bifrost.NewNoOpLogger())
	_, err := provider.BatchCreate(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("key")}, &schemas.BifrostBatchCreateRequest{
		Model: schemas.Ptr("openai/gpt-4o"), Endpoint: schemas.BatchEndpointChatCompletions,
		Requests: []schemas.BatchRequestItem{{CustomID: "a", Body: map[string]any{"messages": []any{}}}},
	})
	if err == nil {
		t.Fatal("expected an upstream error")
	}
	captured, ok := err.ExtraFields.RawResponse.(json.RawMessage)
	if !ok || string(captured) != errorBody {
		t.Fatalf("raw response = %q, want %q", captured, errorBody)
	}
	for range 16 {
		response := fasthttp.AcquireResponse()
		response.SetBodyString(strings.Repeat("x", len(errorBody)))
		fasthttp.ReleaseResponse(response)
	}
	if string(captured) != errorBody {
		t.Fatalf("raw error borrowed the released response buffer: %q", captured)
	}
}

func TestOpenRouterBatchParamsInput(t *testing.T) {
	_, err := openrouter.ToOpenRouterBatchRequest(nil)
	if err == nil {
		t.Fatal("nil input must fail validation")
	}
	body := map[string]any{"model": "openai/gpt-4o", "messages": []any{}}
	native, err := openrouter.ToOpenRouterBatchRequest(&schemas.BifrostBatchCreateRequest{
		Model: schemas.Ptr("openai/gpt-4o"), Endpoint: schemas.BatchEndpointChatCompletions,
		Requests: []schemas.BatchRequestItem{{CustomID: "params-id", Method: "POST", URL: "/v1/chat/completions", Params: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if native.Requests[0].CustomID != "params-id" || native.Requests[0].Body["model"] != body["model"] {
		t.Fatalf("params must become native custom_id/body items: %+v", native.Requests[0])
	}
	wire, err := providerUtils.MarshalSorted(native)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"method", "url", "params"} {
		if strings.Contains(string(wire), `"`+field+`"`) {
			t.Fatalf("Bifrost-only item field %q reached the wire: %s", field, wire)
		}
	}
}

func TestOpenRouterBatchListFiltersAndValidation(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("after") != "cursor & next" || q.Get("created_after") != "2026-10-01T00:00:00Z" || q.Get("created_before") != "2026-10-22T00:00:00Z" || len(q["status"]) != 2 {
			t.Errorf("filters lost: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"object":"list","data":[],"has_more":false}`)
	}))
	defer server.Close()
	p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, bifrost.NewNoOpLogger())
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keys := []schemas.Key{{ID: "available", Name: "available", Value: *schemas.NewSecretVar("key")}}
	_, err := p.BatchList(ctx, keys, &schemas.BifrostBatchListRequest{After: schemas.Ptr("cursor & next"), ExtraParams: map[string]any{
		"status": []any{"completed", "failed"}, "created_after": "2026-10-01T00:00:00Z", "created_before": "2026-10-22T00:00:00Z",
	}})
	if err != nil {
		t.Fatal(err.GetErrorString())
	}
	for name, req := range map[string]*schemas.BifrostBatchListRequest{
		"nil": nil, "negative limit": {Limit: -1}, "large limit": {Limit: 101}, "empty after": {After: schemas.Ptr("")},
		"unsupported pagination": {AfterID: schemas.Ptr("id")}, "invalid status": {ExtraParams: map[string]any{"status": "ended"}},
		"invalid status type": {ExtraParams: map[string]any{"status": []any{1}}}, "timestamp type": {ExtraParams: map[string]any{"created_after": 1}},
		"unknown filter": {ExtraParams: map[string]any{"unknown": "value"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.BatchList(ctx, keys, req)
			if err == nil || err.StatusCode == nil || *err.StatusCode != 400 {
				t.Fatalf("expected validation 400, got %+v", err)
			}
		})
	}
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "unknown")
	_, err = p.BatchList(ctx, keys, &schemas.BifrostBatchListRequest{})
	if err == nil {
		t.Fatal("unknown key pin must fail")
	}
	if calls != 1 {
		t.Fatalf("invalid requests must not reach upstream: %d calls", calls)
	}
}

func TestOpenRouterBatchCredentialFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":{"code":%d,"message":"failure"}}`, status)
			}))
			defer server.Close()
			p := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true}}, bifrost.NewNoOpLogger())
			_, err := p.BatchRetrieve(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), []schemas.Key{{Value: *schemas.NewSecretVar("one")}, {Value: *schemas.NewSecretVar("two")}}, &schemas.BifrostBatchRetrieveRequest{BatchID: "batch-1"})
			want := 1
			if status == 401 || status == 403 || status == 404 {
				want = 2
			}
			if err == nil || err.StatusCode == nil || *err.StatusCode != status || calls != want {
				t.Fatalf("status=%d calls=%d want=%d error=%+v", status, calls, want, err)
			}
		})
	}
}

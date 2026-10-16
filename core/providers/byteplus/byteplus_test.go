package byteplus_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/byteplus"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// TestByteplus is named to match the make test-core recipe, which derives TestByteplus from PROVIDER=byteplus.
func TestByteplus(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("BYTEPLUS_API_KEY")) == "" {
		t.Skip("Skipping BytePlus tests because BYTEPLUS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.BytePlus,
		ChatModel: "deepseek-v4-flash-260425",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.BytePlus, Model: "deepseek-v4-flash-260425"},
		},
		ReasoningModel: "deepseek-v4-flash-260425",
		EmbeddingModel: "skylark-embedding-vision-251215",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // deepseek-v4-flash rejects image input
			ImageBase64:           false,
			CompleteEnd2End:       true,
			ListModels:            true,
			Reasoning:             true,
			Embedding:             true,
		},
	}

	t.Run("ByteplusTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

type capturedRequest struct {
	method string
	path   string
	query  string
	auth   string
	body   []byte
}

// newMockBytePlus starts a server that records each request and replies with the response registered for "METHOD /path".
func newMockBytePlus(t *testing.T, responses map[string]func(w http.ResponseWriter, r *http.Request)) (*byteplus.BytePlusProvider, func() []capturedRequest, string) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), body: body})
		mu.Unlock()
		if respond, ok := responses[r.Method+" "+r.URL.Path]; ok {
			respond(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	provider, err := byteplus.NewBytePlusProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/",
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewBytePlusProvider: %v", err)
	}
	return provider, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}, server.URL
}

func jsonResponse(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func testContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}

func userChatRequest(params *schemas.ChatParameters) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.BytePlus,
		Model:    "seed-2-0-lite-260228",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   params,
	}
}

const chatSuccessBody = `{"id":"c1","object":"chat.completion","created":1,"model":"seed-2-0-lite-260228","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hello","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"completion_tokens_details":{"reasoning_tokens":1}}}`

func TestBytePlusChatUsesArkPathAndReadsReasoningContent(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
	})
	params := &schemas.ChatParameters{ExtraParams: map[string]interface{}{"thinking": map[string]interface{}{"type": "enabled"}}}

	resp, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(params))
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion error: %+v", bifrostErr.Error)
	}
	reqs := captured()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 upstream request, got %d", len(reqs))
	}
	if reqs[0].auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", reqs[0].auth)
	}
	if got := gjson.GetBytes(reqs[0].body, "thinking.type").String(); got != "enabled" {
		t.Errorf("thinking extra param not passed through, got %q: %s", got, reqs[0].body)
	}
	msg := resp.Choices[0].ChatNonStreamResponseChoice.Message
	if msg.ChatAssistantMessage == nil || msg.ChatAssistantMessage.Reasoning == nil || *msg.ChatAssistantMessage.Reasoning != "thinking" {
		t.Errorf("reasoning_content not mapped to Reasoning: %+v", msg.ChatAssistantMessage)
	}
}

// Ark rejects reasoning_effort "none", and returns 400 when reasoning_effort is sent with thinking disabled.
func TestBytePlusChatReasoningEffortAndThinking(t *testing.T) {
	tests := []struct {
		name         string
		effort       string
		thinking     string // caller's thinking.type, "" for none
		wantEffort   string // "" means reasoning_effort must be absent
		wantThinking string // "" means thinking must be absent
	}{
		{name: "none disables thinking", effort: "none", wantThinking: "disabled"},
		{name: "effort dropped when caller disabled thinking", effort: "high", thinking: "disabled", wantThinking: "disabled"},
		{name: "effort kept with thinking enabled", effort: "low", thinking: "enabled", wantEffort: "low", wantThinking: "enabled"},
		{name: "minimal is kept", effort: "minimal", wantEffort: "minimal"},
		{name: "xhigh clamps to high", effort: "xhigh", wantEffort: "high"},
		{name: "max clamps to high", effort: "max", wantEffort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
				"POST /api/v3/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
			})
			reasoning := &schemas.ChatReasoning{Effort: schemas.Ptr(tt.effort)}
			params := &schemas.ChatParameters{Reasoning: reasoning}
			if tt.thinking != "" {
				params.ExtraParams = map[string]interface{}{"thinking": map[string]interface{}{"type": tt.thinking}}
			}

			if _, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(params)); bifrostErr != nil {
				t.Fatalf("ChatCompletion error: %+v", bifrostErr.Error)
			}
			body := captured()[0].body
			effort := gjson.GetBytes(body, "reasoning_effort")
			if tt.wantEffort == "" && effort.Exists() {
				t.Errorf("reasoning_effort should be absent: %s", body)
			}
			if tt.wantEffort != "" && effort.String() != tt.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q: %s", effort.String(), tt.wantEffort, body)
			}
			if got := gjson.GetBytes(body, "thinking.type").String(); got != tt.wantThinking {
				t.Errorf("thinking.type = %q, want %q: %s", got, tt.wantThinking, body)
			}
			if reasoning.Effort == nil || *reasoning.Effort != tt.effort {
				t.Errorf("caller's reasoning params were mutated: %+v", reasoning)
			}
		})
	}
}

func TestBytePlusErrorKeepsArkCode(t *testing.T) {
	provider, _, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/chat/completions": jsonResponse(http.StatusNotFound, `{"error":{"code":"ModelNotOpen","message":"Your account has not activated the model seed-2-0-lite-260228.","param":"","type":"NotFound"}}`),
	})

	_, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(nil))
	if bifrostErr == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusNotFound {
		t.Errorf("status code = %v, want 404", bifrostErr.StatusCode)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "ModelNotOpen" {
		t.Errorf("error code = %v, want ModelNotOpen", bifrostErr.Error.Code)
	}
	if !strings.Contains(bifrostErr.Error.Message, "has not activated") {
		t.Errorf("error message = %q", bifrostErr.Error.Message)
	}
}

func TestBytePlusResponsesUsesNativeEndpoint(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/responses": jsonResponse(http.StatusOK, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"seed-2-0-lite-260228","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`),
	})

	resp, bifrostErr := provider.Responses(testContext(t), testKey(), &schemas.BifrostResponsesRequest{
		Provider: schemas.BytePlus,
		Model:    "seed-2-0-lite-260228",
		Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hi")},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses error: %+v", bifrostErr.Error)
	}
	if reqs := captured(); len(reqs) != 1 || reqs[0].auth != "Bearer test-key" {
		t.Fatalf("unexpected upstream requests: %+v", reqs)
	}
	if len(resp.Output) != 1 {
		t.Errorf("expected 1 output item, got %d", len(resp.Output))
	}
}

// Ark's Responses decoder rejects reasoning.summary outright ("unknown field \"summary\""), even as null.
func TestBytePlusResponsesOmitsReasoningSummary(t *testing.T) {
	const responseBody = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"deepseek-v4-flash-260425","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	for _, summary := range []*string{nil, schemas.Ptr("detailed")} {
		provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
			"POST /api/v3/responses": jsonResponse(http.StatusOK, responseBody),
		})
		_, bifrostErr := provider.Responses(testContext(t), testKey(), &schemas.BifrostResponsesRequest{
			Provider: schemas.BytePlus,
			Model:    "deepseek-v4-flash-260425",
			Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hi")},
			Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high"), Summary: summary}},
		})
		if bifrostErr != nil {
			t.Fatalf("Responses error: %+v", bifrostErr.Error)
		}
		body := captured()[0].body
		if gjson.GetBytes(body, "reasoning.summary").Exists() {
			t.Errorf("reasoning.summary must not be sent (summary=%v): %s", summary, body)
		}
		if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "high" {
			t.Errorf("reasoning.effort = %q, want high: %s", got, body)
		}
	}
}

func TestBytePlusListModelsComesFromKeyConfig(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, nil)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"seed-2-0-lite-260228", "seedream-4-5-251128"}}

	resp, bifrostErr := provider.ListModels(testContext(t), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.BytePlus})
	if bifrostErr != nil {
		t.Fatalf("ListModels error: %+v", bifrostErr.Error)
	}
	if len(captured()) != 0 {
		t.Errorf("ListModels should not call upstream, got %d requests", len(captured()))
	}
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "seed-2-0-lite-260228") || !strings.Contains(joined, "seedream-4-5-251128") {
		t.Errorf("models = %v, want both configured models", ids)
	}
}

func embeddingRequest(items ...schemas.EmbeddingInputItem) *schemas.BifrostEmbeddingRequest {
	return &schemas.BifrostEmbeddingRequest{Provider: schemas.BytePlus, Model: "skylark-embedding-vision-250615", Input: items}
}

func textItem(text string) schemas.EmbeddingInputItem {
	return schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr(text)}}}
}

// Ark's multimodal endpoint fuses a request's parts into one vector, so each input item is its own call.
func TestBytePlusEmbeddingFansOutOneCallPerItem(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/embeddings/multimodal": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			vector := `[0.1,0.2]`
			if gjson.GetBytes(body, "input.0.text").String() == "second" {
				vector = `[0.3,0.4]`
			}
			jsonResponse(http.StatusOK, `{"id":"e1","model":"skylark-embedding-vision-250615","object":"list","data":{"embedding":`+vector+`,"object":"embedding"},"usage":{"prompt_tokens":4,"total_tokens":4,"prompt_tokens_details":{"text_tokens":4,"image_tokens":0}}}`)(w, r)
		},
	})
	req := embeddingRequest(textItem("first"), textItem("second"))
	req.Params = &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(1024)}

	resp, bifrostErr := provider.Embedding(testContext(t), testKey(), req)
	if bifrostErr != nil {
		t.Fatalf("Embedding error: %+v", bifrostErr.Error)
	}
	reqs := captured()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(reqs))
	}
	for _, r := range reqs {
		if gjson.GetBytes(r.body, "input.#").Int() != 1 || gjson.GetBytes(r.body, "input.0.type").String() != "text" {
			t.Errorf("each call should carry exactly one text part: %s", r.body)
		}
		if gjson.GetBytes(r.body, "dimensions").Int() != 1024 {
			t.Errorf("dimensions not forwarded: %s", r.body)
		}
	}
	if len(resp.Data) != 2 || resp.Data[0].Index != 0 || resp.Data[1].Index != 1 {
		t.Fatalf("unexpected data: %+v", resp.Data)
	}
	if got := resp.Data[1].Embedding.EmbeddingArray; len(got) != 2 || got[0] != 0.3 {
		t.Errorf("second vector out of order: %v", got)
	}
	if resp.Usage.PromptTokens != 8 || resp.Usage.TotalTokens != 8 {
		t.Errorf("usage not summed: %+v", resp.Usage)
	}
}

func TestToBytePlusMultimodalEmbeddingRequest(t *testing.T) {
	t.Run("image part becomes a data uri", func(t *testing.T) {
		item := schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("caption")},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: schemas.Ptr("aGVsbG8="), MIMEType: schemas.Ptr("image/png")}},
		}}
		req, err := byteplus.ToBytePlusMultimodalEmbeddingRequest("m", item, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(req.Input) != 2 || req.Input[1].Type != "image_url" || req.Input[1].ImageURL.URL != "data:image/png;base64,aGVsbG8=" {
			t.Errorf("unexpected input: %+v", req.Input)
		}
	})
	t.Run("item params override request params", func(t *testing.T) {
		item := textItem("x")
		item.Params = &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(2048)}
		req, err := byteplus.ToBytePlusMultimodalEmbeddingRequest("m", item, &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(1024)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Dimensions == nil || *req.Dimensions != 2048 {
			t.Errorf("dimensions = %v, want 2048", req.Dimensions)
		}
	})
	t.Run("rejects non-float encodings", func(t *testing.T) {
		_, err := byteplus.ToBytePlusMultimodalEmbeddingRequest("m", textItem("x"), &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("base64")})
		if err == nil {
			t.Fatal("expected an error for base64 encoding")
		}
	})
	t.Run("rejects token parts", func(t *testing.T) {
		item := schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeTokens, Tokens: []int{1, 2}}}}
		if _, err := byteplus.ToBytePlusMultimodalEmbeddingRequest("m", item, nil); err == nil {
			t.Fatal("expected an error for token parts")
		}
	})
}

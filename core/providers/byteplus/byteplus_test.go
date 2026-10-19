package byteplus_test

import (
	"bytes"
	"context"
	"encoding/json"
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
		ReasoningModel:       "deepseek-v4-flash-260425",
		EmbeddingModel:       "skylark-embedding-vision-251215",
		ImageGenerationModel: "dola-seedream-5-0-pro-260628",
		VideoGenerationModel: "dreamina-seedance-2-5-260628",
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
			ImageGeneration:       true,
			// The shared image edit test always sends a mask, which Seedream rejects.
			// Mask-free edits are covered by TestBytePlusImageGenerationAndEdit and the harness.
			ImageEdit:       false,
			VideoGeneration: os.Getenv("BYTEPLUS_RUN_VIDEO_TESTS") == "1", // Seedance bills per generated video
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
		{name: "none dropped when caller enabled thinking", effort: "none", thinking: "enabled", wantThinking: "enabled"},
		{name: "none dropped when caller set auto thinking", effort: "none", thinking: "auto", wantThinking: "auto"},
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

// Per-item params override the request-level ones for dimensions; task_type and title have no Ark field and are not forwarded.
func TestBytePlusEmbeddingPerItemParams(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/embeddings/multimodal": jsonResponse(http.StatusOK, `{"id":"e1","model":"skylark-embedding-vision-250615","object":"list","data":{"embedding":[0.1,0.2],"object":"embedding"},"usage":{"prompt_tokens":4,"total_tokens":4,"prompt_tokens_details":{"text_tokens":4,"image_tokens":0}}}`),
	})
	item := textItem("first")
	item.Params = &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(1024), TaskType: schemas.Ptr("retrieval_query"), Title: schemas.Ptr("a title")}
	req := embeddingRequest(item)
	req.Params = &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(2048)}

	if _, bifrostErr := provider.Embedding(testContext(t), testKey(), req); bifrostErr != nil {
		t.Fatalf("Embedding error: %+v", bifrostErr.Error)
	}
	body := captured()[0].body
	if gjson.GetBytes(body, "dimensions").Int() != 1024 {
		t.Errorf("per-item dimensions should override the request-level value: %s", body)
	}
	if gjson.GetBytes(body, "task_type").Exists() || gjson.GetBytes(body, "title").Exists() {
		t.Errorf("task_type and title are not forwarded to Ark: %s", body)
	}
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

func TestToBytePlusImageGenerationRequest(t *testing.T) {
	req, err := byteplus.ToBytePlusImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
		Model: "seedream-4-5-251128",
		Input: &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params: &schemas.ImageGenerationParameters{
			N:              schemas.Ptr(3),
			Size:           schemas.Ptr("2048x2048"),
			ResponseFormat: schemas.Ptr("b64_json"),
			InputImages:    []string{"https://example.com/ref.png"},
			ExtraParams:    map[string]interface{}{"watermark": false, "guidance_scale": 5.5},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.SequentialImageGeneration == nil || *req.SequentialImageGeneration != "auto" || req.SequentialImageGenerationOptions == nil || req.SequentialImageGenerationOptions.MaxImages != 3 {
		t.Errorf("n=3 should enable sequential generation with max_images 3: %+v %+v", req.SequentialImageGeneration, req.SequentialImageGenerationOptions)
	}
	if req.Size == nil || *req.Size != "2048x2048" || req.ResponseFormat == nil || *req.ResponseFormat != "b64_json" {
		t.Errorf("size/response_format not mapped: %+v %+v", req.Size, req.ResponseFormat)
	}
	if len(req.Image) != 1 || req.Image[0] != "https://example.com/ref.png" {
		t.Errorf("input images not mapped: %v", req.Image)
	}
	if req.Watermark == nil || *req.Watermark {
		t.Errorf("watermark not lifted: %v", req.Watermark)
	}
	if _, ok := req.ExtraParams["guidance_scale"]; !ok {
		t.Errorf("unknown extra params should pass through: %v", req.ExtraParams)
	}
}

func TestBytePlusImageGenerationAndEdit(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/images/generations": jsonResponse(http.StatusOK, `{"model":"seedream-4-5-251128","created":1757321139,"data":[{"url":"https://cdn.example/1.png","size":"2048x2048"},{"error":{"code":"OutputImageSensitiveContentDetected","message":"blocked"}}],"usage":{"generated_images":1,"output_tokens":16384,"total_tokens":16384}}`),
	})
	ctx := testContext(t)

	resp, bifrostErr := provider.ImageGeneration(ctx, testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "seedream-4-5-251128",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
	})
	if bifrostErr != nil {
		t.Fatalf("ImageGeneration error: %+v", bifrostErr.Error)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://cdn.example/1.png" {
		t.Errorf("failed images should be skipped, got %+v", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.OutputTokens != 16384 || resp.Created != 1757321139 {
		t.Errorf("usage/created not mapped: %+v created=%d", resp.Usage, resp.Created)
	}

	if _, bifrostErr := provider.ImageEdit(ctx, testKey(), &schemas.BifrostImageEditRequest{
		Provider: schemas.BytePlus,
		Model:    "seedream-4-5-251128",
		Input:    &schemas.ImageEditInput{Prompt: "make it blue", Images: []schemas.ImageInput{{Image: []byte("\x89PNG\r\n\x1a\n0000")}, {URL: "https://example.com/b.png"}}},
	}); bifrostErr != nil {
		t.Fatalf("ImageEdit error: %+v", bifrostErr.Error)
	}
	edit := captured()[1].body
	if got := gjson.GetBytes(edit, "image.0").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("image bytes should be sent as a data uri, got %q", got)
	}
	if got := gjson.GetBytes(edit, "image.1").String(); got != "https://example.com/b.png" {
		t.Errorf("image url not forwarded, got %q", got)
	}
}

// Converter refusals are the caller's mistake, so they must surface as a 400 with the reason, not a conversion failure.
// background is a typed OpenAI-shaped field, so the HTTP layer never puts it in ExtraParams.
func TestToBytePlusImageGenerationRequestDropsInvalidSequentialOptions(t *testing.T) {
	for name, invalid := range map[string]interface{}{"string": "bogus", "zero max_images": map[string]interface{}{"max_images": 0}} {
		t.Run(name, func(t *testing.T) {
			req, err := byteplus.ToBytePlusImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
				Model: "seedream-4-5-251128",
				Input: &schemas.ImageGenerationInput{Prompt: "a cat"},
				Params: &schemas.ImageGenerationParameters{
					N:           schemas.Ptr(2),
					ExtraParams: map[string]interface{}{"sequential_image_generation_options": invalid},
				},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.SequentialImageGenerationOptions == nil || req.SequentialImageGenerationOptions.MaxImages != 2 {
				t.Errorf("n=2 should keep max_images 2: %+v", req.SequentialImageGenerationOptions)
			}
			if _, ok := req.ExtraParams["sequential_image_generation_options"]; ok {
				t.Errorf("invalid passthrough value would overwrite the typed options at merge time: %v", req.ExtraParams)
			}
		})
	}
}

func TestBytePlusImageTypedBackgroundReachesArk(t *testing.T) {
	gen, err := byteplus.ToBytePlusImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
		Model:  "dola-seedream-5-0-pro-260628",
		Input:  &schemas.ImageGenerationInput{Prompt: "a logo"},
		Params: &schemas.ImageGenerationParameters{Background: schemas.Ptr("transparent")},
	})
	if err != nil {
		t.Fatalf("generation convert: %v", err)
	}
	genBody, _ := json.Marshal(gen)
	if gjson.GetBytes(genBody, "background").String() != "transparent" {
		t.Errorf("generation background should reach Ark, got %s", genBody)
	}
	edit, err := byteplus.ToBytePlusImageEditRequest(&schemas.BifrostImageEditRequest{
		Model:  "dola-seedream-5-0-pro-260628",
		Input:  &schemas.ImageEditInput{Prompt: "a logo", Images: []schemas.ImageInput{{URL: "https://example.com/a.png"}}},
		Params: &schemas.ImageEditParameters{Background: schemas.Ptr("transparent")},
	})
	if err != nil {
		t.Fatalf("edit convert: %v", err)
	}
	editBody, _ := json.Marshal(edit)
	if gjson.GetBytes(editBody, "background").String() != "transparent" {
		t.Errorf("edit background should reach Ark, got %s", editBody)
	}
}

func TestBytePlusConverterRefusalsAreBadRequests(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, nil)
	ctx := testContext(t)
	check := func(name string, bifrostErr *schemas.BifrostError, wantMessage string) {
		t.Helper()
		if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected a 400, got %+v", name, bifrostErr)
			return
		}
		if !strings.Contains(bifrostErr.Error.Message, wantMessage) {
			t.Errorf("%s: message = %q, want it to mention %q", name, bifrostErr.Error.Message, wantMessage)
		}
	}

	_, bifrostErr := provider.ImageEdit(ctx, testKey(), &schemas.BifrostImageEditRequest{
		Provider: schemas.BytePlus,
		Model:    "dola-seedream-5-0-pro-260628",
		Input:    &schemas.ImageEditInput{Prompt: "make it blue", Images: []schemas.ImageInput{{URL: "https://example.com/a.png"}}},
		Params:   &schemas.ImageEditParameters{Mask: []byte("mask")},
	})
	check("image edit mask", bifrostErr, "mask is not supported")

	_, bifrostErr = provider.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "dreamina-seedance-2-5-260628",
		Input:    &schemas.VideoGenerationInput{Prompt: "a dog", InputReference: schemas.Ptr("https://example.com/first.png")},
		Params:   &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"reference_images": []string{"https://example.com/r.png"}}},
	})
	check("video reference_images with first frame", bifrostErr, "cannot be combined")

	if len(captured()) != 0 {
		t.Errorf("refused requests should not reach Ark, got %d calls", len(captured()))
	}
}

func TestBytePlusImageGenerationAllImagesFailed(t *testing.T) {
	provider, _, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/images/generations": jsonResponse(http.StatusOK, `{"model":"seedream-4-5-251128","created":1,"data":[{"error":{"code":"OutputImageSensitiveContentDetected","message":"blocked"}}]}`),
	})

	_, bifrostErr := provider.ImageGeneration(testContext(t), testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "seedream-4-5-251128",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
	})
	if bifrostErr == nil {
		t.Fatal("expected an error when every image failed")
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "OutputImageSensitiveContentDetected" {
		t.Errorf("error code = %v", bifrostErr.Error.Code)
	}
}

func TestToBytePlusVideoGenerationRequest(t *testing.T) {
	req, err := byteplus.ToBytePlusVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "seedance-1-0-pro-fast-251015",
		Input: &schemas.VideoGenerationInput{Prompt: "a dog", InputReference: schemas.Ptr("https://example.com/first.png")},
		Params: &schemas.VideoGenerationParameters{
			Seconds:     schemas.Ptr("5"),
			Size:        "1280x720",
			Seed:        schemas.Ptr(7),
			Audio:       schemas.Ptr(true),
			ExtraParams: map[string]any{"last_frame": "https://example.com/last.png", "camera_fixed": true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Content) != 3 || req.Content[1].Role != "first_frame" || req.Content[2].Role != "last_frame" {
		t.Errorf("unexpected content: %+v", req.Content)
	}
	if req.Duration == nil || *req.Duration != 5 || req.Resolution == nil || *req.Resolution != "720p" || req.Ratio == nil || *req.Ratio != "16:9" {
		t.Errorf("duration/resolution/ratio not mapped: %v %v %v", req.Duration, req.Resolution, req.Ratio)
	}
	if req.GenerateAudio == nil || !*req.GenerateAudio || req.Seed == nil || *req.Seed != 7 {
		t.Errorf("audio/seed not mapped: %v %v", req.GenerateAudio, req.Seed)
	}
	if req.ExtraParams["camera_fixed"] != true {
		t.Errorf("camera_fixed should pass through: %v", req.ExtraParams)
	}

	_, err = byteplus.ToBytePlusVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model:  "seedance-1-0-pro-fast-251015",
		Input:  &schemas.VideoGenerationInput{Prompt: "a dog", InputReference: schemas.Ptr("https://example.com/first.png")},
		Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"reference_images": []string{"https://example.com/r.png"}}},
	})
	if err == nil {
		t.Error("reference_images combined with a first frame should be rejected")
	}
}

func TestBytePlusVideoLifecycle(t *testing.T) {
	const taskID = "cgt-20250331175019-68d9t"
	var serverURL string
	provider, captured, url := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/contents/generations/tasks": jsonResponse(http.StatusOK, `{"id":"`+taskID+`"}`),
		"GET /api/v3/contents/generations/tasks/" + taskID: func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(http.StatusOK, `{"id":"`+taskID+`","model":"seedance-1-0-pro-fast-251015","status":"succeeded","content":{"video_url":"`+serverURL+`/files/v.mp4"},"usage":{"completion_tokens":108900,"total_tokens":108900},"created_at":100,"updated_at":200,"resolution":"720p","ratio":"16:9","duration":5}`)(w, r)
		},
		"GET /api/v3/contents/generations/tasks":              jsonResponse(http.StatusOK, `{"items":[{"id":"`+taskID+`","model":"seedance-1-0-pro-fast-251015","status":"running","created_at":100},{"id":"cgt-2","model":"seedance-1-0-pro-fast-251015","status":"failed","error":{"code":"InputTextSensitiveContentDetected","message":"blocked"},"created_at":90}],"total":5}`),
		"DELETE /api/v3/contents/generations/tasks/" + taskID: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		"GET /files/v.mp4": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4data"))
		},
	})
	serverURL = url
	ctx := testContext(t)

	created, bifrostErr := provider.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "seedance-1-0-pro-fast-251015",
		Input:    &schemas.VideoGenerationInput{Prompt: "a dog"},
		Params:   &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("5")},
	})
	if bifrostErr != nil {
		t.Fatalf("VideoGeneration error: %+v", bifrostErr.Error)
	}
	if created.Status != schemas.VideoStatusQueued || !strings.HasPrefix(created.ID, taskID) || created.ID == taskID {
		t.Errorf("unexpected create response: id=%s status=%s", created.ID, created.Status)
	}
	if created.Seconds == nil || *created.Seconds != "5" {
		t.Errorf("seconds = %v, want 5", created.Seconds)
	}

	retrieved, bifrostErr := provider.VideoRetrieve(ctx, testKey(), &schemas.BifrostVideoRetrieveRequest{Provider: schemas.BytePlus, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve error: %+v", bifrostErr.Error)
	}
	if retrieved.Status != schemas.VideoStatusCompleted || retrieved.ID != created.ID || retrieved.CompletedAt == nil || *retrieved.CompletedAt != 200 {
		t.Errorf("unexpected retrieve response: %+v", retrieved)
	}
	if len(retrieved.Videos) != 1 || retrieved.Videos[0].URL == nil || !strings.HasSuffix(*retrieved.Videos[0].URL, "/files/v.mp4") {
		t.Errorf("unexpected videos: %+v", retrieved.Videos)
	}

	downloaded, bifrostErr := provider.VideoDownload(ctx, testKey(), &schemas.BifrostVideoDownloadRequest{Provider: schemas.BytePlus, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoDownload error: %+v", bifrostErr.Error)
	}
	if string(downloaded.Content) != "mp4data" || downloaded.ContentType != "video/mp4" {
		t.Errorf("unexpected download: %q %s", downloaded.Content, downloaded.ContentType)
	}

	listed, bifrostErr := provider.VideoList(ctx, testKey(), &schemas.BifrostVideoListRequest{Provider: schemas.BytePlus, Limit: schemas.Ptr(2)})
	if bifrostErr != nil {
		t.Fatalf("VideoList error: %+v", bifrostErr.Error)
	}
	if len(listed.Data) != 2 || listed.Data[0].Status != schemas.VideoStatusInProgress || listed.Data[1].Status != schemas.VideoStatusFailed {
		t.Errorf("unexpected list data: %+v", listed.Data)
	}
	if listed.Data[1].Error == nil || listed.Data[1].Error.Code != "InputTextSensitiveContentDetected" {
		t.Errorf("failed task error not mapped: %+v", listed.Data[1].Error)
	}
	if listed.HasMore == nil || *listed.HasMore || listed.Data[0].ID == taskID {
		t.Errorf("has_more must be false because no after cursor is accepted, and id must carry the suffix: has_more=%v id=%s", listed.HasMore, listed.Data[0].ID)
	}

	deleted, bifrostErr := provider.VideoDelete(ctx, testKey(), &schemas.BifrostVideoDeleteRequest{Provider: schemas.BytePlus, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoDelete error: %+v", bifrostErr.Error)
	}
	if !deleted.Deleted || deleted.ID != created.ID {
		t.Errorf("unexpected delete response: %+v", deleted)
	}

	for _, r := range captured() {
		if r.method == http.MethodGet && r.path == "/api/v3/contents/generations/tasks" && r.query != "page_num=1&page_size=2" {
			t.Errorf("list query = %q, want page_num=1&page_size=2", r.query)
		}
		if r.method == http.MethodPost && gjson.GetBytes(r.body, "content.0.text").String() != "a dog" {
			t.Errorf("content[0].text not sent: %s", r.body)
		}
	}
}

func TestBytePlusVideoListRejectsAfterCursor(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, nil)

	_, bifrostErr := provider.VideoList(testContext(t), testKey(), &schemas.BifrostVideoListRequest{Provider: schemas.BytePlus, After: schemas.Ptr("cgt-1")})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400 for an after cursor, got %+v", bifrostErr)
	}
	if len(captured()) != 0 {
		t.Errorf("rejected list should not call upstream")
	}
}

func TestToBifrostVideoGenerationResponseTerminalStatuses(t *testing.T) {
	for _, status := range []byteplus.BytePlusVideoTaskStatus{byteplus.BytePlusVideoTaskStatusFailed, byteplus.BytePlusVideoTaskStatusCancelled, byteplus.BytePlusVideoTaskStatusExpired} {
		resp, bifrostErr := byteplus.ToBifrostVideoGenerationResponse(&byteplus.BytePlusVideoTask{ID: "t", Status: status})
		if bifrostErr != nil {
			t.Fatalf("unexpected error: %+v", bifrostErr)
		}
		if resp.Status != schemas.VideoStatusFailed || resp.Error == nil || resp.Error.Code != string(status) {
			t.Errorf("%s: status=%s error=%+v", status, resp.Status, resp.Error)
		}
	}
}

func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

func sseResponse(events ...string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, event := range events {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}
}

func drainStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return chunks
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		case <-timeout.C:
			t.Fatal("stream did not close")
		}
	}
}

const (
	seedreamStreamImage0 = `{"type":"image_generation.partial_succeeded","model":"seedream-4-5-251128","created":1757396757,"image_index":0,"url":"https://cdn.example/0.jpeg","size":"2048x2048"}`
	seedreamStreamFailed = `{"type":"image_generation.partial_failed","model":"seedream-4-5-251128","created":1757396757,"image_index":1,"error":{"code":"OutputImageSensitiveContentDetected","message":"blocked"}}`
	seedreamStreamImage2 = `{"type":"image_generation.partial_succeeded","model":"seedream-4-5-251128","created":1757396758,"image_index":2,"url":"https://cdn.example/2.jpeg","size":"2048x2048"}`
	seedreamStreamDone   = `{"type":"image_generation.completed","model":"seedream-4-5-251128","created":1757396759,"usage":{"generated_images":2,"output_tokens":32768,"total_tokens":32768}}`
)

// Seedream streams each finished image as its own event and ends with a usage-only completed event.
func TestBytePlusImageGenerationStream(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/images/generations": sseResponse(seedreamStreamImage0, seedreamStreamFailed, seedreamStreamImage2, seedreamStreamDone),
	})

	stream, bifrostErr := provider.ImageGenerationStream(testContext(t), passthroughPostHook, nil, testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "seedream-4-5-251128",
		Input:    &schemas.ImageGenerationInput{Prompt: "four seasons"},
		Params:   &schemas.ImageGenerationParameters{N: schemas.Ptr(3)},
	})
	if bifrostErr != nil {
		t.Fatalf("ImageGenerationStream error: %+v", bifrostErr.Error)
	}
	chunks := drainStream(t, stream)

	body := captured()[0].body
	if !gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "sequential_image_generation").String() != "auto" {
		t.Errorf("stream and sequential generation should be sent, got %s", body)
	}
	if len(chunks) != 3 {
		t.Fatalf("want 2 image chunks and 1 completed chunk, got %d", len(chunks))
	}
	for i, want := range []struct {
		index int
		url   string
	}{{0, "https://cdn.example/0.jpeg"}, {2, "https://cdn.example/2.jpeg"}} {
		chunk := chunks[i].BifrostImageGenerationStreamResponse
		if chunk == nil || chunk.Type != schemas.ImageGenerationEventTypePartial || chunk.Index != want.index || chunk.URL != want.url || chunk.Size != "2048x2048" {
			t.Errorf("chunk %d = %+v, want partial image %d at %s", i, chunk, want.index, want.url)
		}
	}
	final := chunks[2].BifrostImageGenerationStreamResponse
	if final == nil || final.Type != schemas.ImageGenerationEventTypeCompleted {
		t.Fatalf("last chunk should be completed, got %+v", chunks[2])
	}
	if final.Usage == nil || final.Usage.OutputTokens != 32768 || final.Usage.OutputTokensDetails == nil || final.Usage.OutputTokensDetails.NImages != 2 {
		t.Errorf("completed usage not mapped: %+v", final.Usage)
	}
	if final.URL != "https://cdn.example/2.jpeg" {
		t.Errorf("completed chunk should carry the last image, got %q", final.URL)
	}
}

func TestBytePlusImageEditStreamUsesEditEvents(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/images/generations": sseResponse(seedreamStreamImage0, seedreamStreamDone),
	})

	stream, bifrostErr := provider.ImageEditStream(testContext(t), passthroughPostHook, nil, testKey(), &schemas.BifrostImageEditRequest{
		Provider: schemas.BytePlus,
		Model:    "seedream-4-5-251128",
		Input:    &schemas.ImageEditInput{Prompt: "make it blue", Images: []schemas.ImageInput{{URL: "https://example.com/a.png"}}},
	})
	if bifrostErr != nil {
		t.Fatalf("ImageEditStream error: %+v", bifrostErr.Error)
	}
	chunks := drainStream(t, stream)

	if body := captured()[0].body; !gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "image.0").String() != "https://example.com/a.png" {
		t.Errorf("edit stream body = %s", body)
	}
	if len(chunks) != 2 || chunks[0].BifrostImageGenerationStreamResponse == nil || chunks[0].BifrostImageGenerationStreamResponse.Type != schemas.ImageEditEventTypePartial ||
		chunks[1].BifrostImageGenerationStreamResponse == nil || chunks[1].BifrostImageGenerationStreamResponse.Type != schemas.ImageEditEventTypeCompleted {
		t.Fatalf("edit stream should use image_edit events, got %+v", chunks)
	}
}

// Ark stops the stream after an internal error, so it ends the Bifrost stream too. A moderation miss on one image does not.
func TestBytePlusImageGenerationStreamErrors(t *testing.T) {
	cases := []struct {
		name     string
		events   []string
		wantCode string
		wantType string
	}{
		{"internal error ends the stream", []string{seedreamStreamImage0, `{"type":"image_generation.partial_failed","model":"seedream-4-5-251128","created":1,"image_index":1,"error":{"code":"InternalServiceError","message":"internal"}}`}, "InternalServiceError", "server_error"},
		{"every image failed", []string{seedreamStreamFailed, `{"type":"image_generation.completed","model":"seedream-4-5-251128","created":1,"usage":{"generated_images":0,"output_tokens":0,"total_tokens":0}}`}, "OutputImageSensitiveContentDetected", "content_filter"},
		{"request-level error event", []string{`{"error":{"code":"BadRequest","message":"missing required parameter"}}`}, "BadRequest", "invalid_request_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, _, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
				"POST /api/v3/images/generations": sseResponse(tc.events...),
			})
			stream, bifrostErr := provider.ImageGenerationStream(testContext(t), passthroughPostHook, nil, testKey(), &schemas.BifrostImageGenerationRequest{
				Provider: schemas.BytePlus,
				Model:    "seedream-4-5-251128",
				Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
			})
			if bifrostErr != nil {
				t.Fatalf("ImageGenerationStream error: %+v", bifrostErr.Error)
			}
			chunks := drainStream(t, stream)
			if len(chunks) == 0 {
				t.Fatal("expected an error chunk")
			}
			last := chunks[len(chunks)-1].BifrostError
			if last == nil || last.Error == nil || last.Error.Code == nil || *last.Error.Code != tc.wantCode {
				t.Fatalf("last chunk should be an error with code %s, got %+v", tc.wantCode, chunks[len(chunks)-1])
			}
			if last.Error.Type == nil || *last.Error.Type != tc.wantType {
				t.Errorf("error type = %v, want %s", last.Error.Type, tc.wantType)
			}
		})
	}
}

// Seedream-only fields Bifrost has no typed slot for must reach Ark without the passthrough header.
func TestBytePlusImageExtraParamsReachArk(t *testing.T) {
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/images/generations": jsonResponse(http.StatusOK, `{"model":"dola-seedream-5-0-pro-260628","created":1,"data":[{"url":"https://cdn.example/1.png"}]}`),
	})
	if _, bifrostErr := provider.ImageGeneration(testContext(t), testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "dola-seedream-5-0-pro-260628",
		Input:    &schemas.ImageGenerationInput{Prompt: "a logo"},
		Params:   &schemas.ImageGenerationParameters{ExtraParams: map[string]interface{}{"background": "transparent", "optimize_prompt_options": map[string]interface{}{"mode": "fast"}}},
	}); bifrostErr != nil {
		t.Fatalf("ImageGeneration error: %+v", bifrostErr.Error)
	}
	body := captured()[0].body
	if gjson.GetBytes(body, "background").String() != "transparent" || gjson.GetBytes(body, "optimize_prompt_options.mode").String() != "fast" {
		t.Errorf("Seedream extra params should reach Ark, got %s", body)
	}
}

func TestToBytePlusVideoGenerationRequestOmniReferences(t *testing.T) {
	req, err := byteplus.ToBytePlusVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "dreamina-seedance-2-5-260628",
		Input: &schemas.VideoGenerationInput{Prompt: "dance to the beat", VideoURI: schemas.Ptr("https://example.com/a.mp4")},
		Params: &schemas.VideoGenerationParameters{
			OutputFormat: schemas.Ptr("mov"),
			ExtraParams: map[string]any{
				"reference_videos": []string{"https://example.com/b.mp4", "asset://asset-123"},
				"reference_audios": []string{"https://example.com/beat.mp3"},
				"reference_images": []string{"asset://asset-456"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var videos, audios, images []string
	for _, item := range req.Content {
		switch item.Role {
		case "reference_video":
			videos = append(videos, item.VideoURL.URL)
		case "reference_audio":
			if item.Type != "audio_url" || item.AudioURL == nil {
				t.Errorf("reference audio should be an audio_url item: %+v", item)
				continue
			}
			audios = append(audios, item.AudioURL.URL)
		case "reference_image":
			images = append(images, item.ImageURL.URL)
		}
	}
	if strings.Join(videos, ",") != "https://example.com/a.mp4,https://example.com/b.mp4,asset://asset-123" {
		t.Errorf("reference videos = %v", videos)
	}
	if strings.Join(audios, ",") != "https://example.com/beat.mp3" || strings.Join(images, ",") != "asset://asset-456" {
		t.Errorf("reference audios = %v, images = %v", audios, images)
	}
	if req.OutputFormat == nil || *req.OutputFormat != "mov" {
		t.Errorf("output_format not mapped: %v", req.OutputFormat)
	}
	for _, key := range []string{"reference_videos", "reference_audios", "reference_images"} {
		if _, ok := req.ExtraParams[key]; ok {
			t.Errorf("%s should be lifted into content, not passed through", key)
		}
	}
}

// A draft is turned into a final video by sending its task id, as Bifrost returned it; the prompt is optional then.
func TestToBytePlusVideoGenerationRequestDraftTask(t *testing.T) {
	req, err := byteplus.ToBytePlusVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model:  "dreamina-seedance-2-5-260628",
		Input:  &schemas.VideoGenerationInput{},
		Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"draft_task_id": "cgt-draft-1:byteplus"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Content) != 1 || req.Content[0].Type != "draft_task" || req.Content[0].DraftTask == nil || req.Content[0].DraftTask.ID != "cgt-draft-1" {
		t.Errorf("draft task content = %+v", req.Content)
	}

	if _, err := byteplus.ToBytePlusVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{Model: "m", Input: &schemas.VideoGenerationInput{}}); err == nil {
		t.Error("a request with neither a prompt nor a draft task should be rejected")
	}
}

// Seedance task options with no Bifrost field must reach Ark, and a requested last frame must come back.
func TestBytePlusVideoExtraParamsAndLastFrame(t *testing.T) {
	const taskID = "cgt-lastframe"
	provider, captured, _ := newMockBytePlus(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v3/contents/generations/tasks":          jsonResponse(http.StatusOK, `{"id":"`+taskID+`"}`),
		"GET /api/v3/contents/generations/tasks/" + taskID: jsonResponse(http.StatusOK, `{"id":"`+taskID+`","model":"dreamina-seedance-2-5-260628","status":"succeeded","content":{"video_url":"https://cdn.example/v.mov","last_frame_url":"https://cdn.example/last.jpeg"},"created_at":100,"updated_at":200,"resolution":"720p","duration":5}`),
	})
	ctx := testContext(t)

	created, bifrostErr := provider.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{
		Provider: schemas.BytePlus,
		Model:    "dreamina-seedance-2-5-260628",
		Input:    &schemas.VideoGenerationInput{Prompt: "a dog"},
		Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{
			"return_last_frame": true, "service_tier": "flex", "draft": true, "priority": 5, "omni_reference_task_type": "extend",
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("VideoGeneration error: %+v", bifrostErr.Error)
	}
	body := captured()[0].body
	for _, field := range []string{"return_last_frame", "service_tier", "draft", "priority", "omni_reference_task_type"} {
		if !gjson.GetBytes(body, field).Exists() {
			t.Errorf("%s should reach Ark, got %s", field, body)
		}
	}

	retrieved, bifrostErr := provider.VideoRetrieve(ctx, testKey(), &schemas.BifrostVideoRetrieveRequest{Provider: schemas.BytePlus, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve error: %+v", bifrostErr.Error)
	}
	if len(retrieved.Videos) != 2 {
		t.Fatalf("want the video and its last frame, got %+v", retrieved.Videos)
	}
	if retrieved.Videos[0].ContentType != "video/quicktime" || *retrieved.Videos[0].URL != "https://cdn.example/v.mov" {
		t.Errorf("video output = %+v", retrieved.Videos[0])
	}
	if retrieved.Videos[1].ContentType != "image/jpeg" || *retrieved.Videos[1].URL != "https://cdn.example/last.jpeg" {
		t.Errorf("last frame output = %+v", retrieved.Videos[1])
	}
}

func BenchmarkToBytePlusVideoGenerationRequest(b *testing.B) {
	req := &schemas.BifrostVideoGenerationRequest{
		Model:  "dreamina-seedance-2-5-260628",
		Input:  &schemas.VideoGenerationInput{Prompt: "a dog", InputReference: schemas.Ptr("https://example.com/first.png")},
		Params: &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("5"), Size: "1280x720"},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := byteplus.ToBytePlusVideoGenerationRequest(req); err != nil {
			b.Fatal(err)
		}
	}
}

package minimax_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/minimax"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func TestMinimax(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")) == "" {
		t.Skip("Skipping MiniMax tests because MINIMAX_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.MiniMax,
		ChatModel: "MiniMax-M3",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.MiniMax, Model: "MiniMax-M3"},
		},
		ReasoningModel:       "MiniMax-M3",
		VisionModel:          "MiniMax-M3",
		ImageGenerationModel: "image-01",
		VideoGenerationModel: "MiniMax-H3-Max",
		SpeechSynthesisModel: "speech-2.8-turbo",
		TranscriptionModel:   "asr-1.0",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			CompleteEnd2End:       true,
			ListModels:            true,
			Reasoning:             true,
			ImageGeneration:       true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			VideoGeneration:       os.Getenv("MINIMAX_RUN_VIDEO_TESTS") == "1", // H3 billing is per output second
		},
	}

	t.Run("MinimaxTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

type capturedRequest struct {
	method string
	path   string
	auth   string
	header http.Header
	body   []byte
}

// newMockMiniMax starts a server that records each request and replies with the response registered for its path.
func newMockMiniMax(t *testing.T, responses map[string]func(w http.ResponseWriter)) (*minimax.MiniMaxProvider, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), header: r.Header.Clone(), body: body})
		mu.Unlock()
		if respond, ok := responses[r.Method+" "+r.URL.Path]; ok {
			respond(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	provider, err := minimax.NewMiniMaxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewMiniMaxProvider: %v", err)
	}
	return provider, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}
}

func jsonResponse(status int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
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
		Provider: schemas.MiniMax,
		Model:    "MiniMax-M3",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   params,
	}
}

const chatSuccessBody = `{"id":"c1","object":"chat.completion","created":1,"model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hello","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"base_resp":{"status_code":0,"status_msg":""}}`

func TestMiniMaxChatSendsReasoningSplitAndReadsReasoningContent(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
	})

	resp, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(nil))
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
	if !gjson.GetBytes(reqs[0].body, "reasoning_split").Bool() {
		t.Errorf("reasoning_split not sent as true: %s", reqs[0].body)
	}
	msg := resp.Choices[0].ChatNonStreamResponseChoice.Message
	if msg.ChatAssistantMessage == nil || msg.ChatAssistantMessage.Reasoning == nil || *msg.ChatAssistantMessage.Reasoning != "thinking" {
		t.Errorf("reasoning_content not mapped to Reasoning: %+v", msg.ChatAssistantMessage)
	}
}

func TestMiniMaxChatKeepsCallerReasoningSplit(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
	})
	params := &schemas.ChatParameters{ExtraParams: map[string]interface{}{"reasoning_split": false}}

	if _, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(params)); bifrostErr != nil {
		t.Fatalf("ChatCompletion error: %+v", bifrostErr.Error)
	}
	split := gjson.GetBytes(captured()[0].body, "reasoning_split")
	if !split.Exists() || split.Bool() {
		t.Errorf("caller's reasoning_split=false was not preserved: %s", captured()[0].body)
	}
}

func TestMiniMaxChatReasoningEffortNone(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		wantThinking bool
	}{
		{name: "M3 disables thinking", model: "MiniMax-M3", wantThinking: true},
		{name: "M3.1 drops effort without disabling", model: "MiniMax-M3.1-Flash-Preview", wantThinking: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
				"POST /v1/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
			})
			reasoning := &schemas.ChatReasoning{Effort: schemas.Ptr("none")}
			req := userChatRequest(&schemas.ChatParameters{Reasoning: reasoning})
			req.Model = tt.model

			if _, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), req); bifrostErr != nil {
				t.Fatalf("ChatCompletion error: %+v", bifrostErr.Error)
			}
			body := captured()[0].body
			if gjson.GetBytes(body, "reasoning_effort").Exists() {
				t.Errorf("reasoning_effort none should not be sent: %s", body)
			}
			if got := gjson.GetBytes(body, "thinking.type").String() == "disabled"; got != tt.wantThinking {
				t.Errorf("thinking disabled = %v, want %v: %s", got, tt.wantThinking, body)
			}
			if reasoning.Effort == nil || *reasoning.Effort != "none" {
				t.Errorf("caller's reasoning params were mutated: %+v", reasoning)
			}
		})
	}
}

func TestMiniMaxChatReasoningEffortLadder(t *testing.T) {
	tests := []struct{ effort, want string }{
		{effort: "max", want: "max"},
		{effort: "xhigh", want: "xhigh"},
		{effort: "high", want: "high"},
		{effort: "minimal", want: "low"},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
				"POST /v1/chat/completions": jsonResponse(http.StatusOK, chatSuccessBody),
			})
			req := userChatRequest(&schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(tt.effort)}})

			if _, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), req); bifrostErr != nil {
				t.Fatalf("ChatCompletion error: %+v", bifrostErr.Error)
			}
			if got := gjson.GetBytes(captured()[0].body, "reasoning_effort").String(); got != tt.want {
				t.Errorf("reasoning_effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMiniMaxChatBaseRespErrorOn200(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/chat/completions": jsonResponse(http.StatusOK, `{"id":"","choices":null,"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`),
	})

	_, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(nil))
	if bifrostErr == nil {
		t.Fatal("expected an error for non-zero base_resp")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status code = %v, want 402", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "insufficient balance" {
		t.Errorf("message = %+v, want insufficient balance", bifrostErr.Error)
	}
}

func TestMiniMaxChatStreamBaseRespErrorOn200(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/chat/completions": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"\",\"choices\":null,\"base_resp\":{\"status_code\":1008,\"status_msg\":\"insufficient balance\"}}\n\ndata: [DONE]\n\n"))
		},
	})

	stream, bifrostErr := provider.ChatCompletionStream(testContext(t), func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return resp, err
	}, nil, testKey(), userChatRequest(nil))
	if bifrostErr != nil {
		t.Fatalf("unexpected setup error: %+v", bifrostErr)
	}

	var streamErr *schemas.BifrostError
	for chunk := range stream {
		if chunk != nil && chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
		}
	}
	if streamErr == nil {
		t.Fatal("expected a stream error for non-zero base_resp")
	}
	if streamErr.StatusCode == nil || *streamErr.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status code = %v, want 402", streamErr.StatusCode)
	}
	if streamErr.Error == nil || streamErr.Error.Message != "insufficient balance" {
		t.Errorf("message = %+v, want insufficient balance", streamErr.Error)
	}
}

func TestMiniMaxHTTPErrorUsesErrorBody(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/chat/completions": jsonResponse(http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid api key","http_code":"401"},"request_id":"r1"}`),
	})

	_, bifrostErr := provider.ChatCompletion(testContext(t), testKey(), userChatRequest(nil))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected a 401 error, got %+v", bifrostErr)
	}
	if bifrostErr.Error.Message != "invalid api key" {
		t.Errorf("message = %q, want invalid api key", bifrostErr.Error.Message)
	}
}

func TestToMiniMaxImageGenerationRequest(t *testing.T) {
	tests := []struct {
		name            string
		params          *schemas.ImageGenerationParameters
		wantWidth       int64
		wantAspectRatio string
		wantFormat      string
	}{
		{name: "valid size becomes width and height", params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x768")}, wantWidth: 1024},
		{name: "out of range size snaps to aspect ratio", params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("2560x1440")}, wantAspectRatio: "16:9"},
		{name: "explicit aspect ratio wins", params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024"), AspectRatio: schemas.Ptr("9:16")}, wantAspectRatio: "9:16"},
		{name: "b64_json maps to base64", params: &schemas.ImageGenerationParameters{ResponseFormat: schemas.Ptr("b64_json")}, wantFormat: "base64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := minimax.ToMiniMaxImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
				Model:  "image-01",
				Input:  &schemas.ImageGenerationInput{Prompt: "a cat"},
				Params: tt.params,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantWidth != 0 && (req.Width == nil || int64(*req.Width) != tt.wantWidth) {
				t.Errorf("width = %v, want %d", req.Width, tt.wantWidth)
			}
			if tt.wantAspectRatio != "" && (req.AspectRatio == nil || *req.AspectRatio != tt.wantAspectRatio) {
				t.Errorf("aspect_ratio = %v, want %s", req.AspectRatio, tt.wantAspectRatio)
			}
			if tt.wantAspectRatio != "" && req.Width != nil {
				t.Errorf("width should be unset when aspect_ratio is used, got %d", *req.Width)
			}
			if tt.wantFormat != "" && (req.ResponseFormat == nil || *req.ResponseFormat != tt.wantFormat) {
				t.Errorf("response_format = %v, want %s", req.ResponseFormat, tt.wantFormat)
			}
		})
	}
}

func TestMiniMaxImageGeneration(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/image_generation": jsonResponse(http.StatusOK, `{"id":"img1","data":{"image_urls":["https://cdn.example/a.png","https://cdn.example/b.png"]},"metadata":{"success_count":2,"failed_count":0},"base_resp":{"status_code":0,"status_msg":"success"}}`),
	})

	resp, bifrostErr := provider.ImageGeneration(testContext(t), testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.MiniMax,
		Model:    "image-01",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params:   &schemas.ImageGenerationParameters{N: schemas.Ptr(2)},
	})
	if bifrostErr != nil {
		t.Fatalf("ImageGeneration error: %+v", bifrostErr.Error)
	}
	if len(resp.Data) != 2 || resp.Data[1].URL != "https://cdn.example/b.png" || resp.Data[1].Index != 1 {
		t.Errorf("unexpected data: %+v", resp.Data)
	}
	if got := gjson.GetBytes(captured()[0].body, "n").Int(); got != 2 {
		t.Errorf("n = %d, want 2", got)
	}
}

func TestMiniMaxImageGenerationBaseRespError(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/image_generation": jsonResponse(http.StatusOK, `{"id":"img1","data":{},"base_resp":{"status_code":1026,"status_msg":"input new_sensitive"}}`),
	})

	_, bifrostErr := provider.ImageGeneration(testContext(t), testKey(), &schemas.BifrostImageGenerationRequest{
		Provider: schemas.MiniMax,
		Model:    "image-01",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
	})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400 content filter error, got %+v", bifrostErr)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "1026" {
		t.Errorf("code = %v, want 1026", bifrostErr.Error.Code)
	}
}

func TestToMiniMaxVideoGenerationRequest(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		input          *schemas.VideoGenerationInput
		params         *schemas.VideoGenerationParameters
		wantDuration   int
		wantResolution string
		wantRatio      string
		wantContent    int
	}{
		{
			name:  "text to video defaults",
			model: "MiniMax-H3", input: &schemas.VideoGenerationInput{Prompt: "a dog"},
			wantDuration: 6, wantResolution: "768P", wantRatio: "16:9", wantContent: 1,
		},
		{
			name:  "image to video uses adaptive ratio",
			model: "MiniMax-H3", input: &schemas.VideoGenerationInput{Prompt: "a dog", InputReference: schemas.Ptr("https://example.com/a.png")},
			wantDuration: 6, wantResolution: "768P", wantRatio: "adaptive", wantContent: 2,
		},
		{
			name:  "size and seconds map with clamping on H3-Max",
			model: "MiniMax-H3-Max", input: &schemas.VideoGenerationInput{Prompt: "a dog"},
			params:       &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("4"), Size: "480x854"},
			wantDuration: 5, wantResolution: "480P", wantRatio: "9:16", wantContent: 1,
		},
		{
			name:  "large size on H3 picks 2K",
			model: "MiniMax-H3", input: &schemas.VideoGenerationInput{Prompt: "a dog"},
			params:       &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("20"), Size: "2560x1440"},
			wantDuration: 15, wantResolution: "2K", wantRatio: "16:9", wantContent: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := minimax.ToMiniMaxVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{Model: tt.model, Input: tt.input, Params: tt.params})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Duration != tt.wantDuration {
				t.Errorf("duration = %d, want %d", req.Duration, tt.wantDuration)
			}
			if req.Resolution != tt.wantResolution {
				t.Errorf("resolution = %s, want %s", req.Resolution, tt.wantResolution)
			}
			if req.Ratio == nil || *req.Ratio != tt.wantRatio {
				t.Errorf("ratio = %v, want %s", req.Ratio, tt.wantRatio)
			}
			if len(req.Content) != tt.wantContent {
				t.Errorf("content items = %d, want %d", len(req.Content), tt.wantContent)
			}
		})
	}
}

func TestMiniMaxVideoLifecycle(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v2/video_generation":                      jsonResponse(http.StatusOK, `{"task_id":"424010985738629"}`),
		"GET /v2/query/video_generation/424010985738629": jsonResponse(http.StatusOK, `{"task":{"id":"424010985738629","model":"MiniMax-H3","status":"succeeded","created_at":100,"updated_at":200,"content":{"url":"https://cdn.example/v.mp4"},"resolution":"768P","duration":6}}`),
		"DELETE /v2/video_generation/424010985738629":    jsonResponse(http.StatusOK, `{"task_id":"424010985738629","action":"deleted","status":"deleted"}`),
	})
	ctx := testContext(t)

	created, bifrostErr := provider.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{
		Provider: schemas.MiniMax,
		Model:    "MiniMax-H3",
		Input:    &schemas.VideoGenerationInput{Prompt: "a dog"},
	})
	if bifrostErr != nil {
		t.Fatalf("VideoGeneration error: %+v", bifrostErr.Error)
	}
	if created.Status != schemas.VideoStatusQueued || !strings.HasPrefix(created.ID, "424010985738629") || created.ID == "424010985738629" {
		t.Errorf("unexpected create response: id=%s status=%s", created.ID, created.Status)
	}
	if created.Seconds == nil || *created.Seconds != "6" {
		t.Errorf("seconds = %v, want 6", created.Seconds)
	}

	retrieved, bifrostErr := provider.VideoRetrieve(ctx, testKey(), &schemas.BifrostVideoRetrieveRequest{Provider: schemas.MiniMax, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve error: %+v", bifrostErr.Error)
	}
	if retrieved.Status != schemas.VideoStatusCompleted || retrieved.ID != created.ID {
		t.Errorf("unexpected retrieve response: id=%s status=%s", retrieved.ID, retrieved.Status)
	}
	if len(retrieved.Videos) != 1 || retrieved.Videos[0].URL == nil || *retrieved.Videos[0].URL != "https://cdn.example/v.mp4" {
		t.Errorf("unexpected videos: %+v", retrieved.Videos)
	}

	deleted, bifrostErr := provider.VideoDelete(ctx, testKey(), &schemas.BifrostVideoDeleteRequest{Provider: schemas.MiniMax, ID: created.ID})
	if bifrostErr != nil {
		t.Fatalf("VideoDelete error: %+v", bifrostErr.Error)
	}
	if !deleted.Deleted || deleted.ID != created.ID {
		t.Errorf("unexpected delete response: %+v", deleted)
	}

	reqs := captured()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 upstream requests, got %d", len(reqs))
	}
	if got := gjson.GetBytes(reqs[0].body, "content.0.text").String(); got != "a dog" {
		t.Errorf("content[0].text = %q, want a dog", got)
	}
}

func TestToBifrostVideoGenerationResponseFailedTask(t *testing.T) {
	resp, bifrostErr := minimax.ToBifrostVideoGenerationResponse(&minimax.MiniMaxVideoTask{
		ID:     "t1",
		Status: minimax.MiniMaxVideoTaskStatusFailed,
		Error:  &minimax.MiniMaxTaskError{Code: "content_policy", Message: "blocked"},
	})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %+v", bifrostErr.Error)
	}
	if resp.Status != schemas.VideoStatusFailed || resp.Error == nil || resp.Error.Code != "content_policy" || resp.Error.Message != "blocked" {
		t.Errorf("unexpected failed task mapping: %+v", resp)
	}
}

func passthroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func sseResponse(events ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, e := range events {
			_, _ = w.Write([]byte("data: " + e + "\n\n"))
		}
	}
}

func collectStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// multipartFields parses a captured multipart request into its text fields and file bytes.
func multipartFields(t *testing.T, req capturedRequest) (map[string]string, []byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(req.header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(req.body), params["boundary"])
	fields := map[string]string{}
	var file []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			file = data
			continue
		}
		fields[part.FormName()] = string(data)
	}
	return fields, file
}

func speechRequest(voice string, params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
	if params == nil {
		params = &schemas.SpeechParameters{}
	}
	if voice != "" {
		params.VoiceConfig = &schemas.SpeechVoiceInput{Voice: schemas.Ptr(voice)}
	}
	return &schemas.BifrostSpeechRequest{
		Provider: schemas.MiniMax,
		Model:    "speech-2.8-turbo",
		Input:    &schemas.SpeechInput{Input: "Hello there"},
		Params:   params,
	}
}

func TestToMiniMaxSpeechRequest(t *testing.T) {
	t.Run("voice is required", func(t *testing.T) {
		if _, err := minimax.ToMiniMaxSpeechRequest(speechRequest("", nil), false); err == nil || !strings.Contains(err.Error(), "voice is required") {
			t.Fatalf("expected a voice error, got %v", err)
		}
	})
	t.Run("unsupported format is rejected", func(t *testing.T) {
		if _, err := minimax.ToMiniMaxSpeechRequest(speechRequest("English_Graceful_Lady", &schemas.SpeechParameters{ResponseFormat: "aac"}), false); err == nil {
			t.Fatal("expected aac to be rejected")
		}
	})
	t.Run("tuning extra params move into voice and audio settings", func(t *testing.T) {
		req, err := minimax.ToMiniMaxSpeechRequest(speechRequest("English_Graceful_Lady", &schemas.SpeechParameters{
			ResponseFormat: "wav",
			Speed:          schemas.Ptr(1.25),
			ExtraParams:    map[string]interface{}{"vol": 2.0, "pitch": 3.0, "emotion": "happy", "sample_rate": 24000.0, "language_boost": "English", "subtitle_enable": true},
		}), false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		vs := req.VoiceSetting
		if vs.VoiceID != "English_Graceful_Lady" || *vs.Speed != 1.25 || *vs.Vol != 2 || *vs.Pitch != 3 || *vs.Emotion != "happy" {
			t.Errorf("unexpected voice_setting: %+v", vs)
		}
		if req.AudioSetting == nil || *req.AudioSetting.Format != "wav" || *req.AudioSetting.SampleRate != 24000 {
			t.Errorf("unexpected audio_setting: %+v", req.AudioSetting)
		}
		if req.LanguageBoost == nil || *req.LanguageBoost != "English" {
			t.Errorf("language_boost = %v", req.LanguageBoost)
		}
		if _, ok := req.ExtraParams["subtitle_enable"]; !ok || len(req.ExtraParams) != 1 {
			t.Errorf("only unknown keys should stay in extra params, got %v", req.ExtraParams)
		}
		if req.OutputFormat == nil || *req.OutputFormat != "hex" || req.Stream {
			t.Errorf("non-streaming request should ask for hex output: %+v", req)
		}
	})
	t.Run("stream excludes the aggregated final audio", func(t *testing.T) {
		req, err := minimax.ToMiniMaxSpeechRequest(speechRequest("English_Graceful_Lady", nil), true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !req.Stream || req.StreamOptions == nil || !req.StreamOptions.ExcludeAggregatedAudio || req.OutputFormat != nil {
			t.Errorf("unexpected stream request: %+v", req)
		}
	})
}

func TestMiniMaxSpeech(t *testing.T) {
	audio := []byte("ID3fake-mp3")
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/t2a_v2": jsonResponse(http.StatusOK, `{"data":{"audio":"`+hex.EncodeToString(audio)+`","status":2},"extra_info":{"audio_length":900,"usage_characters":11},"trace_id":"t1","base_resp":{"status_code":0,"status_msg":"success"}}`),
	})

	resp, bifrostErr := provider.Speech(testContext(t), testKey(), speechRequest("English_Graceful_Lady", nil))
	if bifrostErr != nil {
		t.Fatalf("Speech error: %+v", bifrostErr.Error)
	}
	if !bytes.Equal(resp.Audio, audio) {
		t.Errorf("audio = %q, want %q", resp.Audio, audio)
	}
	if resp.Usage == nil || resp.Usage.InputChars != 11 {
		t.Errorf("usage = %+v, want 11 input chars", resp.Usage)
	}
	body := captured()[0].body
	if gjson.GetBytes(body, "voice_setting.voice_id").String() != "English_Graceful_Lady" || gjson.GetBytes(body, "output_format").String() != "hex" {
		t.Errorf("unexpected request body: %s", body)
	}
}

func TestMiniMaxSpeechBaseRespErrorOn200(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/t2a_v2": jsonResponse(http.StatusOK, `{"base_resp":{"status_code":2054,"status_msg":"voice id not exist"}}`),
	})

	_, bifrostErr := provider.Speech(testContext(t), testKey(), speechRequest("not_a_voice", nil))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400, got %+v", bifrostErr)
	}
	if bifrostErr.Error.Message != "voice id not exist" {
		t.Errorf("message = %q", bifrostErr.Error.Message)
	}
}

func TestMiniMaxSpeechStream(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/t2a_v2": sseResponse(
			`{"data":{"audio":"aa","status":1},"trace_id":"t1","base_resp":{"status_code":0,"status_msg":""}}`,
			`{"data":{"audio":"bbcc","status":1},"trace_id":"t1","base_resp":{"status_code":0,"status_msg":""}}`,
			`{"data":{"audio":"","status":1},"trace_id":"t1","base_resp":{"status_code":0,"status_msg":""}}`,
			`{"data":{"audio":"","status":2},"extra_info":{"audio_length":900,"usage_characters":11},"trace_id":"t1","base_resp":{"status_code":0,"status_msg":"success"}}`,
		),
	})

	stream, bifrostErr := provider.SpeechStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), speechRequest("English_Graceful_Lady", nil))
	if bifrostErr != nil {
		t.Fatalf("SpeechStream error: %+v", bifrostErr.Error)
	}
	chunks := collectStream(t, stream)
	var audio []byte
	var types []schemas.SpeechStreamResponseType
	var usage *schemas.SpeechUsage
	for _, c := range chunks {
		if c.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", c.BifrostError.Error)
		}
		types = append(types, c.BifrostSpeechStreamResponse.Type)
		audio = append(audio, c.BifrostSpeechStreamResponse.Audio...)
		if c.BifrostSpeechStreamResponse.Usage != nil {
			usage = c.BifrostSpeechStreamResponse.Usage
		}
	}
	want := []schemas.SpeechStreamResponseType{schemas.SpeechStreamResponseTypeDelta, schemas.SpeechStreamResponseTypeDelta, schemas.SpeechStreamResponseTypeDone}
	if len(types) != len(want) || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Errorf("event types = %v, want %v", types, want)
	}
	if !bytes.Equal(audio, []byte{0xaa, 0xbb, 0xcc}) {
		t.Errorf("audio = %x, want aabbcc", audio)
	}
	if usage == nil || usage.InputChars != 11 {
		t.Errorf("usage = %+v, want 11 input chars", usage)
	}
	if !gjson.GetBytes(captured()[0].body, "stream_options.exclude_aggregated_audio").Bool() {
		t.Errorf("exclude_aggregated_audio not sent: %s", captured()[0].body)
	}
}

func TestMiniMaxSpeechStreamBaseRespErrorOn200(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/t2a_v2": jsonResponse(http.StatusOK, `{"base_resp":{"status_code":2054,"status_msg":"voice id not exist"}}`),
	})

	stream, bifrostErr := provider.SpeechStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), speechRequest("not_a_voice", nil))
	if bifrostErr != nil {
		t.Fatalf("SpeechStream setup error: %+v", bifrostErr.Error)
	}
	chunks := collectStream(t, stream)
	if len(chunks) != 1 || chunks[0].BifrostError == nil {
		t.Fatalf("expected exactly one error chunk, got %d chunks", len(chunks))
	}
	got := chunks[0].BifrostError
	if got.StatusCode == nil || *got.StatusCode != http.StatusBadRequest || got.Error.Message != "voice id not exist" {
		t.Errorf("unexpected error: status=%v message=%q", got.StatusCode, got.Error.Message)
	}
}

func TestMiniMaxSpeechStreamTruncated(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/t2a_v2": sseResponse(`{"data":{"audio":"aa","status":1},"base_resp":{"status_code":0,"status_msg":""}}`),
	})

	stream, bifrostErr := provider.SpeechStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), speechRequest("English_Graceful_Lady", nil))
	if bifrostErr != nil {
		t.Fatalf("SpeechStream setup error: %+v", bifrostErr.Error)
	}
	chunks := collectStream(t, stream)
	if len(chunks) == 0 || chunks[len(chunks)-1].BifrostError == nil {
		t.Fatalf("a stream cut before its status 2 chunk must end with an error, got %d chunks", len(chunks))
	}
}

func transcriptionRequest(params *schemas.TranscriptionParameters) *schemas.BifrostTranscriptionRequest {
	return &schemas.BifrostTranscriptionRequest{
		Provider: schemas.MiniMax,
		Model:    "asr-1.0",
		Input:    &schemas.TranscriptionInput{File: []byte("ID3fake-mp3"), Filename: "clip.mp3"},
		Params:   params,
	}
}

func TestMiniMaxTranscription(t *testing.T) {
	tests := []struct {
		name         string
		params       *schemas.TranscriptionParameters
		response     string
		wantFields   map[string]string
		wantLanguage string
		wantText     string
		wantWords    int
		wantSegments int
		wantPlainFmt string
		wantSeconds  float64
	}{
		{
			name:       "json with language header",
			params:     &schemas.TranscriptionParameters{Language: schemas.Ptr("en")},
			response:   `{"text":"The quick brown fox.","duration":3.312,"trace_id":"t1"}`,
			wantFields: map[string]string{"model": "asr-1.0"}, wantLanguage: "en",
			wantText: "The quick brown fox.", wantSeconds: 3.312,
		},
		{
			name:       "word granularity maps segments to words",
			params:     &schemas.TranscriptionParameters{TimestampGranularities: []string{"word"}},
			response:   `{"text":"The quick","duration":1,"n_speakers":1,"segments":[{"id":0,"start":0.14,"end":0.26,"speaker":"S1","text":"The "},{"id":1,"start":0.26,"end":0.6,"speaker":"S1","text":"quick "}]}`,
			wantFields: map[string]string{"response_format": "verbose_json", "timestamp_level": "word"},
			wantText:   "The quick", wantWords: 2, wantSeconds: 1,
		},
		{
			name:       "verbose_json keeps sentence segments",
			params:     &schemas.TranscriptionParameters{ResponseFormat: schemas.Ptr("verbose_json")},
			response:   `{"text":"Hello. Bye.","duration":2,"segments":[{"id":0,"start":0,"end":1,"speaker":"S1","text":"Hello."},{"id":1,"start":1,"end":2,"speaker":"S2","text":"Bye."}]}`,
			wantFields: map[string]string{"response_format": "verbose_json"},
			wantText:   "Hello. Bye.", wantSegments: 2, wantSeconds: 2,
		},
		{
			name:       "srt comes back as plain text",
			params:     &schemas.TranscriptionParameters{ResponseFormat: schemas.Ptr("srt")},
			response:   "1\n00:00:00,140 --> 00:00:03,120\nThe quick brown fox.\n",
			wantFields: map[string]string{"response_format": "srt"},
			wantText:   "1\n00:00:00,140 --> 00:00:03,120\nThe quick brown fox.\n", wantPlainFmt: "srt",
		},
		{
			name:       "text is served from json",
			params:     &schemas.TranscriptionParameters{ResponseFormat: schemas.Ptr("text")},
			response:   `{"text":"Hi.","duration":0.5}`,
			wantFields: map[string]string{"response_format": ""},
			wantText:   "Hi.", wantPlainFmt: "text", wantSeconds: 0.5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
				"POST /v1/speech_to_text": func(w http.ResponseWriter) {
					if strings.HasPrefix(tt.response, "{") {
						w.Header().Set("Content-Type", "application/json")
					} else {
						w.Header().Set("Content-Type", "text/plain")
					}
					_, _ = w.Write([]byte(tt.response))
				},
			})

			resp, bifrostErr := provider.Transcription(testContext(t), testKey(), transcriptionRequest(tt.params))
			if bifrostErr != nil {
				t.Fatalf("Transcription error: %+v", bifrostErr.Error)
			}
			req := captured()[0]
			fields, file := multipartFields(t, req)
			if string(file) != "ID3fake-mp3" {
				t.Errorf("file = %q", file)
			}
			for k, v := range tt.wantFields {
				if fields[k] != v {
					t.Errorf("field %s = %q, want %q (all: %v)", k, fields[k], v, fields)
				}
			}
			if got := req.header.Get("language"); got != tt.wantLanguage {
				t.Errorf("language header = %q, want %q", got, tt.wantLanguage)
			}
			if resp.Text != tt.wantText {
				t.Errorf("text = %q, want %q", resp.Text, tt.wantText)
			}
			if len(resp.Words) != tt.wantWords || len(resp.Segments) != tt.wantSegments {
				t.Errorf("words = %d, segments = %d, want %d and %d", len(resp.Words), len(resp.Segments), tt.wantWords, tt.wantSegments)
			}
			if tt.wantWords > 0 && (resp.Words[1].Word != "quick" || resp.Words[1].Speaker == nil || *resp.Words[1].Speaker != "S1") {
				t.Errorf("unexpected word: %+v", resp.Words[1])
			}
			gotFmt := ""
			if resp.ResponseFormat != nil {
				gotFmt = *resp.ResponseFormat
			}
			if tt.wantPlainFmt != "" && gotFmt != tt.wantPlainFmt {
				t.Errorf("response format = %q, want %q", gotFmt, tt.wantPlainFmt)
			}
			if tt.wantSeconds > 0 && (resp.Usage == nil || resp.Usage.Seconds == nil || *resp.Usage.Seconds != tt.wantSeconds) {
				t.Errorf("usage = %+v, want %v seconds", resp.Usage, tt.wantSeconds)
			}
		})
	}
}

func TestMiniMaxTranscriptionHTTPError(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/speech_to_text": jsonResponse(http.StatusBadRequest, `{"type":"error","error":{"type":"bad_request_error","message":"invalid params, audio duration 623.4s exceeds the limit of 500s (2013)","http_code":"400"},"request_id":"r1"}`),
	})

	_, bifrostErr := provider.Transcription(testContext(t), testKey(), transcriptionRequest(nil))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400, got %+v", bifrostErr)
	}
	if !strings.Contains(bifrostErr.Error.Message, "exceeds the limit of 500s") {
		t.Errorf("message = %q", bifrostErr.Error.Message)
	}
}

func TestMiniMaxTranscriptionStream(t *testing.T) {
	provider, captured := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/speech_to_text": sseResponse(
			`{"index":0,"delta":"The","finish":false}`,
			`{"index":1,"delta":" quick brown fox jumps o","finish":false}`,
			`{"index":2,"delta":"ver the lazy dog.","finish":true,"duration":3.312}`,
		),
	})

	stream, bifrostErr := provider.TranscriptionStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), transcriptionRequest(&schemas.TranscriptionParameters{Language: schemas.Ptr("en")}))
	if bifrostErr != nil {
		t.Fatalf("TranscriptionStream error: %+v", bifrostErr.Error)
	}
	chunks := collectStream(t, stream)
	if len(chunks) != 3 {
		t.Fatalf("expected 2 deltas and a done event, got %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if c.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", c.BifrostError.Error)
		}
	}
	done := chunks[2].BifrostTranscriptionStreamResponse
	if done.Type != schemas.TranscriptionStreamResponseTypeDone || done.Text != "The quick brown fox jumps over the lazy dog." {
		t.Errorf("unexpected done event: type=%s text=%q", done.Type, done.Text)
	}
	if done.Usage == nil || done.Usage.Seconds == nil || *done.Usage.Seconds != 3.312 {
		t.Errorf("usage = %+v, want 3.312 seconds", done.Usage)
	}
	if d := chunks[1].BifrostTranscriptionStreamResponse; d.Type != schemas.TranscriptionStreamResponseTypeDelta || *d.Delta != " quick brown fox jumps o" {
		t.Errorf("unexpected delta: %+v", d)
	}
	req := captured()[0]
	fields, _ := multipartFields(t, req)
	if fields["stream"] != "true" || req.header.Get("language") != "en" {
		t.Errorf("stream field = %q, language header = %q", fields["stream"], req.header.Get("language"))
	}
}

func TestMiniMaxTranscriptionStreamBaseRespError(t *testing.T) {
	provider, _ := newMockMiniMax(t, map[string]func(http.ResponseWriter){
		"POST /v1/speech_to_text": sseResponse(
			`{"index":0,"delta":"The","finish":false}`,
			`{"base_resp":{"status_code":1004,"status_msg":"authentication failed"}}`,
		),
	})

	stream, bifrostErr := provider.TranscriptionStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), transcriptionRequest(nil))
	if bifrostErr != nil {
		t.Fatalf("TranscriptionStream error: %+v", bifrostErr.Error)
	}
	var streamErr *schemas.BifrostError
	for _, c := range collectStream(t, stream) {
		if c.BifrostError != nil {
			streamErr = c.BifrostError
		}
	}
	if streamErr == nil || streamErr.Error == nil || streamErr.Error.Message != "authentication failed" {
		t.Fatalf("expected the MiniMax base_resp error, got %+v", streamErr)
	}
	if streamErr.StatusCode == nil || *streamErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status code = %v, want 401", streamErr.StatusCode)
	}
}

func TestMiniMaxTranscriptionStreamRejectsTimestampGranularities(t *testing.T) {
	for name, params := range map[string]*schemas.TranscriptionParameters{
		"timestamp_granularities": {TimestampGranularities: []string{"word"}},
		"extra_params":            {ExtraParams: map[string]any{"timestamp_level": "sentence"}},
	} {
		t.Run(name, func(t *testing.T) {
			provider, captured := newMockMiniMax(t, nil)
			_, bifrostErr := provider.TranscriptionStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), transcriptionRequest(params))
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected a 400, got %+v", bifrostErr)
			}
			if len(captured()) != 0 {
				t.Errorf("no request should reach MiniMax, got %d", len(captured()))
			}
		})
	}
}

func TestMiniMaxTranscriptionStreamRejectsNonJSONFormat(t *testing.T) {
	provider, captured := newMockMiniMax(t, nil)

	_, bifrostErr := provider.TranscriptionStream(testContext(t), passthroughPostHook, func(context.Context) {}, testKey(), transcriptionRequest(&schemas.TranscriptionParameters{ResponseFormat: schemas.Ptr("verbose_json")}))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected a 400, got %+v", bifrostErr)
	}
	if len(captured()) != 0 {
		t.Errorf("no request should reach MiniMax, got %d", len(captured()))
	}
}

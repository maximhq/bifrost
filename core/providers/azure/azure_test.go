package azure_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/azure"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzure(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(os.Getenv("AZURE_API_KEY")) == "" {
		t.Skip("Skipping Azure tests because AZURE_API_KEY is not set")
	}
	if strings.TrimSpace(os.Getenv("AZURE_ENDPOINT")) == "" {
		t.Skip("Skipping Azure tests because AZURE_ENDPOINT is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.Azure,
		ChatModel:          "gpt-4o",
		PromptCachingModel: "gpt-4o",
		VisionModel:        "gpt-4o",
		ChatAudioModel:     "gpt-4o-mini-audio-preview",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Azure, Model: "gpt-4o"},
		},
		TextModel:               "", // Azure doesn't support text completion in newer models
		EmbeddingModel:          "text-embedding-ada-002",
		ReasoningModel:          "claude-opus-4-5",
		SpeechSynthesisModel:    "gpt-4o-mini-tts",
		TranscriptionModel:      "gpt-4o-transcribe",
		ImageGenerationModel:    "gpt-image-2",
		ImageEditModel:          "gpt-image-2",
		VideoGenerationModel:    "sora-2",
		PassthroughModel:        "gpt-4o",
		ExternalCompactionModel: "gpt-4o",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:               false, // Not supported
			SimpleChat:                   true,
			CompletionStream:             true,
			MultiTurnConversation:        true,
			ToolCalls:                    true,
			ToolCallsStreaming:           true,
			MultipleToolCalls:            true,
			MultipleToolCallsStreaming:   true,
			End2EndToolCalling:           true,
			AutomaticFunctionCall:        true,
			ImageURL:                     true,
			ImageBase64:                  true,
			MultipleImages:               true,
			CompleteEnd2End:              true,
			Embedding:                    true,
			ListModels:                   true,
			Reasoning:                    true,
			ChatAudio:                    false,
			Transcription:                false, // Disabled for azure because of 3 calls/minute quota
			TranscriptionStream:          false, // Not properly supported yet by Azure
			SpeechSynthesis:              false, // Disabled for azure because of 3 calls/minute quota
			SpeechSynthesisStream:        false, // Disabled for azure because of 3 calls/minute quota
			StructuredOutputs:            true,  // Structured outputs with nullable enum support
			PromptCaching:                true,
			ImageGeneration:              false, // Skipped for Azure
			ImageGenerationStream:        false, // Skipped for Azure
			ImageEdit:                    false, // Model not deployed on Azure endpoint
			ImageEditStream:              false, // Model not deployed on Azure endpoint
			ImageVariation:               false, // Not supported by Azure
			VideoGeneration:              false, // disabled for now because of long running operations
			VideoDownload:                false,
			VideoRetrieve:                false,
			VideoRemix:                   false,
			VideoList:                    false,
			VideoDelete:                  false,
			InterleavedThinking:          true,
			PassthroughAPI:               true,
			EagerInputStreaming:          true, // fine-grained-tool-streaming-2025-05-14 (Beta on Azure Foundry)
			ServerToolsViaOpenAIEndpoint: true, // web_search / web_fetch / code_execution on Azure per Table 20
			ContainerCreate:              true,
			ContainerList:                false, // not supported (hangs with 0 bytes)
			ContainerRetrieve:            true,
			ContainerDelete:              true,
			ContainerFileCreate:          true,
			ContainerFileList:            true,
			ContainerFileRetrieve:        true,
			ContainerFileContent:         true,
			ContainerFileDelete:          true,
			ExternalCompaction:           true,
		},
		DisableParallelFor: []string{"Transcription"}, // Azure Whisper has 3 calls/minute quota
	}

	t.Run("AzureTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// routingTestLogger is a no-op logger for provider construction in routing tests.
type routingTestLogger struct{}

func (routingTestLogger) Debug(msg string, args ...any)                     {}
func (routingTestLogger) Info(msg string, args ...any)                      {}
func (routingTestLogger) Warn(msg string, args ...any)                      {}
func (routingTestLogger) Error(msg string, args ...any)                     {}
func (routingTestLogger) Fatal(msg string, args ...any)                     {}
func (routingTestLogger) SetLevel(level schemas.LogLevel)                   {}
func (routingTestLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (routingTestLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// azureRoutingUpstream is a fake Azure resource that records which OpenAI-compatible
// path each request hit and answers both shapes: a chat completion truncated by
// max_tokens (finish_reason "length"), and a completed Responses object.
type azureRoutingUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newAzureRoutingUpstream(t *testing.T) *azureRoutingUpstream {
	t.Helper()
	u := &azureRoutingUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		u.mu.Unlock()
		var body strings.Builder
		if r.Body != nil {
			buf := make([]byte, 1<<16)
			n, _ := r.Body.Read(buf)
			body.Write(buf[:n])
		}
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			if strings.Contains(body.String(), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"FW-GLM-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"1\n2\n3"},"finish_reason":null}]}` + "\n\n" +
					`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"FW-GLM-5.2","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16,"total_tokens":26}}` + "\n\n" +
					"data: [DONE]\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"FW-GLM-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"1\n2\n3"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16,"total_tokens":26}}`))
		case "/openai/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-4o","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(u.Server.Close)
	return u
}

func (u *azureRoutingUpstream) hitPaths() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...)
}

// TestAzureResponsesHonoursDatasheetSupportedEndpoints pins #6782: Azure Foundry
// deployments of Fireworks models (FW-GLM-5.2 and friends) hard-cap output at 4096
// tokens on /openai/v1/responses while /openai/v1/chat/completions honours
// max_tokens. Microsoft lists every Fireworks catalog model as a "Chat completions"
// model with Responses only via the Foundry Projects API. When the model's datasheet
// row lists supported_endpoints without /v1/responses, the azure provider must serve
// Responses (and ResponsesStream) through chat completions, mirroring the Bedrock
// Mantle gate. A row that lists /v1/responses, or no row at all, keeps today's route.
func TestAzureResponsesHonoursDatasheetSupportedEndpoints(t *testing.T) {
	// The capability resolver is process-global, so this test must not run in
	// parallel with anything that also installs one.
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.Azure {
			return nil
		}
		switch model {
		case "FW-GLM-5.2":
			return &schemas.ModelCapabilities{SupportedEndpoints: []string{"/v1/chat/completions"}}
		case "gpt-4o":
			return &schemas.ModelCapabilities{SupportedEndpoints: []string{"/v1/chat/completions", "/v1/batch", "/v1/responses"}}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	newProvider := func(t *testing.T) *azure.AzureProvider {
		t.Helper()
		provider, err := azure.NewAzureProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
		}, routingTestLogger{})
		if err != nil {
			t.Fatalf("NewAzureProvider: %v", err)
		}
		return provider
	}
	newKey := func(endpoint string) schemas.Key {
		return schemas.Key{
			Value:          *schemas.NewSecretVar("test-key"),
			Models:         []string{"*"},
			AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint)},
		}
	}
	newRequest := func(model string) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Provider: schemas.Azure,
			Model:    model,
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Count from 1 to 3000, one number per line.")},
			}},
			Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(8000)},
		}
	}
	passthroughPostHook := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return resp, err
	}
	assertOnlyPath := func(t *testing.T, upstream *azureRoutingUpstream, want string) {
		t.Helper()
		paths := upstream.hitPaths()
		if len(paths) != 1 || paths[0] != want {
			t.Fatalf("upstream paths = %v, want exactly [%s]", paths, want)
		}
	}

	t.Run("ChatOnlyRowRoutesResponsesToChatCompletions", func(t *testing.T) {
		upstream := newAzureRoutingUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, bifrostErr := newProvider(t).Responses(ctx, newKey(upstream.URL), newRequest("FW-GLM-5.2"))
		if bifrostErr != nil {
			t.Fatalf("Responses: %+v", bifrostErr)
		}
		assertOnlyPath(t, upstream, "/openai/v1/chat/completions")
		if resp.Status == nil || *resp.Status != schemas.ResponsesResponseStatusIncomplete {
			t.Errorf("status = %v, want incomplete (finish_reason length must survive the conversion)", resp.Status)
		}
		if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != schemas.ResponsesResponseIncompleteReasonMaxOutputTokens {
			t.Errorf("incomplete_details = %+v, want reason max_output_tokens", resp.IncompleteDetails)
		}
	})

	t.Run("ChatOnlyRowRoutesResponsesStreamToChatCompletions", func(t *testing.T) {
		upstream := newAzureRoutingUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stream, bifrostErr := newProvider(t).ResponsesStream(ctx, passthroughPostHook, nil, newKey(upstream.URL), newRequest("FW-GLM-5.2"))
		if bifrostErr != nil {
			t.Fatalf("ResponsesStream: %+v", bifrostErr)
		}
		var last *schemas.BifrostStreamChunk
		var streamErr *schemas.BifrostError
		timeout := time.NewTimer(20 * time.Second)
		defer timeout.Stop()
	drain:
		for {
			select {
			case chunk, ok := <-stream:
				if !ok {
					break drain
				}
				if chunk != nil && chunk.BifrostError != nil && streamErr == nil {
					streamErr = chunk.BifrostError
				}
				if chunk != nil && chunk.BifrostResponsesStreamResponse != nil {
					last = chunk
				}
			case <-timeout.C:
				t.Fatal("timed out waiting for the provider stream to close")
			}
		}
		// Route first: a wrong path is the defect; whatever the fake answered there is noise.
		assertOnlyPath(t, upstream, "/openai/v1/chat/completions")
		if streamErr != nil {
			t.Fatalf("stream error: %+v", streamErr)
		}
		if last == nil {
			t.Fatal("no Responses stream events were re-assembled from the chat chunks")
		}
		if got := last.BifrostResponsesStreamResponse.Type; got != schemas.ResponsesStreamResponseTypeIncomplete {
			t.Errorf("terminal event = %q, want %q", got, schemas.ResponsesStreamResponseTypeIncomplete)
		}
	})

	t.Run("RowWithResponsesEndpointKeepsResponsesRoute", func(t *testing.T) {
		upstream := newAzureRoutingUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, bifrostErr := newProvider(t).Responses(ctx, newKey(upstream.URL), newRequest("gpt-4o")); bifrostErr != nil {
			t.Fatalf("Responses: %+v", bifrostErr)
		}
		assertOnlyPath(t, upstream, "/openai/v1/responses")
	})

	t.Run("UnknownModelKeepsResponsesRoute", func(t *testing.T) {
		upstream := newAzureRoutingUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, bifrostErr := newProvider(t).Responses(ctx, newKey(upstream.URL), newRequest("my-custom-deployment")); bifrostErr != nil {
			t.Fatalf("Responses: %+v", bifrostErr)
		}
		assertOnlyPath(t, upstream, "/openai/v1/responses")
	})

	t.Run("AliasModelNameResolvesTheDatasheetRow", func(t *testing.T) {
		// The reporter's deployment is named azure-glm-5.2; the key alias maps it to
		// the catalog ID, which is also how pricing finds the row.
		upstream := newAzureRoutingUpstream(t)
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
			Config: &schemas.AliasConfig{ModelID: "azure-glm-5.2", ModelName: schemas.Ptr("FW-GLM-5.2")},
		})

		if _, bifrostErr := newProvider(t).Responses(ctx, newKey(upstream.URL), newRequest("azure-glm-5.2")); bifrostErr != nil {
			t.Fatalf("Responses: %+v", bifrostErr)
		}
		assertOnlyPath(t, upstream, "/openai/v1/chat/completions")
	})
}

// Microsoft-Decision-1 bodies and errors below are as captured from a live Foundry
// deployment named "Decision-1".
const azureDecisionResponseBody = `{"model":"microsoft-decision-1","answers":{"team":{"choice":"engineering","confidence":0.99,"probabilities":{"billing":0.001,"engineering":0.998,"support":0.001},"type":"choice"}},"usage":{"input_tokens":64,"output_tokens":1}}`

func newAzureDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Azure,
		Model:    "Decision-1",
		Input:    input,
		Questions: []schemas.DecisionQuestion{{
			Type:         schemas.DecisionTypeChoice,
			Name:         schemas.Ptr("team"),
			Instructions: schemas.NewDecisionText("Which team should handle this ticket?"),
			Choices: []schemas.DecisionChoice{
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("Charges, invoices, and refunds")},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("engineering")}, Description: schemas.NewDecisionText("Bugs, errors, and outages")},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("support")}, Description: schemas.NewDecisionText("How-to and account questions")},
			},
		}},
	}
}

func newAzureDecisionProvider(t *testing.T) *azure.AzureProvider {
	t.Helper()
	provider, err := azure.NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
	}, routingTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}
	return provider
}

func newAzureDecisionKey(endpoint string) schemas.Key {
	return schemas.Key{
		Value:          *schemas.NewSecretVar("test-key"),
		Models:         []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint)},
	}
}

// TestAzureDecisionSendsTypesafeBodyToSystemOne pins the native call: the body is exactly
// Typesafe's systemone request with the deployment name as the model, it goes to the
// resource's /providers/microsoft/v1/systemone with no api-version, and authenticates with
// the api-key header, or Bearer when a token is on the context.
func TestAzureDecisionSendsTypesafeBodyToSystemOne(t *testing.T) {
	request := newAzureDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("The API returns 500 on every call.")})
	native, err := typesafe.ToTypesafeDecisionRequest(request)
	if err != nil {
		t.Fatalf("ToTypesafeDecisionRequest: %v", err)
	}
	wantBody, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{"api-key": "", "bearer": "entra-token"} {
		t.Run(name, func(t *testing.T) {
			type capturedRequest struct {
				path, query, apiKey, auth string
				body                      []byte
			}
			// The handler runs on the server's goroutine, so the request is handed over on a channel.
			captured := make(chan capturedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				captured <- capturedRequest{path: r.URL.Path, query: r.URL.RawQuery, apiKey: r.Header.Get("api-key"), auth: r.Header.Get("Authorization"), body: body}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, azureDecisionResponseBody)
			}))
			defer server.Close()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if token != "" {
				ctx.SetValue(azure.AzureAuthorizationTokenKey, token)
			}
			resp, bifrostErr := newAzureDecisionProvider(t).Decision(ctx, newAzureDecisionKey(server.URL), request)
			if bifrostErr != nil {
				t.Fatalf("Decision: %+v", bifrostErr.Error)
			}
			sent := <-captured
			gotPath, gotQuery, gotAPIKey, gotAuth, gotBody := sent.path, sent.query, sent.apiKey, sent.auth, sent.body

			if gotPath != "/providers/microsoft/v1/systemone" || gotQuery != "" {
				t.Errorf("request = %s?%s, want /providers/microsoft/v1/systemone with no query", gotPath, gotQuery)
			}
			if token == "" && (gotAPIKey != "test-key" || gotAuth != "") {
				t.Errorf("auth = api-key %q, Authorization %q, want api-key test-key only", gotAPIKey, gotAuth)
			}
			if token != "" && (gotAuth != "Bearer "+token || gotAPIKey != "") {
				t.Errorf("auth = api-key %q, Authorization %q, want Bearer %s only", gotAPIKey, gotAuth, token)
			}
			var got, want any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Fatalf("body is not JSON: %s", gotBody)
			}
			_ = json.Unmarshal(wantBody, &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body =\n%s\nwant Typesafe's\n%s", gotBody, wantBody)
			}

			if len(resp.Answers) != 1 || resp.Answers[0].Choice == nil || resp.Answers[0].Choice.Str == nil || *resp.Answers[0].Choice.Str != "engineering" {
				t.Errorf("answers = %+v, want team=engineering", resp.Answers)
			}
			if resp.Usage == nil || resp.Usage.PromptTokens != 64 || resp.Usage.CompletionTokens != 1 {
				t.Errorf("usage = %+v, want 64 input and 1 output tokens", resp.Usage)
			}
		})
	}
}

// TestAzureDecisionRejectsImageInputLocally pins that an image never reaches
// Microsoft-Decision-1: the model is text-only and answers an image part read as JSON
// text with a 200, so the request is a local 400 instead.
func TestAzureDecisionRejectsImageInputLocally(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer server.Close()

	request := newAzureDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
		Role: "user",
		Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
		}},
	}}})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := newAzureDecisionProvider(t).Decision(ctx, newAzureDecisionKey(server.URL), request)
	if bifrostErr == nil {
		t.Fatal("expected an error for an image input")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "this provider cannot read") {
		t.Errorf("message = %+v, want the provider-neutral image rejection", bifrostErr.Error)
	}
	if hit {
		t.Error("the request reached Azure")
	}
}

// TestAzureDecisionForwardsAssets pins that images sent by reference reach Azure as
// Typesafe's assets rather than being refused locally, and that Azure's own verdict
// comes back: Microsoft-Decision-1 rejects the field today.
func TestAzureDecisionForwardsAssets(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"unrecognized_request_argument","message":"Unrecognized request argument supplied: assets"}}`)
	}))
	defer server.Close()

	request := newAzureDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
		Role: "user",
		Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Is r-a a red square?")},
			{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r-a"), Data: []byte("\x89PNG\r\n\x1a\n")},
		}},
	}}})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := newAzureDecisionProvider(t).Decision(ctx, newAzureDecisionKey(server.URL), request)
	if body := <-bodies; !strings.Contains(string(body), `"assets":{"r-a":{"type":"image","format":"png"`) {
		t.Errorf("body sent to Azure = %s, want the asset", body)
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Message != "Unrecognized request argument supplied: assets" {
		t.Errorf("error = %+v, want Azure's own message", bifrostErr)
	}
}

// TestAzureDecisionPassesAzureErrorsThrough pins that Azure's own errors reach the caller
// with their status and message, and that the reply's headers stay on the context.
func TestAzureDecisionPassesAzureErrorsThrough(t *testing.T) {
	cases := map[string]struct {
		status      int
		body        string
		wantMessage string
	}{
		"deployment not found": {
			status:      http.StatusNotFound,
			body:        `{"error":{"code":"DeploymentNotFound","message":"The API deployment no-such-deployment does not exist.","details":"The API deployment no-such-deployment does not exist."}}`,
			wantMessage: "The API deployment no-such-deployment does not exist.",
		},
		"unrecognized argument": {
			status:      http.StatusBadRequest,
			body:        `{"error":{"code":"unrecognized_request_argument","message":"Unrecognized request argument supplied: foo","details":"Unrecognized request argument supplied: foo"}}`,
			wantMessage: "Unrecognized request argument supplied: foo",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("apim-request-id", "req-"+name)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, bifrostErr := newAzureDecisionProvider(t).Decision(ctx, newAzureDecisionKey(server.URL), newAzureDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
			if bifrostErr == nil {
				t.Fatal("expected an error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tc.status {
				t.Errorf("status = %v, want %d", bifrostErr.StatusCode, tc.status)
			}
			if bifrostErr.Error == nil || bifrostErr.Error.Message != tc.wantMessage {
				t.Errorf("message = %+v, want %q", bifrostErr.Error, tc.wantMessage)
			}
			headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
			found := false
			for k, v := range headers {
				if strings.EqualFold(k, "apim-request-id") && v == "req-"+name {
					found = true
				}
			}
			if !found {
				t.Errorf("context headers = %v, want apim-request-id from the error reply", headers)
			}
		})
	}
}

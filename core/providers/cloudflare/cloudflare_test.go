package cloudflare_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/cloudflare"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCloudflare(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")) == "" || strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")) == "" {
		t.Skip("Skipping Cloudflare tests because CLOUDFLARE_API_TOKEN or CLOUDFLARE_ACCOUNT_ID is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Cloudflare,
		ChatModel:      "@cf/meta/llama-4-scout-17b-16e-instruct",
		TextModel:      "", // Cloudflare's OpenAI-compatible API has no text completions
		EmbeddingModel: "@cf/baai/bge-large-en-v1.5",
		DecisionModel:  "clef-flash",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Decision:              true,
		},
	}

	t.Run("CloudflareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

const chatResponseBody = `{"id":"id-1","object":"chat.completion","created":1,"model":"@cf/meta/llama-4-scout-17b-16e-instruct","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

func newTestProvider(t *testing.T, baseURL string) *cloudflare.CloudflareProvider {
	t.Helper()
	provider, err := cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewCloudflareProvider: %v", err)
	}
	return provider
}

func newTestKey(accountID string) schemas.Key {
	return schemas.Key{
		Value:               schemas.SecretVar{Val: "test-token"},
		Models:              []string{"*"},
		CloudflareKeyConfig: &schemas.CloudflareKeyConfig{AccountID: schemas.SecretVar{Val: accountID}},
	}
}

func newChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Cloudflare,
		Model:    "@cf/meta/llama-4-scout-17b-16e-instruct",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func TestChatCompletion_UsesKeyAccountURL(t *testing.T) {
	var capturedPath, capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatResponseBody)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.ChatCompletion(ctx, newTestKey("acct-1"), newChatRequest()); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/accounts/acct-1/ai/v1/chat/completions" {
		t.Errorf("path = %q, want /accounts/acct-1/ai/v1/chat/completions", capturedPath)
	}
	if capturedAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", capturedAuth)
	}
}

func TestEmbedding_UsesKeyAccountURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Cloudflare,
		Model:    "@cf/baai/bge-large-en-v1.5",
		Input:    schemas.EmbeddingInput{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hi")}}}},
	}
	if _, bifrostErr := provider.Embedding(ctx, newTestKey("acct-2"), request); bifrostErr != nil {
		t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/accounts/acct-2/ai/v1/embeddings" {
		t.Errorf("path = %q, want /accounts/acct-2/ai/v1/embeddings", capturedPath)
	}
}

func TestChatCompletion_MissingAccountIDIsConfigError(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-token"}}

	_, bifrostErr := provider.ChatCompletion(ctx, key, newChatRequest())
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "cloudflare_key_config.account_id") {
		t.Fatalf("expected a missing account ID error, got %+v", bifrostErr)
	}
}

func TestChatCompletion_RejectsAccountIDThatIsNotOnePathSegment(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	for _, accountID := range []string{"acct-1/../other", "acct-1?x=1", ".."} {
		_, bifrostErr := provider.ChatCompletion(ctx, newTestKey(accountID), newChatRequest())
		if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "invalid cloudflare_key_config.account_id") {
			t.Errorf("account ID %q: expected an invalid account ID error, got %+v", accountID, bifrostErr)
		}
	}
}

func TestChatCompletion_ParsesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"errors":[{"message":"AiError: No such model: No such model @cf/meta/nope or task (req-1)","code":5007},{"code":"invalid_type","message":"Expected array, received string"}],"success":false,"result":{},"messages":[]}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.ChatCompletion(ctx, newTestKey("acct-1"), newChatRequest())
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
	}
	want := "AiError: No such model: No such model @cf/meta/nope or task (req-1); Expected array, received string"
	if bifrostErr.Error.Message != want {
		t.Errorf("message = %q, want %q", bifrostErr.Error.Message, want)
	}
}

func TestListModels_PagesFiltersTasksAndListsClef(t *testing.T) {
	// A full first page forces a second request; the second page carries the
	// models that must be filtered out.
	firstPage := make([]string, 0, 100)
	for i := range 100 {
		firstPage = append(firstPage, fmt.Sprintf(`{"name":"@cf/test/chat-%d","task":{"name":"Text Generation"}}`, i))
	}
	secondPage := []string{
		`{"name":"@cf/baai/bge-large-en-v1.5","description":"embeddings","task":{"name":"Text Embeddings"}}`,
		`{"name":"@cf/cloudflare/clef-flash","task":{"name":"Text Generation"}}`,
		`{"name":"@cf/cloudflare/clef-omni","task":{"name":"Text Generation"}}`,
		`{"name":"@cf/black-forest-labs/flux-1-schnell","task":{"name":"Text-to-Image"}}`,
	}

	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acct-1/ai/models/search" {
			t.Errorf("path = %q, want /accounts/acct-1/ai/models/search", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		result := firstPage
		if page == "2" {
			result = secondPage
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"success":true,"errors":[],"result":[%s]}`, strings.Join(result, ","))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{newTestKey("acct-1")}, &schemas.BifrostListModelsRequest{Provider: schemas.Cloudflare})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}

	if strings.Join(pages, ",") != "1,2" {
		t.Errorf("pages requested = %v, want [1 2]", pages)
	}
	if len(resp.Data) != 103 {
		t.Fatalf("models = %d, want 103 (100 chat + 1 embedding + clef-flash + clef-omni)", len(resp.Data))
	}
	byID := make(map[string]schemas.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	if _, ok := byID["cloudflare/@cf/baai/bge-large-en-v1.5"]; !ok {
		t.Fatal("embedding model missing")
	}
	for _, clef := range []string{"cloudflare/@cf/cloudflare/clef-flash", "cloudflare/@cf/cloudflare/clef-omni"} {
		if _, ok := byID[clef]; !ok {
			t.Fatalf("%s must be listed under its Cloudflare ID", clef)
		}
	}
	for _, excluded := range []string{"cloudflare/clef-flash", "cloudflare/@cf/black-forest-labs/flux-1-schnell"} {
		if _, ok := byID[excluded]; ok {
			t.Errorf("%s must not be listed", excluded)
		}
	}
}

const clefDecisionResponseBody = `{"result":{"model":"clef-flash","answers":{"is_red":{"type":"noul","noul":0.97}},"usage":{"input_tokens":210,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`

const testImageURL = "data:image/png;base64,iVBORw0KGgo="

func newDecisionRequest(model string, input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Cloudflare,
		Model:    model,
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

func textPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

func imagePart(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func userMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

// encodeDecisionBody encodes a converted request the way the provider sends it.
func encodeDecisionBody(t *testing.T, request *schemas.BifrostDecisionRequest) map[string]any {
	t.Helper()
	native, err := cloudflare.ToCloudflareDecisionRequest(request)
	if err != nil {
		t.Fatalf("ToCloudflareDecisionRequest: %v", err)
	}
	data, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatalf("MarshalProviderRequest: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return body
}

func bodyKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestToCloudflareDecisionRequest_EncodesOnlyClefFields pins Clef's body to its schema.
// Clef rejects unknown fields with a 422, so a field added to Typesafe's request, which
// this body embeds, must fail here first.
func TestToCloudflareDecisionRequest_EncodesOnlyClefFields(t *testing.T) {
	text := encodeDecisionBody(t, newDecisionRequest("clef-flash", schemas.DecisionInput{Text: schemas.Ptr("The sky is blue.")}))
	if got, want := bodyKeys(text), []string{"model", "questions", "state"}; !slices.Equal(got, want) {
		t.Errorf("text-only body keys = %v, want %v", got, want)
	}
	withImage := encodeDecisionBody(t, newDecisionRequest("clef-flash", schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{userMessage(textPart("Look"), imagePart(testImageURL))}}))
	if got, want := bodyKeys(withImage), []string{"images", "model", "questions", "state"}; !slices.Equal(got, want) {
		t.Errorf("image body keys = %v, want %v", got, want)
	}
}

func TestToCloudflareDecisionRequest_MovesImagesOutOfTheInput(t *testing.T) {
	second := "data:image/jpeg;base64,/9j/4AAQ"
	request := newDecisionRequest("clef-flash", schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		userMessage(textPart("Compare these"), imagePart(testImageURL)),
		userMessage(imagePart(second)),
	}})
	before, err := json.Marshal(request.Input)
	if err != nil {
		t.Fatal(err)
	}

	body := encodeDecisionBody(t, request)

	if got := body["images"]; !reflect.DeepEqual(got, []any{testImageURL, second}) {
		t.Errorf("images = %v, want both data URLs in order", got)
	}
	state, err := json.Marshal(body["state"])
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"content":[{"text":"Compare these","type":"input_text"}],"role":"user"}]`; string(state) != want {
		t.Errorf("state = %s, want only the text message %s", state, want)
	}
	after, err := json.Marshal(request.Input)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("conversion modified the request input:\nbefore %s\nafter  %s", before, after)
	}
}

func TestToCloudflareDecisionRequest_ImageOnlyInputSendsEmptyState(t *testing.T) {
	body := encodeDecisionBody(t, newDecisionRequest("clef-flash", schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{userMessage(imagePart(testImageURL))}}))
	if state, ok := body["state"]; !ok || state != "" {
		t.Errorf("state = %#v, want an empty string (Clef rejects a null state)", state)
	}
}

func TestToCloudflareDecisionRequest_KeepsStructuredInput(t *testing.T) {
	structured := map[string]any{"ticket": "A-4021", "charges": []any{149.0, 149.0}}
	body := encodeDecisionBody(t, newDecisionRequest("clef-flash", schemas.DecisionInput{Structured: structured}))
	if !reflect.DeepEqual(body["state"], structured) {
		t.Errorf("state = %v, want the structured input unchanged %v", body["state"], structured)
	}
	if _, ok := body["images"]; ok {
		t.Error("a structured input must not carry images")
	}
}

func TestToCloudflareDecisionRequest_RejectsUnreadableParts(t *testing.T) {
	for name, part := range map[string]schemas.DecisionInputPart{
		"image without url": {Type: schemas.DecisionInputPartTypeImage},
		"audio":             {Type: "input_audio"},
		"untyped":           {},
	} {
		request := newDecisionRequest("clef-flash", schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{userMessage(textPart("hi"), part)}})
		if _, err := cloudflare.ToCloudflareDecisionRequest(request); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDecision_SendsBareClefNameInURLAndBody(t *testing.T) {
	for _, model := range []string{"clef", "clef-flash", "@cf/cloudflare/clef", "@cf/cloudflare/clef-flash"} {
		t.Run(model, func(t *testing.T) {
			var capturedPath string
			var capturedBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&capturedBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, clefDecisionResponseBody)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Decision(ctx, newTestKey("acct-1"), newDecisionRequest(model, schemas.DecisionInput{Text: schemas.Ptr("hi")}))
			if bifrostErr != nil {
				t.Fatalf("Decision: %v", bifrostErr.Error.Message)
			}

			bare := strings.TrimPrefix(model, "@cf/cloudflare/")
			if want := "/accounts/acct-1/ai/run/@cf/cloudflare/" + bare; capturedPath != want {
				t.Errorf("path = %q, want %q", capturedPath, want)
			}
			if capturedBody["model"] != bare {
				t.Errorf("body model = %v, want %q", capturedBody["model"], bare)
			}
			if len(resp.Answers) != 1 || resp.Usage == nil || resp.Usage.PromptTokens != 210 {
				t.Errorf("unexpected response: %+v", resp)
			}
		})
	}
}

func TestDecision_PreservesProviderResponseHeaders(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cf-Ray", "ray-123")
				w.Header().Set("Connection", "close")
				if status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":1015,"message":"rate limited"}]}`)
					return
				}
				_, _ = fmt.Fprint(w, clefDecisionResponseBody)
			}))
			defer server.Close()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := newTestProvider(t, server.URL).Decision(ctx, newTestKey("acct-1"), newDecisionRequest("clef-flash", schemas.DecisionInput{Text: schemas.Ptr("red apple")}))
			if status == http.StatusOK {
				if bifrostErr != nil {
					t.Fatalf("Decision: %v", bifrostErr)
				}
				if got := resp.ExtraFields.ProviderResponseHeaders["Cf-Ray"]; got != "ray-123" {
					t.Errorf("response Cf-Ray = %q, want ray-123", got)
				}
			} else if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != status {
				t.Fatalf("Decision error = %v, want status %d", bifrostErr, status)
			}

			headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
			if got := headers["Cf-Ray"]; got != "ray-123" {
				t.Errorf("context Cf-Ray = %q, want ray-123", got)
			}
			if status == http.StatusTooManyRequests && headers["Retry-After"] != "7" {
				t.Errorf("Retry-After = %q, want 7", headers["Retry-After"])
			}
			if _, ok := headers["Connection"]; ok {
				t.Error("hop-by-hop Connection header must be filtered")
			}
		})
	}
}

func TestDecision_ResolvedAliasUsesTheClefTarget(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, clefDecisionResponseBody)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// Core has already replaced the alias with its target before the provider runs.
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "my-classifier", Config: &schemas.AliasConfig{ModelID: "clef-flash"}})
	if _, bifrostErr := provider.Decision(ctx, newTestKey("acct-1"), newDecisionRequest("clef-flash", schemas.DecisionInput{Text: schemas.Ptr("hi")})); bifrostErr != nil {
		t.Fatalf("Decision: %v", bifrostErr.Error.Message)
	}
	if want := "/accounts/acct-1/ai/run/@cf/cloudflare/clef-flash"; capturedPath != want {
		t.Errorf("path = %q, want %q", capturedPath, want)
	}
}

// TestDecision_SendsAnyModelAndPassesTheErrorThrough pins that Decision does not gate on
// the model name: a model Workers AI does not serve is sent, and its error comes back as is.
func TestDecision_SendsAnyModelAndPassesTheErrorThrough(t *testing.T) {
	const message = "AiError: No such model: No such model @cf/cloudflare/Clef-Flash or task (req-1)"
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		payload, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": message, "code": 5007}}, "success": false, "result": map[string]any{}})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, newTestKey("acct-1"), newDecisionRequest("Clef-Flash", schemas.DecisionInput{Text: schemas.Ptr("hi")}))

	if want := "/accounts/acct-1/ai/run/@cf/cloudflare/Clef-Flash"; capturedPath != want {
		t.Errorf("path = %q, want %q (the model is sent, not rejected locally)", capturedPath, want)
	}
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the upstream 400, got %+v", bifrostErr)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != message {
		t.Errorf("message = %q, want the Workers AI message", bifrostErr.Error.Message)
	}
}

func TestDecision_EnvelopeWithoutResultIsError(t *testing.T) {
	for name, body := range map[string]string{
		"success false":  `{"result":{},"success":false,"errors":[{"code":5000,"message":"upstream failed"}]}`,
		"missing result": `{"success":true,"errors":[]}`,
		"null result":    `{"result":null,"success":true,"errors":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, bifrostErr := provider.Decision(ctx, newTestKey("acct-1"), newDecisionRequest("clef-flash", schemas.DecisionInput{Text: schemas.Ptr("hi")}))
			if bifrostErr == nil {
				t.Fatal("expected an error for an envelope without a result")
			}
		})
	}
}

func TestDecision_PassesClefValidationErrorsThrough(t *testing.T) {
	const message = `AiError: AiError: {"error":{"type":"invalid_request","message":"Request body failed validation","details":{"formErrors":[],"fieldErrors":{"images":["image must be an embedded base64 data URI"]}}}} (req-1)`
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				payload, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": message, "code": 5012}}, "success": false, "result": map[string]any{}})
				_, _ = w.Write(payload)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, bifrostErr := provider.Decision(ctx, newTestKey("acct-1"), newDecisionRequest("clef-flash", schemas.DecisionInput{Text: schemas.Ptr("hi")}))
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != status {
				t.Fatalf("expected status %d, got %+v", status, bifrostErr)
			}
			if bifrostErr.Error == nil || bifrostErr.Error.Message != message {
				t.Errorf("message = %q, want Clef's message", bifrostErr.Error.Message)
			}
		})
	}
}

// TestListModels_DecodesCompressedPages pins that a gzip-encoded model page is decoded
// before it is parsed, as the other response paths do.
func TestListModels_DecodesCompressedPages(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"name":"@cf/baai/bge-large-en-v1.5","task":{"name":"Text Embeddings"}}]}`))
	_ = writer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{newTestKey("acct-1")}, &schemas.BifrostListModelsRequest{Provider: schemas.Cloudflare})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "cloudflare/@cf/baai/bge-large-en-v1.5" {
		t.Fatalf("models = %+v, want the one embedding model", resp.Data)
	}
}

// TestListModels_MatchesAllowListAgainstCloudflareIDs pins that a key whose allow-list is
// written in Cloudflare's IDs still lists Clef, since listing keeps those IDs unchanged.
func TestListModels_MatchesAllowListAgainstCloudflareIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"errors":[],"result":[{"name":"@cf/cloudflare/clef-flash","task":{"name":"Text Generation"}},{"name":"@cf/baai/bge-large-en-v1.5","task":{"name":"Text Embeddings"}}]}`)
	}))
	defer server.Close()

	key := newTestKey("acct-1")
	key.Models = []string{"regex:^@cf/cloudflare/.*"}
	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.Cloudflare})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "cloudflare/@cf/cloudflare/clef-flash" {
		t.Fatalf("models = %+v, want only Clef under its Cloudflare ID", resp.Data)
	}
}

const clairicExtractionResponseBody = `{"result":{"model":"clairic","answers":{"invoice_number":{"type":"text","p_null":0.00001,"span":[8,16],"guess":"INV-2291","confidence":0.99,"value":"INV-2291"},"due_date":{"type":"date","guess":"2026-10-02","confidence":0.98,"value":null},"days_to_pay":{"type":"estimate","value":30,"mean":29.7,"median":30,"p10":25,"p90":33,"p_below":0.0000005,"p_above":0.000003}},"truncated":false,"state_text":"Invoice INV-2291, net 30.","usage":{"state_tokens":52,"image_tokens":0,"schema_tokens":312,"input_tokens":416,"output_tokens":0,"passes":2}},"success":true,"errors":[],"messages":[]}`

func newExtractionRequest(model string) *schemas.BifrostExtractionRequest {
	return &schemas.BifrostExtractionRequest{
		Provider: schemas.Cloudflare,
		Model:    model,
		Input:    "Invoice INV-2291, net 30.",
		Fields: map[string]schemas.ExtractionFieldSpec{
			"invoice_number": {Type: schemas.ExtractionFieldTypeText},
			"days_to_pay":    {Type: schemas.ExtractionFieldTypeEstimate, Min: json.RawMessage(`0`), Max: json.RawMessage(`120`), Step: json.RawMessage(`1`)},
		},
	}
}

// encodeExtractionBody encodes a converted request the way the provider sends it.
func encodeExtractionBody(t *testing.T, request *schemas.BifrostExtractionRequest) map[string]any {
	t.Helper()
	data, err := providerUtils.MarshalProviderRequest(cloudflare.ToCloudflareExtractionRequest(request))
	if err != nil {
		t.Fatalf("MarshalProviderRequest: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return body
}

// TestToCloudflareExtractionRequest_EncodesOnlyClairicFields pins Clairic's body to its schema
// (model, state, fields, images, reference_date, options). Clairic answers 422 to any other
// property, so a field added to the normalized request must fail here first.
func TestToCloudflareExtractionRequest_EncodesOnlyClairicFields(t *testing.T) {
	plain := encodeExtractionBody(t, newExtractionRequest("clairic"))
	if got, want := bodyKeys(plain), []string{"fields", "model", "state"}; !slices.Equal(got, want) {
		t.Errorf("plain body keys = %v, want %v", got, want)
	}

	full := newExtractionRequest("clairic")
	full.Images = []schemas.ExtractionImage{{URL: schemas.Ptr(testImageURL)}, {ContentType: "image/png", Base64: "iVBORw0KGgo="}}
	full.Params = &schemas.ExtractionParameters{ReferenceDate: schemas.Ptr("2026-09-02"), IncludeQuantiles: schemas.Ptr(true), ReturnStateText: schemas.Ptr(true)}
	full.ExtraParams = map[string]interface{}{"questions": map[string]any{}}
	body := encodeExtractionBody(t, full)
	if got, want := bodyKeys(body), []string{"fields", "images", "model", "options", "reference_date", "state"}; !slices.Equal(got, want) {
		t.Errorf("full body keys = %v, want %v", got, want)
	}
	if got, want := bodyKeys(body["options"].(map[string]any)), []string{"include_quantiles", "return_state_text"}; !slices.Equal(got, want) {
		t.Errorf("options keys = %v, want %v", got, want)
	}
	images := body["images"].([]any)
	if images[0] != testImageURL {
		t.Errorf("data URL image = %v, want it sent as a string", images[0])
	}
	if object, ok := images[1].(map[string]any); !ok || object["content_type"] != "image/png" || object["base64"] != "iVBORw0KGgo=" {
		t.Errorf("base64 image = %v, want a content_type/base64 object", images[1])
	}
	estimate := body["fields"].(map[string]any)["days_to_pay"].(map[string]any)
	if got, want := bodyKeys(estimate), []string{"max", "min", "step", "type"}; !slices.Equal(got, want) {
		t.Errorf("estimate field keys = %v, want %v", got, want)
	}
}

// An empty input is still sent as a state: Clairic accepts "" when images carry the evidence.
func TestToCloudflareExtractionRequest_SendsEmptyState(t *testing.T) {
	request := newExtractionRequest("clairic")
	request.Input = ""
	if state, ok := encodeExtractionBody(t, request)["state"]; !ok || state != "" {
		t.Errorf("state = %v (present %v), want an empty string", state, ok)
	}
}

func TestExtraction_SendsBareClairicNameInURLAndBody(t *testing.T) {
	for _, model := range []string{"clairic", "@cf/cloudflare/clairic"} {
		t.Run(model, func(t *testing.T) {
			var capturedPath, capturedAuth string
			var capturedBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				capturedAuth = r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&capturedBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, clairicExtractionResponseBody)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Extraction(ctx, newTestKey("acct-1"), newExtractionRequest(model))
			if bifrostErr != nil {
				t.Fatalf("Extraction: %v", bifrostErr.Error.Message)
			}

			if want := "/accounts/acct-1/ai/run/@cf/cloudflare/clairic"; capturedPath != want {
				t.Errorf("path = %q, want %q", capturedPath, want)
			}
			if capturedAuth != "Bearer test-token" {
				t.Errorf("authorization = %q, want the key's token", capturedAuth)
			}
			if capturedBody["model"] != "clairic" || capturedBody["state"] != "Invoice INV-2291, net 30." {
				t.Errorf("body = %v, want the bare model and the input as state", capturedBody)
			}
			if resp.Model != "clairic" || resp.Truncated || resp.StateText == nil || *resp.StateText != "Invoice INV-2291, net 30." {
				t.Errorf("response = %+v, want Clairic's model, truncated and state_text", resp)
			}
		})
	}
}

// Answers keep Clairic's values as sent: a text span, a null value with a guess, and an
// estimate's distribution. Usage bills input_tokens, which already include schema tokens.
func TestExtraction_MapsAnswersAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, clairicExtractionResponseBody)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Extraction(ctx, newTestKey("acct-1"), newExtractionRequest("clairic"))
	if bifrostErr != nil {
		t.Fatalf("Extraction: %v", bifrostErr.Error.Message)
	}

	invoice := resp.Answers["invoice_number"]
	if invoice.Type != schemas.ExtractionFieldTypeText || string(invoice.Value) != `"INV-2291"` || !slices.Equal(invoice.Span, []int{8, 16}) || invoice.Confidence == nil || *invoice.Confidence != 0.99 {
		t.Errorf("invoice_number = %+v", invoice)
	}
	due := resp.Answers["due_date"]
	if string(due.Value) != "null" || string(due.Guess) != `"2026-10-02"` {
		t.Errorf("due_date value = %s guess = %s, want null and the guess", due.Value, due.Guess)
	}
	estimate := resp.Answers["days_to_pay"]
	if string(estimate.Value) != "30" || string(estimate.P10) != "25" || string(estimate.P90) != "33" || estimate.PAbove == nil {
		t.Errorf("days_to_pay = %+v", estimate)
	}

	usage := resp.Usage
	if usage == nil || usage.PromptTokens != 416 || usage.CompletionTokens != 0 || usage.TotalTokens != 416 {
		t.Fatalf("usage = %+v, want 416 prompt tokens", usage)
	}
	if usage.StateTokens == nil || *usage.StateTokens != 52 || usage.Truncated != nil {
		t.Errorf("usage state tokens = %v truncated = %v", usage.StateTokens, usage.Truncated)
	}

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var wire map[string]any
	_ = json.Unmarshal(encoded, &wire)
	if got, want := bodyKeys(wire), []string{"answers", "extra_fields", "model", "state_text", "truncated", "usage"}; !slices.Equal(got, want) {
		t.Errorf("response keys = %v, want %v", got, want)
	}
}

func TestExtraction_EnvelopeWithoutResultIsError(t *testing.T) {
	for name, body := range map[string]string{
		"success false":  `{"result":{},"success":false,"errors":[{"code":5000,"message":"upstream failed"}]}`,
		"missing result": `{"success":true,"errors":[]}`,
		"null result":    `{"result":null,"success":true,"errors":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if _, bifrostErr := provider.Extraction(ctx, newTestKey("acct-1"), newExtractionRequest("clairic")); bifrostErr == nil {
				t.Fatal("expected an error for an envelope without a result")
			}
		})
	}
}

func TestExtraction_PassesClairicValidationErrorsThrough(t *testing.T) {
	const message = "AiError: AiError: Request body failed validation: questions: Extra inputs are not permitted (req-1)"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		payload, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": message, "code": 3030}}, "success": false, "result": map[string]any{}})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Extraction(ctx, newTestKey("acct-1"), newExtractionRequest("clairic"))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %+v", bifrostErr)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != message {
		t.Errorf("message = %q, want Clairic's message", bifrostErr.Error.Message)
	}
}

func TestExtraction_MissingAccountIDIsConfigError(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Extraction(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-token"}}, newExtractionRequest("clairic"))
	if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, "account ID") {
		t.Fatalf("error = %+v, want the missing account ID error", bifrostErr)
	}
}

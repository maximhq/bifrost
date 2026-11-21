package subq_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/subq"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSubq(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SUBQ_API_KEY")) == "" {
		t.Skip("Skipping Subq tests because SUBQ_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:      schemas.Subq,
		ChatModel:     "subq-savant-1.4",
		TextModel:     "",
		DecisionModel: "subq-snap-1.2",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false, // Subq tool_choice is only auto/none
			ImageURL:              false, // Subq rejects image URLs
			ImageBase64:           true,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Decision:              true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           false, // Subq has no download endpoint
		},
	}

	t.Run("SubqTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestToSubqDecisionRequest_ConvertsDataURI(t *testing.T) {
	imageURL := "data:image/png;base64,abc123"
	req := &schemas.BifrostDecisionRequest{
		Provider: schemas.Subq,
		Model:    "subq-snap-1.2",
		Input: schemas.DecisionInput{
			Messages: []schemas.DecisionInputMessage{
				{
					Role: "user",
					Content: schemas.DecisionInputContent{
						Parts: []schemas.DecisionInputPart{
							{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Is this a keyboard?")},
							{Type: schemas.DecisionInputPartTypeImage, ImageURL: &imageURL},
						},
					},
				},
			},
		},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_keyboard"), Instructions: schemas.NewDecisionText("Is a keyboard visible?")},
		},
	}

	native, err := subq.ToSubqDecisionRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(native.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(native.Images))
	}
	if native.Images[0].MediaType != "image/png" || native.Images[0].Base64 != "abc123" {
		t.Fatalf("unexpected image %#v", native.Images[0])
	}
}

func TestToSubqDecisionRequest_RejectsHTTPURL(t *testing.T) {
	url := "https://example.com/keyboard.png"
	req := &schemas.BifrostDecisionRequest{
		Provider: schemas.Subq,
		Model:    "subq-snap-1.2",
		Input: schemas.DecisionInput{
			Messages: []schemas.DecisionInputMessage{
				{
					Role: "user",
					Content: schemas.DecisionInputContent{
						Parts: []schemas.DecisionInputPart{
							{Type: schemas.DecisionInputPartTypeImage, ImageURL: &url},
						},
					},
				},
			},
		},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_keyboard"), Instructions: schemas.NewDecisionText("Is a keyboard visible?")},
		},
	}
	_, err := subq.ToSubqDecisionRequest(req)
	if err == nil {
		t.Fatal("expected error for http image url")
	}
}

func TestSubqFileUploadForcesAssistantsPurpose(t *testing.T) {
	var gotPurpose, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		gotPurpose = r.FormValue("purpose")
		_, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		gotContentType = header.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "file_test",
			"object":     "file",
			"bytes":      4,
			"created_at": 1,
			"filename":   "note.txt",
			"purpose":    "assistants",
			"status":     "processed",
		})
	}))
	defer server.Close()

	provider, err := subq.NewSubqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	_, bifrostErr := provider.FileUpload(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostFileUploadRequest{
		File:     []byte("hi\n"),
		Filename: "note.txt",
		Purpose:  schemas.FilePurposeBatch, // ignored; Subq only accepts assistants
	})
	if bifrostErr != nil {
		t.Fatalf("upload failed: %v", bifrostErr.Error.Message)
	}
	if gotPurpose != "assistants" {
		t.Fatalf("expected purpose assistants, got %q", gotPurpose)
	}
	if gotContentType != "text/plain" {
		t.Fatalf("expected content-type text/plain, got %q", gotContentType)
	}
}

func TestSubqDecisionPostsSystemOneBody(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "subq-snap-1.2",
			"answers": map[string]any{
				"is_frustrated": map[string]any{"type": "noul", "noul": 0.9},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		})
	}))
	defer server.Close()

	provider, err := subq.NewSubqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	state := "Customer is yelling about a refund"
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resp, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostDecisionRequest{
		Provider: schemas.Subq,
		Model:    "subq-snap-1.2",
		Input:    schemas.DecisionInput{Text: &state},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated"), Instructions: schemas.NewDecisionText("Is the customer frustrated?")},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("decision failed: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Answers) != 1 {
		t.Fatalf("unexpected response %#v", resp)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["model"] != "subq-snap-1.2" {
		t.Fatalf("unexpected model %v", body["model"])
	}
}

func newTestSubqProvider(t *testing.T, baseURL string) *subq.SubqProvider {
	t.Helper()
	provider, err := subq.NewSubqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 5},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return provider
}

func TestSubqFileUploadEscapesFilename(t *testing.T) {
	for _, filename := range []string{`report"final.txt`, "a\r\nX-Injected: 1.txt"} {
		var gotFilename, gotInjected string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			gotFilename = header.Filename
			gotInjected = header.Header.Get("X-Injected")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "file_test", "object": "file", "purpose": "assistants", "status": "processed"})
		}))
		ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
		_, bifrostErr := newTestSubqProvider(t, server.URL).FileUpload(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostFileUploadRequest{
			File:     []byte("hi\n"),
			Filename: filename,
		})
		cancel()
		server.Close()
		if bifrostErr != nil {
			t.Fatalf("%q: upload failed: %v", filename, bifrostErr.Error.Message)
		}
		if gotInjected != "" {
			t.Fatalf("%q: filename injected a part header", filename)
		}
		if !strings.Contains(filename, "\r") && gotFilename != filename {
			t.Fatalf("expected filename %q, got %q", filename, gotFilename)
		}
	}
}

func TestSubqFileListPaginatesAcrossKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]any{}
		if r.Header.Get("Authorization") == "Bearer key-b" {
			data = append(data, map[string]any{"id": "file_b", "object": "file", "purpose": "assistants", "status": "processed"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
	}))
	defer server.Close()
	provider := newTestSubqProvider(t, server.URL)
	keys := []schemas.Key{{Value: *schemas.NewSecretVar("key-a")}, {Value: *schemas.NewSecretVar("key-b")}}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	first, bifrostErr := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{})
	if bifrostErr != nil {
		t.Fatalf("first page: %v", bifrostErr.Error.Message)
	}
	if len(first.Data) != 0 || !first.HasMore || first.After == nil {
		t.Fatalf("expected empty first page pointing at the next key, got %#v", first)
	}
	second, bifrostErr := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{After: first.After})
	if bifrostErr != nil {
		t.Fatalf("second page: %v", bifrostErr.Error.Message)
	}
	if len(second.Data) != 1 || second.Data[0].ID != "file_b" || second.HasMore {
		t.Fatalf("expected file_b from the second key, got %#v", second)
	}
}

func toolOutputImageInput() []schemas.ResponsesMessage {
	return []schemas.ResponsesMessage{{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{{
					Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
					ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr("data:image/png;base64,iVBORw0KGgo=")},
				}},
			},
		},
	}}
}

func TestSubqResponsesToolOutputImageFallsBackToChat(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl_1", "object": "chat.completion", "model": "subq-savant-1.4",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "ok"}}},
		})
	}))
	defer server.Close()
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	_, bifrostErr := newTestSubqProvider(t, server.URL).Responses(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostResponsesRequest{
		Provider: schemas.Subq, Model: "subq-savant-1.4", Input: toolOutputImageInput(),
	})
	if bifrostErr != nil {
		t.Fatalf("responses failed: %v", bifrostErr.Error.Message)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("expected chat fallback, got path %q", gotPath)
	}
}

func TestSubqResponsesRejectsImageWithPreviousResponseID(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	provider := newTestSubqProvider(t, server.URL)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Subq, Model: "subq-savant-1.4", Input: toolOutputImageInput(),
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("resp_prev")},
	}
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	if _, bifrostErr := provider.Responses(ctx, key, request); bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("expected 400 on Responses, got %#v", bifrostErr)
	}
	if _, bifrostErr := provider.ResponsesStream(ctx, nil, nil, key, request); bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("expected 400 on ResponsesStream, got %#v", bifrostErr)
	}
	if called {
		t.Fatal("request reached Subq")
	}
}

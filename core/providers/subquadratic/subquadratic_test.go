package subquadratic_test

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
	"github.com/maximhq/bifrost/core/providers/subquadratic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSubquadratic(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SUBQUADRATIC_API_KEY")) == "" {
		t.Skip("Skipping Subquadratic tests because SUBQUADRATIC_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:      schemas.Subquadratic,
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
			AutomaticFunctionCall: false, // SubQ tool_choice is only auto/none
			ImageURL:              false, // SubQ rejects image URLs
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
			FileContent:           false, // SubQ has no download endpoint
		},
	}

	t.Run("SubquadraticTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestToSubquadraticDecisionRequest_ConvertsDataURI(t *testing.T) {
	imageURL := "data:image/png;base64,abc123"
	req := &schemas.BifrostDecisionRequest{
		Provider: schemas.Subquadratic,
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

	native, err := subquadratic.ToSubquadraticDecisionRequest(req)
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

func TestToSubquadraticDecisionRequest_RejectsHTTPURL(t *testing.T) {
	url := "https://example.com/keyboard.png"
	req := &schemas.BifrostDecisionRequest{
		Provider: schemas.Subquadratic,
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
	_, err := subquadratic.ToSubquadraticDecisionRequest(req)
	if err == nil {
		t.Fatal("expected error for http image url")
	}
}

func TestSubquadraticFileUploadForcesAssistantsPurpose(t *testing.T) {
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

	provider, err := subquadratic.NewSubquadraticProvider(&schemas.ProviderConfig{
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
		Purpose:  schemas.FilePurposeBatch, // ignored; SubQ only accepts assistants
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

func TestSubquadraticDecisionPostsSystemOneBody(t *testing.T) {
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

	provider, err := subquadratic.NewSubquadraticProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	state := "Customer is yelling about a refund"
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resp, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostDecisionRequest{
		Provider: schemas.Subquadratic,
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

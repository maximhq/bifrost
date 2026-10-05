package perplexity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type decisionTestLogger struct{}

func (decisionTestLogger) Debug(string, ...any)                   {}
func (decisionTestLogger) Info(string, ...any)                    {}
func (decisionTestLogger) Warn(string, ...any)                    {}
func (decisionTestLogger) Error(string, ...any)                   {}
func (decisionTestLogger) Fatal(string, ...any)                   {}
func (decisionTestLogger) SetLevel(schemas.LogLevel)              {}
func (decisionTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (decisionTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func perplexityChoiceRequest(optionCount int) *schemas.BifrostDecisionRequest {
	criteria := make(map[string]any, optionCount)
	for i := 0; i < optionCount; i++ {
		criteria[fmt.Sprintf("option_%03d", i)] = fmt.Sprintf("Option %d", i)
	}
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Perplexity,
		Model:    "pplx-decider-v1-27b",
		State:    map[string]any{"content": "My laptop will not start"},
		Questions: map[string]schemas.DecisionQuestion{
			"category": {
				Kind:         schemas.DecisionKindChoice,
				Instructions: "Choose the matching category",
				Criteria:     criteria,
			},
		},
	}
}

func TestToPerplexityDecisionRequestMapsChoiceShape(t *testing.T) {
	request := perplexityChoiceRequest(2)
	request.Questions["category"] = schemas.DecisionQuestion{
		Kind:         schemas.DecisionKindChoice,
		Instructions: "Choose the matching category",
		Criteria: map[string]any{
			"hardware": "Physical device problems",
			"other":    nil,
		},
	}

	native, err := toPerplexityDecisionRequest(request)
	if err != nil {
		t.Fatalf("toPerplexityDecisionRequest() error = %v", err)
	}

	question := native.Questions["category"]
	if question.Type != perplexityQuestionTypeChoice {
		t.Fatalf("question type = %q, want %q", question.Type, perplexityQuestionTypeChoice)
	}
	criteria, ok := question.Criteria.(map[string]any)
	if !ok {
		t.Fatalf("criteria type = %T, want map[string]any", question.Criteria)
	}
	if _, ok := criteria["hardware"]; !ok {
		t.Fatal("hardware option key was not preserved")
	}
	if value, ok := criteria["other"]; !ok || value != nil {
		t.Fatalf("other option = %#v, want preserved null", value)
	}

	encoded, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), `"kind"`) {
		t.Fatalf("native request contains Bifrost kind field: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"type":"choice"`) {
		t.Fatalf("native request does not contain Perplexity type field: %s", encoded)
	}
}

func TestToPerplexityDecisionRequestEnforcesLimits(t *testing.T) {
	t.Run("choice options", func(t *testing.T) {
		_, err := toPerplexityDecisionRequest(perplexityChoiceRequest(perplexityMaxChoiceOptions + 1))
		if err == nil || !strings.Contains(err.Error(), "at most 255") {
			t.Fatalf("error = %v, want choice limit error", err)
		}
	})

	t.Run("questions", func(t *testing.T) {
		request := perplexityChoiceRequest(1)
		request.Questions = make(map[string]schemas.DecisionQuestion, perplexityMaxQuestions+1)
		for i := 0; i <= perplexityMaxQuestions; i++ {
			request.Questions[fmt.Sprintf("question_%03d", i)] = schemas.DecisionQuestion{
				Kind:         schemas.DecisionKindNoul,
				Instructions: "Is this true?",
			}
		}
		_, err := toPerplexityDecisionRequest(request)
		if err == nil || !strings.Contains(err.Error(), "at most 128") {
			t.Fatalf("error = %v, want question limit error", err)
		}
	})
}

func TestToBifrostPerplexityDecisionResponseMapsChoiceAndUsage(t *testing.T) {
	request := perplexityChoiceRequest(1)
	choice := "option_000"
	confidence := 0.91
	response := &PerplexityDecisionResponse{
		ID:    "decision-123",
		Model: "pplx-decider-v1-27b",
		Answers: map[string]PerplexityDecisionAnswer{
			"category": {
				Type:          perplexityQuestionTypeChoice,
				Choice:        &choice,
				Confidence:    &confidence,
				Probabilities: map[string]float64{"option_000": 0.91},
			},
		},
		Usage: &PerplexityDecisionUsage{InputTokens: 37},
	}

	result, bifrostErr := toBifrostPerplexityDecisionResponse(response, request)
	if bifrostErr != nil {
		t.Fatalf("toBifrostPerplexityDecisionResponse() error = %v", bifrostErr)
	}
	answer := result.Answers["category"]
	if answer.Value != choice {
		t.Fatalf("answer value = %#v, want %q", answer.Value, choice)
	}
	if answer.Confidence == nil || *answer.Confidence != confidence {
		t.Fatalf("confidence = %#v, want %v", answer.Confidence, confidence)
	}
	if answer.Probabilities[choice] != confidence {
		t.Fatalf("probability = %v, want %v", answer.Probabilities[choice], confidence)
	}
	if result.Usage == nil || result.Usage.PromptTokens != 37 || result.Usage.TotalTokens != 37 {
		t.Fatalf("usage = %#v, want 37 input and total tokens", result.Usage)
	}
}

func TestToBifrostPerplexityDecisionResponseValidatesScoreRange(t *testing.T) {
	request := &schemas.BifrostDecisionRequest{
		Provider: schemas.Perplexity,
		Model:    "pplx-decider-v1-27b",
		State:    map[string]any{"content": "The whole team is locked out"},
		Questions: map[string]schemas.DecisionQuestion{
			"severity": {
				Kind:         schemas.DecisionKindScore,
				Instructions: "How severe is the customer impact?",
				Criteria:     []string{"No impact", "Minor", "Blocks a workflow", "Outage"},
			},
		},
	}
	testCases := []struct {
		name    string
		score   float64
		wantErr bool
	}{
		{name: "lowest level", score: 0},
		{name: "fractional expected level", score: 2.98},
		{name: "highest level", score: 3},
		{name: "below range", score: -0.01, wantErr: true},
		{name: "above range", score: 3.01, wantErr: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			score := testCase.score
			response := &PerplexityDecisionResponse{
				Model: "pplx-decider-v1-27b",
				Answers: map[string]PerplexityDecisionAnswer{
					"severity": {Type: perplexityQuestionTypeScore, Score: &score},
				},
			}

			result, bifrostErr := toBifrostPerplexityDecisionResponse(response, request)
			if testCase.wantErr {
				if bifrostErr == nil {
					t.Fatalf("toBifrostPerplexityDecisionResponse() accepted out-of-range score %v", score)
				}
				return
			}
			if bifrostErr != nil {
				t.Fatalf("toBifrostPerplexityDecisionResponse() error = %v", bifrostErr)
			}
			if result.Answers["severity"].Value != score {
				t.Fatalf("answer value = %#v, want %v", result.Answers["severity"].Value, score)
			}
		})
	}
}

func TestPerplexityDecisionUsesNativeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/decisions" {
			http.Error(writer, "wrong path", http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-api-key" {
			http.Error(writer, "wrong auth", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "read failed", http.StatusInternalServerError)
			return
		}
		if strings.Contains(string(body), `"kind"`) || !strings.Contains(string(body), `"type":"choice"`) {
			http.Error(writer, "wrong question shape", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("x-request-id", "perplexity-request-123")
		_, _ = fmt.Fprint(writer, `{
			"id": "decision-123",
			"model": "pplx-decider-v1-27b",
			"answers": {
				"category": {
					"type": "choice",
					"choice": "option_000",
					"confidence": 0.98,
					"probabilities": {"option_000": 0.98}
				}
			},
			"usage": {"input_tokens": 42}
		}`)
	}))
	defer server.Close()

	provider, err := NewPerplexityProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 2,
		},
	}, decisionTestLogger{})
	if err != nil {
		t.Fatalf("NewPerplexityProvider() error = %v", err)
	}
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, key, perplexityChoiceRequest(1))
	if bifrostErr != nil {
		t.Fatalf("Decision() error = %v", bifrostErr)
	}
	if response.Answers["category"].Value != "option_000" {
		t.Fatalf("answer = %#v, want option_000", response.Answers["category"].Value)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 42 {
		t.Fatalf("usage = %#v, want 42 input tokens", response.Usage)
	}
	if response.ExtraFields.ProviderResponseHeaders["X-Request-Id"] != "perplexity-request-123" &&
		response.ExtraFields.ProviderResponseHeaders["x-request-id"] != "perplexity-request-123" {
		t.Fatalf("provider headers = %#v, want x-request-id", response.ExtraFields.ProviderResponseHeaders)
	}
}

func TestPerplexityDecisionPreservesEmulationForChatModels(t *testing.T) {
	provider, err := NewPerplexityProvider(&schemas.ProviderConfig{}, decisionTestLogger{})
	if err != nil {
		t.Fatalf("NewPerplexityProvider() error = %v", err)
	}
	request := perplexityChoiceRequest(1)
	request.Model = "sonar-pro"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, request)
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("Decision() error = %#v, want unsupported operation for emulation", bifrostErr)
	}
}

func TestPerplexityDecisionRejectsOversizedBody(t *testing.T) {
	provider, err := NewPerplexityProvider(&schemas.ProviderConfig{}, decisionTestLogger{})
	if err != nil {
		t.Fatalf("NewPerplexityProvider() error = %v", err)
	}
	request := perplexityChoiceRequest(1)
	request.State = strings.Repeat("x", perplexityMaxDecisionBodyBytes)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, request)
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Decision() error = %#v, want 400 for oversized body", bifrostErr)
	}
}

func TestPerplexityDecisionErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, body: `{"error":{"message":"invalid decision"}}`},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, body: `{"error":{"message":"invalid api key"}}`},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`},
		{name: "provider failure", statusCode: http.StatusInternalServerError, body: `{"error":{"message":"provider failed"}}`},
		{name: "html gateway timeout", statusCode: http.StatusGatewayTimeout, body: `<html>gateway timeout</html>`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.statusCode)
				_, _ = fmt.Fprint(writer, testCase.body)
			}))
			defer server.Close()

			provider, err := NewPerplexityProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 2,
				},
			}, decisionTestLogger{})
			if err != nil {
				t.Fatalf("NewPerplexityProvider() error = %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, bifrostErr := provider.Decision(ctx, schemas.Key{}, perplexityChoiceRequest(1))
			if bifrostErr == nil {
				t.Fatal("Decision() error = nil, want provider error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != testCase.statusCode {
				t.Fatalf("status = %#v, want %d", bifrostErr.StatusCode, testCase.statusCode)
			}
			if bifrostErr.Error == nil || strings.TrimSpace(bifrostErr.Error.Message) == "" {
				t.Fatalf("error = %#v, want non-empty message", bifrostErr)
			}
		})
	}
}

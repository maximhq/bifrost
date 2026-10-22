package perplexity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/perplexity"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPerplexity(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("PERPLEXITY_API_KEY")) == "" {
		t.Skip("Skipping Perplexity tests because PERPLEXITY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Perplexity,
		ChatModel:      "sonar-pro",
		TextModel:      "", // Perplexity doesn't support text completion
		EmbeddingModel: "", // Perplexity doesn't support embedding
		DecisionModel:  "pplx-decider-v1.1-27b",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:         false, // Not supported
			SimpleChat:             true,
			CompletionStream:       true,
			MultiTurnConversation:  true,
			ToolCalls:              false,
			MultipleToolCalls:      false,
			End2EndToolCalling:     false,
			AutomaticFunctionCall:  false,
			ImageURL:               false, // Not supported yet
			ImageBase64:            false, // Not supported yet
			MultipleImages:         false, // Not supported yet
			CompleteEnd2End:        false,
			FileBase64:             false,
			FileURL:                false,
			Embedding:              false, // Not supported yet
			ListModels:             false,
			PassThroughExtraParams: true,
			Decision:               true,
		},
	}

	t.Run("PerplexityTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestToBifrostChatResponse_Citations(t *testing.T) {
	t.Parallel()

	t.Run("citations are mapped to bifrost response", func(t *testing.T) {
		response := &perplexity.PerplexityChatResponse{
			ID:      "test-id",
			Model:   "sonar-pro",
			Object:  "chat.completion",
			Created: 1234567890,
			Citations: []string{
				"https://example.com/article1",
				"https://example.com/article2",
				"https://example.com/article3",
			},
			SearchResults: []schemas.SearchResult{
				{Title: "Article 1", URL: "https://example.com/article1"},
			},
			Videos: []schemas.VideoResult{
				{URL: "https://example.com/video1"},
			},
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: schemas.Ptr("stop"),
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    "assistant",
							Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Here is the answer with citations [1][2][3].")},
						},
					},
				},
			},
			Usage: &perplexity.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
				CitationTokens:   schemas.Ptr(5),
				NumSearchQueries: schemas.Ptr(2),
			},
		}

		bifrostResp := response.ToBifrostChatResponse("sonar-pro")

		if bifrostResp == nil {
			t.Fatal("expected non-nil bifrost response")
		}

		// Verify citations are preserved
		if len(bifrostResp.Citations) != 3 {
			t.Fatalf("expected 3 citations, got %d", len(bifrostResp.Citations))
		}
		if bifrostResp.Citations[0] != "https://example.com/article1" {
			t.Errorf("expected first citation to be 'https://example.com/article1', got '%s'", bifrostResp.Citations[0])
		}
		if bifrostResp.Citations[1] != "https://example.com/article2" {
			t.Errorf("expected second citation to be 'https://example.com/article2', got '%s'", bifrostResp.Citations[1])
		}
		if bifrostResp.Citations[2] != "https://example.com/article3" {
			t.Errorf("expected third citation to be 'https://example.com/article3', got '%s'", bifrostResp.Citations[2])
		}

		// Verify search results are preserved
		if len(bifrostResp.SearchResults) != 1 {
			t.Fatalf("expected 1 search result, got %d", len(bifrostResp.SearchResults))
		}

		// Verify videos are preserved
		if len(bifrostResp.Videos) != 1 {
			t.Fatalf("expected 1 video, got %d", len(bifrostResp.Videos))
		}

		// Verify usage citation tokens are mapped
		if bifrostResp.Usage == nil || bifrostResp.Usage.CompletionTokensDetails == nil {
			t.Fatal("expected usage with completion token details")
		}
		if bifrostResp.Usage.CompletionTokensDetails.CitationTokens == nil || *bifrostResp.Usage.CompletionTokensDetails.CitationTokens != 5 {
			t.Error("expected citation_tokens to be 5")
		}
		if bifrostResp.Usage.CompletionTokensDetails.NumSearchQueries == nil || *bifrostResp.Usage.CompletionTokensDetails.NumSearchQueries != 2 {
			t.Error("expected num_search_queries to be 2")
		}
		if bifrostResp.Usage.ToolUsage.WebSearch.NumRequests != 2 {
			t.Error("expected tool_usage.web_search.num_requests to be 2")
		}
	})

	t.Run("nil citations remain nil", func(t *testing.T) {
		response := &perplexity.PerplexityChatResponse{
			ID:      "test-id-2",
			Model:   "sonar-pro",
			Object:  "chat.completion",
			Created: 1234567890,
			Choices: []schemas.BifrostResponseChoice{},
		}

		bifrostResp := response.ToBifrostChatResponse("sonar-pro")

		if bifrostResp == nil {
			t.Fatal("expected non-nil bifrost response")
		}
		if bifrostResp.Citations != nil {
			t.Errorf("expected nil citations, got %v", bifrostResp.Citations)
		}
		if bifrostResp.SearchResults != nil {
			t.Errorf("expected nil search results, got %v", bifrostResp.SearchResults)
		}
	})

	t.Run("empty citations slice is preserved", func(t *testing.T) {
		response := &perplexity.PerplexityChatResponse{
			ID:        "test-id-3",
			Model:     "sonar-pro",
			Object:    "chat.completion",
			Created:   1234567890,
			Citations: []string{},
			Choices:   []schemas.BifrostResponseChoice{},
		}

		bifrostResp := response.ToBifrostChatResponse("sonar-pro")

		if bifrostResp == nil {
			t.Fatal("expected non-nil bifrost response")
		}
		if bifrostResp.Citations == nil {
			t.Error("expected non-nil (empty) citations slice")
		}
		if len(bifrostResp.Citations) != 0 {
			t.Errorf("expected 0 citations, got %d", len(bifrostResp.Citations))
		}
	})
}

func TestWebSearchOption_JSONSerialization(t *testing.T) {
	t.Parallel()

	t.Run("corrected field name serializes as image_results_enhanced_relevance", func(t *testing.T) {
		option := perplexity.WebSearchOption{
			SearchContextSize:             schemas.Ptr("high"),
			ImageResultsEnhancedRelevance: schemas.Ptr(true),
			SearchType:                    schemas.Ptr("news"),
		}

		data, err := json.Marshal(option)
		if err != nil {
			t.Fatalf("failed to marshal WebSearchOption: %v", err)
		}

		jsonStr := string(data)

		// Verify corrected field name
		if !strings.Contains(jsonStr, `"image_results_enhanced_relevance"`) {
			t.Errorf("expected JSON to contain 'image_results_enhanced_relevance', got: %s", jsonStr)
		}
		// Verify old incorrect field name is NOT present
		if strings.Contains(jsonStr, `"image_search_relevance_enhanced"`) {
			t.Errorf("JSON should not contain old field name 'image_search_relevance_enhanced', got: %s", jsonStr)
		}
		// Verify search_type is present
		if !strings.Contains(jsonStr, `"search_type"`) {
			t.Errorf("expected JSON to contain 'search_type', got: %s", jsonStr)
		}
	})

	t.Run("deserialization with corrected field name", func(t *testing.T) {
		jsonStr := `{"search_context_size":"medium","image_results_enhanced_relevance":true,"search_type":"academic"}`

		var option perplexity.WebSearchOption
		if err := json.Unmarshal([]byte(jsonStr), &option); err != nil {
			t.Fatalf("failed to unmarshal WebSearchOption: %v", err)
		}

		if option.SearchContextSize == nil || *option.SearchContextSize != "medium" {
			t.Error("expected search_context_size to be 'medium'")
		}
		if option.ImageResultsEnhancedRelevance == nil || !*option.ImageResultsEnhancedRelevance {
			t.Error("expected image_results_enhanced_relevance to be true")
		}
		if option.SearchType == nil || *option.SearchType != "academic" {
			t.Error("expected search_type to be 'academic'")
		}
	})

	t.Run("search_type extraction from ExtraParams", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "sonar-pro",
			Input: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")}},
			},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"web_search_options": []interface{}{
						map[string]interface{}{
							"search_context_size":              "high",
							"image_results_enhanced_relevance": true,
							"search_type":                      "news",
						},
					},
				},
			},
		}

		result := perplexity.ToPerplexityChatCompletionRequest(bifrostReq)

		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if len(result.WebSearchOptions) != 1 {
			t.Fatalf("expected 1 web search option, got %d", len(result.WebSearchOptions))
		}
		opt := result.WebSearchOptions[0]
		if opt.SearchContextSize == nil || *opt.SearchContextSize != "high" {
			t.Error("expected search_context_size to be 'high'")
		}
		if opt.ImageResultsEnhancedRelevance == nil || !*opt.ImageResultsEnhancedRelevance {
			t.Error("expected image_results_enhanced_relevance to be true")
		}
		if opt.SearchType == nil || *opt.SearchType != "news" {
			t.Error("expected search_type to be 'news'")
		}
	})
}

func TestBifrostCost_DeserializationWithPerplexityFields(t *testing.T) {
	t.Parallel()

	t.Run("maps legacy flat shape onto nested", func(t *testing.T) {
		jsonStr := `{
			"input_tokens_cost": 0.001,
			"output_tokens_cost": 0.002,
			"reasoning_tokens_cost": 0.003,
			"citation_tokens_cost": 0.004,
			"search_queries_cost": 0.005,
			"request_cost": 0.006,
			"total_cost": 0.021
		}`

		var cost schemas.BifrostCost
		if err := cost.UnmarshalJSON([]byte(jsonStr)); err != nil {
			t.Fatalf("failed to unmarshal BifrostCost: %v", err)
		}

		// The legacy flat shape maps onto the nested schema: the request surcharge
		// folds into the input side; reasoning/citation/search are output-side
		// categories. Detail fields carry the exact JSON values.
		if cost.InputCostDetails == nil || cost.OutputCostDetails == nil {
			t.Fatalf("expected nested cost details, got %+v", cost)
		}
		if cost.InputCostDetails.TextCost != 0.001 {
			t.Errorf("expected input text cost 0.001, got %f", cost.InputCostDetails.TextCost)
		}
		if cost.InputCostDetails.RequestCost != 0.006 {
			t.Errorf("expected request cost 0.006, got %f", cost.InputCostDetails.RequestCost)
		}
		if cost.OutputCostDetails.TextCost != 0.002 {
			t.Errorf("expected output text cost 0.002, got %f", cost.OutputCostDetails.TextCost)
		}
		if cost.OutputCostDetails.ReasoningCost != 0.003 {
			t.Errorf("expected reasoning cost 0.003, got %f", cost.OutputCostDetails.ReasoningCost)
		}
		if cost.OutputCostDetails.CitationCost != 0.004 {
			t.Errorf("expected citation cost 0.004, got %f", cost.OutputCostDetails.CitationCost)
		}
		if cost.OutputCostDetails.SearchQueriesCost != 0.005 {
			t.Errorf("expected search queries cost 0.005, got %f", cost.OutputCostDetails.SearchQueriesCost)
		}
		if cost.TotalCost != 0.021 {
			t.Errorf("expected total_cost 0.021, got %f", cost.TotalCost)
		}
	})

	t.Run("still works with float-only cost", func(t *testing.T) {
		var cost schemas.BifrostCost
		if err := cost.UnmarshalJSON([]byte(`0.42`)); err != nil {
			t.Fatalf("failed to unmarshal float cost: %v", err)
		}
		if cost.TotalCost != 0.42 {
			t.Errorf("expected total_cost 0.42, got %f", cost.TotalCost)
		}
	})

	t.Run("perplexity usage cost round-trip", func(t *testing.T) {
		jsonStr := `{
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"citation_tokens": 10,
			"cost": {
				"input_tokens_cost": 0.01,
				"output_tokens_cost": 0.02,
				"citation_tokens_cost": 0.005,
				"search_queries_cost": 0.003,
				"total_cost": 0.038
			}
		}`

		var usage perplexity.Usage
		if err := json.Unmarshal([]byte(jsonStr), &usage); err != nil {
			t.Fatalf("failed to unmarshal Usage: %v", err)
		}

		if usage.Cost == nil {
			t.Fatal("expected non-nil cost")
		}
		if usage.Cost.CitationTokensCost != 0.005 {
			t.Errorf("expected citation_tokens_cost 0.005, got %f", usage.Cost.CitationTokensCost)
		}
		if usage.Cost.SearchQueriesCost != 0.003 {
			t.Errorf("expected search_queries_cost 0.003, got %f", usage.Cost.SearchQueriesCost)
		}
	})
}

func TestToPerplexityChatCompletionRequest_ToolCalling(t *testing.T) {
	t.Parallel()

	t.Run("tools and tool_choice are mapped", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "sonar-pro",
			Input: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What's the weather?")}},
			},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{
					{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        "get_weather",
							Description: schemas.Ptr("Get the weather for a location"),
						},
					},
				},
				ToolChoice: &schemas.ChatToolChoice{
					ChatToolChoiceStr: schemas.Ptr("auto"),
				},
				ParallelToolCalls: schemas.Ptr(true),
			},
		}

		result := perplexity.ToPerplexityChatCompletionRequest(bifrostReq)

		if result == nil {
			t.Fatal("expected non-nil result")
		}

		// Verify tools
		if len(result.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(result.Tools))
		}
		if result.Tools[0].Type != schemas.ChatToolTypeFunction {
			t.Errorf("expected tool type 'function', got '%s'", result.Tools[0].Type)
		}
		if result.Tools[0].Function == nil || result.Tools[0].Function.Name != "get_weather" {
			t.Error("expected tool function name 'get_weather'")
		}

		// Verify tool_choice
		if result.ToolChoice == nil {
			t.Fatal("expected non-nil tool_choice")
		}
		if result.ToolChoice.ChatToolChoiceStr == nil || *result.ToolChoice.ChatToolChoiceStr != "auto" {
			t.Error("expected tool_choice to be 'auto'")
		}

		// Verify parallel_tool_calls
		if result.ParallelToolCalls == nil || !*result.ParallelToolCalls {
			t.Error("expected parallel_tool_calls to be true")
		}
	})

	t.Run("nil tools remain nil", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "sonar-pro",
			Input: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
			},
			Params: &schemas.ChatParameters{},
		}

		result := perplexity.ToPerplexityChatCompletionRequest(bifrostReq)

		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Tools != nil {
			t.Errorf("expected nil tools, got %v", result.Tools)
		}
		if result.ToolChoice != nil {
			t.Error("expected nil tool_choice")
		}
		if result.ParallelToolCalls != nil {
			t.Error("expected nil parallel_tool_calls")
		}
	})
}

func TestToPerplexityChatCompletionRequest_NewFields(t *testing.T) {
	t.Parallel()

	t.Run("stop, logprobs, top_logprobs are mapped", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "sonar-pro",
			Input: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")}},
			},
			Params: &schemas.ChatParameters{
				Stop:        []string{"END", "STOP"},
				LogProbs:    schemas.Ptr(true),
				TopLogProbs: schemas.Ptr(5),
			},
		}

		result := perplexity.ToPerplexityChatCompletionRequest(bifrostReq)

		if result == nil {
			t.Fatal("expected non-nil result")
		}

		// Verify stop
		if len(result.Stop) != 2 {
			t.Fatalf("expected 2 stop sequences, got %d", len(result.Stop))
		}
		if result.Stop[0] != "END" || result.Stop[1] != "STOP" {
			t.Errorf("expected stop sequences ['END', 'STOP'], got %v", result.Stop)
		}

		// Verify logprobs
		if result.LogProbs == nil || !*result.LogProbs {
			t.Error("expected logprobs to be true")
		}

		// Verify top_logprobs
		if result.TopLogProbs == nil || *result.TopLogProbs != 5 {
			t.Error("expected top_logprobs to be 5")
		}
	})

	t.Run("perplexity-specific fields from ExtraParams", func(t *testing.T) {
		bifrostReq := &schemas.BifrostChatRequest{
			Model: "sonar-pro",
			Input: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")}},
			},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"num_search_results":     5,
					"num_images":             3,
					"search_language_filter": []interface{}{"en", "fr"},
					"image_format_filter":    []interface{}{"png", "jpg"},
					"image_domain_filter":    []interface{}{"example.com"},
					"safe_search":            true,
					"stream_mode":            "partial",
				},
			},
		}

		result := perplexity.ToPerplexityChatCompletionRequest(bifrostReq)

		if result == nil {
			t.Fatal("expected non-nil result")
		}

		// Verify num_search_results
		if result.NumSearchResults == nil || *result.NumSearchResults != 5 {
			t.Errorf("expected num_search_results to be 5, got %v", result.NumSearchResults)
		}

		// Verify num_images
		if result.NumImages == nil || *result.NumImages != 3 {
			t.Errorf("expected num_images to be 3, got %v", result.NumImages)
		}

		// Verify search_language_filter
		if len(result.SearchLanguageFilter) != 2 {
			t.Fatalf("expected 2 search language filters, got %d", len(result.SearchLanguageFilter))
		}
		if result.SearchLanguageFilter[0] != "en" || result.SearchLanguageFilter[1] != "fr" {
			t.Errorf("expected search_language_filter ['en', 'fr'], got %v", result.SearchLanguageFilter)
		}

		// Verify image_format_filter
		if len(result.ImageFormatFilter) != 2 {
			t.Fatalf("expected 2 image format filters, got %d", len(result.ImageFormatFilter))
		}
		if result.ImageFormatFilter[0] != "png" || result.ImageFormatFilter[1] != "jpg" {
			t.Errorf("expected image_format_filter ['png', 'jpg'], got %v", result.ImageFormatFilter)
		}

		// Verify image_domain_filter
		if len(result.ImageDomainFilter) != 1 {
			t.Fatalf("expected 1 image domain filter, got %d", len(result.ImageDomainFilter))
		}
		if result.ImageDomainFilter[0] != "example.com" {
			t.Errorf("expected image_domain_filter ['example.com'], got %v", result.ImageDomainFilter)
		}

		// Verify safe_search
		if result.SafeSearch == nil || !*result.SafeSearch {
			t.Error("expected safe_search to be true")
		}

		// Verify stream_mode
		if result.StreamMode == nil || *result.StreamMode != "partial" {
			t.Errorf("expected stream_mode to be 'partial', got %v", result.StreamMode)
		}

		// Verify fields are removed from ExtraParams
		for _, key := range []string{"num_search_results", "num_images", "search_language_filter", "image_format_filter", "image_domain_filter", "safe_search", "stream_mode"} {
			if _, exists := result.ExtraParams[key]; exists {
				t.Errorf("expected '%s' to be removed from ExtraParams", key)
			}
		}
	})

	t.Run("new fields serialize correctly in JSON", func(t *testing.T) {
		req := perplexity.PerplexityChatRequest{
			Model: "sonar-pro",
			Messages: []schemas.ChatMessage{
				{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")}},
			},
			NumSearchResults:     schemas.Ptr(10),
			NumImages:            schemas.Ptr(5),
			SearchLanguageFilter: []string{"en"},
			SafeSearch:           schemas.Ptr(true),
			StreamMode:           schemas.Ptr("partial"),
			Stop:                 []string{"END"},
			LogProbs:             schemas.Ptr(true),
			TopLogProbs:          schemas.Ptr(3),
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal request: %v", err)
		}

		jsonStr := string(data)

		expectedFields := []string{
			`"num_search_results":10`,
			`"num_images":5`,
			`"search_language_filter":["en"]`,
			`"safe_search":true`,
			`"stream_mode":"partial"`,
			`"stop":["END"]`,
			`"logprobs":true`,
			`"top_logprobs":3`,
		}

		for _, field := range expectedFields {
			if !strings.Contains(jsonStr, field) {
				t.Errorf("expected JSON to contain '%s', got: %s", field, jsonStr)
			}
		}
	})
}

const deciderResponseBody = `{"model":"pplx-decider-v1.1-27b","answers":{"is_red":{"type":"noul","noul":0.97}},"usage":{"input_tokens":180,"output_tokens":1}}`

const testImageURL = "data:image/png;base64,iVBORw0KGgo="

func newDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider:         schemas.Perplexity,
		Model:            "pplx-decider-v1.1-27b",
		Input:            input,
		SafetyIdentifier: schemas.Ptr("user-123"),
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

func decisionUserMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

func decisionTextPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

func decisionImagePart(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func encodePerplexityDecision(t *testing.T, request *schemas.BifrostDecisionRequest) string {
	t.Helper()
	native, err := perplexity.ToPerplexityDecisionRequest(request)
	if err != nil {
		t.Fatalf("ToPerplexityDecisionRequest: %v", err)
	}
	data, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatalf("MarshalProviderRequest: %v", err)
	}
	return string(data)
}

// TestToPerplexityDecisionRequest_MatchesTypesafeWithoutImages pins that an input without
// images sends exactly Typesafe's body, so questions and state behave as they do there,
// and that no user field is sent: Perplexity rejects unknown top-level fields.
func TestToPerplexityDecisionRequest_MatchesTypesafeWithoutImages(t *testing.T) {
	for name, input := range map[string]schemas.DecisionInput{
		"text":       {Text: schemas.Ptr("The sky is blue.")},
		"structured": {Structured: map[string]any{"ticket": "A-4021"}},
		"messages":   {Messages: []schemas.DecisionInputMessage{decisionUserMessage(decisionTextPart("hi"))}},
	} {
		request := newDecisionRequest(input)
		got := encodePerplexityDecision(t, request)
		native, err := typesafe.ToTypesafeDecisionRequest(request)
		if err != nil {
			t.Fatalf("%s: ToTypesafeDecisionRequest: %v", name, err)
		}
		want, err := providerUtils.MarshalProviderRequest(native)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: body =\n%s\nwant Typesafe's\n%s", name, got, want)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got), &top); err != nil {
			t.Fatal(err)
		}
		if _, ok := top["user"]; ok {
			t.Errorf("%s: body carries a user field: %s", name, got)
		}
	}
}

// TestToPerplexityDecisionRequest_SendsImagesAsStateItems pins the image layout Perplexity
// reads: plain-string text items and image_url items, in order, in the state array.
func TestToPerplexityDecisionRequest_SendsImagesAsStateItems(t *testing.T) {
	body := encodePerplexityDecision(t, newDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		decisionUserMessage(decisionTextPart("Look at this"), decisionImagePart(testImageURL)),
	}}))
	want := `"state":["Look at this",{"type":"image_url","image_url":{"url":"` + testImageURL + `"}}]`
	if !strings.Contains(body, want) {
		t.Errorf("body =\n%s\nwant state\n%s", body, want)
	}
}

func TestToPerplexityDecisionRequest_RejectsImageWithoutURL(t *testing.T) {
	request := newDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		decisionUserMessage(decisionTextPart("hi"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage}),
	}})
	if _, err := perplexity.ToPerplexityDecisionRequest(request); err == nil {
		t.Error("expected an error for an input_image part without an image_url")
	}
}

func newTestProvider(t *testing.T, baseURL string) *perplexity.PerplexityProvider {
	t.Helper()
	provider, err := perplexity.NewPerplexityProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewPerplexityProvider: %v", err)
	}
	return provider
}

func newTestKey() schemas.Key {
	return schemas.Key{Value: schemas.SecretVar{Val: "test-token"}, Models: []string{"*"}}
}

// TestResponses_RoutesSonarFamilyToChatCompletions pins the Responses routing: the
// Sonar family, bare "sonar" included, is served only on /chat/completions (Perplexity's
// /v1/responses answers `model "sonar" is not supported`), and every other model goes to
// /v1/responses.
func TestResponses_RoutesSonarFamilyToChatCompletions(t *testing.T) {
	cases := []struct {
		model    string
		wantPath string
	}{
		{"sonar", "/chat/completions"},
		{"perplexity/sonar", "/chat/completions"},
		{"SONAR", "/chat/completions"},
		{"sonar-pro", "/chat/completions"},
		{"perplexity/sonar-reasoning-pro", "/chat/completions"},
		{"openai/gpt-5.4", "/v1/responses"},
		{"sonar2", "/v1/responses"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			var capturedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"error":{"message":"stop here","type":"invalid_request_error"}}`)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, _ = provider.Responses(ctx, newTestKey(), &schemas.BifrostResponsesRequest{
				Provider: schemas.Perplexity,
				Model:    tc.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
				}},
			})
			if capturedPath != tc.wantPath {
				t.Errorf("path = %q, want %q", capturedPath, tc.wantPath)
			}
		})
	}
}

// TestDecision_PostsToDecisionsAndTakesIDFromHeader pins the native call: the request goes
// to /v1/decisions with Bearer auth, and the id comes from x-request-id since the body has none.
func TestDecision_PostsToDecisionsAndTakesIDFromHeader(t *testing.T) {
	var capturedPath, capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "14bbdf68-23cc-40bd-ac49-64e7b76845a3")
		_, _ = fmt.Fprint(w, deciderResponseBody)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Decision(ctx, newTestKey(), newDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	if bifrostErr != nil {
		t.Fatalf("Decision: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/v1/decisions" {
		t.Errorf("path = %q, want /v1/decisions", capturedPath)
	}
	if capturedAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", capturedAuth)
	}
	if resp.ID != "14bbdf68-23cc-40bd-ac49-64e7b76845a3" {
		t.Errorf("id = %q, want the x-request-id header", resp.ID)
	}
	if len(resp.Answers) != 1 || resp.Answers[0].Probability == nil || *resp.Answers[0].Probability != 0.97 {
		t.Errorf("unexpected answers: %+v", resp.Answers)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 180 || resp.Usage.CompletionTokens != 1 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

// TestDecision_KeepsProviderHeadersOnErrors pins that a failed decision still records
// Perplexity's response headers on the context, which is the only place the HTTP layer
// reads them from for an error reply: without them a 429 loses Retry-After and a 400
// its x-request-id.
func TestDecision_KeepsProviderHeadersOnErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-429")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":null}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.Decision(ctx, newTestKey(), newDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})); bifrostErr == nil {
		t.Fatal("expected an error")
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	got := make(map[string]string, len(headers))
	for name, value := range headers {
		got[strings.ToLower(name)] = value
	}
	if got["x-request-id"] != "req-429" || got["retry-after"] != "7" {
		t.Errorf("context headers = %v, want x-request-id and retry-after from the error reply", headers)
	}
}

// TestDecision_PassesPerplexityErrorsThrough pins that Perplexity's own 400 reaches the
// caller with its status and message.
func TestDecision_PassesPerplexityErrorsThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Images must be base64 PNG, JPEG, or WebP data URLs","type":"invalid_request_error","code":"400","param":null}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, newTestKey(), newDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		decisionUserMessage(decisionImagePart("https://example.com/red.png")),
	}}))
	if bifrostErr == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "Images must be base64") {
		t.Errorf("message = %+v, want Perplexity's", bifrostErr.Error)
	}
}

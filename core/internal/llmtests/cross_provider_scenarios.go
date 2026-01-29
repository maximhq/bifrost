package llmtests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// CORE DATA STRUCTURES
// =============================================================================

// MessageModality defines the type of interaction required
type MessageModality string

const (
	ModalityText      MessageModality = "text"
	ModalityTool      MessageModality = "tool"
	ModalityVision    MessageModality = "vision"
	ModalityReasoning MessageModality = "reasoning"
)

// ValidationLevel defines how strict the evaluation should be
type ValidationLevel string

const (
	ValidationStrict   ValidationLevel = "strict"
	ValidationModerate ValidationLevel = "moderate"
	ValidationLenient  ValidationLevel = "lenient"
)

// CrossProviderTestConfig configures the entire test
type CrossProviderTestConfig struct {
	Providers            []ProviderConfig
	ConversationSettings ConversationSettings
	TestSettings         TestSettings
}

// ProviderConfig defines a provider's capabilities
type ProviderConfig struct {
	Provider        schemas.ModelProvider
	ChatModel       string
	VisionModel     string
	ToolsSupported  bool
	VisionSupported bool
	StreamSupported bool
	Available       bool
}

// ConversationSettings controls conversation generation
type ConversationSettings struct {
	MaxMessages                int
	ConversationGeneratorModel string
	RequiredMessageTypes       []MessageModality
}

// TestSettings controls test execution
type TestSettings struct {
	EnableRetries        bool
	MaxRetriesPerMessage int
	ValidationStrength   ValidationLevel
}

// CrossProviderScenario defines a complete test scenario
type CrossProviderScenario struct {
	Name               string
	Description        string
	InitialMessage     string
	ExpectedFlow       []ScenarioStep
	MaxMessages        int
	RequiredModalities []MessageModality
	SuccessCriteria    ScenarioSuccess
}

// ScenarioStep defines a single step in the scenario
type ScenarioStep struct {
	StepNumber       int
	ExpectedAction   string
	RequiredModality MessageModality
	SuccessCriteria  StepSuccess
}

// StepSuccess defines validation criteria for a step
type StepSuccess struct {
	MustContainKeywords    []string
	MustNotContainWords    []string
	ExpectedToolCalls      []string
	RequiresDataExtraction bool
	QualityThreshold       float64
}

// ScenarioSuccess defines overall scenario success criteria
type ScenarioSuccess struct {
	MinStepsCompleted   int
	RequiredModalities  []MessageModality
	OverallQualityScore float64
	MustCompleteGoal    bool
}

// =============================================================================
// PREDEFINED SCENARIOS
// =============================================================================

// GetPredefinedScenarios returns all available test scenarios
func GetPredefinedScenarios() []CrossProviderScenario {
	return []CrossProviderScenario{
		{
			Name:           "FlightBooking",
			Description:    "Complete flight booking from search to confirmation with tools and vision",
			InitialMessage: "Hi! I need to book a flight from New York to London for next Friday. Can you help me find options and handle the booking process?",
			ExpectedFlow: []ScenarioStep{
				{
					StepNumber:       1,
					ExpectedAction:   "Search for flights and show options",
					RequiredModality: ModalityTool,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"new york", "london", "friday", "flight", "search"},
						ExpectedToolCalls:   []string{"weather"}, // Using available weather tool as proxy
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       2,
					ExpectedAction:   "Analyze seat map and layout",
					RequiredModality: ModalityVision,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"seat", "layout", "map", "selection"},
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       3,
					ExpectedAction:   "Calculate total cost and handle booking",
					RequiredModality: ModalityTool,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"cost", "total", "booking", "confirmation"},
						ExpectedToolCalls:   []string{"calculate"},
						QualityThreshold:    0.7,
					},
				},
			},
			MaxMessages:        12,
			RequiredModalities: []MessageModality{ModalityTool, ModalityVision},
			SuccessCriteria: ScenarioSuccess{
				MinStepsCompleted:   2,
				RequiredModalities:  []MessageModality{ModalityTool, ModalityVision},
				OverallQualityScore: 0.7,
				MustCompleteGoal:    true,
			},
		},

		{
			Name:           "RestaurantReservation",
			Description:    "Make restaurant reservation with dietary requirements and menu analysis",
			InitialMessage: "I want to make a dinner reservation for 4 people tomorrow at 7 PM. We have dietary restrictions - one person is gluten-free and another is vegetarian.",
			ExpectedFlow: []ScenarioStep{
				{
					StepNumber:       1,
					ExpectedAction:   "Search for restaurants with dietary filters",
					RequiredModality: ModalityTool,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"restaurant", "4 people", "7 pm", "gluten-free", "vegetarian"},
						ExpectedToolCalls:   []string{"weather"}, // Proxy for restaurant search
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       2,
					ExpectedAction:   "Analyze menu for dietary compatibility",
					RequiredModality: ModalityVision,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"menu", "dietary", "gluten-free", "vegetarian"},
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       3,
					ExpectedAction:   "Complex reasoning about best restaurant choice",
					RequiredModality: ModalityReasoning,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"recommendation", "choice", "suitable", "reservation"},
						QualityThreshold:    0.7,
					},
				},
			},
			MaxMessages:        15,
			RequiredModalities: []MessageModality{ModalityTool, ModalityVision, ModalityReasoning},
			SuccessCriteria: ScenarioSuccess{
				MinStepsCompleted:   2,
				RequiredModalities:  []MessageModality{ModalityTool, ModalityVision},
				OverallQualityScore: 0.7,
				MustCompleteGoal:    true,
			},
		},

		{
			Name:           "EventPlanning",
			Description:    "Plan a corporate event with budget analysis, venue selection, and timeline",
			InitialMessage: "Help me plan a corporate team building event for 50 people with a budget of $10,000. I need venue, catering, activities, and a detailed timeline.",
			ExpectedFlow: []ScenarioStep{
				{
					StepNumber:       1,
					ExpectedAction:   "Calculate budget breakdown",
					RequiredModality: ModalityTool,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"budget", "50 people", "10000", "breakdown"},
						ExpectedToolCalls:   []string{"calculate"},
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       2,
					ExpectedAction:   "Analyze venue layouts and capacity",
					RequiredModality: ModalityVision,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"venue", "layout", "capacity", "50 people"},
						QualityThreshold:    0.7,
					},
				},
				{
					StepNumber:       3,
					ExpectedAction:   "Create comprehensive timeline with dependencies",
					RequiredModality: ModalityReasoning,
					SuccessCriteria: StepSuccess{
						MustContainKeywords: []string{"timeline", "schedule", "dependencies", "planning"},
						QualityThreshold:    0.8,
					},
				},
			},
			MaxMessages:        18,
			RequiredModalities: []MessageModality{ModalityTool, ModalityVision, ModalityReasoning},
			SuccessCriteria: ScenarioSuccess{
				MinStepsCompleted:   3,
				RequiredModalities:  []MessageModality{ModalityTool, ModalityVision, ModalityReasoning},
				OverallQualityScore: 0.75,
				MustCompleteGoal:    true,
			},
		},
	}
}

// =============================================================================
// ROUND-ROBIN PROVIDER MANAGER
// =============================================================================

// ProviderRoundRobin manages provider selection and tracking
type ProviderRoundRobin struct {
	providers    []ProviderConfig
	currentIndex int
	usageStats   map[schemas.ModelProvider]int
	skipStats    map[schemas.ModelProvider]int
	logger       *testing.T
}

// NewProviderRoundRobin creates a new round-robin manager
func NewProviderRoundRobin(providers []ProviderConfig, t *testing.T) *ProviderRoundRobin {
	availableProviders := filterAvailableProviders(providers, t)
	return &ProviderRoundRobin{
		providers:    availableProviders,
		currentIndex: 0,
		usageStats:   make(map[schemas.ModelProvider]int),
		skipStats:    make(map[schemas.ModelProvider]int),
		logger:       t,
	}
}

// GetNextProviderForModality returns the next provider that supports the required modality
func (prr *ProviderRoundRobin) GetNextProviderForModality(modality MessageModality) (ProviderConfig, error) {
	if len(prr.providers) == 0 {
		return ProviderConfig{}, fmt.Errorf("no available providers")
	}

	startIndex := prr.currentIndex
	attempts := 0

	for {
		if attempts >= len(prr.providers) {
			// All providers tried, return best available
			provider := prr.providers[prr.currentIndex]
			prr.advanceIndex()
			prr.usageStats[provider.Provider]++
			prr.logger.Logf("⚠️ No ideal provider for %s, using %s", modality, provider.Provider)
			return provider, nil
		}

		provider := prr.providers[prr.currentIndex]

		if prr.providerSupportsModality(provider, modality) {
			prr.logger.Logf("✅ Selected %s for %s modality", provider.Provider, modality)
			prr.advanceIndex()
			prr.usageStats[provider.Provider]++
			return provider, nil
		}

		// Skip this provider
		prr.skipStats[provider.Provider]++
		prr.logger.Logf("⏭️ Skipping %s (no %s support)", provider.Provider, modality)
		prr.advanceIndex()
		attempts++

		if prr.currentIndex == startIndex && attempts > 0 {
			break
		}
	}

	return ProviderConfig{}, fmt.Errorf("no provider supports modality %s", modality)
}

func (prr *ProviderRoundRobin) providerSupportsModality(provider ProviderConfig, modality MessageModality) bool {
	switch modality {
	case ModalityVision:
		return provider.VisionSupported && provider.VisionModel != ""
	case ModalityTool:
		return provider.ToolsSupported
	case ModalityText, ModalityReasoning:
		return true // All providers support text and reasoning
	default:
		return true
	}
}

func (prr *ProviderRoundRobin) advanceIndex() {
	prr.currentIndex = (prr.currentIndex + 1) % len(prr.providers)
}

func (prr *ProviderRoundRobin) GetUsageStats() map[schemas.ModelProvider]int {
	return prr.usageStats
}

// filterAvailableProviders checks which providers are actually available
func filterAvailableProviders(providers []ProviderConfig, t *testing.T) []ProviderConfig {
	var available []ProviderConfig
	for _, provider := range providers {
		if provider.Available {
			available = append(available, provider)
			t.Logf("✅ Provider %s available for cross-provider testing", provider.Provider)
		} else {
			t.Logf("⚠️ Provider %s skipped (marked unavailable)", provider.Provider)
		}
	}
	return available
}

// =============================================================================
// OPENAI JUDGE SYSTEM
// =============================================================================

// OpenAIJudge evaluates responses using OpenAI
type OpenAIJudge struct {
	client     *bifrost.Bifrost
	judgeModel string
	logger     *testing.T
}

// EvaluationRequest contains data for evaluation
type EvaluationRequest struct {
	ScenarioContext string
	UserMessage     string
	LLMResponse     string
	Provider        schemas.ModelProvider
	Criteria        StepSuccess
	APIType         string // "chat" or "responses"
}

// EvaluationResult contains evaluation results
type EvaluationResult struct {
	Passed            bool     `json:"passed"`
	Score             float64  `json:"score"`
	KeywordCheck      string   `json:"keyword_check"`
	ForbiddenCheck    string   `json:"forbidden_check"`
	ToolCheck         string   `json:"tool_check"`
	QualityAssessment string   `json:"quality_assessment"`
	Suggestions       string   `json:"suggestions"`
	FatalIssues       []string `json:"fatal_issues"`
}

// NewOpenAIJudge creates a new judge instance
func NewOpenAIJudge(client *bifrost.Bifrost, judgeModel string, t *testing.T) *OpenAIJudge {
	return &OpenAIJudge{
		client:     client,
		judgeModel: judgeModel,
		logger:     t,
	}
}

// EvaluateResponse judges an LLM response
func (judge *OpenAIJudge) EvaluateResponse(ctx *schemas.BifrostContext, evaluation EvaluationRequest) (*EvaluationResult, error) {
	prompt := fmt.Sprintf(`You are an expert AI system evaluator. Evaluate this LLM response.

SCENARIO: %s
USER MESSAGE: %s
LLM RESPONSE: %s
PROVIDER: %s
API TYPE: %s

CRITERIA:
- Must contain keywords: %v
- Must NOT contain: %v
- Expected tool calls: %v
- Quality threshold: %.2f

Rate 0-100 points across 4 categories:
1. Keyword presence (0-30 points)
2. Avoids forbidden words (0-20 points)  
3. Appropriate tool usage (0-25 points)
4. Overall quality/helpfulness (0-25 points)

Respond with JSON:
{
  "passed": true/false,
  "score": 0.0-1.0,
  "keyword_check": "details",
  "forbidden_check": "details",
  "tool_check": "details", 
  "quality_assessment": "analysis",
  "suggestions": "improvements",
  "fatal_issues": ["serious problems"]
}`,
		evaluation.ScenarioContext, evaluation.UserMessage, evaluation.LLMResponse,
		evaluation.Provider, evaluation.APIType,
		evaluation.Criteria.MustContainKeywords, evaluation.Criteria.MustNotContainWords,
		evaluation.Criteria.ExpectedToolCalls, evaluation.Criteria.QualityThreshold)

	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    judge.judgeModel,
		Input: []schemas.ChatMessage{
			CreateBasicChatMessage(prompt),
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(600),
			Temperature:         bifrost.Ptr(0.1),
		},
	}

	response, err := judge.client.ChatCompletionRequest(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("judge evaluation failed: %v", GetErrorMessage(err))
	}

	content := GetChatContent(response)
	var result EvaluationResult

	if err := parseJudgeResponse(content, &result); err != nil {
		judge.logger.Logf("⚠️ Failed to parse judge response, using fallback")
		return judge.fallbackEvaluation(evaluation), nil
	}

	judge.logger.Logf("🔍 Judge: %.2f | %s", result.Score,
		truncateString(result.QualityAssessment, 100))
	return &result, nil
}

func (judge *OpenAIJudge) fallbackEvaluation(evaluation EvaluationRequest) *EvaluationResult {
	// Simple keyword-based fallback
	response := strings.ToLower(evaluation.LLMResponse)
	keywordScore := 0.0
	for _, keyword := range evaluation.Criteria.MustContainKeywords {
		if strings.Contains(response, strings.ToLower(keyword)) {
			keywordScore += 1.0
		}
	}
	if len(evaluation.Criteria.MustContainKeywords) > 0 {
		keywordScore /= float64(len(evaluation.Criteria.MustContainKeywords))
	} else {
		keywordScore = 1.0
	}

	return &EvaluationResult{
		Passed:            keywordScore >= 0.5,
		Score:             keywordScore,
		KeywordCheck:      fmt.Sprintf("Fallback evaluation: %.1f%% keywords found", keywordScore*100),
		QualityAssessment: "Fallback evaluation used due to judge parsing error",
		Suggestions:       "Manual review recommended",
	}
}

func parseJudgeResponse(content string, result *EvaluationResult) error {
	// Extract JSON from the response
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start == -1 || end == -1 {
		return fmt.Errorf("no JSON found in response")
	}

	jsonStr := content[start : end+1]
	return json.Unmarshal([]byte(jsonStr), result)
}

// =============================================================================
// CONVERSATION DRIVER
// =============================================================================

// OpenAIConversationDriver generates followup messages
type OpenAIConversationDriver struct {
	client      *bifrost.Bifrost
	driverModel string
	logger      *testing.T
}

// NextMessageRequest contains data for generating next message
type NextMessageRequest struct {
	Scenario            CrossProviderScenario
	ConversationHistory []schemas.ChatMessage
	CurrentStepNumber   int
	NextStep            ScenarioStep
	PreviousEvaluation  *EvaluationResult
	APIType             string // "chat" or "responses"
}

// GeneratedFollowup contains the generated followup message
type GeneratedFollowup struct {
	UserMessage      string `json:"user_message"`
	ModalityContext  string `json:"modality_context"`
	ExpectedBehavior string `json:"expected_behavior"`
	TestFocus        string `json:"test_focus"`
}

// NewOpenAIConversationDriver creates a new conversation driver
func NewOpenAIConversationDriver(client *bifrost.Bifrost, driverModel string, t *testing.T) *OpenAIConversationDriver {
	return &OpenAIConversationDriver{
		client:      client,
		driverModel: driverModel,
		logger:      t,
	}
}

// GenerateNextMessage creates a natural followup message
func (driver *OpenAIConversationDriver) GenerateNextMessage(ctx *schemas.BifrostContext, request NextMessageRequest) (*GeneratedFollowup, error) {
	conversationHistory := driver.formatConversationHistory(request.ConversationHistory)

	prompt := fmt.Sprintf(`Generate the next realistic user message for a %s scenario.

SCENARIO: %s
API TYPE: %s
STEP %d: %s (requires %s)
CONVERSATION SO FAR:
%s

Generate a natural followup that:
- Flows naturally from conversation
- Tests %s modality specifically
- Is realistic and engaging
- For vision: request image/document analysis
- For tools: ask for calculations/lookups
- For reasoning: require complex thinking

JSON response:
{
  "user_message": "actual message",
  "modality_context": "why this modality fits",
  "expected_behavior": "what AI should do",
  "test_focus": "what capability this tests"
}`,
		request.Scenario.Name, request.Scenario.Description, request.APIType,
		request.CurrentStepNumber+1, request.NextStep.ExpectedAction, request.NextStep.RequiredModality,
		conversationHistory, request.NextStep.RequiredModality)

	llmRequest := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    driver.driverModel,
		Input: []schemas.ChatMessage{
			CreateBasicChatMessage(prompt),
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(300),
			Temperature:         bifrost.Ptr(0.7),
		},
	}

	response, err := driver.client.ChatCompletionRequest(ctx, llmRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate next message: %v", GetErrorMessage(err))
	}

	content := GetChatContent(response)
	var followup GeneratedFollowup

	if err := parseDriverResponse(content, &followup); err != nil {
		driver.logger.Logf("⚠️ Driver parse failed, using fallback")
		return driver.generateFallbackMessage(request), nil
	}

	driver.logger.Logf("💭 Generated: %s", truncateString(followup.UserMessage, 80))
	return &followup, nil
}

func (driver *OpenAIConversationDriver) formatConversationHistory(history []schemas.ChatMessage) string {
	var formatted []string
	for i, msg := range history {
		role := "Unknown"
		content := "No content"

		if msg.Role == schemas.ChatMessageRoleUser {
			role = "User"
		} else if msg.Role == schemas.ChatMessageRoleAssistant {
			role = "AI"
		}

		if msg.Content.ContentStr != nil {
			content = *msg.Content.ContentStr
		}

		formatted = append(formatted, fmt.Sprintf("%d. %s: %s",
			i+1, role, truncateString(content, 100)))
	}
	return strings.Join(formatted, "\n")
}

func parseDriverResponse(content string, followup *GeneratedFollowup) error {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start == -1 || end == -1 {
		return fmt.Errorf("no JSON found")
	}

	jsonStr := content[start : end+1]
	return json.Unmarshal([]byte(jsonStr), followup)
}

func (driver *OpenAIConversationDriver) generateFallbackMessage(request NextMessageRequest) *GeneratedFollowup {
	fallbacks := map[MessageModality]string{
		ModalityTool:      "Can you help me with some calculations or data lookup for this?",
		ModalityVision:    "I have an image/document I'd like you to analyze. What do you see?",
		ModalityReasoning: "This requires careful thinking. Can you walk me through the reasoning step by step?",
		ModalityText:      "Can you provide more details about this?",
	}

	return &GeneratedFollowup{
		UserMessage:      fallbacks[request.NextStep.RequiredModality],
		ModalityContext:  fmt.Sprintf("Fallback message for %s", request.NextStep.RequiredModality),
		ExpectedBehavior: "Handle the request appropriately",
		TestFocus:        fmt.Sprintf("Test %s capability", request.NextStep.RequiredModality),
	}
}

// =============================================================================
// MAIN EXECUTION ENGINE
// =============================================================================

// RunCrossProviderScenarioTest executes a complete scenario
func RunCrossProviderScenarioTest(t *testing.T, client *bifrost.Bifrost, ctx *schemas.BifrostContext, config CrossProviderTestConfig, scenario CrossProviderScenario, useResponsesAPI bool) {
	apiType := "Chat Completions"
	if useResponsesAPI {
		apiType = "Responses API"
	}

	t.Logf("🎬 Starting scenario: %s (%s)", scenario.Name, apiType)

	// Initialize components
	roundRobin := NewProviderRoundRobin(config.Providers, t)
	judge := NewOpenAIJudge(client, "gpt-4o-mini", t)
	driver := NewOpenAIConversationDriver(client, config.ConversationSettings.ConversationGeneratorModel, t)

	// Start conversation
	var conversationHistory []schemas.ChatMessage
	var evaluationResults []EvaluationResult

	// Add initial user message
	initialMsg := CreateBasicChatMessage(scenario.InitialMessage)
	conversationHistory = append(conversationHistory, initialMsg)
	t.Logf("👤 User: %s", truncateString(scenario.InitialMessage, 100))

	// Execute conversation steps
	for stepNum := 0; stepNum < len(scenario.ExpectedFlow) && len(conversationHistory) < scenario.MaxMessages*2; stepNum++ {
		currentStep := scenario.ExpectedFlow[stepNum]

		// Get next provider
		provider, err := roundRobin.GetNextProviderForModality(currentStep.RequiredModality)
		if err != nil {
			t.Fatalf("❌ No provider for %s: %v", currentStep.RequiredModality, err)
		}

		t.Logf("🔄 Step %d: %s -> %s (%s)", stepNum+1, provider.Provider,
			currentStep.ExpectedAction, currentStep.RequiredModality)

		// Execute request
		response, llmErr := executeStepWithProvider(t, client, ctx, provider,
			conversationHistory, currentStep, useResponsesAPI)
		if llmErr != nil {
			t.Fatalf("❌ Step %d failed: %v", stepNum+1, GetErrorMessage(llmErr))
		}

		var responseContent string
		// Add response to history
		if useResponsesAPI && response.ResponsesResponse != nil {
			// Convert Responses API output back to ChatMessages for history
			assistantMessages := schemas.ToChatMessages(response.ResponsesResponse.Output)
			conversationHistory = append(conversationHistory, assistantMessages...)
			responseContent = GetResponsesContent(response.ResponsesResponse)
		} else {
			if response.ChatResponse != nil {
				// Use Chat API choices
				for _, choice := range response.ChatResponse.Choices {
					if choice.Message != nil {
						conversationHistory = append(conversationHistory, *choice.Message)
					}
				}
				responseContent = GetChatContent(response.ChatResponse)
			}
		}

		t.Logf("🤖 %s: %s", provider.Provider, truncateString(responseContent, 120))

		// Evaluate with judge
		evaluation, evalErr := judge.EvaluateResponse(ctx, EvaluationRequest{
			ScenarioContext: scenario.Description,
			UserMessage:     getLastUserMessage(conversationHistory),
			LLMResponse:     responseContent,
			Provider:        provider.Provider,
			Criteria:        currentStep.SuccessCriteria,
			APIType:         apiType,
		})

		if evalErr != nil {
			t.Logf("⚠️ Evaluation failed: %v", evalErr)
			continue
		}

		evaluationResults = append(evaluationResults, *evaluation)

		// Check step result
		if !evaluation.Passed {
			t.Logf("❌ Step %d FAILED (%.2f): %s", stepNum+1, evaluation.Score,
				evaluation.QualityAssessment)
			if len(evaluation.FatalIssues) > 0 {
				t.Fatalf("💀 Fatal issues: %v", evaluation.FatalIssues)
			}
		} else {
			t.Logf("✅ Step %d PASSED (%.2f)", stepNum+1, evaluation.Score)
		}

		// Generate next message if not final step
		if stepNum < len(scenario.ExpectedFlow)-1 {
			nextStep := scenario.ExpectedFlow[stepNum+1]
			followup, driverErr := driver.GenerateNextMessage(ctx, NextMessageRequest{
				Scenario:            scenario,
				ConversationHistory: conversationHistory,
				CurrentStepNumber:   stepNum + 1,
				NextStep:            nextStep,
				PreviousEvaluation:  evaluation,
				APIType:             apiType,
			})

			if driverErr != nil {
				t.Logf("⚠️ Driver failed: %v", driverErr)
				break
			}

			// Create appropriate message for modality
			nextUserMessage := createModalityMessage(followup.UserMessage, nextStep.RequiredModality)
			conversationHistory = append(conversationHistory, nextUserMessage)
			t.Logf("👤 User: %s", truncateString(followup.UserMessage, 100))
		}
	}

	// Final evaluation
	finalSuccess := evaluateScenarioSuccess(evaluationResults, scenario.SuccessCriteria)
	if finalSuccess {
		t.Logf("🎉 Scenario %s (%s) COMPLETED SUCCESSFULLY!", scenario.Name, apiType)
	} else {
		t.Fatalf("❌ Scenario %s (%s) FAILED", scenario.Name, apiType)
	}

	// Print summary
	printScenarioSummary(t, scenario, evaluationResults, roundRobin.GetUsageStats(), apiType)
}

// =============================================================================
// CONSISTENCY TESTING
// =============================================================================

// RunCrossProviderConsistencyTest tests same prompt across providers
func RunCrossProviderConsistencyTest(t *testing.T, client *bifrost.Bifrost, ctx *schemas.BifrostContext, config CrossProviderTestConfig, useResponsesAPI bool) {
	apiType := "Chat Completions"
	if useResponsesAPI {
		apiType = "Responses API"
	}

	t.Logf("🔄 Cross-provider consistency test (%s)", apiType)

	// Test prompt
	testPrompt := "Explain the concept of artificial intelligence in exactly 3 sentences, covering its definition, current applications, and future potential."

	var results []ConsistencyResult

	for _, provider := range config.Providers {
		if !provider.Available {
			continue
		}

		t.Logf("Testing %s...", provider.Provider)

		var content string

		if useResponsesAPI {
			// Use Responses API
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: provider.Provider,
				Model:    provider.ChatModel,
				Input: []schemas.ResponsesMessage{
					CreateBasicResponsesMessage(testPrompt),
				},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
					Temperature:     bifrost.Ptr(0.3),
				},
			}
			responsesResponse, err := client.ResponsesRequest(ctx, responsesReq)
			if err != nil {
				t.Logf("❌ %s failed: %v", provider.Provider, GetErrorMessage(err))
				continue
			}
			content = GetResponsesContent(responsesResponse)
		} else {
			// Use Chat Completions API
			chatReq := &schemas.BifrostChatRequest{
				Provider: provider.Provider,
				Model:    provider.ChatModel,
				Input: []schemas.ChatMessage{
					CreateBasicChatMessage(testPrompt),
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
					Temperature:         bifrost.Ptr(0.3),
				},
			}
			chatResponse, err := client.ChatCompletionRequest(ctx, chatReq)
			if err != nil {
				t.Logf("❌ %s failed: %v", provider.Provider, GetErrorMessage(err))
				continue
			}
			content = GetChatContent(chatResponse)
		}

		sentences := strings.Split(strings.TrimSpace(content), ".")

		result := ConsistencyResult{
			Provider:       provider.Provider,
			Response:       content,
			SentenceCount:  len(sentences) - 1, // Last split is usually empty
			WordCount:      len(strings.Fields(content)),
			ContainsAI:     strings.Contains(strings.ToLower(content), "artificial intelligence"),
			ContainsFuture: strings.Contains(strings.ToLower(content), "future"),
		}

		results = append(results, result)
		t.Logf("✅ %s: %d sentences, %d words", provider.Provider, result.SentenceCount, result.WordCount)
	}

	// Analyze consistency
	analyzeConsistency(t, results, apiType)
}

type ConsistencyResult struct {
	Provider       schemas.ModelProvider
	Response       string
	SentenceCount  int
	WordCount      int
	ContainsAI     bool
	ContainsFuture bool
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func executeStepWithProvider(t *testing.T, client *bifrost.Bifrost, ctx *schemas.BifrostContext,
	provider ProviderConfig, history []schemas.ChatMessage, step ScenarioStep, useResponsesAPI bool,
) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Prepare request parameters
	var tools []schemas.ChatTool
	if step.RequiredModality == ModalityTool {
		tools = []schemas.ChatTool{
			*GetSampleChatTool(SampleToolTypeWeather),
			*GetSampleChatTool(SampleToolTypeCalculate),
		}
	}

	if useResponsesAPI {
		// Convert to Responses format
		var responsesMessages []schemas.ResponsesMessage
		for _, msg := range history {
			convertedMessages := msg.ToResponsesMessages()
			responsesMessages = append(responsesMessages, convertedMessages...)
		}

		request := &schemas.BifrostResponsesRequest{
			Provider: provider.Provider,
			Model:    getModelForModality(provider, step.RequiredModality),
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: bifrost.Ptr(300),
				Temperature:     bifrost.Ptr(0.7),
			},
		}

		// Add tools if needed
		if len(tools) > 0 {
			responsesTools := make([]schemas.ResponsesTool, len(tools))
			for i, tool := range tools {
				responsesTools[i] = *tool.ToResponsesTool()
			}
			request.Params.Tools = responsesTools
		}

		responsesResponse, err := client.ResponsesRequest(ctx, request)
		if err != nil {
			return nil, err
		}
		return &schemas.BifrostResponse{ResponsesResponse: responsesResponse}, nil
	} else {
		// Use Chat Completions API
		request := &schemas.BifrostChatRequest{
			Provider: provider.Provider,
			Model:    getModelForModality(provider, step.RequiredModality),
			Input:    history,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(300),
				Temperature:         bifrost.Ptr(0.7),
			},
		}

		if len(tools) > 0 {
			request.Params.Tools = tools
		}

		chatResponse, err := client.ChatCompletionRequest(ctx, request)
		if err != nil {
			return nil, err
		}
		return &schemas.BifrostResponse{ChatResponse: chatResponse}, nil
	}
}

func getModelForModality(provider ProviderConfig, modality MessageModality) string {
	if modality == ModalityVision && provider.VisionModel != "" {
		return provider.VisionModel
	}
	return provider.ChatModel
}

func createModalityMessage(message string, modality MessageModality) schemas.ChatMessage {
	switch modality {
	case ModalityVision:
		// Add test image for vision
		if lionBase64, err := GetLionBase64Image(); err == nil {
			return CreateImageChatMessage(message, lionBase64)
		}
		return CreateBasicChatMessage(message + " [Image analysis requested]")
	default:
		return CreateBasicChatMessage(message)
	}
}

func getLastUserMessage(history []schemas.ChatMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == schemas.ChatMessageRoleUser {
			if history[i].Content.ContentStr != nil {
				return *history[i].Content.ContentStr
			}
		}
	}
	return "Previous user message"
}

func evaluateScenarioSuccess(results []EvaluationResult, criteria ScenarioSuccess) bool {
	if len(results) < criteria.MinStepsCompleted {
		return false
	}

	totalScore := 0.0
	passedSteps := 0
	for _, result := range results {
		totalScore += result.Score
		if result.Passed {
			passedSteps++
		}
	}

	avgScore := totalScore / float64(len(results))
	return avgScore >= criteria.OverallQualityScore && passedSteps >= criteria.MinStepsCompleted
}

func printScenarioSummary(t *testing.T, scenario CrossProviderScenario, results []EvaluationResult,
	usage map[schemas.ModelProvider]int, apiType string,
) {
	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("SCENARIO SUMMARY: %s (%s)", scenario.Name, apiType)
	t.Logf("%s", strings.Repeat("=", 80))

	totalScore := 0.0
	passed := 0
	for i, result := range results {
		status := "❌ FAIL"
		if result.Passed {
			status = "✅ PASS"
			passed++
		}
		t.Logf("Step %d: %s (%.2f) - %s", i+1, status, result.Score,
			truncateString(result.QualityAssessment, 60))
		totalScore += result.Score
	}

	avgScore := 0.0
	if len(results) > 0 {
		avgScore = totalScore / float64(len(results))
	}

	t.Logf("\nProvider Usage:")
	for provider, count := range usage {
		t.Logf("  %s: %d messages", provider, count)
	}

	t.Logf("\nResults: %d/%d passed, Average Score: %.2f", passed, len(results), avgScore)
	t.Logf("%s\n", strings.Repeat("=", 80))
}

func analyzeConsistency(t *testing.T, results []ConsistencyResult, apiType string) {
	t.Logf("\n%s", strings.Repeat("=", 80))
	t.Logf("CONSISTENCY ANALYSIS (%s)", apiType)
	t.Logf("%s", strings.Repeat("=", 80))

	if len(results) < 2 {
		t.Logf("Need at least 2 providers for consistency analysis")
		return
	}

	// Analyze sentence count consistency
	sentences := make([]int, len(results))
	words := make([]int, len(results))

	for i, result := range results {
		sentences[i] = result.SentenceCount
		words[i] = result.WordCount
		t.Logf("%s: %d sentences, %d words", result.Provider, result.SentenceCount, result.WordCount)
	}

	// Calculate variance
	sentenceVariance := calculateVariance(sentences)
	wordVariance := calculateVariance(words)

	t.Logf("\nConsistency Metrics:")
	t.Logf("  Sentence count variance: %.2f", sentenceVariance)
	t.Logf("  Word count variance: %.2f", wordVariance)

	if sentenceVariance < 1.0 {
		t.Logf("✅ Good sentence count consistency")
	} else {
		t.Logf("⚠️ High sentence count variance")
	}

	t.Logf("%s\n", strings.Repeat("=", 80))
}

func calculateVariance(values []int) float64 {
	if len(values) == 0 {
		return 0
	}

	sum := 0
	for _, v := range values {
		sum += v
	}
	mean := float64(sum) / float64(len(values))

	variance := 0.0
	for _, v := range values {
		diff := float64(v) - mean
		variance += diff * diff
	}

	return variance / float64(len(values))
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

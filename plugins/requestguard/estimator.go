package requestguard

import (
	"math"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const estimatedMessageOverhead = 4

// EstimateTokens estimates prompt and completion usage without retaining the
// pooled request or any pointer into it. Text-like content is counted by Unicode
// code point; media is represented by the configured per-image estimate.
func EstimateTokens(req *schemas.BifrostRequest, features RequestFeatures, cfg BudgetGuardConfig, catalog Catalog) (CostEstimate, *schemas.BifrostLLMUsage) {
	chars := 0
	messageCount := 0
	var provider schemas.ModelProvider
	var model string
	var requestType schemas.RequestType

	if req != nil {
		requestType = req.RequestType
		switch {
		case req.ChatRequest != nil && (requestType == schemas.ChatCompletionRequest || requestType == schemas.ChatCompletionStreamRequest || requestType == ""):
			chat := req.ChatRequest
			provider, model = chat.Provider, chat.Model
			requestType = requestTypeOr(requestType, schemas.ChatCompletionRequest)
			for i := range chat.Input {
				message := &chat.Input[i]
				messageCount++
				chars = addChars(chars, runeLenPtr(message.Name))
				if message.Content != nil {
					chars = addChars(chars, runeLenPtr(message.Content.ContentStr))
					for j := range message.Content.ContentBlocks {
						block := &message.Content.ContentBlocks[j]
						chars = addChars(chars, runeLenPtr(block.Text), runeLenPtr(block.Refusal))
					}
				}
				if message.ChatAssistantMessage != nil {
					chars = addChars(chars,
						runeLenPtr(message.ChatAssistantMessage.Reasoning),
						runeLenPtr(message.ChatAssistantMessage.Refusal),
					)
					for j := range message.ChatAssistantMessage.ToolCalls {
						call := &message.ChatAssistantMessage.ToolCalls[j]
						chars = addChars(chars, runeLenPtr(call.Function.Name), runeLenPtr(&call.Function.Arguments))
						if call.Custom != nil {
							chars = addChars(chars, runeLenPtr(call.Custom.Name), runeLenPtr(&call.Custom.Input))
						}
					}
				}
			}
			if chat.Params != nil {
				chars = addSerializedChars(chars, chat.Params.Tools)
				if chat.Params.ResponseFormat != nil {
					chars = addSerializedChars(chars, *chat.Params.ResponseFormat)
				}
			}
		case req.ResponsesRequest != nil && (requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == ""):
			responses := req.ResponsesRequest
			provider, model = responses.Provider, responses.Model
			requestType = requestTypeOr(requestType, schemas.ResponsesRequest)
			for i := range responses.Input {
				message := &responses.Input[i]
				messageCount++
				if message.Content != nil {
					chars = addChars(chars, runeLenPtr(message.Content.ContentStr))
					for j := range message.Content.ContentBlocks {
						block := &message.Content.ContentBlocks[j]
						chars = addChars(chars, runeLenPtr(block.Text))
						if block.ResponsesInputMessageContentBlockFile != nil {
							chars = addChars(chars,
								runeLenPtr(block.ResponsesInputMessageContentBlockFile.FileData),
								runeLenPtr(block.ResponsesInputMessageContentBlockFile.FileURL),
								runeLenPtr(block.ResponsesInputMessageContentBlockFile.Filename),
							)
						}
					}
				}
				if message.ResponsesToolMessage != nil {
					chars = addChars(chars,
						runeLenPtr(message.ResponsesToolMessage.Name),
						runeLenPtr(message.ResponsesToolMessage.Arguments),
					)
				}
			}
			if responses.Params != nil {
				chars = addChars(chars, runeLenPtr(responses.Params.Instructions))
				chars = addSerializedChars(chars, responses.Params.Tools)
				if responses.Params.Text != nil && responses.Params.Text.Format != nil {
					chars = addSerializedChars(chars, responses.Params.Text.Format)
				}
			}
		}
	}

	if provider == "" {
		provider = features.Provider
	}
	if model == "" {
		model = features.Model
	}
	if requestType == "" {
		requestType = features.RequestType
	}

	charsPerToken := cfg.CharsPerToken
	if charsPerToken <= 0 || math.IsNaN(charsPerToken) || math.IsInf(charsPerToken, 0) {
		charsPerToken = 4
	}
	inputTokens := int(math.Ceil(float64(chars) / charsPerToken))
	inputTokens = saturatingAdd(inputTokens, saturatingMultiply(messageCount, estimatedMessageOverhead))
	imageEstimate := cfg.ImageTokenEstimate
	if imageEstimate < 0 {
		imageEstimate = 0
	}
	imageCount := features.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	inputTokens = saturatingAdd(inputTokens, saturatingMultiply(imageCount, imageEstimate))
	inputTokens = saturatingScale(inputTokens, validSafetyMargin(cfg.SafetyMarginMultiplier))

	outputTokens := features.RequestedMaxOutputTokens
	if outputTokens <= 0 {
		outputTokens = cfg.DefaultOutputTokens
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if !isNilCatalog(catalog) {
		modelInfo := catalog.GetModelInfo(provider, model)
		entry := catalog.GetPricingEntryForModel(model, provider)
		if maxOutput := modelMaxOutputTokens(nil, modelInfo, entry); maxOutput != nil && outputTokens > *maxOutput {
			outputTokens = *maxOutput
		}
	}

	totalTokens := saturatingAdd(inputTokens, outputTokens)
	estimate := CostEstimate{InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}
	usage := &schemas.BifrostLLMUsage{
		Estimation:       &schemas.TokenUsageEstimation{Prompt: true, Completion: true, Tokenizer: "requestguard-estimate"},
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      totalTokens,
	}
	if cost, available := PriceEstimate(catalog, usage, provider, model, requestType, nil); available {
		estimate.EstimatedCostUSD = cost
		estimate.PricingAvailable = true
	}
	return estimate, usage
}

// BuildPricingScopes creates a detached pricing scope value. The catalog's
// current scope model has no customer field, so the customer ID is mapped to
// UserID while virtual-key identity uses VirtualKeyID.
func BuildPricingScopes(ctx *schemas.BifrostContext, provider schemas.ModelProvider) *modelcatalog.PricingLookupScopes {
	if ctx == nil {
		return nil
	}
	virtualKeyID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	if virtualKeyID == "" {
		virtualKeyID, _ = ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	}
	customerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID).(string)
	if customerID == "" {
		customerID, _ = ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string)
	}
	return &modelcatalog.PricingLookupScopes{
		UserID:       customerID,
		VirtualKeyID: virtualKeyID,
		Provider:     string(provider),
	}
}

// PriceEstimate prices an estimate only when the model catalog has an entry
// for the requested model. A zero price is still a valid available price.
func PriceEstimate(catalog Catalog, usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) (float64, bool) {
	if isNilCatalog(catalog) || usage == nil {
		return 0, false
	}
	if catalog.GetPricingEntryForModel(model, provider) == nil {
		return 0, false
	}
	cost := catalog.CalculateCostForUsage(usage, provider, model, requestType, scopes)
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, false
	}
	return cost, true
}

func requestTypeOr(value, fallback schemas.RequestType) schemas.RequestType {
	if value == "" {
		return fallback
	}
	return value
}

func runeLenPtr(value *string) int {
	if value == nil {
		return 0
	}
	return utf8.RuneCountInString(*value)
}

func addChars(current int, additions ...int) int {
	for _, addition := range additions {
		current = saturatingAdd(current, addition)
	}
	return current
}

func addSerializedChars(current int, value any) int {
	if value == nil {
		return current
	}
	encoded, err := schemas.MarshalSorted(value)
	if err != nil {
		return current
	}
	return saturatingAdd(current, len(encoded))
}

func validSafetyMargin(value float64) float64 {
	if value < 1 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	return value
}

func saturatingAdd(a, b int) int {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	maxInt := int(^uint(0) >> 1)
	if b > maxInt-a {
		return maxInt
	}
	return a + b
}

func saturatingMultiply(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if a > maxInt/b {
		return maxInt
	}
	return a * b
}

func saturatingScale(value int, multiplier float64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	scaled := float64(value) * multiplier
	if math.IsInf(scaled, 0) || scaled >= float64(maxInt) {
		return maxInt
	}
	return int(math.Ceil(scaled))
}

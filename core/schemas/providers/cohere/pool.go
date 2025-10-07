package cohere

import (
	"sync"
)

// Pool capacity limits to prevent memory leaks from overly large slices
const (
	maxSliceCapacity = 64         // Max capacity for slices before discarding
	maxMapSize       = 32         // Max map size before discarding
	maxTextLength    = 100 * 1024 // Max 100KB text before discarding from pool
)

// ==================== SLICE POOLS ====================

// Pool for CohereMessage slices - most common allocation
var cohereMessagesPool = sync.Pool{
	New: func() interface{} {
		s := make([]CohereMessage, 0, 8) // Typical conversation has 2-8 messages
		return &s
	},
}

// Pool for CohereChatRequestTool slices
var cohereToolsPool = sync.Pool{
	New: func() interface{} {
		s := make([]CohereChatRequestTool, 0, 4) // Most requests have 0-4 tools
		return &s
	},
}

// Pool for CohereContentBlock slices
var cohereContentBlocksPool = sync.Pool{
	New: func() interface{} {
		s := make([]CohereContentBlock, 0, 4) // Most messages have 1-4 content blocks
		return &s
	},
}

// Pool for CohereToolCall slices
var cohereToolCallsPool = sync.Pool{
	New: func() interface{} {
		s := make([]CohereToolCall, 0, 2) // Most messages have 0-2 tool calls
		return &s
	},
}

// Pool for CohereEmbeddingInput slices
var cohereEmbeddingInputsPool = sync.Pool{
	New: func() interface{} {
		s := make([]CohereEmbeddingInput, 0, 4) // Most requests have 1-4 inputs
		return &s
	},
}

// Pool for string slices (texts, stop sequences)
var cohereStringSlicesPool = sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 4) // Most have 1-4 items
		return &s
	},
}

// ==================== STRUCT POOLS ====================

// cohereChatRequestPool provides a pool for Cohere chat request objects.
var cohereChatRequestPool = sync.Pool{
	New: func() interface{} {
		return &CohereChatRequest{}
	},
}

// cohereEmbeddingRequestPool provides a pool for Cohere embedding request objects.
var cohereEmbeddingRequestPool = sync.Pool{
	New: func() interface{} {
		return &CohereEmbeddingRequest{}
	},
}

// Pool for CohereMessageContent objects
var cohereMessageContentPool = sync.Pool{
	New: func() interface{} {
		return &CohereMessageContent{}
	},
}

// Pool for CohereFunction objects
var cohereFunctionPool = sync.Pool{
	New: func() interface{} {
		return &CohereFunction{}
	},
}

// Pool for CohereImageURL objects
var cohereImageURLPool = sync.Pool{
	New: func() interface{} {
		return &CohereImageURL{}
	},
}

// Pool for CohereDocument objects
var cohereDocumentPool = sync.Pool{
	New: func() interface{} {
		return &CohereDocument{}
	},
}

// Pool for CohereThinking objects
var cohereThinkingPool = sync.Pool{
	New: func() interface{} {
		return &CohereThinking{}
	},
}

// ==================== SLICE HELPERS ====================

// acquireCohereMessages gets a CohereMessage slice from the pool.
func acquireCohereMessages() []CohereMessage {
	messages := *cohereMessagesPool.Get().(*[]CohereMessage)
	return messages[:0] // Reset length, keep capacity
}

// releaseCohereMessages returns a CohereMessage slice to the pool.
func releaseCohereMessages(messages []CohereMessage) {
	if cap(messages) <= maxSliceCapacity {
		// Clear nested objects to prevent memory leaks
		for i := 0; i < len(messages); i++ {
			releaseCohereMessageRecursive(&messages[i])
			messages[i] = CohereMessage{} // Reset to zero value
		}
		cohereMessagesPool.Put(&messages)
	}
}

// acquireCohereTools gets a CohereChatRequestTool slice from the pool.
func acquireCohereTools() []CohereChatRequestTool {
	tools := *cohereToolsPool.Get().(*[]CohereChatRequestTool)
	return tools[:0] // Reset length, keep capacity
}

// releaseCohereTools returns a CohereChatRequestTool slice to the pool.
func releaseCohereTools(tools []CohereChatRequestTool) {
	if cap(tools) <= maxSliceCapacity {
		// Clear nested references
		for i := 0; i < len(tools); i++ {
			tools[i] = CohereChatRequestTool{} // Reset to zero value
		}
		cohereToolsPool.Put(&tools)
	}
}

// acquireCohereContentBlocks gets a CohereContentBlock slice from the pool.
func acquireCohereContentBlocks() []CohereContentBlock {
	blocks := *cohereContentBlocksPool.Get().(*[]CohereContentBlock)
	return blocks[:0] // Reset length, keep capacity
}

// releaseCohereContentBlocks returns a CohereContentBlock slice to the pool.
func releaseCohereContentBlocks(blocks []CohereContentBlock) {
	if cap(blocks) <= maxSliceCapacity {
		// Clear nested objects
		for i := 0; i < len(blocks); i++ {
			releaseCohereContentBlockRecursive(&blocks[i])
			blocks[i] = CohereContentBlock{} // Reset to zero value
		}
		cohereContentBlocksPool.Put(&blocks)
	}
}

// acquireCohereToolCalls gets a CohereToolCall slice from the pool.
func acquireCohereToolCalls() []CohereToolCall {
	toolCalls := *cohereToolCallsPool.Get().(*[]CohereToolCall)
	return toolCalls[:0] // Reset length, keep capacity
}

// releaseCohereToolCalls returns a CohereToolCall slice to the pool.
func releaseCohereToolCalls(toolCalls []CohereToolCall) {
	if cap(toolCalls) <= maxSliceCapacity {
		// Clear nested objects
		for i := 0; i < len(toolCalls); i++ {
			if toolCalls[i].Function != nil {
				releaseCohereFunction(toolCalls[i].Function)
			}
			toolCalls[i] = CohereToolCall{} // Reset to zero value
		}
		cohereToolCallsPool.Put(&toolCalls)
	}
}

// acquireCohereEmbeddingInputs gets a CohereEmbeddingInput slice from the pool.
func acquireCohereEmbeddingInputs() []CohereEmbeddingInput {
	inputs := *cohereEmbeddingInputsPool.Get().(*[]CohereEmbeddingInput)
	return inputs[:0] // Reset length, keep capacity
}

// releaseCohereEmbeddingInputs returns a CohereEmbeddingInput slice to the pool.
func releaseCohereEmbeddingInputs(inputs []CohereEmbeddingInput) {
	if cap(inputs) <= maxSliceCapacity {
		// Clear nested content blocks
		for i := 0; i < len(inputs); i++ {
			releaseCohereContentBlocks(inputs[i].Content)
			inputs[i] = CohereEmbeddingInput{} // Reset to zero value
		}
		cohereEmbeddingInputsPool.Put(&inputs)
	}
}

// acquireCohereStringSlice gets a string slice from the pool.
func acquireCohereStringSlice() []string {
	strings := *cohereStringSlicesPool.Get().(*[]string)
	return strings[:0] // Reset length, keep capacity
}

// releaseCohereStringSlice returns a string slice to the pool.
func releaseCohereStringSlice(strings []string) {
	if cap(strings) <= maxSliceCapacity {
		// Clear strings
		for i := 0; i < len(strings); i++ {
			strings[i] = ""
		}
		cohereStringSlicesPool.Put(&strings)
	}
}

// ==================== PUBLIC ACQUIRE/RELEASE ====================

// AcquireChatRequest gets a chat request from the pool and resets it.
func AcquireChatRequest() *CohereChatRequest {
	req := cohereChatRequestPool.Get().(*CohereChatRequest)

	// Reset all fields
	req.Model = ""
	req.Messages = nil
	req.Tools = nil
	req.ToolChoice = nil
	req.Temperature = nil
	req.P = nil
	req.K = nil
	req.MaxTokens = nil
	req.StopSequences = nil
	req.FrequencyPenalty = nil
	req.PresencePenalty = nil
	req.Stream = nil
	req.SafetyMode = nil
	req.LogProbs = nil
	req.StrictToolChoice = nil
	req.Thinking = nil

	return req
}

// ReleaseChatRequest returns a chat request to the pool.
func ReleaseChatRequest(req *CohereChatRequest) {
	if req == nil {
		return
	}

	// Release nested objects first
	if req.Messages != nil {
		releaseCohereMessages(req.Messages)
	}

	if req.Tools != nil {
		releaseCohereTools(req.Tools)
	}

	if req.StopSequences != nil {
		releaseCohereStringSlice(req.StopSequences)
	}

	if req.Thinking != nil {
		releaseCohereThinking(req.Thinking)
	}

	cohereChatRequestPool.Put(req)
}

// AcquireEmbeddingRequest gets an embedding request from the pool and resets it.
func AcquireEmbeddingRequest() *CohereEmbeddingRequest {
	req := cohereEmbeddingRequestPool.Get().(*CohereEmbeddingRequest)

	// Reset all fields
	req.Model = ""
	req.InputType = ""
	req.Texts = nil
	req.Images = nil
	req.Inputs = nil
	req.MaxTokens = nil
	req.OutputDimension = nil
	req.EmbeddingTypes = nil
	req.Truncate = nil

	return req
}

// ReleaseEmbeddingRequest returns an embedding request to the pool.
func ReleaseEmbeddingRequest(req *CohereEmbeddingRequest) {
	if req == nil {
		return
	}

	// Release nested slices
	if req.Texts != nil && len(req.Texts) <= maxSliceCapacity {
		// Only release if capacity is reasonable
		releaseCohereStringSlice(req.Texts)
	}

	if req.Images != nil && len(req.Images) <= maxSliceCapacity {
		releaseCohereStringSlice(req.Images)
	}

	if req.EmbeddingTypes != nil && len(req.EmbeddingTypes) <= maxSliceCapacity {
		releaseCohereStringSlice(req.EmbeddingTypes)
	}

	if req.Inputs != nil {
		releaseCohereEmbeddingInputs(req.Inputs)
	}

	cohereEmbeddingRequestPool.Put(req)
}

// ==================== NESTED OBJECT POOLS ====================

// acquireCohereMessageContent gets a CohereMessageContent from the pool and resets it.
func acquireCohereMessageContent() *CohereMessageContent {
	content := cohereMessageContentPool.Get().(*CohereMessageContent)

	// Reset fields
	content.StringContent = nil
	content.BlocksContent = nil

	return content
}

// releaseCohereMessageContent returns a CohereMessageContent to the pool.
func releaseCohereMessageContent(content *CohereMessageContent) {
	if content == nil {
		return
	}

	// Release nested content blocks
	if content.BlocksContent != nil {
		releaseCohereContentBlocks(content.BlocksContent)
	}

	cohereMessageContentPool.Put(content)
}

// acquireCohereFunction gets a CohereFunction from the pool and resets it.
func acquireCohereFunction() *CohereFunction {
	function := cohereFunctionPool.Get().(*CohereFunction)

	// Reset fields
	function.Name = nil
	function.Arguments = ""

	return function
}

// releaseCohereFunction returns a CohereFunction to the pool.
func releaseCohereFunction(function *CohereFunction) {
	if function != nil {
		cohereFunctionPool.Put(function)
	}
}

// acquireCohereImageURL gets a CohereImageURL from the pool and resets it.
func acquireCohereImageURL() *CohereImageURL {
	imageURL := cohereImageURLPool.Get().(*CohereImageURL)

	// Reset fields
	imageURL.URL = ""

	return imageURL
}

// releaseCohereImageURL returns a CohereImageURL to the pool.
func releaseCohereImageURL(imageURL *CohereImageURL) {
	if imageURL != nil {
		cohereImageURLPool.Put(imageURL)
	}
}

// acquireCohereDocument gets a CohereDocument from the pool and resets it.
func acquireCohereDocument() *CohereDocument {
	doc := cohereDocumentPool.Get().(*CohereDocument)

	// Reset fields
	doc.Data = nil
	doc.ID = nil

	return doc
}

// releaseCohereDocument returns a CohereDocument to the pool.
func releaseCohereDocument(doc *CohereDocument) {
	if doc == nil {
		return
	}

	// Only pool if map size is reasonable
	if len(doc.Data) <= maxMapSize {
		cohereDocumentPool.Put(doc)
	}
}

// acquireCohereThinking gets a CohereThinking from the pool and resets it.
func acquireCohereThinking() *CohereThinking {
	thinking := cohereThinkingPool.Get().(*CohereThinking)

	// Reset fields
	thinking.Type = ""
	thinking.TokenBudget = nil

	return thinking
}

// releaseCohereThinking returns a CohereThinking to the pool.
func releaseCohereThinking(thinking *CohereThinking) {
	if thinking != nil {
		cohereThinkingPool.Put(thinking)
	}
}

// ==================== RECURSIVE HELPERS ====================

// releaseCohereMessageRecursive releases nested pools in a CohereMessage
func releaseCohereMessageRecursive(msg *CohereMessage) {
	if msg == nil {
		return
	}

	// Release message content
	if msg.Content != nil {
		releaseCohereMessageContent(msg.Content)
		msg.Content = nil
	}

	// Release tool calls
	if msg.ToolCalls != nil {
		releaseCohereToolCalls(msg.ToolCalls)
		msg.ToolCalls = nil
	}
}

// releaseCohereContentBlockRecursive releases nested pools in a CohereContentBlock
func releaseCohereContentBlockRecursive(block *CohereContentBlock) {
	if block == nil {
		return
	}

	// Release nested objects based on type
	if block.ImageURL != nil {
		releaseCohereImageURL(block.ImageURL)
		block.ImageURL = nil
	}

	if block.Document != nil {
		releaseCohereDocument(block.Document)
		block.Document = nil
	}
}

package anthropic

import (
	"sync"
)

// Pool capacity limits to prevent memory leaks from overly large slices
const (
	maxSliceCapacity = 64        // Max capacity for slices before discarding
	maxBufferSize    = 32 * 1024 // Max buffer size (32KB) before discarding
)

// ==================== SLICE POOLS ====================

// Pool for ContentBlocks slices - most common allocation
var contentBlocksPool = sync.Pool{
	New: func() interface{} {
		s := make([]AnthropicContentBlock, 0, 4) // Most requests have 1-4 blocks
		return &s
	},
}

// Pool for Messages slices
var messagesPool = sync.Pool{
	New: func() interface{} {
		s := make([]AnthropicMessage, 0, 8) // Typical conversation has 2-8 messages
		return &s
	},
}

// Pool for Tools slices
var toolsPool = sync.Pool{
	New: func() interface{} {
		s := make([]AnthropicTool, 0, 4) // Most requests have 0-4 tools
		return &s
	},
}

// Pool for AnthropicImageSource objects
var anthropicImageSourcePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicImageSource{}
	},
}

// ==================== STRUCT POOLS ====================

// anthropicTextRequestPool provides a pool for Anthropic text request objects.
var anthropicTextRequestPool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextRequest{
			StopSequences: make([]string, 0, 2),
		}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// anthropicChatRequestPool provides a pool for Anthropic chat request objects.
var anthropicChatRequestPool = sync.Pool{
	New: func() interface{} {
		return &AnthropicMessageRequest{
			Messages:      make([]AnthropicMessage, 0, 8),
			StopSequences: make([]string, 0, 2),
			Tools:         make([]AnthropicTool, 0, 4),
		}
	},
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicMessageResponse{
			Content: make([]AnthropicContentBlock, 0, 4),
		}
	},
}

// Pool for AnthropicContent objects
var anthropicContentPool = sync.Pool{
	New: func() interface{} {
		return &AnthropicContent{
			ContentBlocks: make([]AnthropicContentBlock, 0, 4),
		}
	},
}

// Pool for AnthropicToolChoice objects
var anthropicToolChoicePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicToolChoice{}
	},
}

// ==================== SLICE HELPERS ====================

// acquireContentBlocks gets a ContentBlocks slice from the pool.
func acquireContentBlocks() []AnthropicContentBlock {
	blocks := *contentBlocksPool.Get().(*[]AnthropicContentBlock)
	return blocks[:0] // Reset length, keep capacity
}

// releaseContentBlocks returns a ContentBlocks slice to the pool.
func releaseContentBlocks(blocks []AnthropicContentBlock) {
	if cap(blocks) <= maxSliceCapacity {
		// Clear content blocks to prevent memory leaks
		for i := 0; i < len(blocks); i++ {
			blocks[i] = AnthropicContentBlock{} // Reset to zero value
		}
		contentBlocksPool.Put(&blocks)
	}
}

// acquireMessages gets a Messages slice from the pool.
func acquireMessages() []AnthropicMessage {
	messages := *messagesPool.Get().(*[]AnthropicMessage)
	return messages[:0] // Reset length, keep capacity
}

// releaseMessages returns a Messages slice to the pool.
func releaseMessages(messages []AnthropicMessage) {
	if cap(messages) <= maxSliceCapacity {
		// Clear nested slice references to prevent memory leaks
		for i := 0; i < len(messages); i++ {
			messages[i] = AnthropicMessage{} // Reset struct to zero value
		}
		messagesPool.Put(&messages)
	}
}

// acquireTools gets a Tools slice from the pool.
func acquireTools() []AnthropicTool {
	tools := *toolsPool.Get().(*[]AnthropicTool)
	return tools[:0] // Reset length, keep capacity
}

// releaseTools returns a Tools slice to the pool.
func releaseTools(tools []AnthropicTool) {
	if cap(tools) <= maxSliceCapacity {
		// Clear nested references
		for i := 0; i < len(tools); i++ {
			tools[i] = AnthropicTool{} // Reset struct to zero value
		}
		toolsPool.Put(&tools)
	}
}

// acquireAnthropicImageSource gets an AnthropicImageSource from the pool and resets it.
func acquireAnthropicImageSource() *AnthropicImageSource {
	source := anthropicImageSourcePool.Get().(*AnthropicImageSource)

	// Reset fields
	source.Type = ""
	source.MediaType = nil
	source.Data = nil
	source.URL = nil

	return source
}

// releaseAnthropicImageSource returns an AnthropicImageSource to the pool.
func releaseAnthropicImageSource(source *AnthropicImageSource) {
	if source != nil {
		anthropicImageSourcePool.Put(source)
	}
}

// ==================== STRUCT ACQUIRE/RELEASE ====================

// AcquireTextRequest gets a Text request from the pool and resets it.
func AcquireTextRequest() *AnthropicTextRequest {
	req := anthropicTextRequestPool.Get().(*AnthropicTextRequest)

	// Reset primitive fields
	req.Model = ""
	req.Prompt = ""
	req.MaxTokensToSample = 0
	req.Temperature = nil
	req.TopP = nil
	req.TopK = nil
	req.Stream = nil

	// Reset slice while preserving capacity
	req.StopSequences = req.StopSequences[:0]

	return req
}

// ReleaseTextRequest returns a Text request to the pool.
func ReleaseTextRequest(req *AnthropicTextRequest) {
	if req == nil {
		return
	}

	// Only pool if slice capacity is reasonable
	if cap(req.StopSequences) <= maxSliceCapacity {
		anthropicTextRequestPool.Put(req)
	}
}

// AcquireTextResponse gets a Text response from the pool and resets it.
func AcquireTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)

	// Reset all fields
	resp.ID = ""
	resp.Type = ""
	resp.Completion = ""
	resp.Model = ""
	resp.Usage.InputTokens = 0
	resp.Usage.OutputTokens = 0

	return resp
}

// ReleaseTextResponse returns a Text response to the pool.
func ReleaseTextResponse(resp *AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// AcquireChatRequest gets a Chat request from the pool and resets it.
func AcquireChatRequest() *AnthropicMessageRequest {
	req := anthropicChatRequestPool.Get().(*AnthropicMessageRequest)

	// Reset primitive fields
	req.Model = ""
	req.MaxTokens = 0
	req.Temperature = nil
	req.TopP = nil
	req.TopK = nil
	req.Stream = nil
	req.System = nil
	req.ToolChoice = nil

	// Reset slices while preserving capacity
	req.Messages = req.Messages[:0]
	req.StopSequences = req.StopSequences[:0]
	req.Tools = req.Tools[:0]

	return req
}

// ReleaseChatRequest returns a Chat request to the pool.
func ReleaseChatRequest(req *AnthropicMessageRequest) {
	if req == nil {
		return
	}

	// Release nested objects first
	if req.System != nil {
		releaseAnthropicContent(req.System)
		req.System = nil
	}

	if req.ToolChoice != nil {
		releaseAnthropicToolChoice(req.ToolChoice)
		req.ToolChoice = nil
	}

	// Release nested pooled objects recursively
	for i := 0; i < len(req.Messages); i++ {
		// Release nested content in each message
		releaseAnthropicContentRecursive(&req.Messages[i].Content)
		req.Messages[i] = AnthropicMessage{} // Reset to zero value
	}

	for i := 0; i < len(req.Tools); i++ {
		req.Tools[i] = AnthropicTool{} // Reset to zero value
	}

	for i := 0; i < len(req.StopSequences); i++ {
		req.StopSequences[i] = ""
	}

	// Only pool if slice capacities are reasonable
	if cap(req.Messages) <= maxSliceCapacity &&
		cap(req.StopSequences) <= maxSliceCapacity &&
		cap(req.Tools) <= maxSliceCapacity {
		anthropicChatRequestPool.Put(req)
	}
}

// AcquireChatResponse gets a Chat response from the pool and resets it.
func AcquireChatResponse() *AnthropicMessageResponse {
	resp := anthropicChatResponsePool.Get().(*AnthropicMessageResponse)

	// Reset primitive fields
	resp.ID = ""
	resp.Type = ""
	resp.Role = ""
	resp.Model = ""
	resp.StopReason = nil
	resp.StopSequence = nil
	resp.Usage = nil

	// Reset slice while preserving capacity
	resp.Content = resp.Content[:0]

	return resp
}

// ReleaseChatResponse returns a Chat response to the pool.
func ReleaseChatResponse(resp *AnthropicMessageResponse) {
	if resp == nil {
		return
	}

	// Release nested pooled objects recursively
	for i := 0; i < len(resp.Content); i++ {
		releaseAnthropicContentBlockRecursive(&resp.Content[i])
		resp.Content[i] = AnthropicContentBlock{} // Reset to zero value
	}

	// Only pool if slice capacity is reasonable
	if cap(resp.Content) <= maxSliceCapacity {
		anthropicChatResponsePool.Put(resp)
	}
}

// ==================== NESTED OBJECT POOLS ====================

// acquireAnthropicContent gets an AnthropicContent from the pool and resets it.
func acquireAnthropicContent() *AnthropicContent {
	content := anthropicContentPool.Get().(*AnthropicContent)

	// Reset fields
	content.ContentStr = nil
	content.ContentBlocks = content.ContentBlocks[:0] // Preserve capacity

	return content
}

// releaseAnthropicContent returns an AnthropicContent to the pool.
func releaseAnthropicContent(content *AnthropicContent) {
	if content == nil {
		return
	}

	// Release nested content blocks recursively
	for i := 0; i < len(content.ContentBlocks); i++ {
		releaseAnthropicContentBlockRecursive(&content.ContentBlocks[i])
		content.ContentBlocks[i] = AnthropicContentBlock{} // Reset to zero value
	}

	// Release the ContentBlocks slice if it was from pool
	if cap(content.ContentBlocks) <= maxSliceCapacity {
		releaseContentBlocks(content.ContentBlocks)
	}

	// Reset fields and return to pool
	content.ContentStr = nil
	content.ContentBlocks = nil
	anthropicContentPool.Put(content)
}

// releaseAnthropicContentRecursive releases AnthropicContent and its nested pools
func releaseAnthropicContentRecursive(content *AnthropicContent) {
	if content == nil {
		return
	}

	// For embedded AnthropicContent (not pointer), we can't release to pool
	// but we must release its nested pooled objects
	for i := 0; i < len(content.ContentBlocks); i++ {
		releaseAnthropicContentBlockRecursive(&content.ContentBlocks[i])
	}

	// Release the ContentBlocks slice if it was from pool
	if cap(content.ContentBlocks) <= maxSliceCapacity {
		releaseContentBlocks(content.ContentBlocks)
	}
}

// releaseAnthropicContentBlockRecursive releases nested pools in a ContentBlock
func releaseAnthropicContentBlockRecursive(block *AnthropicContentBlock) {
	if block == nil {
		return
	}

	// Release nested AnthropicContent if present
	if block.Content != nil {
		releaseAnthropicContent(block.Content)
		block.Content = nil
	}

	// Release nested AnthropicImageSource if present
	if block.Source != nil {
		releaseAnthropicImageSource(block.Source)
		block.Source = nil
	}
}

// acquireAnthropicToolChoice gets an AnthropicToolChoice from the pool and resets it.
func acquireAnthropicToolChoice() *AnthropicToolChoice {
	choice := anthropicToolChoicePool.Get().(*AnthropicToolChoice)

	// Reset fields
	choice.Type = ""
	choice.Name = ""
	choice.DisableParallelToolUse = nil

	return choice
}

// releaseAnthropicToolChoice returns an AnthropicToolChoice to the pool.
func releaseAnthropicToolChoice(choice *AnthropicToolChoice) {
	if choice != nil {
		anthropicToolChoicePool.Put(choice)
	}
}

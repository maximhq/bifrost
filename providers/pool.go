// Package providers implements various LLM providers and their response pooling mechanisms.
// This file contains the implementation of object pools for different provider responses
// to optimize memory allocation and garbage collection.
package providers

import (
	"sync"

	"github.com/maximhq/bifrost/interfaces"
)

// Inital Memory Allocations in the pools are done in provider specific files,
// taking concurrency provided in the account interface implementation as the initial pool size.

// openAIResponsePool provides a pool for OpenAI response objects.
// This pool is used to reuse OpenAIResponse objects to reduce memory allocations.
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		return &OpenAIResponse{}
	},
}

// azureChatResponsePool provides a pool for Azure chat response objects.
// This pool is used to reuse AzureChatResponse objects to reduce memory allocations.
var azureChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureChatResponse{}
	},
}

// azureTextCompletionResponsePool provides a pool for Azure text completion response objects.
// This pool is used to reuse AzureTextResponse objects to reduce memory allocations.
var azureTextCompletionResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureTextResponse{}
	},
}

// cohereResponsePool provides a pool for Cohere response objects.
// This pool is used to reuse CohereChatResponse objects to reduce memory allocations.
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &CohereChatResponse{}
	},
}

// bedrockChatResponsePool provides a pool for Bedrock response objects.
// This pool is used to reuse BedrockChatResponse objects to reduce memory allocations.
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &BedrockChatResponse{}
	},
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
// This pool is used to reuse AnthropicChatResponse objects to reduce memory allocations.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicChatResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
// This pool is used to reuse AnthropicTextResponse objects to reduce memory allocations.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// bifrostResponsePool provides a pool for Bifrost response objects.
// This pool is used to reuse BifrostResponse objects to reduce memory allocations.
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		return &interfaces.BifrostResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool and resets it.
// Returns a clean OpenAIResponse object ready for use.
func acquireOpenAIResponse() *OpenAIResponse {
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool.
// The response object will be reused in future allocations.
func releaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIResponsePool.Put(resp)
	}
}

// acquireAzureChatResponse gets an Azure chat response from the pool and resets it.
// Returns a clean AzureChatResponse object ready for use.
func acquireAzureChatResponse() *AzureChatResponse {
	resp := azureChatResponsePool.Get().(*AzureChatResponse)
	*resp = AzureChatResponse{} // Reset the struct
	return resp
}

// releaseAzureChatResponse returns an Azure chat response to the pool.
// The response object will be reused in future allocations.
func releaseAzureChatResponse(resp *AzureChatResponse) {
	if resp != nil {
		azureChatResponsePool.Put(resp)
	}
}

// acquireAzureTextResponse gets an Azure text completion response from the pool and resets it.
// Returns a clean AzureTextResponse object ready for use.
func acquireAzureTextResponse() *AzureTextResponse {
	resp := azureTextCompletionResponsePool.Get().(*AzureTextResponse)
	*resp = AzureTextResponse{} // Reset the struct
	return resp
}

// releaseAzureTextResponse returns an Azure text completion response to the pool.
// The response object will be reused in future allocations.
func releaseAzureTextResponse(resp *AzureTextResponse) {
	if resp != nil {
		azureTextCompletionResponsePool.Put(resp)
	}
}

// acquireCohereResponse gets a Cohere response from the pool and resets it.
// Returns a clean CohereChatResponse object ready for use.
func acquireCohereResponse() *CohereChatResponse {
	resp := cohereResponsePool.Get().(*CohereChatResponse)
	*resp = CohereChatResponse{} // Reset the struct
	return resp
}

// releaseCohereResponse returns a Cohere response to the pool.
// The response object will be reused in future allocations.
func releaseCohereResponse(resp *CohereChatResponse) {
	if resp != nil {
		cohereResponsePool.Put(resp)
	}
}

// acquireBedrockChatResponse gets a Bedrock response from the pool and resets it.
// Returns a clean BedrockChatResponse object ready for use.
func acquireBedrockChatResponse() *BedrockChatResponse {
	resp := bedrockChatResponsePool.Get().(*BedrockChatResponse)
	*resp = BedrockChatResponse{} // Reset the struct
	return resp
}

// releaseBedrockChatResponse returns a Bedrock response to the pool.
// The response object will be reused in future allocations.
func releaseBedrockChatResponse(resp *BedrockChatResponse) {
	if resp != nil {
		bedrockChatResponsePool.Put(resp)
	}
}

// acquireAnthropicChatResponse gets an Anthropic chat response from the pool and resets it.
// Returns a clean AnthropicChatResponse object ready for use.
func acquireAnthropicChatResponse() *AnthropicChatResponse {
	resp := anthropicChatResponsePool.Get().(*AnthropicChatResponse)
	*resp = AnthropicChatResponse{} // Reset the struct
	return resp
}

// releaseAnthropicChatResponse returns an Anthropic chat response to the pool.
// The response object will be reused in future allocations.
func releaseAnthropicChatResponse(resp *AnthropicChatResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool and resets it.
// Returns a clean AnthropicTextResponse object ready for use.
func acquireAnthropicTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)
	*resp = AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
// The response object will be reused in future allocations.
func releaseAnthropicTextResponse(resp *AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// acquireBifrostResponse gets a Bifrost response from the pool and resets it.
// Returns a clean BifrostResponse object ready for use.
func acquireBifrostResponse() *interfaces.BifrostResponse {
	resp := bifrostResponsePool.Get().(*interfaces.BifrostResponse)
	*resp = interfaces.BifrostResponse{} // Reset the struct
	return resp
}

// releaseBifrostResponse returns a Bifrost response to the pool.
// The response object will be reused in future allocations.
func releaseBifrostResponse(resp *interfaces.BifrostResponse) {
	if resp != nil {
		bifrostResponsePool.Put(resp)
	}
}

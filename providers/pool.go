package providers

import (
	"sync"

	"github.com/maximhq/bifrost/interfaces"
)

// openAIResponsePool provides a pool for OpenAI response objects
var openAIResponsePool = sync.Pool{
	New: func() interface{} {
		return &OpenAIResponse{}
	},
}

// azureResponsePool provides a pool for Azure response objects
var azureChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureChatResponse{}
	},
}

// azureTextCompletionResponsePool provides a pool for Azure text completion response objects
var azureTextCompletionResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureTextResponse{}
	},
}

// cohereResponsePool provides a pool for Cohere response objects
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &CohereChatResponse{}
	},
}

// bedrockResponsePool provides a pool for Bedrock response objects
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &BedrockChatResponse{}
	},
}

// anthropicResponsePool provides a pool for Anthropic response objects
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicChatResponse{}
	},
}

var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// bifrostResponsePool provides a pool for Bifrost response objects
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		return &interfaces.BifrostResponse{}
	},
}

// acquireOpenAIResponse gets an OpenAI response from the pool
func acquireOpenAIResponse() *OpenAIResponse {
	resp := openAIResponsePool.Get().(*OpenAIResponse)
	*resp = OpenAIResponse{} // Reset the struct
	return resp
}

// releaseOpenAIResponse returns an OpenAI response to the pool
func releaseOpenAIResponse(resp *OpenAIResponse) {
	if resp != nil {
		openAIResponsePool.Put(resp)
	}
}

// acquireAzureChatResponse gets an Azure response from the pool
func acquireAzureChatResponse() *AzureChatResponse {
	resp := azureChatResponsePool.Get().(*AzureChatResponse)
	*resp = AzureChatResponse{} // Reset the struct
	return resp
}

// releaseAzureChatResponse returns an Azure response to the pool
func releaseAzureChatResponse(resp *AzureChatResponse) {
	if resp != nil {
		azureChatResponsePool.Put(resp)
	}
}

// acquireAzureTextResponse gets an Azure text completion response from the pool
func acquireAzureTextResponse() *AzureTextResponse {
	resp := azureTextCompletionResponsePool.Get().(*AzureTextResponse)
	*resp = AzureTextResponse{} // Reset the struct
	return resp
}

// releaseAzureTextResponse returns an Azure text completion response to the pool
func releaseAzureTextResponse(resp *AzureTextResponse) {
	if resp != nil {
		azureTextCompletionResponsePool.Put(resp)
	}
}

// acquireCohereResponse gets a Cohere response from the pool
func acquireCohereResponse() *CohereChatResponse {
	resp := cohereResponsePool.Get().(*CohereChatResponse)
	*resp = CohereChatResponse{} // Reset the struct
	return resp
}

// releaseCohereResponse returns a Cohere response to the pool
func releaseCohereResponse(resp *CohereChatResponse) {
	if resp != nil {
		cohereResponsePool.Put(resp)
	}
}

// AcquireBedrockResponse gets a Bedrock response from the pool
func acquireBedrockChatResponse() *BedrockChatResponse {
	resp := bedrockChatResponsePool.Get().(*BedrockChatResponse)
	*resp = BedrockChatResponse{} // Reset the struct
	return resp
}

// ReleaseBedrockResponse returns a Bedrock response to the pool
func releaseBedrockChatResponse(resp *BedrockChatResponse) {
	if resp != nil {
		bedrockChatResponsePool.Put(resp)
	}
}

// AcquireAnthropicResponse gets an Anthropic response from the pool
func acquireAnthropicChatResponse() *AnthropicChatResponse {
	resp := anthropicChatResponsePool.Get().(*AnthropicChatResponse)
	*resp = AnthropicChatResponse{} // Reset the struct
	return resp
}

// ReleaseAnthropicResponse returns an Anthropic response to the pool
func releaseAnthropicChatResponse(resp *AnthropicChatResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

func acquireAnthropicTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)
	*resp = AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool
func releaseAnthropicTextResponse(resp *AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// acquireBifrostResponse gets a Bifrost response from the pool
func acquireBifrostResponse() *interfaces.BifrostResponse {
	resp := bifrostResponsePool.Get().(*interfaces.BifrostResponse)
	*resp = interfaces.BifrostResponse{} // Reset the struct
	return resp
}

// releaseBifrostResponse returns a Bifrost response to the pool
func releaseBifrostResponse(resp *interfaces.BifrostResponse) {
	if resp != nil {
		bifrostResponsePool.Put(resp)
	}
}

package api

import (
	"strings"
)

// IsOpenAIModel checks for OpenAI model patterns
func IsOpenAIModel(model string) bool {
	// Exclude Azure models to prevent overlap
	if strings.Contains(model, "azure/") {
		return false
	}

	openaiPatterns := []string{
		"gpt", "davinci", "curie", "babbage", "ada", "o1", "o3", "o4",
		"text-embedding", "dall-e", "whisper", "tts", "chatgpt",
	}

	return matchesAnyPattern(model, openaiPatterns)
}

// IsAzureModel checks for Azure OpenAI specific patterns
func IsAzureModel(model string) bool {
	azurePatterns := []string{
		"azure", "model-router", "computer-use-preview",
	}

	return matchesAnyPattern(model, azurePatterns)
}

// IsAnthropicModel checks for Anthropic Claude model patterns
func IsAnthropicModel(model string) bool {
	anthropicPatterns := []string{
		"claude", "anthropic/",
	}

	return matchesAnyPattern(model, anthropicPatterns)
}

// IsVertexModel checks for Google Vertex AI model patterns
func IsVertexModel(model string) bool {
	vertexPatterns := []string{
		"gemini", "palm", "bison", "gecko", "vertex/", "google/",
	}

	return matchesAnyPattern(model, vertexPatterns)
}

// IsBedrockModel checks for AWS Bedrock model patterns
func IsBedrockModel(model string) bool {
	bedrockPatterns := []string{
		"bedrock", "bedrock.amazonaws.com/", "bedrock/",
		"amazon.titan", "amazon.nova", "aws/amazon.",
		"ai21.jamba", "ai21.j2", "aws/ai21.",
		"meta.llama", "aws/meta.",
		"stability.stable-diffusion", "stability.sd3", "aws/stability.",
		"anthropic.claude", "aws/anthropic.",
		"cohere.command", "cohere.embed", "aws/cohere.",
		"mistral.mistral", "mistral.mixtral", "aws/mistral.",
		"titan-text", "titan-embed", "nova-micro", "nova-lite", "nova-pro",
		"jamba-instruct", "j2-ultra", "j2-mid",
		"llama-2", "llama-3", "llama-3.1", "llama-3.2",
		"stable-diffusion-xl", "sd3-large",
	}

	return matchesAnyPattern(model, bedrockPatterns)
}

// IsCohereModel checks for Cohere model patterns
func IsCohereModel(model string) bool {
	coherePatterns := []string{
		"command-", "embed-", "cohere",
	}

	return matchesAnyPattern(model, coherePatterns)
}

// matchesAnyPattern checks if the model matches any of the given patterns
func matchesAnyPattern(model string, patterns []string) bool {
	model = strings.ToLower(model) // <- normalise once
	for _, pattern := range patterns {
		if strings.Contains(model, pattern) {
			return true
		}
	}
	return false
}